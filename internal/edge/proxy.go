package edge

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HealthPath is the path the edge answers itself.
//
// WHY THE EDGE ANSWERS THIS ONE AND NOT THE OTHER. Core pins exactly two probe
// paths — `/healthz` and `/readyz` (core schemas/telemetry/probes.schema.json,
// two `const`s) — and a box needs a third thing neither of them is: an answer
// about the EDGE. `/readyz` is the app's, because readiness is a fact about
// the app's dependencies and the edge has none. `/healthz` is the edge's,
// because a liveness answer about the edge is the one answer a box can give
// when the app behind a hostname is gone, and the alternative is a probe that
// goes through the very thing it is probing.
//
// The consequence is the failure mode internal/failure's eleventh entry is
// about: on a box whose edge is down, every app's `/readyz` still answers 200
// from inside the box and the control plane's heartbeat still looks green,
// because nothing anywhere was asking the edge. This path is the thing to ask.
const HealthPath = "/healthz"

// probeInterval is how often the edge asks an upstream how it is. It is the
// edge's own loop, not a customer's configuration: a routing table whose
// health column is a week old is a routing table that says "healthy" about a
// release that was rolled back on Thursday.
const probeInterval = 15 * time.Second

// probeTimeout is how long one upstream gets to answer. It is short because a
// probe that hangs holds the answer the health column needs, and a health
// column that is waiting on a dead upstream is a health column that is stale.
const probeTimeout = 2 * time.Second

// SpecSource is where the gateway reads the routing record. It is a function
// rather than a *Spec because the record changes underneath a running gateway
// on every deploy, and a gateway holding a copy would route a host the last
// deploy retired.
//
// Returning an error means "the box cannot say what it serves", and the
// gateway answers 503 for every request rather than falling back to a stale
// table: an edge that routes from a record it failed to read is an edge that
// serves whatever happened to be in memory when the deploy happened.
type SpecSource func() (Spec, error)

// SpecSink is where the gateway writes the health it observed. It is a second
// seam rather than a directory for one reason: the health column is the edge's
// own fact, and the edge owns its own state — which means the edge has to be
// able to write the record it just measured without knowing where the record
// / lives or who else is holding it.
//
// A nil sink means health is observed and answered but not persisted, which is
// what a box with a read-only state directory or a test that does not want a
// second writer gets. The edge never lies about it: the answer is the same
// either way, only the durable copy differs.
type SpecSink func(Spec) error

// Gateway is the edge the box runs: one listener, many hostnames, TLS
// terminated here, and an atomic release switch underneath.
type Gateway struct {
	// Spec is the routing record. It is called per request.
	Spec SpecSource

	// Sink writes the health the edge observed. Nil means the health is
	// answered but not persisted — see SpecSink.
	Sink SpecSink

	// Certificates serves the TLS material. A nil source means the gateway
	// serves plaintext, which is what the challenge listener and the
	// cluster-internal listener are for.
	Certificates Certificates

	// Transport is the upstream transport. Nil gets a default with a pooled
	// connection per host, because an edge that opens a connection per
	// request is an edge that adds a handshake to every request.
	Transport http.RoundTripper

	// ErrorLog receives the reverse proxy's upstream failures. Nil discards
	// them, and the default is the standard logger's because a 502 nobody
	// logs is a 502 nobody can diagnose.
	ErrorLog *log.Logger

	// probes is the health column, keyed by hostname. It is the edge's own
	// state and it is written only by the probe loop: the spec is the
	// deploy's record and the health is the edge's observation, and merging
	// them would make a deploy rewrite something it did not measure.
	probes probeTable

	// writeMu serializes the probe loop's read-merge-write against itself and
	// against a concurrent probeAll a test drives by hand.
	writeMu sync.Mutex

	// stop closes the probe loop started by Serve.
	stop chan struct{}
}

// probeTable is the health column, safe for the probe goroutine and the
// request goroutines at once.
type probeTable struct {
	mu sync.Mutex
	// health is the last answer per hostname, and detail is the sentence
	// behind it.
	health map[string]Health
	detail map[string]string
}

func (p *probeTable) set(name string, h Health, detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.health == nil {
		p.health = map[string]Health{}
		p.detail = map[string]string{}
	}
	p.health[name] = h
	p.detail[name] = detail
}

func (p *probeTable) get(name string) (Health, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.health[name], p.detail[name]
}

// Handler is the gateway as an http.Handler, so it can be mounted behind a
// listener this package does not own — a boxd route, a test's httptest
// server, or the plaintext :80 listener that answers ACME challenges.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	// The challenge path is the edge's, and it is registered before the
	// catch-all so a request for a token is never proxied to an app.
	mux.Handle(ChallengePathPrefix, http.HandlerFunc(g.challenge))
	mux.Handle("/", http.HandlerFunc(g.ServeHTTP))
	return mux
}

// Serve starts the probe loop and blocks serving until the context is
// canceled. The probe loop is started here rather than in a constructor
// because a gateway that is constructed and never served should not have a
// goroutine waking every fifteen seconds for nothing.
func (g *Gateway) Serve(ctx context.Context, ln net.Listener) error {
	if g.stop != nil {
		return errors.New("this gateway is already serving")
	}
	g.stop = make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.probeLoop(ctx)
	}()
	srv := &http.Server{Handler: g.Handler(), ReadHeaderTimeout: 10 * time.Second}
	err := srv.Serve(ln)
	close(g.stop)
	<-done
	return err
}

// ListenTLS opens the TLS listener the box answers on. The certificate comes
// from the source per handshake, which is what makes a renewal on disk take
// effect without a restart (see CertStore).
func (g *Gateway) ListenTLS(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	if g.Certificates == nil {
		return ln, nil
	}
	return tls.NewListener(ln, &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := g.Certificates.Certificate(NormalizeHost(hello.ServerName))
			if err != nil {
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrNoCertificate) {
					return nil, err
				}
				return nil, err
			}
			return cert, nil
		},
		MinVersion: tls.VersionTLS12,
	}), nil
}

// ServeHTTP is the routing decision, and it is one lookup and one of two
// outcomes.
//
// The Host header is the whole key. A request for a hostname the box does not
// serve is a 404 that names the hostname and lists what the box does serve,
// because "no route" on a personal box is a question about a name and the
// answer is the name plus the table — not a default upstream that serves
// whichever app happens to be there.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	spec, err := g.spec()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":  "edge_unavailable",
			"detail": err.Error(),
		})
		return
	}
	host := NormalizeHost(r.Host)
	if r.URL.Path == HealthPath && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		writeJSON(w, http.StatusOK, g.HealthDocument(spec))
		return
	}
	row, ok := spec.Host(host)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":  "no_route",
			"detail": fmt.Sprintf("this box does not serve %q", host),
			"hosts":  spec.sortedNames(),
		})
		return
	}
	// The health the decision reads is the EDGE'S observation, not the deploy's
	// belief: a deploy writes HealthUnknown because it has not looked yet, and
	// a routing decision that read that would route every request to a dead
	// upstream on the strength of a value nobody measured.
	if health, detail := g.observedHealth(spec, row); health == HealthUnhealthy {
		// The edge answers rather than proxying, and the answer is 503 with
		// the edge's own reason. An unhealthy upstream is refused here rather
		// than at the proxy because the proxy's failure would be a 502 from
		// the app's point of view, and a customer reading it would go and
		// debug an app that is already down for a reason the edge knows.
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":  "upstream_unhealthy",
			"detail": detail,
		})
		return
	}
	g.handlerFor(row).ServeHTTP(w, r)
}

// spec reads the record through the source, or the record it was handed.
func (g *Gateway) spec() (Spec, error) {
	if g.Spec == nil {
		return Spec{}, errors.New("this gateway has no routing record")
	}
	return g.Spec()
}

func (s Spec) sortedNames() []string {
	out := make([]string, 0, len(s.Hosts))
	for _, h := range s.Hosts {
		out = append(out, h.Name)
	}
	return out
}

// proxy builds the reverse proxy for one row. It is built per request rather
// than cached per row because the row's upstream is read fresh from the spec
// each time — a cached proxy would hold the address the last deploy replaced.
func (g *Gateway) handlerFor(row Host) http.Handler {
	target := &url.URL{Scheme: "http", Host: row.Upstream}
	rp := httputil.NewSingleHostReverseProxy(target)
	if g.Transport != nil {
		rp.Transport = g.Transport
	} else {
		rp.Transport = defaultTransport()
	}
	if g.ErrorLog != nil {
		rp.ErrorLog = g.ErrorLog
	}
	previous := rp.Director
	rp.Director = func(r *http.Request) {
		previous(r)
		// The Host header is what the app sees, and it is the hostname the
		// client asked for rather than the upstream's address: an app that
		// builds absolute URLs, checks a CORS origin, or signs a cookie for
		// a host would otherwise produce answers for `127.0.0.1:8080`.
		r.Host = row.Name
		r.Header.Set("X-Forwarded-Host", row.Name)
	}
	return rp
}

func defaultTransport() http.RoundTripper {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// HealthDocument is the edge's answer about itself: the box, the mode, the
// release serving, and every host with what the edge last knew about it.
//
// It carries no secret and no certificate: which mode the box terminates TLS
// in is a fact about configuration and the certificate itself is never here.
// That is what lets this document be the answer a boxd route returns and a
// control-plane heartbeat quotes without a walk for Secret-shaped values —
// internal/boxd's load-bearing test would pass over it unchanged.
func (g *Gateway) HealthDocument(spec Spec) map[string]any {
	hosts := make([]map[string]any, 0, len(spec.Hosts))
	for _, h := range spec.SortedHosts() {
		health, detail := g.observedHealth(spec, h)
		entry := map[string]any{
			"name":     h.Name,
			"upstream": h.Upstream,
			"service":  h.Service,
			"health":   string(health),
		}
		if detail != "" {
			entry["detail"] = detail
		}
		hosts = append(hosts, entry)
	}
	return map[string]any{
		"edge":     true,
		"box":      spec.Box,
		"mode":     string(spec.Mode),
		"current":  spec.Current,
		"previous": spec.Previous,
		"hosts":    hosts,
	}
}

// observedHealth is what the edge knows about one row right now: the live
// observation if it has one, and the record's own health if it does not.
//
// The live observation wins because it is newer — the probe loop has just
// asked — and the record is the fallback because a gateway that has never
// probed (a reader that mounted the handler without Serve) still has to be
// able to answer from something. Neither is the deploy's guess: a deploy
// writes HealthUnknown, and HealthUnknown is not unhealthy, so a row nobody
// has measured yet is routed to rather than refused.
func (g *Gateway) observedHealth(spec Spec, row Host) (Health, string) {
	if health, detail := g.probes.get(row.Name); health != "" {
		return health, detail
	}
	return row.Health, ""
}

// detailFor is the sentence behind an unhealthy answer, and it prefers the
// observed one.
func (g *Gateway) detailFor(row Host) string {
	_, detail := g.probes.get(row.Name)
	if detail == "" {
		return fmt.Sprintf("the edge has not reached %s", row.Upstream)
	}
	return detail
}

// probeLoop asks every upstream how it is, on the interval. It stops when the
// context is canceled or the gateway stops serving.
//
// IT WRITES WHAT IT MEASURED, and that is the edge owning its own state rather
// than being handed a health column by whoever deployed. A health nobody
// measured is a health nobody can act on; the loop below is the only writer,
// it writes after it has asked, and it writes through the Sink seam so the
// record stays wherever the box keeps it.
func (g *Gateway) probeLoop(ctx context.Context) {
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	g.probeAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.stop:
			return
		case <-ticker.C:
			g.probeAll(ctx)
		}
	}
}

func (g *Gateway) probeAll(ctx context.Context) {
	// One write at a time, and one read before it. The probe loop and a
	// deploy can both be writing this record — one is recording health, the
	// other a new release — and the merge below is only correct if the health
	// is applied to the spec that is on disk NOW rather than to the copy the
	// loop started with. Without the re-read, a probe would write back a
	// spec whose Current release the deploy had already moved.
	observed := map[string]probeAnswer{}
	spec, err := g.spec()
	if err != nil {
		return
	}
	for _, h := range spec.Hosts {
		observed[h.Name] = g.probeOne(ctx, h)
	}

	g.writeMu.Lock()
	defer g.writeMu.Unlock()

	current, err := g.spec()
	if err != nil {
		current = spec
	}
	changed := false
	for _, h := range current.Hosts {
		answer := observed[h.Name]
		if answer == (probeAnswer{}) {
			// The host appeared between the two reads; it gets probed on the
			// next tick rather than guessed at now.
			continue
		}
		if answer.health == h.Health && answer.detail == h.Detail {
			continue
		}
		// The record is rewritten only when the observation moved: a loop
		// that wrote every tick would move the record's timestamp on every
		// tick, and "when did this last change" is the question a reader
		// asks of a health column.
		current = current.WithHost(Host{
			Name: h.Name, Upstream: h.Upstream, Service: h.Service,
			Health: answer.health, Detail: answer.detail,
		})
		changed = true
		g.probes.set(h.Name, answer.health, answer.detail)
	}
	if !changed || g.Sink == nil {
		return
	}
	if err := g.Sink(current); err != nil {
		// A health that could not be recorded is a health that will be
		// re-measured on the next tick, so this is logged and not fatal.
		if g.ErrorLog != nil {
			g.ErrorLog.Printf("edge: record the observed health: %v", err)
		}
	}
}

// probeAnswer is one observation: what the edge saw and the sentence behind it.
type probeAnswer struct {
	health Health
	detail string
}

func (g *Gateway) probeOne(ctx context.Context, row Host) probeAnswer {
	health, detail := g.probe(ctx, row)
	return probeAnswer{health: health, detail: detail}
}

// probe asks one upstream its readiness. The path is `/readyz` and not
// `/healthz`, and the asymmetry is deliberate: `/healthz` is the edge's own
// path (see HealthPath) and asking an app for it would be asking the app to
// answer a question about the box.
func (g *Gateway) probe(ctx context.Context, row Host) (Health, string) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+row.Upstream+"/readyz", nil)
	if err != nil {
		return HealthUnhealthy, err.Error()
	}
	resp, err := defaultTransport().RoundTrip(req)
	if err != nil {
		return HealthUnhealthy, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return HealthHealthy, ""
	}
	return HealthUnhealthy, fmt.Sprintf("%s answered %d", row.Upstream, resp.StatusCode)
}

// challenge serves the ACME HTTP-01 tokens. The source is the directory under
// the box's state directory, resolved the same way the spec's is: the edge
// owns what is inside, not where it sits.
func (g *Gateway) challenge(w http.ResponseWriter, r *http.Request) {
	ChallengeHandler(g.certDir()).ServeHTTP(w, r)
}

// certDir is the sibling of the cert directory the ACME tokens live in.
func (g *Gateway) certDir() string {
	if d, ok := g.Certificates.(*CertStore); ok {
		return filepath.Join(filepath.Dir(d.Dir), ACMEChallengeDirName)
	}
	return ""
}

// writeJSON is one JSON document with one status code. It is the edge's whole
// output shape — no HTML, no templates — because every answer the edge gives
// is read by either a machine or a person with curl, and both of them want
// the same bytes.
func writeJSON(w http.ResponseWriter, code int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		body = []byte(`{"error":"encode_failed"}`)
		code = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if _, err := w.Write(body); err != nil {
		return
	}
}

// trimPort is what a Host header loses before it is compared: the port, and
// nothing else. NormalizeHost in spec.go is the one that runs on every
// request; this one is here for a caller holding a name from somewhere else.
func trimPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

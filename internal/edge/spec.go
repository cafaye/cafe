package edge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SpecFileName is the name of the one file the edge owns.
//
// It is state, not configuration: it is written by a deploy and read by a
// rollback, and it lives under the box's own state directory rather than in
// the project a release came from. A release arrived from a repository, was
// built somewhere, and is served here — the record of what is serving is the
// box's fact about itself, which is the same argument internal/secret's
// StateFileName makes for the rotation schedule.
const SpecFileName = "edge.json"

// SpecVersion is the version of the record's shape. A spec whose version this
// binary does not know is refused rather than interpreted: an unknown field in
// a routing record is a route somebody added on a newer box, and guessing what
// it meant is how a box ends up serving something nobody deployed.
const SpecVersion = 1

// Mode is how the box terminates TLS.
//
// The three are the three states a box is actually in, and the plan's TLS note
// (moon/PLAN-names-urls-rollout.md §4) names the first two: Let's Encrypt over
// HTTP-01 once the names are proxied, a Cloudflare origin certificate while
// DNS is still grey-clouded. The third is not a compromise — `*.svc` names
// never appear in public DNS (the plan's stability rule 4), so the names that
// are only ever reached from inside the cluster are terminated with a cert the
// box issues itself and no ACME account is involved at all.
type Mode string

const (
	// ModeACME is Let's Encrypt over HTTP-01, for `api.<box>` and `auth.<box>`
	// once the zone is proxied. The solver needs no DNS-provider credential,
	// which is the whole reason it is the default rather than a convenience:
	// a credential held on a customer's box is a credential a box compromise
	// spends.
	ModeACME Mode = "acme-http01"

	// ModeOrigin is a Cloudflare origin certificate, for the grey-cloud
	// stage before the names are proxied and HTTP-01 can reach them. It is
	// long-lived and it is uploaded by an operator rather than issued on the
	// box, so it is the mode that ends and not the mode that lasts.
	ModeOrigin Mode = "cloudflare-origin"

	// ModeInternal is a certificate the box issues itself for names that are
	// never public. It exists so the `.svc` zone has an answer that is
	// honest — encrypted on the wire inside the cluster — instead of a
	// self-signed fallback nobody chose.
	ModeInternal Mode = "internal"
)

// Valid reports whether the mode is one the edge knows.
func (m Mode) Valid() bool {
	switch m {
	case ModeACME, ModeOrigin, ModeInternal:
		return true
	}
	return false
}

// Health is what the edge knows about one route's upstream. It is the edge's
// own observation and nothing else: the edge asks the upstream's health path
// and records the answer, so `Health` is a statement about the last time the
// edge looked rather than a promise about the next request.
type Health string

const (
	// HealthUnknown is the state before the first probe and after a probe
	// that could not be made at all. It is distinct from HealthUnhealthy
	// because "we have not looked" and "we looked and it is broken" are
	// different situations with different remedies, and collapsing them
	// would send an operator to restart a service the edge never reached.
	HealthUnknown Health = "unknown"
	// HealthHealthy is a probe that answered 2xx.
	HealthHealthy Health = "healthy"
	// HealthUnhealthy is a probe that answered at all and not 2xx, or did
	// not answer in time.
	HealthUnhealthy Health = "unhealthy"
)

// Host is one row: a hostname, the upstream it answers to, and what the edge
// last knew about it.
//
// There is no path here. Host routing is the whole vocabulary, and a route
// that needed a path would be a route that needed two boxes to agree about
// their URL layout — which is the ingress-shaped thinking this package exists
// to avoid.
type Host struct {
	// Name is the hostname as it arrives in the Host header, lowercased and
	// without a port. The port is stripped because a client that reaches the
	// box on a non-default port is still asking for the same app, and a
	// row that only matched the default port would 404 behind a tunnel.
	Name string `json:"name"`

	// Upstream is the address the host answers to — host:port, the published
	// address of the release's service. It is an address and not a path
	// because the release directory is the thing that moved, and the
	// address is how the edge finds it without knowing what it is.
	Upstream string `json:"upstream"`

	// Service is which service of the release this row came from, recorded so
	// a human reading the spec can tell which row a deploy touched. It is
	// descriptive: routing never reads it.
	Service string `json:"service,omitempty"`

	// Health is the edge's last answer about Upstream.
	Health Health `json:"health,omitempty"`

	// Detail is the sentence behind an unhealthy answer — the status or the
	// error — because a health nobody can act on is a color.
	Detail string `json:"detail,omitempty"`
}

// Spec is the one piece of state the edge owns: which hostnames this box
// answers for, where each one goes, what the edge last knew about it, and
// which release is serving.
//
// It is deliberately small and deliberately complete. Small, because every
// field here is a field two commands have to agree about. Complete, because a
// routing fact the spec did not carry would be a routing fact kept somewhere
// else — and the somewhere else is what this type exists to make unnecessary.
type Spec struct {
	// Version is SpecVersion. A spec whose version differs is refused by
	// Validate rather than read optimistically.
	Version int `json:"version"`

	// Box is the box this spec belongs to. It is recorded so a spec copied
	// onto another box — a restore, a mistaken rsync — is refused by name
	// rather than silently routing a second box's hostnames from the first.
	Box string `json:"box"`

	// Mode is how TLS is terminated for these hosts.
	Mode Mode `json:"mode"`

	// Current is the release id `current` points at — the last-deploy
	// pointer. It is a fact about the switch rather than about the
	// symlink, so a reader that has no filesystem access can still answer
	// "what is serving" and a rollback can say what it is going back to.
	Current string `json:"current"`

	// Previous is the release id `current` pointed at before the last
	// switch. It is what makes a rollback a single step rather than a
	// search through the release directory's mtimes, and it is written
	// before the symlink moves so a crash between the two leaves an
	// honest record of a switch that did not happen.
	Previous string `json:"previous,omitempty"`

	// Hosts are the rows, in the order they were added. Order is part of the
	// answer for the same reason internal/boxd's Table is a slice: two runs
	// over one spec render the same list, and a human comparing them is
	// comparing like with like.
	Hosts []Host `json:"hosts"`

	// Updated is when the spec was last written. It is the only timestamp
	// here, and it is when the RECORD changed rather than when a deploy
	// happened — the deploy time is the release directory's own fact.
	Updated time.Time `json:"updated"`
}

// New is the spec of a box that has never deployed anything: no hosts, no
// current release, and a mode the caller chose because a box with no mode is
// a box that would serve plaintext by default.
func New(box string, mode Mode) Spec {
	return Spec{Version: SpecVersion, Box: box, Mode: mode, Updated: time.Now().UTC()}
}

// Validate is the gate every spec passes before it is written and after it is
// read. It is a pure function of the value so a test can table-drive it, and
// it reports every problem rather than the first, because a routing record
// with two defects is a record an operator fixes twice.
func (s Spec) Validate() error {
	var problems []string
	if s.Version != SpecVersion {
		problems = append(problems, fmt.Sprintf("version %d, want %d", s.Version, SpecVersion))
	}
	if strings.TrimSpace(s.Box) == "" {
		problems = append(problems, "no box named")
	}
	if !s.Mode.Valid() {
		problems = append(problems, fmt.Sprintf("tls mode %q is not one of acme-http01, cloudflare-origin, internal", s.Mode))
	}
	seen := map[string]bool{}
	for i, h := range s.Hosts {
		if h.Name == "" {
			problems = append(problems, fmt.Sprintf("host %d has no name", i))
			continue
		}
		if strings.ContainsAny(h.Name, "/ \t") {
			problems = append(problems, fmt.Sprintf("host %q is not a bare hostname", h.Name))
		}
		if h.Name != strings.ToLower(h.Name) {
			problems = append(problems, fmt.Sprintf("host %q must be lowercased", h.Name))
		}
		if h.Upstream == "" {
			problems = append(problems, fmt.Sprintf("host %q has no upstream", h.Name))
		}
		if seen[h.Name] {
			problems = append(problems, fmt.Sprintf("host %q appears twice: two rows for one hostname is a coin toss", h.Name))
		}
		seen[h.Name] = true
		switch h.Health {
		case "", HealthUnknown, HealthHealthy, HealthUnhealthy:
		default:
			problems = append(problems, fmt.Sprintf("host %q has health %q, which is not a health", h.Name, h.Health))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("edge spec is not valid: %s", strings.Join(problems, "; "))
}

// Host returns the row for one hostname and whether it is there. The name is
// normalised the same way on the way in and on the way out, so a caller who
// read the name off a Host header gets the row the edge would use.
func (s Spec) Host(name string) (Host, bool) {
	name = NormalizeHost(name)
	for _, h := range s.Hosts {
		if h.Name == name {
			return h, true
		}
	}
	return Host{}, false
}

// SortedHosts is the rows by name, for a rendering that has to be stable
// across two runs of one binary.
func (s Spec) SortedHosts() []Host {
	out := make([]Host, len(s.Hosts))
	copy(out, s.Hosts)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// WithHost returns the spec with one row added or replaced. It is pure: the
// receiver is copied, because a deploy that mutated the spec the caller is
// holding would make the spec two values that disagree about what is serving.
func (s Spec) WithHost(h Host) Spec {
	h.Name = NormalizeHost(h.Name)
	out := s.clone()
	for i := range out.Hosts {
		if out.Hosts[i].Name == h.Name {
			out.Hosts[i] = h
			return out
		}
	}
	out.Hosts = append(out.Hosts, h)
	return out
}

// clone is a deep-enough copy: the slice is copied so appending to the result
// cannot reach into the receiver's backing array.
func (s Spec) clone() Spec {
	out := s
	out.Hosts = make([]Host, len(s.Hosts))
	copy(out.Hosts, s.Hosts)
	return out
}

// NormalizeHost is what a Host header becomes before it is looked up:
// lowercased, port stripped, whitespace trimmed.
//
// The port is stripped rather than rejected because a box behind a tunnel or
// a non-default port is still the same box asking for the same app, and a
// lookup that failed there would be a 404 the customer could reproduce only
// by changing their client.
func NormalizeHost(name string) string {
	name = strings.TrimSpace(name)
	if h, _, err := splitHostPort(name); err == nil && h != "" {
		name = h
	}
	// A bracketed IPv6 literal arrives as "[::1]:8080" and loses its port
	// above; it keeps its brackets, which is what a client sends.
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

// splitHostPort is net.SplitHostPort without the import: the edge needs one
// call from a stdlib function and the failure mode of a malformed authority
// is "use the whole string", not an error anybody acts on.
func splitHostPort(hostport string) (string, string, error) {
	colon := strings.LastIndex(hostport, ":")
	if colon == -1 {
		return hostport, "", errNoPort
	}
	if strings.HasPrefix(hostport, "[") {
		end := strings.LastIndex(hostport, "]")
		if end == -1 || end > colon {
			return hostport, "", errNoPort
		}
		return hostport[1:end], hostport[colon+1:], nil
	}
	// A bare colon with nothing after it, or more than one, is not a
	// host:port — it is a malformed authority, and the routing answer for
	// one is the same as for an unknown host.
	if strings.Contains(hostport[:colon], ":") {
		return hostport, "", errNoPort
	}
	return hostport[:colon], hostport[colon+1:], nil
}

var errNoPort = errors.New("no port")

// ---------------------------------------------------------------------------
// reading and writing the record
// ---------------------------------------------------------------------------

// SpecPath is where the record lives under a box's state directory. It is a
// function rather than a constant so a test can name a scratch directory and
// so nothing in this package has an opinion about where the state directory
// is — that is boxd's, the same way the ledger's DefaultDir is the ledger's.
func SpecPath(dir string) string { return filepath.Join(dir, SpecFileName) }

// Load reads the spec. A missing file is a spec that has never deployed —
// New("", ModeACME) with an empty box, which Validate rejects and a caller
// reads as "this box has no edge yet" rather than as an error worth a stack
// trace. A malformed one IS an error: a routing record nobody can read is a
// routing record that will silently stop routing.
func Load(dir string) (Spec, error) {
	raw, err := os.ReadFile(SpecPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Spec{}, nil
		}
		return Spec{}, fmt.Errorf("read the edge spec: %w", err)
	}
	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return Spec{}, fmt.Errorf("the edge spec at %s is not JSON: %w", SpecPath(dir), err)
	}
	if s.Version == 0 && s.Box == "" {
		return Spec{}, nil
	}
	if err := s.Validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

// Save writes the spec, atomically and in one call. The bytes go to a sibling
// temporary file and are renamed over the target, so a crash mid-write leaves
// the previous spec intact rather than a half-written routing record — the
// same discipline internal/secret's state file holds and for the same reason.
//
// It creates the directory it is writing into, because the first write on a
// box happens before anything else has made one and a caller that had to
// remember to MkdirAll first would be a caller that forgot once per command.
func Save(dir string, s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the edge state directory: %w", err)
	}
	s.Updated = time.Now().UTC()
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the edge spec: %w", err)
	}
	raw = append(raw, '\n')
	// A temporary name the OS makes unique, not one this package spells: the
	// probe loop writes the health it observed while a deploy may be writing
	// a new release, and two writers sharing one temporary path is two writers
	// that can delete each other's bytes. The uniqueness has to be per CALL
	// and not per process — two goroutines in one process are two writers —
	// which is why the name comes from CreateTemp rather than from a pid.
	tmp, err := os.CreateTemp(dir, filepath.Base(SpecPath(dir))+".tmp.*")
	if err != nil {
		return fmt.Errorf("create the temporary spec: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("write the edge spec: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("close the temporary spec: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return fmt.Errorf("set the edge spec's mode: %w", err)
	}
	if err := os.Rename(name, SpecPath(dir)); err != nil {
		os.Remove(name)
		return fmt.Errorf("install the edge spec: %w", err)
	}
	return nil
}

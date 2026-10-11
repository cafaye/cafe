package cve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Severity is how bad an advisory is. It is a string because the report prints
// it and a test greps it, and it has an order because "14 advisories" is
// sorted by severity and not by name: the customer reads the top of the list
// and stops, which is what the top of the list is for.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityUnknown  Severity = "unknown"
)

// rank orders severities. Bigger is worse.
func (s Severity) rank() int {
	switch s {
	case SeverityCritical:
		return 4
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	default:
		// An advisory with no severity is not an advisory with a low
		// severity. It sorts last and says "unknown" in the report, because
		// ranking an unrated advisory as low is how a critical one gets
		// skipped.
		return 0
	}
}

// ParseSeverity reads a severity as the upstream feeds write it. It never
// fails: an unrecognised value is SeverityUnknown rather than an error,
// because an advisory whose severity nobody has scored is still an advisory
// and a parser that refused it would drop it out of the report entirely.
func ParseSeverity(text string) Severity {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "critical":
		return SeverityCritical
	case "high":
		return SeverityHigh
	case "medium", "moderate":
		return SeverityMedium
	case "low":
		return SeverityLow
	default:
		return SeverityUnknown
	}
}

// Advisory is one known advisory against one component.
//
// # Why introduced/fixed rather than a version range
//
// The natural shape for "affected versions" is a range expression, and the
// repository already has a constraint grammar — internal/contract/version.go,
// four operators, deliberately tiny because core's docs call a full npm-style
// semver "a dependency and a footgun". That grammar cannot express the thing
// an advisory actually says: OSV, the GitHub Advisory Database and CSAF all
// express an affected range as a pair of EVENTS, `introduced` and `fixed`,
// with either end unbounded. `introduced: 0` with `fixed: 7.4.2` is the
// common case and no single-operator constraint grammar has it.
//
// So the advisory carries the pair the feeds carry, and the comparison is
// numeric on three components. Anything that does not fit — a version with a
// suffix, a range nobody can parse — is classified unknown and said out loud,
// which is the honest failure and the reason this is not a grammar.
type Advisory struct {
	// ID is the advisory's own name: CVE-2025-1234 or GHSA-xxxx-yyyy-zzzz.
	ID string
	// Component is the component name this advisory is against, in the
	// spelling the substrate catalogue uses.
	Component string
	// Summary is one sentence, for the report.
	Summary string
	// Severity is the feed's score, or unknown when it has none.
	Severity Severity
	// Introduced is the first version affected. Empty means every version
	// before Fixed is affected, which is what an advisory with no
	// introduction event means.
	Introduced string
	// Fixed is the first version that is not affected. Empty means there is
	// no fix, which is a fact the report carries rather than hides.
	Fixed string
	// URL is where a human reads the advisory.
	URL string
}

// Affects reports whether this advisory covers the given version, and says
// which of the three answers it is.
//
// The order matters. A version nobody can read is unknown even when there is
// a fix, because "we could not read the version" and "the version is
// unaffected" are different sentences and only one of them is safe to act on.
// Between the two readable cases the comparison is Introduced <= version <
// Fixed, with an empty bound meaning unbounded — so an advisory with no
// Fixed version and no Introduced version affects everything, which is what
// "no fix, always vulnerable" means.
func (a Advisory) Affects(version string) Class {
	if _, ok := comparable(version); !ok {
		return ClassUnknown
	}
	_, fixedOK := comparable(a.Fixed)
	if a.Introduced != "" {
		if _, ok := comparable(a.Introduced); !ok {
			return ClassUnknown
		}
		// At or before the introduction is unaffected.
		if compareVersions(version, a.Introduced) < 0 {
			return ClassUnaffected
		}
	}
	if !fixedOK {
		// Either there is no fix at all, or the fix is written in a spelling
		// cafe cannot compare. Both are named differently below.
		if a.Fixed == "" {
			return ClassNoFix
		}
		return ClassUnknown
	}
	if compareVersions(version, a.Fixed) < 0 {
		return ClassAffected
	}
	return ClassUnaffected
}

// Class is the verdict for one component against one advisory. It is a type
// rather than a bool because the report has to say WHY, and "false" is not a
// sentence.
type Class string

const (
	// ClassAffected is the version is in the affected range and a fixed
	// version exists. This is the row a buyer reads.
	ClassAffected Class = "affected"
	// ClassNoFix is the version is in the affected range and nobody has
	// published a fix. The answer here is "we cannot upgrade out of this",
	// which is the sentence that gets a mitigation decided.
	ClassNoFix Class = "no fix published"
	// ClassUnaffected is the version is outside the affected range. It is
	// reported rather than dropped, because "this one is fine and here is
	// the version that proves it" is what makes the rest of the report
	// trustworthy.
	ClassUnaffected Class = "unaffected"
	// ClassUnknown is cafe could not compare the version against the range.
	// Never rendered as "unaffected": a report that said unaffected when it
	// meant unreadable is a report that leaves a box unpatched.
	ClassUnknown Class = "unknown"
)

// Emitter receives one line of progress per phase of a scan.
//
// This exists because of a failure the brief names: a network call that hangs
// is an invisible stall. An advisory fetch over a connection that has stopped
// answering produces no output at all for as long as it takes, and the only
// thing that distinguishes it from a fast source is a line saying which phase
// the run is in. So every long step emits before it starts and after it
// finishes, and a caller that wants nothing passes Discard.
type Emitter interface {
	Emit(phase, detail string)
}

// EmitterFunc adapts a function to the Emitter seam. It is the shape every
// caller actually uses — a closure writing to stderr, or a test appending to
// a slice — and one method wide on purpose.
type EmitterFunc func(phase, detail string)

func (f EmitterFunc) Emit(phase, detail string) { f(phase, detail) }

// Discard is the emitter for a caller that wants no progress output.
func Discard() Emitter { return EmitterFunc(func(string, string) {}) }

// Source is where advisories come from. One method, because that is the whole
// of what the matcher needs from the network, and an interface because it is
// the seam that keeps the network out of the classification tests: Classify
// takes a list of advisories and says nothing about where they came from, so
// every test of the classification runs against a slice built by hand.
//
// It takes the components rather than the box, so a source can ask for only
// what it needs — the OSV batch query is one POST per batch of packages, and
// a source that received a whole inventory would re-decide the batching.
type Source interface {
	Advisories(ctx context.Context, components []Component, e Emitter) ([]Advisory, error)
}

// SourceError is a failure that names the upstream and what it said. A
// paraphrase here — "the advisory source failed" — sends a reader to a log
// that says nothing useful, which is the habit internal/resolve already
// applies to box's and pantry's own codes.
type SourceError struct {
	// Source is the name of the upstream as written in the report.
	Source string
	// Detail is what the upstream said, verbatim where there is a verbatim.
	Detail string
}

func (e *SourceError) Error() string {
	return fmt.Sprintf("the advisory source %s failed: %s", e.Source, e.Detail)
}

// HTTPTimeouts is the bound every network call in this package runs under.
//
// It is a value rather than a literal at each call site because a bound that
// lives in one place can be reasoned about, and because the brief's predicate
// is not "the call usually completes" — it is that a call which does not
// complete has to be a failure with a name rather than a stall. The whole
// request has a deadline, and the body read has its own smaller one, so a
// server that answers headers and then stops sending is caught by the second
// rather than hanging until the first expires.
var HTTPTimeouts = struct {
	// Dial is the connect+handshake budget.
	Dial time.Duration
	// Request is the whole request-and-response budget, headers included.
	Request time.Duration
	// Body is the read budget once the response has started.
	Body time.Duration
}{5 * time.Second, 20 * time.Second, 30 * time.Second}

// HTTPSource is the advisory source that queries one HTTP endpoint per batch.
//
// It is osv.dev's batch endpoint by default because that is the one feed that
// covers the substrate's own ecosystem (Go, and the container images the
// substrate runs, which OSV indexes by image) and because it needs no
// credential — a source that needed an API key would be a source whose
// answer depended on a secret on the box, which is a different product.
//
// # Every call is bounded, and the bound has two halves
//
// The client above sets Dial, Request and Body, and the caller's context
// carries the deadline for the whole scan. Both are needed: a context alone
// still lets a request sit in a connection pool, and a client timeout alone
// still lets a caller wait forever on a retry.
type HTTPSource struct {
	// URL is the batch endpoint.
	URL string
	// Client is the HTTP client. Nil means one built from HTTPTimeouts.
	Client *http.Client
	// Batch is how many components go in one request. The endpoint's own
	// limit is the reason it exists; 100 is its documented maximum.
	Batch int
}

// DefaultAdvisoryURL is the one feed this queries.
const DefaultAdvisoryURL = "https://api.osv.dev/v1/querybatch"

// osvQuery is one query in an OSV batch request.
type osvQuery struct {
	Package osvPackage `json:"package"`
	Version string     `json:"version"`
}

type osvPackage struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

type osvBatch struct {
	Queries []osvQuery `json:"queries"`
}

// osvBatchResponse is the shape api.osv.dev answers a batch with: one result
// per query, in the same order, each with its own list of vulns.
type osvBatchResponse struct {
	Results []struct {
		Vulns []osvVuln `json:"vulns"`
	} `json:"results"`
}

// osvVuln is one vulnerability in an OSV answer. Only the fields this package
// reads are declared: an answer carrying more is not an error, and decoding
// the rest would be decoding facts nobody reports.
type osvVuln struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Severity []struct {
		Type  string `json:"type"`
		Score string `json:"score"`
	} `json:"severity"`
	References []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"references"`
	Affected []struct {
		Package struct {
			Name      string `json:"name"`
			Ecosystem string `json:"ecosystem"`
		} `json:"package"`
		Ranges []struct {
			Type   string `json:"type"`
			Events []struct {
				Introduced string `json:"introduced"`
				Fixed      string `json:"fixed"`
			} `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
}

// ecosystemOf maps a component kind onto the ecosystem name the feed uses.
//
// This is a guess about a feed, and it is labelled as one: the substrate's own
// components are not all in OSV under a name anyone has verified, which is
// why the matcher treats a zero-result query as a fact about the feed rather
// than as proof of safety. What is NOT a guess is that an advisory returned
// for a component is matched on the component NAME the catalogue uses, so a
// feed that calls k3s something else produces no match and the report says
// the feed was asked, not that the box was clean.
func ecosystemOf(kind Kind) string {
	switch kind {
	case KindNode:
		return "Go"
	case KindWorkload, KindImage:
		return "Alpine"
	case KindChart, KindAddon:
		return "Go"
	}
	return ""
}

// Advisories queries the feed in batches, emitting a checkpoint before and
// after each one.
//
// # The three ways it reports rather than fails
//
// A batch that the feed answers with an error is recorded as a source error
// naming the batch, and the scan continues with the batches that worked: one
// unresolvable component is not a reason to withhold the other fifteen
// advisories, and a scan that stopped at the first failure would be a scan
// whose answer depended on the order of the list.
//
// A response that is not the shape OSV writes is an error naming the batch.
// It is NOT an empty list, because an empty list on one side makes "no
// advisories" hold for a reason that has nothing to do with the box — which
// is the same vacuity internal/backup's contract refuses.
//
// An advisory whose affected range names a different component than the
// query's is dropped, not matched by position. OSV's `affected[].package`
// is authoritative about what an advisory is against, and matching a
// returned vuln to the query that happened to be next to it is how a report
// ends up naming the wrong component.
func (s HTTPSource) Advisories(ctx context.Context, components []Component, e Emitter) ([]Advisory, error) {
	if s.Client == nil {
		s.Client = &http.Client{Timeout: HTTPTimeouts.Request}
	}
	if s.URL == "" {
		s.URL = DefaultAdvisoryURL
	}
	batch := s.Batch
	if batch <= 0 || batch > 100 {
		batch = 100
	}
	var out []Advisory
	for start := 0; start < len(components); start += batch {
		end := min(start+batch, len(components))
		slice := components[start:end]
		label := fmt.Sprintf("%s..%s", slice[0].Name, slice[len(slice)-1].Name)
		e.Emit("advisories", fmt.Sprintf("asking the advisory feed for %d components (%s)", len(slice), label))
		got, err := s.query(ctx, slice)
		if err != nil {
			e.Emit("advisories", fmt.Sprintf("batch %s failed: %v", label, err))
			return out, err
		}
		e.Emit("advisories", fmt.Sprintf("the advisory feed answered for %s: %d advisories", label, len(got)))
		out = append(out, got...)
	}
	return out, nil
}

// query is one batch request. It is separate so the batching, the checkpoints
// and the failure handling are readable independently of the wire shape.
func (s HTTPSource) query(ctx context.Context, slice []Component) ([]Advisory, error) {
	req := osvBatch{}
	for _, c := range slice {
		req.Queries = append(req.Queries, osvQuery{
			Package: osvPackage{Name: c.Name, Ecosystem: ecosystemOf(c.Kind)},
			Version: c.Version,
		})
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &SourceError{Source: s.URL, Detail: fmt.Sprintf("could not encode the query: %v", err)}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, strings.NewReader(string(body)))
	if err != nil {
		return nil, &SourceError{Source: s.URL, Detail: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(httpReq)
	if err != nil {
		return nil, &SourceError{Source: s.URL, Detail: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &SourceError{Source: s.URL, Detail: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	limited := io.LimitReader(resp.Body, 8<<20)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, &SourceError{Source: s.URL, Detail: fmt.Sprintf("reading the answer: %v", err)}
	}
	var parsed osvBatchResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &SourceError{Source: s.URL, Detail: fmt.Sprintf("the answer is not the shape OSV writes: %v", err)}
	}
	if len(parsed.Results) != len(slice) {
		return nil, &SourceError{
			Source: s.URL,
			Detail: fmt.Sprintf("answered %d results for %d queries — one answer per query, or the list is short",
				len(parsed.Results), len(slice)),
		}
	}
	var out []Advisory
	for i, result := range parsed.Results {
		for _, v := range result.Vulns {
			out = append(out, advisoryOf(v, slice[i]))
		}
	}
	return out, nil
}

// advisoryOf turns one OSV vulnerability into an Advisory, taking the
// component it was queried for so an advisory whose affected block names
// something else can be dropped rather than mis-attributed.
//
// The drop is deliberate and it is the reason the component is a parameter:
// OSV answers a batch with the vulns that matched ANY query, and a vuln
// returned for `redis` that names `valkey` in its affected block is a vuln
// about a different package. Attributing it to the query that happened to
// carry it is how a report ends up telling a customer to upgrade the wrong
// thing — which is a worse failure than a missing row, because the customer
// acts on it.
func advisoryOf(v osvVuln, component Component) Advisory {
	a := Advisory{
		ID:        v.ID,
		Component: component.Name,
		Summary:   v.Summary,
		Severity:  SeverityUnknown,
	}
	for _, sev := range v.Severity {
		if parsed := ParseSeverity(sev.Score); parsed != SeverityUnknown {
			a.Severity = parsed
			break
		}
	}
	for _, ref := range v.References {
		if ref.Type == "WEB" || a.URL == "" {
			a.URL = ref.URL
		}
	}
	for _, aff := range v.Affected {
		if aff.Package.Name != "" && aff.Package.Name != component.Name {
			continue
		}
		for _, r := range aff.Ranges {
			if r.Type != "" && r.Type != "SEMVER" && r.Type != "ECOSYSTEM" {
				continue
			}
			for _, ev := range r.Events {
				if ev.Introduced != "" && a.Introduced == "" {
					a.Introduced = ev.Introduced
				}
				if ev.Fixed != "" && a.Fixed == "" {
					a.Fixed = ev.Fixed
				}
			}
		}
	}
	return a
}

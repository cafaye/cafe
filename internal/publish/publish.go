package publish

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cafaye/caf/internal/contract"
	"github.com/cafaye/caf/internal/resolve"
)

// The kinds a project can be registered as. They are pantry's ProjectKind
// vocabulary, and two of them are CURATED rather than derived: a manifest that
// declares no contract surface cannot say whether it is a binary or a server,
// so the publisher states it (pantry's own validator says the same thing).
const (
	KindAPI    = "api"
	KindWorker = "worker"
	KindBoth   = "both"
	KindCLI    = "cli"
)

// The dependency classes a compatibility edge can carry. They are pantry's
// RequirementDeclarationDependency vocabulary; the manifest's `required` flag
// collapses onto the first two — `dev` is not expressible in a manifest, and
// inventing it here would be a second, looser copy of the schema's vocabulary.
const (
	DepRequired = "required"
	DepSoft     = "soft"
	DepDev      = "dev"
)

// The state a first submission registers. Docker Hub's model (MD28): push and it
// is public — there is no review queue, and a `caf publish` that left the
// project pantry's default `draft` would be a command that published nothing
// visible. Pantry defaults to draft only when the body says nothing; this
// command is the caller saying so.
const StatePublished = "published"

// Step statuses. One vocabulary for the three operations, so a report reads as
// a sequence of the same kind of fact.
const (
	StatusCreated       = "created"
	StatusAlreadyExists = "already_exists"
	StatusFailed        = "failed"
)

// The three write operations, as a report names them.
const (
	OpRegister    = "register"
	OpVersion     = "publish_version"
	OpRequirement = "declare_requirement"
)

// The idempotent phrases in pantry's own 409 details. A 409 carrying one of
// these is "the write already happened"; any other 409 is a refusal — the
// ordering rule, whose detail begins "no published semver version", is the
// load-bearing case the safe direction exists for.
const (
	idempotentRegistered = "already registered"
	idempotentPublished  = "already published"
	idempotentDeclared   = "already declared"
)

// DefaultPublishTimeout is what a nil Timeout on HTTPPublisher means.
const DefaultPublishTimeout = 30 * time.Second

// Publisher is pantry's write surface: the three operations one publish runs,
// publisher-authenticated. It is a seam so a test can drive the whole decision
// table — dry run, refusal, 409 tolerance — against a fake that records calls
// and answers without a network.
//
// The token is a parameter of every call, for the reason resolve's tier-2 call
// takes it: the credential is presented for the length of exactly one write
// and held by nothing. A Publisher that stored the token would be a Publisher
// that outlives the confirmation the token was minted for.
type Publisher interface {
	// Register is POST /v1/projects: the project's own manifest, as JSON,
	// stored verbatim so the recorded copy can be diffed against a checkout.
	Register(ctx context.Context, token string, reg Registration) (Registered, error)
	// PublishVersion is POST /v1/projects/{name}/versions: the publisher's
	// real git tag, verbatim, leading `v` included.
	PublishVersion(ctx context.Context, token, name, tag string) (Published, error)
	// DeclareRequirement is POST /v1/projects/{name}/requirements: one edge
	// to a required project, which may belong to another publisher.
	DeclareRequirement(ctx context.Context, token, name string, req Requirement) (Declared, error)
}

// Registration is the POST /v1/projects body: everything a publisher claims
// about a project they own. The shape is pantry's wire shape — a client that
// invented flatter field names would be a second copy of the document, and the
// second copy is the one that drifts.
type Registration struct {
	Name           string         `json:"name"`
	Description    string         `json:"description,omitempty"`
	Language       string         `json:"language"`
	Kind           string         `json:"kind"`
	Core           string         `json:"core"`
	BasePath       string         `json:"basePath,omitempty"`
	Consumes       []string       `json:"consumes,omitempty"`
	Exposes        *Exposes       `json:"exposes,omitempty"`
	Repository     Repository     `json:"repository"`
	Owner          Owner          `json:"owner"`
	Manifest       map[string]any `json:"manifest"`
	ManifestSHA256 string         `json:"manifest_sha256"`
	State          string         `json:"state,omitempty"`
}

// Exposes is the manifest's contract surface as the registry carries it: the
// OpenAPI document's repository-relative path and the event types published.
type Exposes struct {
	API    string   `json:"api,omitempty"`
	Events []string `json:"events,omitempty"`
}

// Repository is what the publisher states about their repository on write: the
// coordinates tier 1 does not publish. url is required and SSH — core's schema
// rejects an HTTPS remote for a cafaye repository, so the manifest's value is
// the only one there can be.
type Repository struct {
	URL           string `json:"url"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	Visibility    string `json:"visibility,omitempty"`
}

// Owner is the accountable team, not the author.
type Owner struct {
	Team    string `json:"team"`
	Contact string `json:"contact,omitempty"`
}

// Requirement is one `requires` edge. Name is the TARGET — the required
// project — not the project in the path; a body that named the wrong end would
// be an edge pointing the wrong way.
type Requirement struct {
	Name         string `json:"name"`
	VersionRange string `json:"version_range"`
	Dependency   string `json:"dependency,omitempty"`
}

// Registered is what pantry answered for the registration: the name echoed
// back. The rest of the answer is tier 1's listing entry, which a publish has
// no use for — the writes that follow name the project by the path.
type Registered struct {
	Name string `json:"name"`
}

// Published is what pantry answered for the version: the tag verbatim, the
// parsed x.y.z (empty when the tag is not semver — a fact with a consequence,
// not a failure), and whether the release was yanked.
type Published struct {
	Tag     string `json:"tag"`
	Version string `json:"version,omitempty"`
	Yanked  bool   `json:"yanked"`
}

// Declared is what pantry answered for the edge: the target, the range
// verbatim, and the dependency class.
type Declared struct {
	Name         string `json:"name"`
	VersionRange string `json:"version_range"`
	Dependency   string `json:"dependency"`
}

// Proposal is what the command will do, built and printed BEFORE anything is
// written. The approval-card pattern's whole point: the human confirms a
// document, not a vibe.
type Proposal struct {
	Registration Registration  `json:"registration"`
	Tag          string        `json:"tag"`
	Requirements []Requirement `json:"requirements"`
}

// Step is one write's outcome. Name is the thing the operation acted on — the
// project for register and publish_version, the TARGET for declare_requirement,
// which is the end of the edge the manifest's reader cannot see from the
// proposal alone. Detail carries the server's own words on a failure and the
// idempotent phrase on an already-done — never a paraphrase, because an agent
// reading the report must be able to grep the server's log.
type Step struct {
	Operation string `json:"operation"`
	Name      string `json:"name,omitempty"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
}

// Report is the machine-readable outcome: the proposal, one step per write in
// order, the verdict, and — when nothing was written — the refusal that says
// why. The proposal is in the report and not only in the pre-confirmation
// print, so an agent that ran the command once has the whole story in one
// document.
type Report struct {
	Proposal  Proposal `json:"proposal"`
	Steps     []Step   `json:"steps"`
	Published bool     `json:"published"`
	Refusal   string   `json:"refusal,omitempty"`
}

// ProposalFor builds the proposal from a validated manifest. It is the pure
// half of the command — the half with no network, no terminal and no clock —
// so the decisions below are testable as a table:
//
//   - the manifest's language is the registry's vocabulary, and `spec` is not
//     in it: a specification repository is not a project and cannot be
//     registered (pantry's own validator says so in as many words).
//   - the kind is DERIVED from the contract surface when the manifest declares
//     one, and required from the publisher when it does not — the two curated
//     values are a judgement the manifest cannot make (pantry's ProjectKind).
//   - the manifest is normalized through encoding/json and hashed, so the
//     bytes caf sends and the bytes pantry re-encodes to check manifest_sha256
//     are the same bytes. A hash of the YAML-to-JSON conversion would be a
//     hash of bytes the server never sees, and the 400 it produced would blame
//     the publisher for caf's encoding.
//   - a dependency with no version is refused here rather than sent: pantry
//     resolves a range against a target's releases at write time, and an
//     unbounded edge is a 400 whose detail names a rule the manifest already
//     knew.
func ProposalFor(manifest contract.Manifest, tag, kind string) (Proposal, error) {
	language := manifest.Language()
	if language == "spec" {
		return Proposal{}, fmt.Errorf("language `spec` is for specification-only repositories, which are not projects " +
			"and cannot be registered; a cafaye.yml that declares no contract surface of its own is a specification")
	}

	derived, ok := deriveKind(manifest)
	if !ok {
		// The curated case. The manifest declares no contract surface, so it
		// cannot say whether it is a binary or a server — and a `worker` with
		// nothing to derive from is a claim somebody has to make.
		if kind == "" {
			return Proposal{}, fmt.Errorf("the manifest declares no contract surface, so caf cannot derive whether this " +
				"project is a binary, a server or an event worker — pass --kind with what it is (api, worker, both, cli)")
		}
		derived = kind
	}

	document, normalized, err := manifestDocument(manifest)
	if err != nil {
		return Proposal{}, err
	}
	sum := sha256.Sum256(normalized)

	core, err := manifest.CoreConstraint()
	if err != nil {
		return Proposal{}, fmt.Errorf("the manifest's `core` does not parse: %w", err)
	}

	requirements := make([]Requirement, 0, len(manifest.Dependencies()))
	for _, dep := range manifest.Dependencies() {
		if strings.TrimSpace(dep.Version) == "" {
			return Proposal{}, fmt.Errorf("dependency `%s` declares no version: a compatibility edge needs a range, "+
				"and the registry resolves it against the target's releases at write time — add `version: ^X.Y.Z` "+
				"to the dependency in cafaye.yml", dep.Name)
		}
		class := DepRequired
		if !dep.Required {
			class = DepSoft
		}
		requirements = append(requirements, Requirement{
			Name:         dep.Name,
			VersionRange: dep.Version,
			Dependency:   class,
		})
	}

	proposal := Proposal{
		Registration: Registration{
			Name:        manifest.ServiceName(),
			Description: manifest.Description(),
			Language:    language,
			Kind:        derived,
			Core:        core.String(),
			Consumes:    manifest.Consumes(),
			Repository: Repository{
				URL:           manifest.RepositoryURL(),
				DefaultBranch: manifest.DefaultBranch(),
			},
			Owner: Owner{
				Team:    manifest.OwnerTeam(),
				Contact: manifest.OwnerContact(),
			},
			Manifest:       document,
			ManifestSHA256: hex.EncodeToString(sum[:]),
			State:          StatePublished,
		},
		Tag:          tag,
		Requirements: requirements,
	}
	if api := manifest.APIDocumentPath(); api != "" {
		proposal.Registration.Exposes = &Exposes{
			API:    api,
			Events: manifest.Publishes(),
		}
	}
	return proposal, nil
}

// deriveKind is pantry's ProjectKind table for the three derivable rows: the
// contract surface says what the project is, and the two curated rows are left
// to the publisher.
func deriveKind(manifest contract.Manifest) (string, bool) {
	servesHTTP := manifest.ServesHTTP()
	consumes := len(manifest.Consumes()) > 0
	events := len(manifest.Publishes()) > 0
	switch {
	case servesHTTP && consumes:
		return KindBoth, true
	case servesHTTP:
		return KindAPI, true
	case consumes || events:
		return KindWorker, true
	default:
		return "", false
	}
}

// manifestDocument returns the manifest as a JSON document — the decoded map
// the registration carries — and the exact bytes it is encoded as. The two
// come from one round trip so they cannot disagree: the map re-marshals to the
// bytes, and the bytes are what manifest_sha256 is computed over.
//
// The round trip is through encoding/json on purpose. The YAML-to-JSON
// conversion's byte order and escaping are goccy's; the bytes on the wire are
// Go's; pantry hashes the re-encode of what it decoded, so the bytes caf hashes
// must be the bytes a Go json.Marshal of the decoded document produces.
// Diverging here is a 400 that blames the publisher for a bug in the client.
func manifestDocument(manifest contract.Manifest) (map[string]any, []byte, error) {
	var document map[string]any
	if err := json.Unmarshal(manifest.DocumentJSON(), &document); err != nil {
		return nil, nil, fmt.Errorf("encode the manifest for the registry: %w", err)
	}
	normalized, err := json.Marshal(document)
	if err != nil {
		return nil, nil, fmt.Errorf("encode the manifest for the registry: %w", err)
	}
	return document, normalized, nil
}

// Run executes the three writes in order — register, publish the version,
// declare each requirement — and reports every outcome.
//
// The order is the semantics: a version cannot be published for a project that
// is not registered, and an edge cannot be declared from a project that does
// not exist. A 409 that means the write already happened is a report line and
// the sequence continues; any other failure NAMES itself on the step and stops
// the sequence, because the steps after a 401 would be three copies of the same
// sentence and the first one already says what to fix.
func Run(ctx context.Context, p Publisher, token string, proposal Proposal) Report {
	report := Report{Proposal: proposal}

	name := proposal.Registration.Name

	_, err := p.Register(ctx, token, proposal.Registration)
	if err != nil {
		if idempotent(err, idempotentRegistered) {
			report.Steps = append(report.Steps, Step{OpRegister, name, StatusAlreadyExists, err.Error()})
		} else {
			report.Steps = append(report.Steps, Step{OpRegister, name, StatusFailed, err.Error()})
			return report
		}
	} else {
		report.Steps = append(report.Steps, Step{OpRegister, name, StatusCreated, ""})
	}

	if _, err := p.PublishVersion(ctx, token, name, proposal.Tag); err != nil {
		if idempotent(err, idempotentPublished) {
			report.Steps = append(report.Steps, Step{OpVersion, name, StatusAlreadyExists, err.Error()})
		} else {
			report.Steps = append(report.Steps, Step{OpVersion, name, StatusFailed, err.Error()})
			return report
		}
	} else {
		report.Steps = append(report.Steps, Step{OpVersion, name, StatusCreated, ""})
	}

	for _, req := range proposal.Requirements {
		if _, err := p.DeclareRequirement(ctx, token, name, req); err != nil {
			if idempotent(err, idempotentDeclared) {
				report.Steps = append(report.Steps, Step{OpRequirement, req.Name, StatusAlreadyExists, err.Error()})
				continue
			}
			report.Steps = append(report.Steps, Step{OpRequirement, req.Name, StatusFailed, err.Error()})
			continue
		}
		report.Steps = append(report.Steps, Step{OpRequirement, req.Name, StatusCreated, ""})
	}

	report.Published = true
	for _, step := range report.Steps {
		if step.Status == StatusFailed {
			report.Published = false
			break
		}
	}
	return report
}

// idempotent asks whether a failure is pantry's "the write already happened"
// answer rather than a refusal. The status must be 409 AND the detail must
// carry the operation's own phrase: the ordering rule's 409 ("no published
// semver version of …") is a refusal that arrives under the same status, and
// calling it idempotent would tell a publisher their edge was stored when the
// database had rolled it back.
//
// An unrecognized 409 detail is NOT idempotent. If pantry ever rewords a
// detail, this reports a named failure rather than a false "already done" —
// the safe direction, because the retry is cheap and the false success is not.
func idempotent(err error, phrase string) bool {
	var perr *resolve.PantryError
	if errors.As(err, &perr) && perr.Status == http.StatusConflict {
		return strings.Contains(perr.Detail, phrase)
	}
	return false
}

// HTTPPublisher is the real Publisher: one POST per operation against pantry's
// write routes, publisher-authenticated through the caller's caf token. It is
// the write half of the same client resolve reads the catalog with — same
// origin, same bearer, same problem envelope — because a registry client that
// had two ideas about pantry's wire shape would eventually have two shapes.
type HTTPPublisher struct {
	// BaseURL is pantry's origin, e.g. https://pantry.cafaye.com. Empty is
	// refused rather than defaulted, for the reason HTTPPantry refuses it: a
	// client with no endpoint is a client that would POST a relative path.
	BaseURL string
	// Client is the HTTP client. Nil means one with a timeout.
	Client *http.Client
	// Timeout bounds each call. The writes are few and a human is waiting.
	Timeout time.Duration
}

func (c HTTPPublisher) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultPublishTimeout
	}
	return &http.Client{Timeout: timeout}
}

// Register is POST /v1/projects.
func (c HTTPPublisher) Register(ctx context.Context, token string, reg Registration) (Registered, error) {
	var out Registered
	if err := c.post(ctx, token, "/v1/projects", reg, &out); err != nil {
		return Registered{}, err
	}
	return out, nil
}

// PublishVersion is POST /v1/projects/{name}/versions.
func (c HTTPPublisher) PublishVersion(ctx context.Context, token, name, tag string) (Published, error) {
	var out Published
	body := struct {
		Tag string `json:"tag"`
	}{Tag: tag}
	if err := c.post(ctx, token, "/v1/projects/"+name+"/versions", body, &out); err != nil {
		return Published{}, err
	}
	return out, nil
}

// DeclareRequirement is POST /v1/projects/{name}/requirements, one edge per
// call: the route takes one declaration, and a batch would be a second route
// nobody serves.
func (c HTTPPublisher) DeclareRequirement(ctx context.Context, token, name string, req Requirement) (Declared, error) {
	var out Declared
	if err := c.post(ctx, token, "/v1/projects/"+name+"/requirements", req, &out); err != nil {
		return Declared{}, err
	}
	return out, nil
}

// post issues one POST and decodes a 2xx body into out. A non-2xx answer
// becomes the pantry error carrying the problem's own code and detail — the
// same envelope decode the read path uses, because pantry names its refusals
// in the body and a client that paraphrased it would be a client whose
// failures cannot be grepped against the server's log.
func (c HTTPPublisher) post(ctx context.Context, token, path string, body, out any) error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return &resolve.PantryError{Code: "no_catalog", Detail: "no pantry URL configured"}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, strings.NewReader(string(encoded)))
	if err != nil {
		return fmt.Errorf("build the pantry request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("call pantry: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read pantry's answer: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return resolve.ProblemError(res.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode pantry's answer: %w", err)
	}
	return nil
}

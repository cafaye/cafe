package boxd

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// World is everything a route may touch. It is an interface because the seam
// is the rule: what a handler can do to the box is exactly the methods listed
// here, and a route that needs one of them added gets a review of THIS type
// rather than a conversation about what the handler was thinking.
//
// There is deliberately no method that runs a command and no method that
// returns a Secret. The first is why "no arbitrary exec" is structural rather
// than aspirational — `exec.Command` does not exist on this interface — and the
// second is why a secret-read path cannot be added by a handler that was never
// given a way to ask for one.
type World interface {
	// Box identity: the enrolled facts about this box. Read-only, and holds
	// no key material — the identity is a name and an address, and the key is
	// the transport's, loaded per connection in client.go.
	Identity() Identity
	// Stack is what the substrate is running: the services, their phase, and
	// the install phase the last plan reached. Read-only.
	Stack(ctx context.Context) (StackStatus, error)
	// Apply advances the substrate by one phase of the recorded plan.
	Apply(ctx context.Context, phase int) (ApplyResult, error)
	// Envelope receives a sealed provider credential and installs it on the
	// box as a Kubernetes Secret. The input is ciphertext this daemon cannot
	// read without the box key, and the output is a status — the value stays
	// on the box, which is invariant 3: secrets are born where they are used.
	Envelope(ctx context.Context, env Envelope) (EnvelopeResult, error)
}

// Handler is one route's implementation. It receives the world it may touch,
// the capability's decoded parameters, and returns the answer — whose type the
// load-bearing test walks before this package is allowed to ship.
type Handler[In, Out any] func(ctx context.Context, w World, in In) (Out, error)

// Route is one entry in the whitelist: what it is called on the wire, what a
// reader is told it does, whether it mutates, and its typed input and output.
//
// In and Out are zero values rather than reflect machinery, so the wire shape
// is the Go type the handler actually takes and returns — a route whose
// documented answer and its real one disagree is a bug the compiler can see.
type Route struct {
	// Name is the wire name conductor's capability carries. It is matched
	// exactly: there is no prefix matching, no fallback, no wildcard.
	Name string
	// Summary is one sentence a reader of the table understands.
	Summary string
	// ReadOnly is whether the route mutates the box. It is a statement about
	// the route rather than an annotation for a host: a conductor will only
	// send a mutating route with a capability a human approved, and this field
	// is how the daemon says which ones those are.
	ReadOnly bool
	// In and Out are zero values of the route's parameter and answer types.
	In  any
	Out any
	// run dispatches a decoded parameter through the typed handler. It exists
	// so Redeem can drive the table through one erased signature without the
	// table losing its typed handlers, and it is set by Register rather than
	// written by hand anywhere.
	run func(ctx context.Context, w World, params json.RawMessage) (any, error)
}

// Table is the whitelist, in the order `caf boxd routes` renders it. It is a
// value rather than a map because order is part of the answer: the list a
// human reads to find out what their box can be told to do should not change
// between two runs of one binary.
type Table []Route

// Register returns the table with every handler bound. It is a function so the
// one construction of the whitelist is a single expression a test renders, and
// so no caller can be handed a half-built table missing its handlers.
func Register() Table {
	return Table{
		boxStatusRoute(),
		stackStatusRoute(),
		stackApplyRoute(),
		providerConnectRoute(),
	}
}

// Names is the route names in table order, for help output and for the test
// that asserts the table and the rendered list agree.
func (t Table) Names() []string {
	names := make([]string, len(t))
	for i, r := range t {
		names[i] = r.Name
	}
	return names
}

// Lookup is the whitelist decision. It returns the route and whether it is in
// the table — an unknown name is a refusal the caller reports by name, which
// is the whole of the whitelist: a capability naming a route that is not here
// never reaches a handler at all.
func (t Table) Lookup(name string) (Route, bool) {
	for _, r := range t {
		if r.Name == name {
			return r, true
		}
	}
	return Route{}, false
}

// Redeem is the one entry point a capability has into the box. It refuses, by
// name and in order: a route not in the table, parameters that do not decode
// into that route's own input, a capability bound to a different box, and one
// whose TTL has passed. Only then does a handler run, through the world it was
// given, and whatever comes back is the answer — which the load-bearing test
// has already proven carries no Secret-shaped value.
//
// The order is the argument. A capability naming an unknown route is refused
// before its parameters are even looked at, because "the box cannot do that"
// is the first thing the wire is entitled to be told; and the box binding is
// checked before expiry so that a capability replayed against the wrong box
// is a wrong-box refusal rather than an expiry that hides the replay.
func Redeem(ctx context.Context, table Table, w World, cap Capability) (any, Outcome, error) {
	route, known := table.Lookup(cap.Route)
	if !known {
		return nil, refuse(cap, RouteRefused, fmt.Sprintf("route %q is not in this box's table", cap.Route)), nil
	}
	if route.run == nil {
		return nil, refuse(cap, RouteRefused, fmt.Sprintf("route %q has no handler bound", cap.Route)), nil
	}
	if w.Identity().BoxID != cap.BoxID {
		return nil, refuse(cap, WrongBox,
			fmt.Sprintf("capability is bound to box %q and this box is %q", cap.BoxID, w.Identity().BoxID)),
			nil
	}
	if !cap.alive(time.Now()) {
		return nil, refuse(cap, Expired, fmt.Sprintf("capability expired at %s", cap.ExpiresAt.Format(time.RFC3339))), nil
	}
	answer, err := route.run(ctx, w, cap.Params)
	if err != nil {
		return nil, Outcome{CapabilityID: cap.ID, Route: cap.Route, Status: Failed, Message: err.Error()}, err
	}
	return answer, Outcome{CapabilityID: cap.ID, Route: cap.Route, Status: Accepted}, nil
}

// refuse builds the outcome for a capability the daemon will not act on. The
// message is the answer: a refusal nobody can read is a refusal nobody can
// act on, and a capability refused for the wrong reason sends an operator
// looking in the wrong place.
func refuse(cap Capability, status Status, why string) Outcome {
	return Outcome{CapabilityID: cap.ID, Route: cap.Route, Status: status, Message: why}
}

// ---------------------------------------------------------------------------
// the four routes
// ---------------------------------------------------------------------------

// boxStatus answers "who is this box and is it well". It is the route a
// conductor polls to know an enrollment is alive, and it carries only
// enrolled facts — a name, an address, a version — because there is nothing
// about a box that conductor needs which is not already in its own record.
func boxStatusRoute() Route {
	type status struct {
		BoxID     string `json:"box_id"`
		Version   string `json:"version"`
		Conductor string `json:"conductor"`
	}
	h := Handler[struct{}, status](func(_ context.Context, w World, _ struct{}) (status, error) {
		id := w.Identity()
		return status{BoxID: id.BoxID, Version: id.Version, Conductor: id.Conductor}, nil
	})
	return route("box.status", "report the box's enrolled identity and the daemon's version", true, struct{}{}, status{}, h)
}

// stackStatus answers "what is the substrate running and where did the install
// plan stop". Read-only, and the answer is phase and service names — never
// the configuration a service was given, which is where credentials live.
func stackStatusRoute() Route {
	type status struct {
		Services []string `json:"services"`
		Phase    int      `json:"phase"`
		Detail   string   `json:"detail,omitempty"`
	}
	h := Handler[struct{}, status](func(ctx context.Context, w World, _ struct{}) (status, error) {
		s, err := w.Stack(ctx)
		if err != nil {
			return status{}, err
		}
		return status{Services: s.Services, Phase: s.Phase, Detail: s.Detail}, nil
	})
	return route("stack.status", "report which substrate services run and the phase the install reached", true, struct{}{}, status{}, h)
}

// stackApply advances the substrate by one phase. It is the mutating half of
// the install loop and is the reason a capability is single-use and
// short-TTL: the capability that says "apply phase 3" is worth exactly one
// application for exactly a few minutes, which is invariant 2.
func stackApplyRoute() Route {
	type params struct {
		Phase int `json:"phase"`
	}
	type result struct {
		Applied int    `json:"applied"`
		Phase   int    `json:"phase"`
		Detail  string `json:"detail,omitempty"`
	}
	h := Handler[params, result](func(ctx context.Context, w World, in params) (result, error) {
		if in.Phase < 0 {
			return result{}, fmt.Errorf("phase %d is not a phase", in.Phase)
		}
		r, err := w.Apply(ctx, in.Phase)
		if err != nil {
			return result{}, err
		}
		return result{Applied: r.Applied, Phase: r.Phase, Detail: r.Detail}, nil
	})
	return route("stack.apply", "apply one phase of the recorded substrate plan", false, params{}, result{}, h)
}

// providerConnect is the sensitive one: it receives a provider credential
// sealed to this box's public key and installs it on the box as a Kubernetes
// Secret. The envelope arrives as opaque bytes and leaves as a status. No
// answer this route can produce contains the credential, because the only
// type that holds one is Secret, and Secret-shaped types are what the
// load-bearing test hunts for across every route in the table — including
// this one, the route most likely to grow the path invariant 4 forbids.
func providerConnectRoute() Route {
	type params struct {
		Provider string          `json:"provider"`
		Envelope json.RawMessage `json:"envelope"`
	}
	type result struct {
		Provider string `json:"provider"`
		Installed bool  `json:"installed"`
		Detail   string `json:"detail,omitempty"`
	}
	h := Handler[params, result](func(ctx context.Context, w World, in params) (result, error) {
		if in.Provider == "" {
			return result{}, fmt.Errorf("no provider named")
		}
		r, err := w.Envelope(ctx, Envelope{Provider: in.Provider, Ciphertext: in.Envelope})
		if err != nil {
			return result{}, err
		}
		return result{Provider: in.Provider, Installed: r.Installed, Detail: r.Detail}, nil
	})
	return route("provider.connect", "install a provider credential sealed to this box, as a Kubernetes Secret", false, params{}, result{}, h)
}

// route constructs one table entry with its handler bound. In and Out are
// carried as zero values so the walk in secret_test.go runs over the DECLARED
// types — the answer a route would produce — rather than over a value that
// only exists once a capability has arrived.
func route[I, O any](name, summary string, readOnly bool, in I, out O, h Handler[I, O]) Route {
	return Route{
		Name:     name,
		Summary:  summary,
		ReadOnly: readOnly,
		In:       in,
		Out:      out,
		run: func(ctx context.Context, w World, params json.RawMessage) (any, error) {
			var decoded I
			if len(params) > 0 {
				if err := json.Unmarshal(params, &decoded); err != nil {
					return nil, fmt.Errorf("route %s: parameters do not decode: %w", name, err)
				}
			}
			answer, err := h(ctx, w, decoded)
			if err != nil {
				return nil, err
			}
			return answer, nil
		},
	}
}

// SortedNames is the table's names, sorted, for the two tests that want the
// whitelist as a set and the help rendering that wants it stable.
func (t Table) SortedNames() []string {
	names := t.Names()
	sort.Strings(names)
	return names
}

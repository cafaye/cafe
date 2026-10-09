package boxd

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This file is invariant 4 as code: "the enforcement lives on the box, not in
// the agent's instructions — a whitelist enforced by the box daemon, asserted
// by a test that renders the route table."
//
// It renders EVERY route in the table through a recording world, walks the
// answer each one produces, and fails if any reachable type in any answer is
// Secret-shaped. A route table with a secret-read path is a route table that
// fails here, which is the difference between a security boundary and a
// paragraph about one.

// recordingWorld is what every route is rendered through. It answers each
// world question with a value that stands in for the real box: a StackStatus
// with every field set, an ApplyResult, an installed envelope. The point is
// that the routes run for real — a handler that panicked, or answered a shape
// its declaration does not match, fails this file before the walk does.
type recordingWorld struct {
	identity Identity
	stack    StackStatus
}

func (r recordingWorld) Identity() Identity { return r.identity }
func (r recordingWorld) Stack(context.Context) (StackStatus, error) {
	return r.stack, nil
}
func (r recordingWorld) Apply(_ context.Context, phase int) (ApplyResult, error) {
	return ApplyResult{Applied: 1, Phase: phase, Detail: "applied"}, nil
}
func (r recordingWorld) Envelope(_ context.Context, env Envelope) (EnvelopeResult, error) {
	// The recording world records that it was ASKED and reports installation.
	// It does not decode the ciphertext: what the envelope contains is the
	// box key's business and this seam stands in for the box, not the key.
	return EnvelopeResult{Installed: true}, nil
}

// TestEveryRouteRefusesToAnswerWithASecret is the load-bearing test of the
// whole packet. Two halves, in one render: each route is driven through the
// world with a representative capability, and the answer is walked. A
// Secret-shaped type reachable from ANY route's answer fails the suite and
// names the route and the path it was found at.
func TestEveryRouteRefusesToAnswerWithASecret(t *testing.T) {
	table := Register()
	if len(table) == 0 {
		t.Fatal("the route table is empty; there is nothing to enforce a whitelist over")
	}
	world := recordingWorld{
		identity: Identity{BoxID: "box-test", Conductor: "conductor.example.com:443", Version: "test"},
		stack:    StackStatus{Services: []string{"k3s", "identity", "courier"}, Phase: 3, Detail: "phase 3 of 8"},
	}

	for _, route := range table {
		t.Run(route.Name, func(t *testing.T) {
			params := representativeParams(t, route)
			answer, err := route.run(context.Background(), world, params)
			if err != nil {
				t.Fatalf("route %s failed to render: %v", route.Name, err)
			}
			if found := secretShapedPath(reflect.TypeOf(answer), route.Name); found != "" {
				t.Fatalf("route %s answered with a Secret-shaped value at %s; "+
					"a route that can return a secret is a secret-read path, and invariant 4 forbids it",
					route.Name, found)
			}
			// The declared output type is walked too, not only the value that
			// arrived: a route whose handler happens to return the zero value
			// of a Secret-bearing type is the same defect with a quieter
			// symptom, and the walk over the DECLARED type catches it before a
			// capability does.
			if found := secretShapedPath(reflect.TypeOf(route.Out), route.Name); found != "" {
				t.Fatalf("route %s declares an answer with a Secret-shaped value at %s", route.Name, found)
			}
			if found := secretShapedPath(reflect.TypeOf(route.In), route.Name); found != "" {
				t.Fatalf("route %s declares a parameter with a Secret-shaped value at %s; "+
					"a parameter that IS a secret arrives as ciphertext — see provider.connect",
					route.Name, found)
			}
		})
	}
}

// representativeParams builds the parameters a real capability for this route
// would carry. It is derived from the route's own declared input type by
// filling each string field with its own name, so a route stays renderable
// when its input grows — a hand-written table of parameters would be a second
// copy of the routes' shapes, and it would drift.
func representativeParams(t *testing.T, r Route) json.RawMessage {
	t.Helper()
	v := reflect.New(reflect.TypeOf(r.In)).Elem()
	fillStrings(v)
	// json.RawMessage fields must stay valid JSON: the fill above put a bare
	// string in them, so the ones that are raw messages are re-filled with
	// empty JSON bytes — which is what a route that takes no parameters sees.
	fixRawMessages(v)
	data, err := json.Marshal(v.Interface())
	if err != nil {
		t.Fatalf("route %s: representative parameters do not encode: %v", r.Name, err)
	}
	return data
}

func fillStrings(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(v.Type().Name() + "-value")
		}
	case reflect.Struct:
		for i := range v.NumField() {
			fillStrings(v.Field(i))
		}
	case reflect.Pointer:
		fillStrings(v.Elem())
	}
}

// fixRawMessages replaces the string filler inside json.RawMessage fields with
// an empty JSON object, because a raw message is unmarshalled verbatim into
// the target and a bare word is not JSON.
func fixRawMessages(v reflect.Value) {
	if v.Kind() != reflect.Struct {
		return
	}
	for i := range v.NumField() {
		f := v.Field(i)
		if f.Type() == reflect.TypeOf(json.RawMessage{}) {
			f.Set(reflect.ValueOf(json.RawMessage(`{}`)))
			continue
		}
		fixRawMessages(f)
	}
}

// ---------------------------------------------------------------------------
// the negative control: this guard has been watched red
// ---------------------------------------------------------------------------

// controllerSecret is a credential type that carries the marker — what the
// discipline asks of a new secret-shaped type, and what proves the marker
// half of the guard is wired to something.
type controllerSecret struct{ Secret }

// leakyAnswer is the route answer that must not exist: it carries a
// credential in four disguises — the shape itself, behind a pointer, inside a
// map value, and under a name in the list. Each disguise is caught by the same
// walk the real test uses, and the test below drives all of them red on
// purpose. The rule in AGENTS-FIRST is that every invariant has a tripwire
// test; a tripwire nobody has seen fire is a decoration.
type leakyAnswer struct {
	Credential controllerSecret
	Indirect   *Secret
	Values     map[string]PrivateKey
	Token      BearerToken
}

func TestTheGuardRefusesARouteThatWouldReturnASecret(t *testing.T) {
	leaky := Route{Name: "leaky.status", Out: leakyAnswer{}}

	found := secretShapedPath(reflect.TypeOf(leaky.Out), leaky.Name)
	if found == "" {
		t.Fatal("the guard did not catch a route whose answer carries a Credential; it is not guarding anything")
	}
	if !strings.Contains(found, "leaky.status.Credential") {
		t.Errorf("the guard named %q; it must name the route and the field: leaky.status.Credential", found)
	}

	// The pointer half is reached through the same walk.
	var holder struct{ Password *Secret }
	if found := secretShapedPath(reflect.TypeOf(holder), "holder"); found == "" {
		t.Error("the guard did not catch a Secret behind a pointer")
	}

	// A named credential type that forgot the marker is caught by name.
	var named struct{ Thing BearerToken }
	if found := secretShapedPath(reflect.TypeOf(named), "named"); found == "" {
		t.Error("the guard did not catch a BearerToken by its name")
	}

	// A plain struct with no secret-shaped field passes, which is the control
	// over the control: a walk that called everything a secret would pass this
	// test while being useless.
	type clean struct {
		Services []string
		Phase    int
	}
	if found := secretShapedPath(reflect.TypeOf(clean{}), "clean"); found != "" {
		t.Errorf("the guard called a clean answer Secret-shaped at %q", found)
	}
}

// PrivateKey and BearerToken live here because the negative control needs
// them; they are the names a real credential type might carry, and the walk is
// asserted to catch them.

type PrivateKey struct{ PEM string }
type BearerToken struct{ Token string }

// TestTheControllerSecretCarriesTheMarker proves the negative control is
// wired to the marker mechanism the guard uses: the controller type
// implements secretShaped, so the walk above fired for the reason it claims
// rather than for some unrelated one.
func TestTheControllerSecretCarriesTheMarker(t *testing.T) {
	var c controllerSecret
	if !reflect.TypeOf(c).Implements(reflect.TypeOf((*secretShaped)(nil)).Elem()) {
		t.Fatal("controllerSecret does not carry the marker; the negative control is not testing the guard")
	}
}

// ---------------------------------------------------------------------------
// the whitelist itself
// ---------------------------------------------------------------------------

// The four refusals are the whitelist, driven through Redeem rather than
// inspected as values: a table that merely LOOKS bounded is not a whitelist
// until something is refused by it.
func TestRedeemRefusesWhatIsNotInTheTable(t *testing.T) {
	table := Register()
	world := recordingWorld{identity: Identity{BoxID: "box-test", Version: "test"}}

	tests := []struct {
		name string
		cap  Capability
		want Status
	}{
		{
			name: "an unknown route is the whitelist firing",
			cap:  Capability{ID: "c1", BoxID: "box-test", Route: "shell.exec"},
			want: RouteRefused,
		},
		{
			name: "a read path that does not exist either",
			cap:  Capability{ID: "c2", BoxID: "box-test", Route: "secret.read"},
			want: RouteRefused,
		},
		{
			name: "a capability for another box is refused by name",
			cap:  Capability{ID: "c3", BoxID: "box-other", Route: "box.status"},
			want: WrongBox,
		},
		{
			name: "an expired capability never reaches a handler",
			cap:  Capability{ID: "c4", BoxID: "box-test", Route: "box.status", ExpiresAt: time.Now().Add(-time.Minute)},
			want: Expired,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, outcome, err := Redeem(context.Background(), table, world, tt.cap)
			if err != nil {
				t.Fatalf("a refusal is not an error to the caller: %v", err)
			}
			if outcome.Status != tt.want {
				t.Fatalf("status = %q, want %q (message: %s)", outcome.Status, tt.want, outcome.Message)
			}
			if outcome.Message == "" {
				t.Error("a refusal with no message is a refusal nobody can act on")
			}
		})
	}
}

// Every route in the table is reachable by its own name: Redeem accepts each
// one's capability, which is the other half of the whitelist — the table is
// not a graveyard of unreachable routes.
func TestEveryRouteInTheTableRedeems(t *testing.T) {
	table := Register()
	world := recordingWorld{
		identity: Identity{BoxID: "box-test", Version: "test"},
		stack:    StackStatus{Services: []string{"k3s"}, Phase: 1},
	}
	for _, route := range table {
		t.Run(route.Name, func(t *testing.T) {
			cap := Capability{
				ID:        "cap-" + route.Name,
				BoxID:     "box-test",
				Route:     route.Name,
				Params:    representativeParams(t, route),
				ExpiresAt: time.Now().Add(time.Minute),
			}
			answer, outcome, err := Redeem(context.Background(), table, world, cap)
			if err != nil {
				t.Fatalf("redeem: %v", err)
			}
			if outcome.Status != Accepted {
				t.Fatalf("status = %q, want %q (message: %s)", outcome.Status, Accepted, outcome.Message)
			}
			if answer == nil {
				t.Fatal("an accepted capability answered with nothing; a route declares a return type for a reason")
			}
		})
	}
}

// Parameters that do not decode into the route's own input are refused
// naming the route. A capability cannot smuggle a shape the route never
// declared, which is what makes the whitelist typed rather than textual.
func TestRedeemRefusesParametersTheRouteDidNotDeclare(t *testing.T) {
	table := Register()
	world := recordingWorld{identity: Identity{BoxID: "box-test"}}
	cap := Capability{
		ID:        "cap-bad",
		BoxID:     "box-test",
		Route:     "box.status",
		Params:    json.RawMessage(`"a string where an object belongs"`),
		ExpiresAt: time.Now().Add(time.Minute),
	}
	_, outcome, err := Redeem(context.Background(), table, world, cap)
	if err == nil {
		t.Fatal("parameters of the wrong shape were accepted")
	}
	if outcome.Status != Failed {
		t.Errorf("status = %q, want %q", outcome.Status, Failed)
	}
	if !strings.Contains(outcome.Message, "box.status") {
		t.Errorf("the failure message does not name the route: %s", outcome.Message)
	}
}

// ---------------------------------------------------------------------------
// no arbitrary exec, asserted over the source rather than the behaviour
// ---------------------------------------------------------------------------

// TestTheDaemonCannotExec walks this package's own source and fails if it
// imports os/exec. This is the structural half of "no arbitrary exec path": a
// route cannot run a command because the World interface offers no method
// that would, and this test holds the line at the package boundary — a future
// edit that adds a process to run has to delete this check to do it, which is
// a deletion a reviewer sees rather than a route nobody notices.
func TestTheDaemonCannotExec(t *testing.T) {
	forbidden := []string{"os/exec", "syscall"}
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			imported := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if imported == bad {
					t.Errorf("%s imports %s; a daemon that can start a process has an arbitrary-exec path", path, bad)
				}
			}
		}
	}
}

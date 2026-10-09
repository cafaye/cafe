package boxd

import (
	"reflect"
	"strings"
)

// Secret is the shape of a value whose disclosure is the failure this package
// exists to prevent: a credential, a key, a password, a token. It is a type
// rather than a comment so the load-bearing test can hunt for it, and it is
// declared HERE — in the daemon's own package — rather than imported, because
// the daemon defines what it is forbidden to return and a definition that
// lived somewhere else could be changed by somewhere else.
//
// No route returns one. `provider.connect` is the route that handles the most
// sensitive capability there is — a provider credential sealed to the box's
// public key — and even it answers with a status, not with the value.
type Secret struct {
	// Name is which secret this is, which is not itself secret and is what a
	// log line may carry.
	Name string
	// Value is the secret. It exists on this type so the type is honest about
	// what it is; it is never rendered into an answer, never printed, and the
	// test that walks every route fails if this type appears in one.
	Value string
}

// secretShaped is the marker a type carries to declare that it IS a secret, or
// carries one. It is the compile-time half of the guard: a new type somebody
// adds to represent a credential implements this (or is named `Secret`), and
// the walk finds it whichever way it was declared.
type secretShaped interface {
	secretShape()
}

func (Secret) secretShape() {}

// secretNames are the type and field names that make a value Secret-shaped on
// their own, normalized (lowercase, separators removed) so `PrivateKey`,
// `private_key` and `privatekey` are one name. A credential type that forgot
// to implement the marker is still caught by name, and a field called
// "Password" is caught whatever its type is called — which is the belt to the
// marker's braces: the walk fails on any of the three signals, so the guard
// does not depend on a discipline a future edit might forget.
var secretNames = map[string]bool{
	"secret":      true,
	"password":    true,
	"passphrase":  true,
	"credential":  true,
	"privatekey":  true,
	"signingkey":  true,
	"apikey":      true,
	"bearertoken": true,
	"token":       true,
}

// normalize makes one name comparable against secretNames: Go's exported names
// are StudlyCased and a secret is spelled the same way in snake_case, so both
// fold to the same key.
func normalize(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '_', '-', ' ':
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// secretShapedAt walks the type graph reachable from t and reports the first
// path that reaches a Secret-shaped value, as a sentence naming the field
// route ("box.Status.via Key"). An empty return means nothing reachable is
// Secret-shaped, which is the only verdict a route answer may produce.
//
// The walk covers the whole graph rather than the top level: a Secret buried
// in a map value, behind a pointer, inside a slice inside a struct, or reached
// through an interface's method set are all reachable from a caller who walks
// the answer, so all of them are walked here. Visited named types are cut off
// — a type graph with a cycle terminates at the cycle rather than at the walk.
func secretShapedPath(t reflect.Type, path string) string {
	seen := map[reflect.Type]bool{}
	return walk(t, path, seen)
}

func walk(t reflect.Type, path string, seen map[reflect.Type]bool) string {
	if t == nil {
		return ""
	}
	// named looks at the type itself before unwrapping: a named type can be
	// secret-shaped whatever it resolves to, and `type Envelope Secret` is
	// exactly the disguise the name walk is for.
	if t.Name() != "" {
		if secretNames[normalize(t.Name())] {
			return path + " (" + t.String() + " is Secret-shaped)"
		}
		if secretShapedType(t) {
			return path + " (" + t.String() + " declares itself Secret-shaped)"
		}
		if seen[t] {
			return ""
		}
		seen[t] = true
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map, reflect.Chan:
		return walk(t.Elem(), path+"[]", seen)
	case reflect.Struct:
		// A named struct is walked field by field, and every reachable field
		// is walked because the route answer is marshalled for the wire in the
		// same shape. The field's NAME is checked as well as its type: an
		// answer field called "Password" is a secret-read path whatever type
		// it was declared with.
		for i := range t.NumField() {
			f := t.Field(i)
			if secretNames[normalize(f.Name)] {
				return path + "." + f.Name + " (the field name is Secret-shaped)"
			}
			field := f.Name
			if field == "" {
				field = f.Type.String()
			}
			if found := walk(f.Type, path+"."+field, seen); found != "" {
				return found
			}
		}
	case reflect.Interface:
		// An interface's own method set is not itself a secret, but a value
		// behind it could be, and the caller cannot know which implementation
		// arrived — so an interface in an answer is a refusal by default,
		// unless the interface is the empty one (`any`), which carries nothing.
		if t.NumMethod() > 0 {
			return path + " (" + t.String() + " could carry any implementation; an answer names its concrete types)"
		}
	case reflect.Func:
		return path + " (a function is not an answer)"
	}
	return ""
}

// secretShapedType reports whether t implements secretShaped. It checks the
// TYPE rather than a value, because the guard runs over declared output types
// before any value exists — the whole point is that a route which WOULD return
// a secret is refused when it is written, not when it fires.
func secretShapedType(t reflect.Type) bool {
	if t.Kind() != reflect.Interface {
		// A non-interface implements the marker only if the pointer to it does
		// as well, which is how reflect reports a value-receiver method on the
		// pointer type; checking both covers either receiver choice.
		ptr := reflect.PointerTo(t)
		if ptr.Implements(secretShapedInterface) || t.Implements(secretShapedInterface) {
			return true
		}
	}
	return false
}

var secretShapedInterface = reflect.TypeOf((*secretShaped)(nil)).Elem()

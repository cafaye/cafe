package secret

import "time"

// Class is what the box can do with an entry: rotate it, rotate it with care,
// or refuse.
//
// The three words are the product. An inventory that said "secret" for all of
// them would be a list of names, and the list of names is exactly what an
// operator already has. What they do not have is which of these is a fork
// waiting to happen.
type Class string

const (
	// Rotatable is the class that rotates on a schedule: replace the key in
	// the Kubernetes Secret and restart the deployment. The installer records
	// the posture itself — "rotate by replacing the secret and restarting the
	// pods" (install.sh:1199, :1217, :1246, :1277) — so this class is doing
	// by hand what the runbook already says is the way.
	Rotatable Class = "rotatable"

	// Conditional is the class that CAN be rotated but not by a kubectl patch
	// alone: a second write, a re-mint against identity's own API, an ALTER
	// ROLE in Postgres, or a change at the provider. The reason names which,
	// because a rotation that did the first half and not the second would
	// produce the outage it was run to prevent.
	Conditional Class = "conditional"

	// NotRotatable is the class where a rotation is a fork or a cutover. The
	// reason names which. `caf secret rotate` REFUSES these by name and by
	// reason, which is the whole point of the class: a silent skip is how
	// somebody spends a weekend discovering it.
	NotRotatable Class = "not-rotatable"
)

// Provenance is where the value came from, spelled the way the installer
// spells it: the SECRET RECORD's own three-way split (install.sh:1083-1105)
// plus the two shapes the record does not name.
type Provenance string

const (
	// Generated is born on the box, never printed, generated once and read
	// back (install.sh:1084-1093).
	Generated Provenance = "generated"
	// Minted is minted on the box in step 11 from identity's own API and
	// patched into both halves (install.sh:1094-1100).
	Minted Provenance = "minted"
	// External is shape-validated on the way in and never echoed, because the
	// provider or the relay on the other end is the only party that can hold
	// it (install.sh:1101-1105, :82-88).
	External Provenance = "external"
	// Derived is not itself a credential: it is assembled from one, the way
	// DATABASE_URL is built from the Cluster and a role password
	// (install.sh:1106-1109).
	Derived Provenance = "derived"
	// Configuration is a key the record carries that is not a secret at all —
	// the mail adapter, the SMTP host and port, the OIDC issuer and key id.
	Configuration Provenance = "configuration"
)

// Entry is one thing a box holds.
//
// Every field is a non-secret, and that is not a comment about discipline: the
// struct has no field that could hold a value, so no render, no error and no
// %v over an Entry can carry one. The same property internal/box.Identity has.
type Entry struct {
	// Name is the key as the installer spells it. For the two entries that
	// are not a key in a namespaced Secret — the Postgres role passwords and
	// muse's vault key — it is the runbook's own name for the thing.
	Name string `json:"name"`
	// Namespace is the Kubernetes namespace the Secret lives in. Empty for an
	// entry that is not a namespaced Secret.
	Namespace string `json:"namespace,omitempty"`
	// Secret is the Kubernetes Secret object's name. Empty for an entry that
	// is not one.
	Secret string `json:"secret,omitempty"`
	// Key is the key inside that Secret. Equal to Name for a record entry;
	// different for the Redis password, whose key is `password`.
	Key string `json:"key,omitempty"`
	// Deployment is the Deployment restarted when this entry rotates. Empty
	// for an entry that does not own one.
	Deployment string     `json:"deployment,omitempty"`
	Class      Class      `json:"class"`
	Provenance Provenance `json:"provenance"`
	// Reason is the sentence that says WHY this class, and Source is the file
	// and line it comes from. The two are separate so a renderer can put the
	// citation where a reader can follow it, and so a test can assert every
	// entry has one — an entry with a class and no reason is an opinion.
	Reason string `json:"reason"`
	Source string `json:"source"`
	// Interval is how often the rotatable class is due. Zero for every other
	// class, because a schedule for something that cannot be rotated is a
	// schedule that will fail.
	Interval time.Duration `json:"interval,omitempty"`
	// Shape is the spelling a fresh value is generated in, for the rotatable
	// class only. It is here because the installer's spelling is the contract:
	// identity refuses a SIGNUP_SIGNING_KEY that is not 32 bytes of base64url
	// at boot, and Phoenix refuses a SECRET_KEY_BASE shorter than 64 bytes.
	Shape string `json:"shape,omitempty"`
	// LastRotated is when this box last rotated the entry, RFC3339, from the
	// schedule state. It is absent when the entry has never been rotated here,
	// which is a different statement from "rotated at the epoch" and must not
	// render as one. It is the only field on this struct that is not a fact
	// about the fleet — every other one is true of every box at once.
	LastRotated string `json:"last_rotated,omitempty"`
	// Writes are the OTHER keys a rotation has to replace in the same run.
	// Only the Redis password has one: gateway's REDIS_URL embeds the password,
	// so a rotation that wrote the secret and not the URL would take gateway
	// down rather than rotate anything. It is a field on the entry rather than
	// a special case in the rotator because the second write IS part of what
	// makes the entry rotatable.
	Writes []Target `json:"writes,omitempty"`
	// Restarts are the Deployments a rotation restarts, beyond the entry's own
	// Deployment. The Redis password has one: `redis` in `data` and `gateway`
	// in `gateway` both hold the value.
	Restarts []Target `json:"restarts,omitempty"`
}

// Target is one place a rotation writes to or restarts: a key inside a
// Kubernetes Secret, or a Deployment in a namespace.
type Target struct {
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Key       string `json:"key,omitempty"`
	// Template is how a second write's value is DERIVED from the first. The
	// Redis password is the only user: REDIS_URL is the password with a URL
	// around it. A single verb is the whole grammar because a rotation with
	// two interdependent values is a rotation that can disagree with itself.
	Template string `json:"template,omitempty"`
}

// RestartTargets is every Deployment a rotation of this entry restarts, its own
// first. The order is the installer's: the value lands before anything that
// reads it is bounced.
func (e Entry) RestartTargets() []Target {
	if e.Deployment == "" {
		return append([]Target{}, e.Restarts...)
	}
	out := []Target{{Namespace: e.Namespace, Name: e.Deployment}}
	return append(out, e.Restarts...)
}

// KeyOrName is the key inside the Secret: the entry's Key when it names one,
// and its Name otherwise.
//
// Most entries are a key whose name is the entry's own — COURIER_SECRET_BOX_KEY
// in courier-secrets — and the two that are not (the Redis password's key is
// `password`) say so. A write target built from e.Key raw would write to the
// empty string on most of the table, which kubectl would answer with an error
// naming nothing.
func (e Entry) KeyOrName() string {
	if e.Key == "" {
		return e.Name
	}
	return e.Key
}

// WriteTargets is every key a rotation replaces, its own first.
func (e Entry) WriteTargets() []Target {
	out := []Target{{Namespace: e.Namespace, Name: e.Secret, Key: e.KeyOrName()}}
	return append(out, e.Writes...)
}

// IsSecret is true when the entry is a credential rather than a setting the
// record happens to carry. The inventory enumerates configuration too — the
// record lists it, and an inventory that quietly dropped a third of the record
// would be a second source of truth — but a command deciding what to rotate
// must not treat the mail adapter as a frozen credential.
func (e Entry) IsSecret() bool { return e.Provenance != Configuration }

// The citation targets. They are constants rather than literals in the table
// below for two reasons: a citation that is spelled two ways in two entries is
// a citation a reader cannot grep for, and the test asserts the fleet files
// these name are the ones the table claims. They are fleet paths, not paths in
// this repository — doc.go says why.
const (
	srcRecord      = "cafaye/kit/templates/substrate/install.sh:1088-1093"
	srcGenerated   = "cafaye/kit/templates/substrate/install.sh:1084-1093"
	srcMinted      = "cafaye/kit/templates/substrate/install.sh:1094-1100"
	srcExternal    = "cafaye/kit/templates/substrate/install.sh:1101-1105"
	srcRelayHdr    = "cafaye/kit/templates/substrate/install.sh:85-88"
	srcDerived     = "cafaye/kit/templates/substrate/install.sh:1106-1109"
	srcRotate      = "cafaye/kit/templates/substrate/install.sh:1199,1217,1246,1277"
	srcKeyBase     = "cafaye/kit/templates/substrate/install.sh:1228-1232,1239-1244"
	srcRedisURL    = "cafaye/kit/templates/substrate/install.sh:1239-1245"
	srcRedisPW     = "cafaye/kit/templates/substrate/install.sh:933-939"
	srcRoles       = "cafaye/kit/templates/substrate/install.sh:887,892-909"
	srcNeverPatch  = "cafaye/kit/templates/substrate/install.sh:1159"
	srcIDReadme    = "cafaye/identity/README.md:299"
	srcOIDCJwks    = "cafaye/identity/README.md:1348"
	srcMFAReadme   = "cafaye/identity/README.md:1035-1039"
	srcOAuthReadme = "cafaye/identity/README.md:310"
	srcPairTTL     = "cafaye/identity/README.md:356"
	srcMuseReadme  = "cafaye/muse/README.md:332-333"
	srcPacket      = "moon/CAFAYE-OPS.md:784-786"
)

// DefaultInterval is the schedule the rotatable class runs on. Ninety days is
// the honest number for this fleet: these are values born on one box with no
// second holder, so the only cost of rotating one is a restart, and the only
// cost of not rotating one is age.
const DefaultInterval = 90 * 24 * time.Hour

// Inventory is every entry one box holds, in the order the installer creates
// them: identity, gateway, courier, parlor, then the three the record does not
// carry.
//
// THE ORDER IS THE INSTALLER'S ORDER, and that is a decision rather than a
// convenience. A second ordering — alphabetical, by namespace, by class — would
// be a second claim about what the substrate is, and the installer is the thing
// that decides it.
func Inventory() []Entry {
	return []Entry{
		{
			Name: "SIGNUP_SIGNING_KEY", Namespace: "identity", Secret: "identity-secrets",
			Deployment: "identity", Class: Conditional, Provenance: Generated,
			Reason: "HMAC-SHA256 over the signup timestamp pair; a pair expires one hour after it is minted, so a rotation invalidates only the pairs still in flight — safe, but a form submitted across the rotation fails closed.",
			Source: srcIDReadme + ", " + srcPairTTL,
		},
		{
			Name: "MFA_ENCRYPTION_KEY", Namespace: "identity", Secret: "identity-secrets",
			Deployment: "identity", Class: NotRotatable, Provenance: Generated,
			Reason: "identity seals every enrolled second factor under it. On a lost key every sealed secret is unreadable and every enrolled user fails closed at their second factor, which is why it is never generated at boot and why a pre-existing identity-secrets is never patched.",
			Source: srcMFAReadme + ", " + srcNeverPatch,
		},
		{
			Name: "OAUTH_ENCRYPTION_KEY", Namespace: "identity", Secret: "identity-secrets",
			Deployment: "identity", Class: NotRotatable, Provenance: Generated,
			Reason: "standard base64 of exactly 32 raw bytes, sealing provider tokens at rest, never generated. A rotation leaves every stored provider token sealed under a key no longer present, and there is no re-seal path.",
			Source: srcOAuthReadme,
		},
		{
			Name: "DATABASE_URL", Namespace: "identity", Secret: "identity-secrets",
			Deployment: "identity", Class: Conditional, Provenance: Derived,
			Reason: "not itself a credential: it agrees with the Cluster by construction, and the password inside it is the Postgres role password step 8 generates. Rotate the role, not the URL.",
			Source: srcDerived + ", " + srcRoles,
		},
		{
			Name: "REDIS_URL", Namespace: "gateway", Secret: "gateway-secrets",
			Deployment: "gateway", Class: Conditional, Provenance: Derived,
			Reason: "the password is read back from the redis-password secret and embedded in this URL. A rotation of the password that does not rewrite this key leaves gateway authenticating with the old one.",
			Source: srcRedisURL,
		},
		{
			Name: "SECRET_KEY_BASE", Namespace: "courier", Secret: "courier-secrets",
			Deployment: "courier", Class: Rotatable, Provenance: Generated, Interval: DefaultInterval,
			Shape:  "base64 of 64 random bytes",
			Reason: "born on the box, 64 bytes because Phoenix refuses shorter, and read back once-only so a re-run never rotates a live system. A rotation is a replace-and-restart; the cost is that sessions signed with the old value end.",
			Source: srcKeyBase + ", " + srcRotate,
		},
		{
			Name: "COURIER_SECRET_BOX_KEY", Namespace: "courier", Secret: "courier-secrets",
			Deployment: "courier", Class: Rotatable, Provenance: Generated, Interval: DefaultInterval,
			Shape:  "base64 of 32 random bytes",
			Reason: "born on the box, 32 bytes base64, with no second holder anywhere — the rotation is entirely inside this box, which is what makes it the safest thing on the box to rotate.",
			Source: srcKeyBase + ", " + srcRotate,
		},
		{
			Name: "COURIER_IDENTITY_TOKEN", Namespace: "courier", Secret: "courier-secrets",
			Deployment: "courier", Class: Conditional, Provenance: Minted,
			Reason: "minted on the box by identity's own API and patched into this secret. A rotation is a re-mint against identity plus a two-place write, which is a kit-installer action rather than a kubectl patch.",
			Source: srcMinted,
		},
		{
			Name: "COURIER_TOKEN", Namespace: "identity", Secret: "identity-secrets",
			Deployment: "identity", Class: Conditional, Provenance: Minted,
			Reason: "the SAME value as courier's COURIER_IDENTITY_TOKEN, written into both secrets because courier requires the pair to agree and no hand-off can be trusted to get it right. Rotating one half alone is the mismatch the installer closed.",
			Source: srcMinted,
		},
		{
			Name: "COURIER_MAIL_ADAPTER", Namespace: "courier", Secret: "courier-secrets",
			Class: NotRotatable, Provenance: Configuration,
			Reason: "configuration, not a secret: the record lists it beside the keys it writes because the chart's only env injection is this secret. There is no value to rotate.",
			Source: srcRecord,
		},
		{
			Name: "COURIER_SMTP_HOST", Namespace: "courier", Secret: "courier-secrets",
			Class: NotRotatable, Provenance: Configuration,
			Reason: "configuration, not a secret — the SMTP relay's host, shape-validated on the way in and never echoed.",
			Source: srcRecord + ", " + srcExternal,
		},
		{
			Name: "COURIER_SMTP_PORT", Namespace: "courier", Secret: "courier-secrets",
			Class: NotRotatable, Provenance: Configuration,
			Reason: "configuration, not a secret — the submission port, default 587, refused unless numeric.",
			Source: srcRecord + ", " + srcExternal,
		},
		{
			Name: "COURIER_SMTP_USERNAME", Namespace: "courier", Secret: "courier-secrets",
			Deployment: "courier", Class: Conditional, Provenance: External,
			Reason: "externally sourced SMTP auth. The rotation happens at the relay; what lands here is a patch of the new value and a restart.",
			Source: srcExternal + ", " + srcRotate,
		},
		{
			Name: "COURIER_SMTP_PASSWORD", Namespace: "courier", Secret: "courier-secrets",
			Deployment: "courier", Class: Conditional, Provenance: External,
			Reason: "externally sourced SMTP auth. The relay is the only party that can mint a replacement, so a rotation here is always the second half of a change made elsewhere.",
			Source: srcExternal,
		},
		{
			Name: "COURIER_INBOUND_RESEND_SECRET", Namespace: "courier", Secret: "courier-secrets",
			Deployment: "courier", Class: Conditional, Provenance: External,
			Reason: "the provider's webhook signing secret, generated by the provider. Caf cannot mint one and neither can the box: rotating it is a provider dashboard action followed by a patch here.",
			Source: "cafaye/kit/templates/substrate/install.sh:82-84, " + srcExternal,
		},
		{
			Name: "COURIER_ERROR_RELAY_TOKEN", Namespace: "courier", Secret: "courier-secrets",
			Deployment: "courier", Class: Conditional, Provenance: External,
			Reason: "a self-contained shared value the installer generates, but the other end holds it too — generating a fresh one here would fork the pair, which is exactly why the header refuses it as an input.",
			Source: srcRelayHdr + ", " + srcGenerated,
		},
		{
			Name: "DATABASE_URL", Namespace: "parlor", Secret: "parlor-secrets",
			Deployment: "parlor", Class: Conditional, Provenance: Derived,
			Reason: "not itself a credential: parlor's own DATABASE_URL, agreeing with the Cluster by construction. Rotate the role password it embeds, not the URL.",
			Source: srcDerived + ", " + srcRoles,
		},
		{
			Name: "password", Namespace: "data", Secret: "redis-password", Key: "password",
			Deployment: "redis", Class: Rotatable, Provenance: Generated, Interval: DefaultInterval,
			Shape:  "hex of 24 random bytes",
			Reason: "born on the box and never regenerated on a re-run. A rotation replaces this secret AND rewrites gateway's REDIS_URL with the new value in the same run, then restarts both — the two writes are one rotation, not two.",
			Source: srcRedisPW + ", " + srcRedisURL,
			Writes: []Target{{
				Namespace: "gateway", Name: "gateway-secrets", Key: "REDIS_URL",
				Template: "redis://:%s@redis.data.svc.cluster.local:6379",
			}},
			Restarts: []Target{{Namespace: "gateway", Name: "gateway"}},
		},
		{
			Name: "postgres-role-<service>", Namespace: "data", Secret: "postgres-role-<service>",
			Key: "password", Class: Conditional, Provenance: Generated,
			Reason: "one password per service role, generated once at step 8 and read back into every DATABASE_URL. A rotation is an ALTER ROLE in CloudNativePG plus a secret replacement plus a restart, which is three things and not one.",
			Source: srcRoles,
		},
		{
			Name: "MUSE_VAULT_KEY", Class: NotRotatable, Provenance: Generated,
			Reason: "muse reads the key per request rather than caching it at boot, so a rotation takes effect on the very next request and every row sealed under the old key becomes unreadable. A rotation does not retire the key, it forks every stored credential.",
			Source: srcMuseReadme,
		},
		{
			Name: "OIDC_SIGNING_KEY", Namespace: "identity", Secret: "identity-secrets",
			Deployment: "identity", Class: NotRotatable, Provenance: Generated,
			Reason: "identity's JWKS publishes exactly one key, so there is no second key a relying party can overlap on: a rotation is a cutover in which every token signed with the old key fails verification the moment the set changes.",
			Source: srcOIDCJwks + ", " + srcPacket,
		},
		{
			Name: "OIDC_SIGNING_KEY_ID", Namespace: "identity", Secret: "identity-secrets",
			Class: NotRotatable, Provenance: Configuration,
			Reason: "the kid published in the JWKS and signed into every token, derived from the key's own thumbprint. It is not a secret and it changes only when the key does — which is the cutover above.",
			Source: "cafaye/identity/README.md:302, " + srcOIDCJwks,
		},
		{
			Name: "OIDC_ISSUER", Namespace: "identity", Secret: "identity-secrets",
			Class: NotRotatable, Provenance: Configuration,
			Reason: "configuration, not a secret: the URL identity publishes as its issuer, required with the signing key and its id or with none of them.",
			Source: "cafaye/identity/README.md:300, " + srcRecord,
		},
	}
}

// Find is the lookup a rotate or an inventory query uses. It matches on the
// entry's Name, and on the `namespace/secret/key` spelling for the two entries
// whose Name is not unique (DATABASE_URL lives in two namespaces) or is not a
// key (the Redis password's key is `password`).
//
// A miss is an error and not a nil Entry, because the caller's next question is
// "what could I have typed", and the answer is the list of names.
func Find(name string) (Entry, error) {
	want := normalize(name)
	for _, e := range Inventory() {
		if normalize(e.Name) == want || normalize(e.String()) == want {
			return e, nil
		}
	}
	return Entry{}, &UnknownError{Name: name, Known: names()}
}

// names is every spellable name, for the unknown-entry refusal.
func names() []string {
	out := make([]string, 0)
	for _, e := range Inventory() {
		out = append(out, e.Name)
		if s := e.String(); s != e.Name {
			out = append(out, s)
		}
	}
	return out
}

// String is the `namespace/secret/key` spelling of where the value lives, which
// is the name a reader recognises from kubectl. It is the key an entry is
// matched on when Name is not unique.
func (e Entry) String() string {
	if e.Secret == "" {
		return ""
	}
	key := e.KeyOrName()
	if e.Namespace == "" {
		return e.Secret + "/" + key
	}
	return e.Namespace + "/" + e.Secret + "/" + key
}

func normalize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		}
	}
	return string(out)
}

// UnknownError names what was asked for and what could have been. It is a type
// rather than a string so a caller can tell "no such secret" from "the rotation
// was refused", which are two different exits with two different fixes.
type UnknownError struct {
	Name  string
	Known []string
}

func (e *UnknownError) Error() string {
	return "caf secret: no inventory entry named " + quote(e.Name) +
		". The inventory holds: " + joinComma(e.Known)
}

func quote(s string) string { return `"` + s + `"` }

func joinComma(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

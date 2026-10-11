package failure

// readyzLatencyFloor is the number of milliseconds a courier `/readyz` takes
// when its database has gone: the doc's own ~4.4s
// (CAFAYE-OPS.md:739, :804). It is a floor rather than an exact threshold
// because the doc's figure is a documented observation and not a measurement
// this packet took, and a rule that fired at exactly 4400 would be a rule that
// missed at 4399.
const readyzLatencyFloor = 4000

// Rule is one entry in the library: a failure mode, the four things the doc's
// table carries, and the condition that identifies it.
//
// Every field is prose except Condition, and that split is the design: the
// prose is what a person reads, the condition is what a machine evaluates, and
// the test that keeps them honest is library_test.go — it fails if an entry has
// an empty field, a duplicate id, or a condition whose predicates read facts
// the Observation does not carry. A library row nobody can evaluate is trivia;
// a library row that evaluates against a fact nobody populates is worse.
type Rule struct {
	// ID is the entry's permanent name. It never changes once an entry ships:
	// the monthly report cites it, a customer's runbook cites it, and an id
	// that moved is a citation that broke.
	ID string

	// Failure is what has happened, in the doc's own words.
	Failure string

	// Sees is what a generic monitor sees. It is here because it is the column
	// that makes the library worth anything: a monitor cannot see these, and a
	// reader comparing this column with their own dashboard learns why their
	// dashboard said what it said.
	Sees string

	// Known is what we know — the scar tissue. The doc's third column, cited.
	Known string

	// Remedy is what a person does about it. Derived from Known where the doc
	// states an action, and stated as what Known rules out where the doc does
	// not — see doc.go.
	Remedy string

	// Contract is the core contract this rule leans on, cited by file and the
	// fact taken from it. Empty means the rule needs none: some failure modes
	// are cafe's own and core says nothing about them, and a citation invented
	// to fill the field would be worse than an empty one.
	Contract string

	// Condition is the identifying condition, as data.
	Condition Condition
}

// Library is the seed: the ten entries CAFAYE-OPS.md:731-748 already records
// plus the eleventh this repository owes its own edge, in the doc's order with
// the eleventh last. It is a function rather than a package-level variable so
// no caller can append to the library the rest of the process reads, and so a
// test can render it without mutating anything.
//
// IT IS ELEVEN AND NOT TWELVE, and the rule that kept it at ten still holds.
// The tenth entry was refused for as long as the library had a floor to raise,
// because a plausible entry is a guess and a guess in a file a customer's box
// is checked against is the kind that gets copied. The eleventh is not a
// guess: it is the one failure mode this repository can observe on itself,
// because the box's edge is code in this tree (internal/edge) and the thing
// that would catch its absence is a predicate over an observation field
// nothing populates yet — which the checker reports as UNKNOWN and names,
// exactly as the other ten do. Adding a twelfth costs a row here, a row in the
// test fixture, and a paragraph saying where it was observed. Nothing else.
func Library() []Rule {
	return []Rule{
		{
			ID:      "courier-readyz-slow-when-db-gone",
			Failure: "courier's /readyz takes ~4.4s to answer when its DB has gone",
			Sees:    "unhealthy → restart",
			Known: "restarting a healthy service is the bug; the probe timeout is the bug, " +
				"not the service (CAFAYE-OPS.md:739, :804-806)",
			Remedy: "Do not restart courier. Raise the probe timeout past the readiness " +
				"check's own latency, so a slow honest 503 is read as a dependency being " +
				"gone rather than as a process to be replaced.",
			Contract: "core schemas/telemetry/probes.schema.json pins /readyz as the " +
				"readiness path, 503 as its answer when a check fails, and `timeoutMs` at a " +
				"10s ceiling because a readiness probe slower than the orchestrator's own " +
				"interval is a probe whose answer arrives after the decision it was meant " +
				"to inform. Cite it; do not restate the paths.",
			Condition: Condition{All: []Predicate{
				{Kind: PredProbeLatencyOver, Service: "courier", Name: "/readyz", Value: readyzLatencyFloor},
				{Kind: PredProbeStatusIs, Service: "courier", Name: "/readyz", Value: 503},
			}},
		},
		{
			ID:      "kit-proxy-healthchecks-up",
			Failure: "kit's proxy healthcheck path is /up",
			Sees:    "404, rollout never goes green",
			Known: "/up is a boot check; every service serves /readyz; `billing` serves " +
				"/up and doesn't check the DB (CAFAYE-OPS.md:740, :807-809)",
			Remedy: "Point the proxy's healthcheck at /healthz or /readyz — core pins both " +
				"paths — and read `billing`'s /up as what it is: a boot check that answers " +
				"200 while its database is gone.",
			Contract: "core schemas/telemetry/probes.schema.json pins the two literal " +
				"paths, `/healthz` (const) and `/readyz` (const), and pins both `auth: " +
				"exempt`. `/up` is neither of them, which is why a 404 here is a " +
				"configuration fact rather than a missing route.",
			Condition: Condition{All: []Predicate{
				{Kind: PredServiceRunning, Service: "kit"},
				{Kind: PredHealthcheckPathIs, Service: "kit", Name: "/up"},
			}},
		},
		{
			ID:      "identity-mfa-routes-absent",
			Failure: "identity's MFA routes are absent",
			Sees:    "404",
			Known: "unset MFA_ENCRYPTION_KEY — absent, not broken; no error is ever " +
				"printed (CAFAYE-OPS.md:741, :810-812). identity's own boot says the same " +
				"thing in its source: the MFA management routes are not mounted when the " +
				"key is unconfigured, and the message is a warning rather than an error " +
				"(identity/cmd/identity/main.go:646, :1185)",
			Remedy: "Set MFA_ENCRYPTION_KEY. The 404 is a setting's absence, so the fix " +
				"is a deployment fact and not a bug report against the routes.",
			Contract: "core schemas/events/identity/mfa/enabled.schema.json and " +
				"disabled.schema.json are the contracted MFA events; the MFA surface is a " +
				"cafaye contract, so its absence is a configuration state and not a " +
				"feature that was never built.",
			Condition: Condition{All: []Predicate{
				{Kind: PredServiceRunning, Service: "identity"},
				{Kind: PredSettingUnset, Service: "identity", Name: "MFA_ENCRYPTION_KEY"},
			}},
		},
		{
			ID:      "oidc-not-mounted",
			Failure: "OIDC not mounted",
			Sees:    "404 on discovery",
			Known: "all three of OIDC_ISSUER/OIDC_SIGNING_KEY/OIDC_SIGNING_KEY_ID must be " +
				"set together (CAFAYE-OPS.md:742, :813-814)",
			Remedy: "Set all three. A partial set mounts nothing and 404s on discovery " +
				"exactly as an empty one does, so check the third before believing the " +
				"first two are wrong.",
			Contract: "",
			Condition: Condition{Any: []Predicate{
				{Kind: PredSettingUnset, Service: "identity", Name: "OIDC_ISSUER"},
				{Kind: PredSettingUnset, Service: "identity", Name: "OIDC_SIGNING_KEY"},
				{Kind: PredSettingUnset, Service: "identity", Name: "OIDC_SIGNING_KEY_ID"},
			}},
		},
		{
			ID:      "postgres-two-pockets",
			Failure: "Two pockets of Postgres, five services",
			Sees:    "role does not exist",
			Known: "KIT_POSTGRES_DATABASES merges last-one-wins in compose " +
				"(CAFAYE-OPS.md:743)",
			Remedy: "Give each service its own database name. A merge that keeps the " +
				"last declaration is a merge that loses a role, and the service that " +
				"lost it is the one whose role does not exist.",
			Contract: "",
			Condition: Condition{All: []Predicate{
				{Kind: PredDatabaseDeclaredTwice},
			}},
		},
		{
			ID:      "traces-going-nowhere",
			Failure: "Traces \"going nowhere\"",
			Sees:    "no error",
			Known: "instrumented in 4 services, no collector deployed anywhere " +
				"(CAFAYE-OPS.md:744, :843-845)",
			Remedy: "Deploy a collector, or point <SERVICE>_OTEL_ENDPOINT at one the " +
				"customer already runs. Nothing is broken and nothing is slow — that is " +
				"why this one is invisible without the library.",
			Contract: "core schemas/telemetry/otel-endpoint.schema.json is the contract: " +
				"`<SERVICE>_OTEL_ENDPOINT` is the only variable, the shipped collector is " +
				"its default value rather than a constant, and unsetting it must be a " +
				"genuinely free no-op via OTEL_SDK_DISABLED=true. Cite the variable; do " +
				"not restate the collector's address.",
			Condition: Condition{All: []Predicate{
				{Kind: PredInstrumented},
				{Kind: PredCollectorAbsent},
			}},
		},
		{
			ID:      "outbox-rows-piling-up",
			Failure: "Outbox rows piling up",
			Sees:    "nothing",
			Known: "no service starts a publisher loop; an event is a row " +
				"(CAFAYE-OPS.md:745, :839-842)",
			Remedy: "Run a publisher. The rows are the event log — nothing is lost and " +
				"nothing moves until something drains them.",
			Contract: "core schemas/event-envelope.schema.json is the envelope an event " +
				"travels in, with its payload shape owned by per-event schemas under " +
				"schemas/events/. The rows in an outbox are envelopes waiting to be sent; " +
				"cite the envelope, do not transcribe a payload.",
			Condition: Condition{All: []Predicate{
				{Kind: PredOutboxRowsOver, Value: 0},
				{Kind: PredPublisherAbsent},
			}},
		},
		{
			ID:      "gateway-sessions-per-process",
			Failure: "gateway sessions are per-process",
			Sees:    "works",
			Known:   "run one replica; scaling to two is a live outage (CAFAYE-OPS.md:746)",
			Remedy: "Run exactly one replica. Everything looks right at two, which is " +
				"what makes it an outage rather than a failure.",
			Contract: "",
			Condition: Condition{All: []Predicate{
				{Kind: PredServiceRunning, Service: "gateway"},
				{Kind: PredReplicasOver, Service: "gateway", Value: 1},
			}},
		},
		{
			ID:      "courier-password-reset-503",
			Failure: "Password reset answers 503",
			Sees:    "transient error",
			Known: "mailer unconfigured — a configuration fact, not a fault " +
				"(CAFAYE-OPS.md:747)",
			Remedy: "Configure the mailer. Read the 503 as a relay nobody is sending " +
				"through rather than as an incident to be retried.",
			Contract: "",
			Condition: Condition{All: []Predicate{
				{Kind: PredServiceRunning, Service: "courier"},
				{Kind: PredSettingUnset, Service: "courier", Name: "COURIER_MAIL_ADAPTER"},
			}},
		},
		{
			ID:      "secrets-frozen-cannot-rotate",
			Failure: "MUSE_VAULT_KEY, OIDC_SIGNING_KEY",
			Sees:    "—",
			Known: "cannot be rotated at all; a rotation is a cutover or a fork " +
				"(CAFAYE-OPS.md:748, :783-788)",
			Remedy: "Enumerate them as frozen, so nobody spends a weekend discovering " +
				"it. Rotate the rotatable class (COURIER_SECRET_BOX_KEY, SECRET_KEY_BASE, " +
				"the Redis password) on a schedule and leave these alone " +
				"(CAFAYE-OPS.md:786-788); OPS-P10 owns the inventory.",
			Contract: "",
			Condition: Condition{Any: []Predicate{
				{Kind: PredSettingSet, Service: "muse", Name: "MUSE_VAULT_KEY"},
				{Kind: PredSettingSet, Service: "identity", Name: "OIDC_SIGNING_KEY"},
			}},
		},
		{
			// The eleventh is this repository's own: the failure mode of the
			// thing boxd runs in front of everything. It is appended after the
			// ten rather than inserted among them because the doc's table has
			// its own order and this entry is not in that table — it is in the
			// plan's Phase B TLS note and in internal/edge.
			ID:      "edge-down-while-every-readyz-is-green",
			Failure: "the box's edge is down and every service's /readyz still answers 200",
			Sees:    "all services healthy, heartbeat green, nothing to page about",
			Known: "the edge is not a service, so nothing in the substrate's own " +
				"health checks it: a box whose edge has gone answers `/readyz` for " +
				"every app from inside the box, and the control-plane heartbeat is a " +
				"different channel entirely (internal/boxd/client.go:15-24 is an " +
				"outbound mTLS connection that survives an edge that is not " +
				"listening). The edge is the only thing between a customer and " +
				"every hostname the box serves, and internal/edge's HealthPath is " +
				"the one path it answers itself so that an answer means the edge " +
				"is alive rather than the app behind it is. The doc's observe-stream " +
				"line already names cert expiry as something the stream has to carry " +
				"(CAFAYE-OPS.md:660) and a box's edge is where that certificate " +
				"lives; the doc's table does not record the edge's own failure, " +
				"which is why this entry is the eleventh rather than a row in it.",
			Remedy: "Probe the edge's own health path from OUTSIDE the box — " +
				"`https://api.<box>/healthz` — and read the routing record it " +
				"answers with. Nothing inside the box can substitute for it: a " +
				"`/readyz` answered by an app behind a dead edge is a fact about " +
				"the app. The symmetric counterpart is the mirror failure and it " +
				"is the reason the edge's health column is the thing to read: the " +
				"edge answers 200 for a hostname whose upstream it has already " +
				"observed unhealthy, so an edge that is UP is not evidence that " +
				"anything behind it is (predicate `edge-host-unhealthy`, over the " +
				"same Edge fact).",
			// No core contract is cited because core says nothing about a box's
			// edge: core pins the two probe paths and the resource attributes,
			// and the edge is a cafaye invention. An invented citation would be
			// worse than an empty field — see Rule.Contract.
			Contract: "",
			Condition: Condition{
				All: []Predicate{
					{Kind: PredEdgeServing, Value: 0},
				},
				Any: []Predicate{
					{Kind: PredServiceRunning, Service: "gateway"},
					{Kind: PredServiceRunning, Service: "identity"},
				},
			},
		},
	}
}

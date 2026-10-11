package failure

import (
	"context"
	"fmt"

	"github.com/cafaye/cafe/internal/boxd"
)

// Observation is every fact a rule may read. It is a value rather than an
// interface so the checker is a pure function of it, and so a test can build
// the exact box a rule was written for without a cluster anywhere in sight.
//
// It carries NAMES and COUNTS and never a configured value. A service's
// configuration is where credentials live — internal/boxd/identity.go:131-134 says
// as much, and the fact the eleven rules need is a name's presence:
// `identity`'s MFA routes are absent because `MFA_ENCRYPTION_KEY` is unset
// (CAFAYE-OPS.md:741), so what identifies the failure mode is whether the name
// is set, not what it is set to.
//
// Every field is optional in the same way: a nil map and a nil slice both mean
// "the observation does not carry this fact", and a rule that reads one is
// answered UNKNOWN rather than false. There is no zero value here that could
// pass for a fact, which is what makes that distinction safe.
type Observation struct {
	// Services are the names running on the box. This is the one fact
	// boxd.World has today (internal/boxd/identity.go:130-135), so it is the
	// one fact every rule can be asked about on a box as it stands.
	Services []string `json:"services,omitempty"`

	// Replicas is how many replicas each service runs. Rule 8 needs it:
	// gateway sessions are per-process, so a second replica is a live outage
	// (CAFAYE-OPS.md:746).
	Replicas map[string]int `json:"replicas,omitempty"`

	// Settings is, per service, the names of the settings that ARE set. Names
	// only — see the type comment. Rules 3, 4, 9 and 10 read presence and
	// absence here.
	Settings map[string][]string `json:"settings,omitempty"`

	// Probes are the probe answers observed, one per service and path. The
	// paths are core's: `/healthz` and `/readyz` are pinned by
	// core/schemas/telemetry/probes.schema.json's two `const`s, and a 503 from
	// `/readyz` is a readiness answer rather than a liveness one — the schema
	// pins 503 there precisely because an orchestrator that reads any non-2xx
	// as "not listening" would restart a running container.
	Probes []Probe `json:"probes,omitempty"`

	// Healthchecks is the healthcheck path the substrate is configured to use
	// for each service. Rule 2 needs it: kit's proxy healthchecks `/up`
	// (CAFAYE-OPS.md:740), which is neither of core's two paths.
	Healthchecks map[string]string `json:"healthchecks,omitempty"`

	// Databases is every database a service declares. Rule 5 needs it: two
	// services declaring one database name is two pockets of Postgres,
	// because `KIT_POSTGRES_DATABASES` merges last-one-wins
	// (CAFAYE-OPS.md:743).
	Databases []Database `json:"databases,omitempty"`

	// Outbox is how many unprocessed outbox rows each service has. Rule 7
	// needs it: an event is a row, and no service starts a publisher loop
	// (CAFAYE-OPS.md:745, :839-842).
	Outbox map[string]int `json:"outbox,omitempty"`

	// Publishers are the services with a publisher loop running. Rule 7 reads
	// the absence in it.
	Publishers []string `json:"publishers,omitempty"`

	// Collectors are the collector addresses that run on this box. Empty means
	// none do — which, on a box whose services export signals, is the
	// traces-going-nowhere fact itself (CAFAYE-OPS.md:744).
	//
	// It is a slice rather than a string for the nil convention the whole type
	// runs on: nil means the observation never looked and empty means it
	// looked and found none. A string could not say which of those it was, and
	// the difference is rule 6's whole answer. Core is why the fact is this
	// shape at all: `<SERVICE>_OTEL_ENDPOINT` is the contract and the shipped
	// collector is only its default value
	// (core/schemas/telemetry/otel-endpoint.schema.json), so "nothing is
	// collecting" is a fact about the box's stack rather than about a variable
	// somebody forgot to set.
	Collectors []string `json:"collectors,omitempty"`

	// Instrumented are the services that export signals. Rule 6 needs it:
	// instrumented in four services with no collector anywhere is traces
	// going nowhere (CAFAYE-OPS.md:744).
	Instrumented []string `json:"instrumented,omitempty"`

	// Edge is what the box's edge reports about itself — whether it is
	// answering its own health path, and what it last knew about each
	// hostname in its routing record. Rule 11 needs it, and it is the one
	// fact in this surface about the thing a customer actually reaches.
	//
	// Nil means the observation never asked the edge, which is the state of
	// every box today: internal/boxd's World carries service names and the
	// install phase and nothing else (internal/boxd/route.go:26-28), so the
	// edge's own answer is exactly what the substrate packet owes this
	// library. That is the same honest UNKNOWN the other ten need, and it is
	// worth naming because this fact is different in kind: an edge nobody
	// probes is an edge that can be down with everything else green.
	Edge *EdgeFact `json:"edge,omitempty"`
}

// EdgeFact is the box's edge, as a fact. It carries no certificate and no
// key: the edge's own health answer is about routing
// (internal/edge.HealthPath), so the one route a control plane can quote from
// a box is also the one that could not carry a credential if it wanted to.
type EdgeFact struct {
	// Serving is whether the edge answered its own health path at all. It is
	// a single boolean rather than a status because the failure mode rule 11
	// identifies is not "the edge is degraded" — it is "the edge is not
	// there", and the difference between those two is the whole entry.
	Serving bool `json:"serving"`

	// Hosts is one row per hostname in the edge's routing record.
	Hosts []EdgeHost `json:"hosts,omitempty"`
}

// EdgeHost is one hostname's row: where it goes, and what the edge last
// observed there.
//
// The health is the EDGE'S observation and not a service's opinion of itself,
// which is what makes it worth carrying: a service that answers `/readyz`
// from inside the box has no idea whether anything can reach it from outside,
// and the edge is the only thing on the box that knows.
type EdgeHost struct {
	Name     string `json:"name"`
	Upstream string `json:"upstream"`
	Health   string `json:"health"`
}

// Probe is one observed probe answer. LatencyMS is the whole answer's latency
// rather than a connection's, because the failure mode it identifies is a
// READINESS check that is slow when its dependency has gone
// (CAFAYE-OPS.md:739) — the connection is fine and the answer takes 4.4
// seconds.
type Probe struct {
	Service string `json:"service"`
	Path    string `json:"path"`
	// Status is the HTTP status the probe answered. 503 from `/readyz` is
	// core's own contract for "a dependency is gone"
	// (probes.schema.json, `readyz.statusCode`), which is what distinguishes
	// rule 1's condition from a service that is merely slow.
	Status int `json:"status"`
	// LatencyMS is how long the answer took. The 4.4s figure is the doc's own
	// (CAFAYE-OPS.md:739, :804), not a measurement of this fleet's.
	LatencyMS int `json:"latency_ms"`
}

// Database is one declared database and the service that declares it. The name
// is the identity: `KIT_POSTGRES_DATABASES` merges last-one-wins, so two rows
// with one name are one database and the second service's role never exists
// (CAFAYE-OPS.md:743).
type Database struct {
	Name    string `json:"name"`
	Service string `json:"service"`
}

// Service reports whether the named service is running.
func (o Observation) Service(name string) bool {
	for _, s := range o.Services {
		if s == name {
			return true
		}
	}
	return false
}

// SettingsFor is the names set on one service, and whether the observation
// carries that service at all. The second value is what makes an absent entry
// distinguishable from a service with nothing set: the first is rule 3's
// condition (MFA routes absent because the name is unset), the second is an
// observation that never looked.
func (o Observation) SettingsFor(service string) ([]string, bool) {
	names, ok := o.Settings[service]
	return names, ok
}

// Observer produces the facts a rule reads. It is a seam for the same reason
// internal/boxd's World is: the checker is provable without a box, and the box
// is provable against a recording observer.
type Observer interface {
	Observe(ctx context.Context) (Observation, error)
}

// BoxObserver adapts what the box daemon can see into the fact surface. It is
// the real first consumer, and it is deliberately thin: boxd's World answers
// which services run and the phase the install reached, so those are the two
// facts it supplies and the other eight fields are left absent — which the
// checker reports as UNKNOWN rather than as a pass.
//
// That is the honest shape of today, and it is the reason this adapter exists
// as code rather than as a promise: a rule set that answered "does not hold"
// against a box whose daemon had never been asked would be a library that
// quietly stopped working the day the observation surface moved.
type BoxObserver struct {
	// World is the box daemon's own seam. It is held rather than consumed at
	// construction because a connection may re-enroll and a world built once
	// would answer as the old enrollment — the same reason
	// internal/boxd/daemon.go:41-46 takes WorldFor as a function.
	World boxd.World
}

// Observe reads the box's facts once.
func (b BoxObserver) Observe(ctx context.Context) (Observation, error) {
	status, err := b.World.Stack(ctx)
	if err != nil {
		return Observation{}, fmt.Errorf("failure: the box daemon could not report its stack: %w", err)
	}
	return Observation{Services: append([]string(nil), status.Services...)}, nil
}

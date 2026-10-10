package failure

import (
	"strconv"
	"strings"
)

// PredicateKind is one kind of test over an Observation. It is an enum rather
// than a string so a typo is a compile error in the rule that carries it, and
// rather than a closure so the rule set stays data — see doc.go.
type PredicateKind string

// The vocabulary. Twelve, because the ten seed entries need these twelve and
// not one more; the rule for adding a thirteenth is in this file's comment on
// evaluate.
const (
	// PredServiceRunning: the service is in the box's service list.
	PredServiceRunning PredicateKind = "service-running"

	// PredReplicasOver: the service runs MORE than Value replicas. More than,
	// not at least, because every rule that reads replicas is about a count
	// that must be exactly one.
	PredReplicasOver PredicateKind = "replicas-over"

	// PredSettingSet / PredSettingUnset: the service's set settings do or do
	// not contain Name. Presence and absence only — never a value, per
	// Observation.Settings.
	PredSettingSet   PredicateKind = "setting-set"
	PredSettingUnset PredicateKind = "setting-unset"

	// PredHealthcheckPathIs: the substrate healthchecks the service at Name,
	// which is how `/up` is seen — a path that is neither of core's two.
	PredHealthcheckPathIs PredicateKind = "healthcheck-path-is"

	// PredProbeLatencyOver: the probe at Name took longer than Value ms to
	// ANSWER. The latency is the whole answer's, because the failure mode is a
	// readiness check that is slow when its dependency has gone.
	PredProbeLatencyOver PredicateKind = "probe-latency-over"

	// PredProbeStatusIs: the probe at Name answered Value. 503 from `/readyz`
	// is core's contract for a dependency gone, and it is what separates rule
	// 1 from a service that is merely slow.
	PredProbeStatusIs PredicateKind = "probe-status-is"

	// PredDatabaseDeclaredTwice: Name is declared by more than one service.
	// Two services one database is two pockets of Postgres, five services
	// (CAFAYE-OPS.md:743).
	PredDatabaseDeclaredTwice PredicateKind = "database-declared-twice"

	// PredOutboxRowsOver: the service has more than Value unprocessed rows.
	PredOutboxRowsOver PredicateKind = "outbox-rows-over"

	// PredPublisherAbsent: no publisher loop runs for the service.
	PredPublisherAbsent PredicateKind = "publisher-absent"

	// PredCollectorAbsent: no collector runs on this box. Reads the whole
	// stack rather than a service, so Service is empty.
	PredCollectorAbsent PredicateKind = "collector-absent"

	// PredInstrumented: at least one service exports signals.
	PredInstrumented PredicateKind = "instrumented"
)

// Predicate is one test, with its arguments. Service, Name and Value carry
// different things depending on Kind, and which is written out per kind above
// and in evaluate — a predicate whose arguments were guessed is a predicate
// that silently reads a fact nobody populated.
type Predicate struct {
	Kind    PredicateKind
	Service string
	Name    string
	Value   int
}

// Condition is one rule's identifying condition: every predicate in All, and
// at least one of Any when Any is non-empty.
//
// Two connectives and not an expression tree, because the ten seed entries need
// exactly this much and an expression tree would be machinery the library's
// growth does not need. The conjunction covers "this fact and that fact" (a
// slow readiness answer while the dependency is gone); the disjunction covers
// "either frozen key is in use" (CAFAYE-OPS.md:748).
type Condition struct {
	All []Predicate
	Any []Predicate
}

// evaluate is one predicate against one observation. The second return is
// whether the observation carries the fact at all, and false is what turns an
// unanswerable rule into UNKNOWN rather than into a green light over a blind
// spot. A predicate the checker does not know is not an error: evaluate says
// it cannot be answered, and the report names it.
func (p Predicate) evaluate(o Observation) (held, known bool) {
	switch p.Kind {
	case PredServiceRunning:
		return o.Service(p.Service), o.Services != nil
	case PredReplicasOver:
		n, ok := o.Replicas[p.Service]
		return n > p.Value, ok
	case PredSettingSet, PredSettingUnset:
		names, ok := o.SettingsFor(p.Service)
		if !ok {
			return false, false
		}
		for _, name := range names {
			if name == p.Name {
				// The only two ways this returns differ by kind; a setting
				// that is set is `setting-set` and nothing else.
				return p.Kind == PredSettingSet, true
			}
		}
		return p.Kind == PredSettingUnset, true
	case PredHealthcheckPathIs:
		path, ok := o.Healthchecks[p.Service]
		return path == p.Name, ok
	case PredProbeLatencyOver, PredProbeStatusIs:
		found := false
		for _, probe := range o.Probes {
			if probe.Service != p.Service || probe.Path != p.Name {
				continue
			}
			found = true
			if p.Kind == PredProbeLatencyOver {
				if probe.LatencyMS > p.Value {
					return true, true
				}
				continue
			}
			if probe.Status == p.Value {
				return true, true
			}
		}
		// Not found is "the observation never probed it", which is unknown;
		// found and unmatched is a real "does not hold".
		if !found {
			return false, false
		}
		return false, true
	case PredDatabaseDeclaredTwice:
		// An empty Name is "some database is declared twice", which is the
		// form the seed uses: the doc records that the merge is
		// last-one-wins (CAFAYE-OPS.md:743) and does not name the database,
		// so naming one here would be inventing it.
		counts := map[string]int{}
		for _, db := range o.Databases {
			if p.Name != "" && db.Name != p.Name {
				continue
			}
			counts[db.Name]++
		}
		if len(counts) == 0 {
			return false, false
		}
		for _, owners := range counts {
			if owners > 1 {
				return true, true
			}
		}
		return false, true
	case PredOutboxRowsOver:
		// An empty Service is "some service has more than Value rows", for the
		// same reason: the doc's row is about the stack's outboxes rather than
		// about one named service.
		if p.Service == "" {
			if len(o.Outbox) == 0 {
				return false, false
			}
			for _, rows := range o.Outbox {
				if rows > p.Value {
					return true, true
				}
			}
			return false, true
		}
		n, ok := o.Outbox[p.Service]
		return n > p.Value, ok
	case PredPublisherAbsent:
		// An empty Service is "no publisher runs anywhere".
		for _, publisher := range o.Publishers {
			if p.Service == "" || publisher == p.Service {
				return false, true
			}
		}
		return true, o.Publishers != nil
	case PredCollectorAbsent:
		return len(o.Collectors) == 0, o.Collectors != nil
	case PredInstrumented:
		return len(o.Instrumented) > 0, o.Instrumented != nil
	}
	// An unknown kind is reported as unanswerable. The alternative — reading
	// it as false — is a rule that quietly stops firing when a vocabulary
	// entry is misspelled or removed, which is the exact failure mode
	// lock/verify.go:134-138 refuses for an unknown kind.
	return false, false
}

// fact is the name of the observation field this predicate reads. It is what an
// UNKNOWN verdict names, so a reader knows what the substrate packet owes this
// library rather than being told a rule "did not fire".
func (p Predicate) fact() string {
	switch p.Kind {
	case PredServiceRunning:
		return "Services"
	case PredReplicasOver:
		return "Replicas"
	case PredSettingSet, PredSettingUnset:
		return "Settings"
	case PredHealthcheckPathIs:
		return "Healthchecks"
	case PredProbeLatencyOver, PredProbeStatusIs:
		return "Probes"
	case PredDatabaseDeclaredTwice:
		return "Databases"
	case PredOutboxRowsOver:
		return "Outbox"
	case PredPublisherAbsent:
		return "Publishers"
	case PredCollectorAbsent:
		return "Collector"
	case PredInstrumented:
		return "Instrumented"
	}
	return "no such predicate kind " + string(p.Kind)
}

// String is the predicate as a sentence, so a report reads as prose rather than
// as a data structure. It is the failure column's identifying condition in the
// words a reader of the library would use.
func (p Predicate) String() string {
	switch p.Kind {
	case PredServiceRunning:
		return p.Service + " is running"
	case PredReplicasOver:
		return p.Service + " runs more than " + strconv.Itoa(p.Value) + " replica(s)"
	case PredSettingSet:
		return p.Service + " has " + p.Name + " set"
	case PredSettingUnset:
		return p.Service + " does not have " + p.Name + " set"
	case PredHealthcheckPathIs:
		return p.Service + " is healthchecked at " + p.Name
	case PredProbeLatencyOver:
		return p.Service + "'s " + p.Name + " took more than " + strconv.Itoa(p.Value) + "ms to answer"
	case PredProbeStatusIs:
		return p.Service + "'s " + p.Name + " answered " + strconv.Itoa(p.Value)
	case PredDatabaseDeclaredTwice:
		if p.Name == "" {
			return "some database is declared by more than one service"
		}
		return "database " + p.Name + " is declared by more than one service"
	case PredOutboxRowsOver:
		if p.Service == "" {
			return "some service has more than " + strconv.Itoa(p.Value) + " unprocessed outbox row(s)"
		}
		return p.Service + " has more than " + strconv.Itoa(p.Value) + " unprocessed outbox row(s)"
	case PredPublisherAbsent:
		if p.Service == "" {
			return "no publisher loop runs anywhere on this box"
		}
		return "no publisher loop runs for " + p.Service
	case PredCollectorAbsent:
		return "no collector runs on this box"
	case PredInstrumented:
		return "at least one service exports signals"
	}
	return "unknown predicate " + string(p.Kind)
}

// describe renders a whole condition the way the report does: the conjunction,
// then the disjunction in parentheses. A condition a reader cannot paraphrase
// is a condition nobody can check by hand.
func (c Condition) describe() string {
	parts := make([]string, 0, len(c.All))
	for _, p := range c.All {
		parts = append(parts, p.String())
	}
	out := strings.Join(parts, " and ")
	if len(c.Any) > 0 {
		anyParts := make([]string, 0, len(c.Any))
		for _, p := range c.Any {
			anyParts = append(anyParts, p.String())
		}
		joined := strings.Join(anyParts, " or ")
		if out == "" {
			return "either " + joined
		}
		return out + ", and either " + joined
	}
	return out
}

// evaluate is a whole condition against one observation: it holds, it does
// not, or the observation cannot say. The second return is the name of the
// observation field the condition needed and the box did not supply, and it is
// empty exactly when the answer is a real yes or no — which is what makes an
// UNKNOWN verdict distinguishable from a pass in the report.
//
// All is walked first and the walk stops at the first unanswerable predicate,
// rather than at the first failing one. A condition whose first two predicates
// hold and whose third cannot be evaluated is a condition nobody knows about;
// reporting it as false would be the false green doc.go refuses.
func (c Condition) evaluate(o Observation) (holds bool, missing string) {
	for _, p := range c.All {
		ok, known := p.evaluate(o)
		if !known {
			return false, p.fact()
		}
		if !ok {
			return false, ""
		}
	}
	if len(c.Any) > 0 {
		missing, answered := "", false
		for _, p := range c.Any {
			ok, known := p.evaluate(o)
			if !known {
				if missing == "" {
					missing = p.fact()
				}
				continue
			}
			answered = true
			if ok {
				return true, ""
			}
		}
		// No leg held. If not a single leg was answerable the condition is
		// unknown and names the fact; if at least one was answered, the
		// condition is genuinely false — one unanswerable leg does not stop
		// the other from firing, and it does not license a pass either.
		if !answered {
			return false, missing
		}
		return false, ""
	}
	return true, ""
}

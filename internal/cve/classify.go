package cve

import (
	"fmt"
	"sort"
	"strings"
)

// Finding is one component against one advisory, with everything the buyer
// needs to decide what to do about it.
//
// It is deliberately not just a pair: the advisory count is free and the
// component name, the fixed version and the blast radius are what the
// customer is paying for. A finding that said "redis has an advisory" would
// leave the operator to work out which redis, what version fixes it and what
// else that touch — and working it out is the whole job.
type Finding struct {
	// Component is the component's name in the substrate's spelling.
	Component string `json:"component"`
	// Version is what the box is running, as written.
	Version string `json:"version"`
	// Kind is how a fix is applied.
	Kind Kind `json:"kind"`
	// Severity is the advisory's score.
	Severity Severity `json:"severity"`
	// AdvisoryID is the advisory's own name.
	AdvisoryID string `json:"advisory"`
	// Summary is one sentence about the advisory.
	Summary string `json:"summary,omitempty"`
	// URL is where a human reads it.
	URL string `json:"url,omitempty"`
	// Class is the verdict, and why it is that verdict.
	Class Class `json:"class"`
	// Fixed is the first version that is not affected. Empty means there is
	// none, which the report says rather than omitting.
	Fixed string `json:"fixed,omitempty"`
	// ReleaseTo is the version an operator would move to: the advisory's
	// fixed version, or empty when there is none. It is a separate field from
	// Fixed because one is a fact about the advisory and the other is the
	// instruction, and a report that conflated them could not say "no fix
	// published" without leaving the fixed version blank in a way that reads
	// as absent data.
	ReleaseTo string `json:"release_to,omitempty"`
	// Impact is what applying the fix touches. It is taken from the
	// substrate catalog, and it is empty for a component the catalog does
	// not characterize.
	Impact Blast `json:"impact,omitempty"`
	// ImpactUnknown says the blast radius is not known, so the report can say
	// so rather than printing an empty impact block that reads as "touches
	// nothing".
	ImpactUnknown bool `json:"impact_unknown,omitempty"`
	// Why is the sentence a reader acts on.
	Why string `json:"why"`
}

// Counts is the shape of the answer in numbers, for the one line a person
// reads first.
type Counts struct {
	Affected   int `json:"affected"`
	NoFix      int `json:"no_fix"`
	Unaffected int `json:"unaffected"`
	Unknown    int `json:"unknown"`
	// Components is how many components were classified, not how many had
	// advisories — so "17 components, 3 affected" is the honest pair and
	// "3 advisories" alone is not.
	Components int `json:"components"`
	// Advisories is how many advisories were fetched, before matching.
	Advisories int `json:"advisories"`
}

// Classify runs every component against every advisory and returns the
// findings that matter, sorted for a reader.
//
// # What is classified, and in what order
//
// Sorting is severity descending, then component name, then advisory id. That
// order is the argument: the top of the report is the sentence the customer
// reads before they stop reading, and a report sorted by advisory number
// would put the medium-severity one above the critical one because CVE-2024
// sorts before CVE-2025.
//
// # What is deliberately NOT dropped
//
// Unaffected advisories are not in the returned list — the report is about
// what a fix would touch, and a list of everything that is fine is a list
// nobody asked for. But they are counted, and the count is printed, because
// "17 components checked, 3 affected" is what makes the other 14 rows
// believable. An advisory whose class is unknown is kept and named unknown:
// dropping it would be the difference between "we do not know" and "nothing
// found", which is the one difference this package must not make quietly.
//
// The classification itself is pure — no clock, no network, no file — so
// every rule below is a table in a test rather than an explanation in prose.
func Classify(components []Component, advisories []Advisory) ([]Finding, Counts) {
	counts := Counts{Components: len(components), Advisories: len(advisories)}
	if len(components) == 0 || len(advisories) == 0 {
		return nil, counts
	}
	byName := make(map[string]Component, len(components))
	for _, c := range components {
		byName[c.Name] = c
	}
	var out []Finding
	for _, a := range advisories {
		c, ok := byName[a.Component]
		if !ok {
			// An advisory for a component nobody is running is not a finding.
			// It is not an error either: a feed that answers for a package
			// spelled slightly differently produces exactly this, and the
			// report's job is to not pretend it is a hit.
			continue
		}
		class := a.Affects(c.Version)
		switch class {
		case ClassAffected:
			counts.Affected++
		case ClassNoFix:
			counts.NoFix++
		case ClassUnaffected:
			counts.Unaffected++
		default:
			counts.Unknown++
		}
		if class == ClassUnaffected {
			continue
		}
		f := Finding{
			Component:  c.Name,
			Version:    c.Version,
			Kind:       c.Kind,
			Severity:   a.Severity,
			AdvisoryID: a.ID,
			Summary:    a.Summary,
			URL:        a.URL,
			Class:      class,
			Fixed:      a.Fixed,
			Impact:     c.Blast,
			Why:        whyFor(c, a, class),
		}
		if c.Blast.Apply == "" {
			f.ImpactUnknown = true
		}
		if class == ClassAffected && a.Fixed != "" {
			f.ReleaseTo = a.Fixed
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity.rank() != out[j].Severity.rank() {
			return out[i].Severity.rank() > out[j].Severity.rank()
		}
		if out[i].Component != out[j].Component {
			return out[i].Component < out[j].Component
		}
		return out[i].AdvisoryID < out[j].AdvisoryID
	})
	return out, counts
}

// whyFor is the sentence a reader acts on, and it names the version on the
// box rather than the range the advisory carries.
//
// Three separate sentences, because they are three separate actions:
// upgrade to a named version; mitigate, because nobody has published a fix;
// or find out what version this box is actually running, because cafe could
// not read it. A single "this component is affected" would collapse all
// three into one instruction that is wrong for two of them.
func whyFor(c Component, a Advisory, class Class) string {
	switch class {
	case ClassAffected:
		return fmt.Sprintf("%s %s is affected; %s fixes it, and applying it touches: %s",
			c.Name, emptyAs("no readable version", c.Version), a.Fixed, blastOf(c))
	case ClassNoFix:
		return fmt.Sprintf("%s %s is affected and no fix is published — this one needs a decision rather than an upgrade",
			c.Name, emptyAs("no readable version", c.Version))
	default:
		return fmt.Sprintf("%s %s could not be compared against %s's affected range (%s introduced, %s fixed) — the version on the box is not three numbers",
			c.Name, emptyAs("no version", c.Version), a.ID,
			emptyAs("unknown", a.Introduced), emptyAs("unknown", a.Fixed))
	}
}

func blastOf(c Component) string {
	if c.Blast.Apply == "" {
		return "an unknown blast radius — nobody has characterized what installing this touches"
	}
	restarts := "nothing else restarts"
	if len(c.Blast.Restarts) > 0 {
		restarts = strings.Join(c.Blast.Restarts, "; ")
	}
	return c.Blast.Apply + ", and " + restarts
}

func emptyAs(fallback, value string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

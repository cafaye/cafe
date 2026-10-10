package failure

import (
	"fmt"
	"sort"
	"strings"
)

// Verdict is what the checker concluded about one rule. It is three values and
// not two because the third is the one that keeps the other two honest: a rule
// the observation cannot answer is neither a hold nor a pass, and collapsing it
// into either is how a library stops being trusted.
type Verdict string

const (
	// Holds means the rule's condition was identified on this box.
	Holds Verdict = "holds"
	// DoesNotHold means the condition was evaluated and was not identified.
	DoesNotHold Verdict = "does-not-hold"
	// Unknown means the observation does not carry a fact the condition reads.
	Unknown Verdict = "unknown"
)

// Finding is one rule's answer: which rule, what the checker concluded, and —
// for an Unknown — the fact it needed and the box did not supply.
type Finding struct {
	Rule Rule
	// Verdict is the conclusion.
	Verdict Verdict
	// Missing is the name of the observation field the rule reads and the
	// observation did not carry. It is set exactly when Verdict is Unknown,
	// and it is the sentence that tells a reader what the box owes this
	// library rather than telling them a rule did not fire.
	Missing string
	// Matched is the condition as the observation satisfied it, for the
	// predicates that matched. It is here so a hold can be checked by hand:
	// "courier's /readyz took more than 4000ms to answer and answered 503" is
	// a sentence a reader can go and confirm.
	Matched []string
}

// Report is what one check produced. It is the product in the same sense
// lock/verify.go's report is: the thing a reader acts on, and the thing a test
// asserts the shape of.
type Report struct {
	// Rules is how many entries were evaluated, so a report over zero of them
	// cannot read as a clean bill of health.
	Rules int
	// Findings is one per rule, in library order — the order the doc's table
	// is in, so a reader comparing the two is comparing like with like.
	Findings []Finding
}

// Holds is the findings whose condition was identified, in library order.
func (r Report) Holds() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Verdict == Holds {
			out = append(out, f)
		}
	}
	return out
}

// Unknown is the findings the observation could not answer.
func (r Report) Unknown() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Verdict == Unknown {
			out = append(out, f)
		}
	}
	return out
}

// Evaluate checks every rule in the library against one observation. It is a
// pure function of the two, and it never fails: an entry whose condition cannot
// be answered is reported as Unknown with the missing fact named, which is the
// only answer that is both true and useful.
//
// Rules is a parameter rather than read from Library() so a caller can check a
// subset, a superset, or the library with an eleventh entry in it — the check
// is the same function whichever set it is handed, which is what makes the
// library's growth free.
func Evaluate(rules []Rule, obs Observation) Report {
	report := Report{Rules: len(rules), Findings: make([]Finding, 0, len(rules))}
	for _, rule := range rules {
		holds, missing := rule.Condition.evaluate(obs)
		finding := Finding{Rule: rule}
		switch {
		case missing != "":
			finding.Verdict = Unknown
			finding.Missing = missing
		case holds:
			finding.Verdict = Holds
			finding.Matched = matched(rule.Condition, obs)
		default:
			finding.Verdict = DoesNotHold
		}
		report.Findings = append(report.Findings, finding)
	}
	return report
}

// matched renders the predicates that were satisfied, so a hold is checkable by
// hand. A predicate that was evaluated and did not hold is left out: the
// sentence is evidence for the conclusion, not a transcript of the walk.
func matched(c Condition, obs Observation) []string {
	var out []string
	for _, p := range c.All {
		if ok, known := p.evaluate(obs); known && ok {
			out = append(out, p.String())
		}
	}
	for _, p := range c.Any {
		if ok, known := p.evaluate(obs); known && ok {
			out = append(out, p.String())
		}
	}
	return out
}

// String is the report, and it is the product. Four rules, each of them the
// alternative measured somewhere in this fleet's history:
//
//	Every hold names the rule, what has happened, what a monitor sees, what we
//	know, and the remedy — because a finding that stopped at "courier /readyz
//	slow" would be a sentence that sends a reader back to the dashboard that
//	could not see it.
//
//	The Unknowns are listed with the fact each one needs. A library that
//	reported only what it could see would be read as a library that had
//	checked everything, and it had not.
//
//	The count, and how many of each verdict. A report of ten findings where one
//	reads as an incident and ten as noise is the difference between an acted-on
//	report and a filed one.
//
//	No remedy is printed twice. Forty copies of the same paragraph above forty
//	filenames is a wall — the argument lock/verify.go:195-201 already made.
func (r Report) String() string {
	var out strings.Builder

	holds := r.Holds()
	unknown := r.Unknown()

	fmt.Fprintf(&out, "caf failure: %d rule(s) evaluated: %d hold, %d do not hold, %d unknown.\n",
		r.Rules, len(holds), r.Rules-len(holds)-len(unknown), len(unknown))

	if len(holds) > 0 {
		fmt.Fprintf(&out, "\nCONDITIONS THAT HOLD\n")
		for _, f := range holds {
			fmt.Fprintf(&out, "\n  %s\n", f.Rule.ID)
			fmt.Fprintf(&out, "    failure:  %s\n", f.Rule.Failure)
			fmt.Fprintf(&out, "    a monitor sees: %s\n", f.Rule.Sees)
			fmt.Fprintf(&out, "    identified by: %s\n", f.Rule.Condition.describe())
			for _, m := range f.Matched {
				fmt.Fprintf(&out, "    observed: %s\n", m)
			}
			fmt.Fprintf(&out, "    what we know: %s\n", f.Rule.Known)
			fmt.Fprintf(&out, "    remedy:   %s\n", f.Rule.Remedy)
			if f.Rule.Contract != "" {
				fmt.Fprintf(&out, "    contract: %s\n", f.Rule.Contract)
			}
		}
	}

	if len(unknown) > 0 {
		fmt.Fprintf(&out, "\nCONDITIONS THAT COULD NOT BE ANSWERED\n")
		for _, f := range unknown {
			fmt.Fprintf(&out, "\n  %s\n", f.Rule.ID)
			fmt.Fprintf(&out, "    failure: %s\n", f.Rule.Failure)
			fmt.Fprintf(&out, "    the observation does not carry %s, so this rule was not evaluated.\n", f.Missing)
		}
		fmt.Fprintf(&out, "\n  A rule that could not be answered is not a rule that passed. The facts above are "+
			"what the box's observation surface owes this library.\n")
	}

	return out.String()
}

// Describe is the one line a caller prints beside the count: how many rules,
// how many held. It is the library's own summary and it is what `caf failure
// check` prints last.
func (r Report) Describe() string {
	return fmt.Sprintf("%d rule(s), %d condition(s) hold, %d unknown", r.Rules, len(r.Holds()), len(r.Unknown()))
}

// SortedMissing is the observation fields the library needed and did not get,
// deduplicated and sorted, so a reader can see the shape of what is missing
// rather than reading the same field named nine times.
func (r Report) SortedMissing() []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range r.Findings {
		if f.Verdict != Unknown || seen[f.Missing] {
			continue
		}
		seen[f.Missing] = true
		out = append(out, f.Missing)
	}
	sort.Strings(out)
	return out
}

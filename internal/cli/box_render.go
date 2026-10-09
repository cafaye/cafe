package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/cafaye/caf/internal/box"
)

// The human half of the split. Every doc the box commands produce is rendered
// twice — as JSON on stdout for a machine and as plain lines here — and this
// file is the plain half.
//
// It uses text/tabwriter and nothing else. This repository has no TUI
// framework and `caf dev` prints plain ordered progress for the same reason:
// the information is the product, and a table with a border is a table someone
// has to keep aligned.
//
// The one rule these renderers hold: a private key never appears. The verify
// rows say `yes` or `no` about the key's presence and never the key's value,
// which is why the renderer for a fact is one line and not the whole struct.

// writeJSON is the machine half. Indented, one trailing newline, and returned
// as an error so a caller that failed to write does not report success.
func writeJSON(v any, w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeHuman(w io.Writer, message string) error {
	_, err := fmt.Fprintln(w, message)
	return err
}

// writeStatusHuman is the sentence a person reads: which box, where it is,
// what is next. An unenrolled box is reported as a status rather than hidden,
// because the question an agent asks a fresh box is exactly this one.
func writeStatusHuman(w io.Writer, st *box.Status) error {
	if !st.Enrolled {
		if st.Journal != "" {
			if _, err := fmt.Fprintf(w, "not enrolled; an enrollment is unfinished (state %s)\n", st.Journal); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintln(w, "not enrolled"); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := fmt.Fprintf(w, "box     %s\n", st.Identity.BoxID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "account %s\n", st.Identity.AccountID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "phase   %s\n", st.Identity.Phase); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "status  %s\n", st.Identity.Status); err != nil {
		return err
	}
	if st.Identity.EnrolledAt != "" {
		if _, err := fmt.Fprintf(w, "since   %s\n", st.Identity.EnrolledAt); err != nil {
			return err
		}
	}
	if st.Next != nil {
		if _, err := fmt.Fprintf(w, "next    %s — %s\n", st.Next.Name, st.Next.Title); err != nil {
			return err
		}
	}
	if len(st.Completed) > 0 {
		if _, err := fmt.Fprintf(w, "reached %s\n", joinLast(st.Completed)); err != nil {
			return err
		}
	}
	return nil
}

// writePlanHuman renders the install as a table. One row per phase, the current
// one marked, and the appliable one flagged, because a plan's whole job is to
// make the next action obvious.
func writePlanHuman(w io.Writer, plan *box.Plan) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintf(tw, "  #\tphase\tstate\tnow\twhat\n"); err != nil {
		return err
	}
	for _, step := range plan.Steps {
		now := ""
		if step.Appliable {
			now = "yes"
		}
		if _, err := fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\n", step.Index, step.Name, step.State, now, step.Title); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if plan.Next != "" {
		_, err := fmt.Fprintf(w, "\nnext: caf box apply --phase %s\n", plan.Next)
		return err
	}
	_, err := fmt.Fprintf(w, "\n%s\n", plan.Reason)
	return err
}

// writeEventsHuman is the log, oldest first, one line each. The phase column is
// blank for an event that was not about a phase.
func writeEventsHuman(w io.Writer, events []box.Event) error {
	if len(events) == 0 {
		_, err := fmt.Fprintln(w, "(no events)")
		return err
	}
	for _, e := range events {
		phase := e.Phase
		if phase == "" {
			phase = "-"
		}
		if _, err := fmt.Fprintf(w, "%s  %-8s %-10s %s\n", e.At.Format("2006-01-02 15:04:05Z"), e.Action, phase, e.Message); err != nil {
			return err
		}
	}
	return nil
}

// writeVerifyHuman is the fact table. `key present` and `key matches` are the
// two rows invariant 1 is about, and both print a yes/no.
func writeVerifyHuman(w io.Writer, v *box.Verify) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, f := range v.Facts {
		mark := "ok  "
		if !f.Pass {
			mark = "FAIL"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", mark, f.Name, f.Detail); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if v.OK {
		_, err := fmt.Fprintln(w, "verified")
		return err
	}
	_, err := fmt.Fprintln(w, "not verified: "+v.Reason)
	return err
}

// joinLast is the last element with its predecessor, for a `reached` line that
// says where a box has got to without listing all six phases.
func joinLast(names []string) string {
	if len(names) == 0 {
		return ""
	}
	if len(names) == 1 {
		return names[0]
	}
	return names[len(names)-2] + ", " + names[len(names)-1]
}

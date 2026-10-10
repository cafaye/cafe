package secret

import (
	"encoding/json"
	"io"
	"time"
)

// Document is the JSON answer `caf secret inventory` prints.
//
// It is one object, not a stream, and it is written to stdout as one document —
// the same contract internal/agent holds, where machine JSON goes to stdout and
// human prose goes to stderr, so a script can pipe it without a parser having to
// guess where the words end.
type Document struct {
	BoxID string `json:"box_id,omitempty"`
	// GeneratedAt is when the document was rendered, RFC3339. It is the only
	// timestamp that is not a fact about a secret.
	GeneratedAt string `json:"generated_at"`
	// Counts is the shape of the inventory in three numbers, so a reader can
	// see "3 rotatable, 12 conditional, 8 not-rotatable" without walking the
	// entries. It is a summary and not a claim: the entries are the claim.
	Counts map[Class]int `json:"counts"`
	// Entries is every entry, in the installer's order.
	Entries []Entry `json:"entries"`
	// Due is the names of the rotatable class that are past their interval.
	Due []string `json:"due,omitempty"`
}

// Render writes the inventory as one JSON document.
//
// An entry's class, reason, source and last-rotated are what the packet asked
// for; the last-rotated comes from the state and is absent when the entry has
// never been rotated on this box, which is a different statement from "rotated
// at the epoch" and must not render as one.
func Render(w io.Writer, boxID string, state State, now time.Time) error {
	entries := Inventory()
	doc := Document{
		BoxID:       boxID,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Counts:      map[Class]int{},
		Entries:     entries,
	}
	for _, e := range entries {
		doc.Counts[e.Class]++
	}
	for _, e := range Due(state, now) {
		doc.Due = append(doc.Due, e.Name)
	}
	for i := range doc.Entries {
		if at, ok := state.Last[doc.Entries[i].StateKey()]; ok {
			doc.Entries[i].LastRotated = at.UTC().Format(time.RFC3339)
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

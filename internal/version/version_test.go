package version

import (
	"strings"
	"testing"
)

// The stamp on the installed agent skill is what tells a person that the
// skill on their disk was written by the binary they are running. It used to
// say "dev" in every release, because the release build never injected a
// version into it.

// withVersion runs f with Current set to v, and puts Current back afterwards.
func withVersion(t *testing.T, v string, f func()) {
	t.Helper()
	old := Current
	Current = v
	t.Cleanup(func() { Current = old })
	f()
}

// A release version appears in the header. This is the whole point of the
// stamp: a skill that says "dev" next to a binary that says "cafe 1.2.3"
// tells the reader nothing.
func TestSkillHeaderCarriesTheVersion(t *testing.T) {
	withVersion(t, "1.2.3", func() {
		header := SkillHeader()

		if !strings.Contains(header, "1.2.3") {
			t.Errorf("header %q does not carry the version", header)
		}
		if !strings.Contains(header, ManagedBy) {
			t.Errorf("header %q does not carry the management marker", header)
		}
		if !strings.HasPrefix(header, "<!--") {
			t.Errorf("header %q is not a comment", header)
		}
	})
}

// An un-injected build says so.
func TestSkillHeaderDefaultsToDev(t *testing.T) {
	withVersion(t, "dev", func() {
		if !strings.Contains(SkillHeader(), "cli_version: dev") {
			t.Errorf("header %q does not say dev", SkillHeader())
		}
	})
}

// A file is ours when its FIRST line carries the marker. A file that merely
// mentions it in its body is not ours, and treating it as ours would
// overwrite a person's file on the strength of a sentence inside it.
func TestIsManaged(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "our own header",
			content: "<!-- managed-by: cafe-cli | cli_version: 1.2.3 -->\n\nbody\n",
			want:    true,
		},
		{
			name:    "a person's own file",
			content: "# my notes\n\nbody\n",
			want:    false,
		},
		{
			name:    "the marker in the body is not the header",
			content: "# my notes\n\nmanaged-by: cafe-cli\n",
			want:    false,
		},
		{
			name:    "an empty file",
			content: "",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsManaged([]byte(tt.content)); got != tt.want {
				t.Errorf("IsManaged = %t, want %t", got, tt.want)
			}
		})
	}
}

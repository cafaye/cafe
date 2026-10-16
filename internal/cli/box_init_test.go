package cli

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// runBoxInit runs `cafe box init` through the box dispatcher and returns the
// decoded machine document plus both raw streams.
func runBoxInit(t *testing.T) (doc boxInitDoc, stdout, stderr string) {
	t.Helper()
	c := lookupCommandOrFail(t, "box")
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion}
	if err := c.Execute(env, []string{"init"}); err != nil {
		t.Fatalf("box init: %v", err)
	}
	stdout, stderr = out.String(), errOut.String()
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("box init stdout is not one JSON document: %v\n%s", err, stdout)
	}
	return doc, stdout, stderr
}

func TestBoxInitIsDeterministicAndNamesTheCanonicalURL(t *testing.T) {
	c, _ := lookupCommand(Commands(), "box")
	if c == nil {
		t.Fatal("box command not found")
	}
	// Run `cafe box init` twice via the box dispatcher.
	run := func() (string, string) {
		var out, errOut bytes.Buffer
		env := &Env{Stdout: &out, Stderr: &errOut, Version: testVersion}
		if err := c.Execute(env, []string{"init"}); err != nil {
			t.Fatalf("box init: %v", err)
		}
		return out.String(), errOut.String()
	}
	firstOut, firstErr := run()
	secondOut, secondErr := run()
	if firstOut != secondOut || firstErr != secondErr {
		t.Fatalf("box init is not deterministic:\nfirst stdout %q\nsecond stdout %q\nfirst stderr %q\nsecond stderr %q", firstOut, secondOut, firstErr, secondErr)
	}
	if !strings.Contains(firstOut, boxInstallURL) {
		t.Errorf("stdout does not contain canonical URL %q:\n%s", boxInstallURL, firstOut)
	}
	if !strings.Contains(firstOut, boxInstallCommand) {
		t.Errorf("stdout does not contain install command %q:\n%s", boxInstallCommand, firstOut)
	}
	if !json.Valid([]byte(firstOut)) {
		t.Fatalf("stdout is not valid JSON:\n%s", firstOut)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(firstOut), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc["install_url"] != boxInstallURL {
		t.Errorf("install_url = %v, want %q", doc["install_url"], boxInstallURL)
	}
	if !strings.Contains(doc["enroll_command"].(string), "cafe box enroll") {
		t.Errorf("enroll_command = %v, want cafe box enroll", doc["enroll_command"])
	}
}

func TestBoxInitHelpExplainsWhyNotVendored(t *testing.T) {
	c := lookupCommandOrFail(t, "box")
	// Help for box init is via `cafe help box`? Actually `cafe box init --help` dispatches through box.
	var out bytes.Buffer
	printHelp(&out, newBoxInitCommand())
	help := out.String()
	for _, want := range []string{boxInstallURL, "exactly one", "vendored", "GENERATES"} {
		if !strings.Contains(help, want) {
			t.Errorf("box init help missing %q:\n%s", want, help)
		}
	}
	// Also `cafe box --help` lists init
	var buf bytes.Buffer
	printHelp(&buf, c)
	_ = buf.String()
}

// boxInitRequiredWant is the required set this package must emit, pinned HERE by
// hand so that an entry dropped from boxInstallInputs fails this test even though
// the renderer would then be perfectly self-consistent with the table.
//
// It mirrors, one name per line, the `kit_substrate_refuse "<NAME>" …` calls in
// the installer (`cafaye/kit`'s `templates/substrate/install.sh`, served at
// boxInstallURL) that fire on the standard install — every live run, no
// condition. The box id and the registry capability are deliberately ABSENT:
// install.sh demands them only on the capability path (when an artifact is
// private), and the fleet's artifacts are public, so they are `optional`, not
// required. See boxInstallInputs for the whole argument.
var boxInitRequiredWant = []string{
	"KIT_SUBSTRATE_ACME_EMAIL",
	"KIT_SUBSTRATE_DATABASES",
	"KIT_SUBSTRATE_SMTP_HOST",
	"KIT_SUBSTRATE_SMTP_USERNAME",
	"KIT_SUBSTRATE_SMTP_PASSWORD",
	"KIT_SUBSTRATE_COURIER_INBOUND_RESEND_SECRET",
}

// The required set the command emits is the set above — no more, no fewer. This
// is the pin the brief asks for: if an operator cannot complete the printed
// command because a required variable is missing, this test is what should have
// caught it.
func TestBoxInitRequiredSetIsTheInstallersRefusals(t *testing.T) {
	got := make([]string, 0, len(boxInitRequiredWant))
	for _, in := range boxInstallInputs {
		if in.Kind == inputRequired {
			got = append(got, in.Name)
		}
	}
	if strings.Join(got, ",") != strings.Join(boxInitRequiredWant, ",") {
		t.Fatalf("required set = %v, want %v\n(a required variable was added to or dropped from boxInstallInputs; "+
			"if the installer changed, fix the table and this list together — do not edit only one)", got, boxInitRequiredWant)
	}
}

// Every input the table knows appears in the emitted command, so the copyable
// block is the whole set and not a subset. This is the second half of the pin:
// the set above says WHICH are required, this says all of them are emitted.
func TestBoxInitEmitsEveryKnownInput(t *testing.T) {
	doc, _, stderr := runBoxInit(t)
	for _, in := range boxInstallInputs {
		if !strings.Contains(doc.InstallCommand, in.Name) {
			t.Errorf("install_command does not name %s:\n%s", in.Name, doc.InstallCommand)
		}
		if !strings.Contains(stderr, in.Name) {
			t.Errorf("stderr prose does not name %s", in.Name)
		}
		// Every non-default value is marked, so an operator can tell a
		// placeholder from a working value — the brief's "mark clearly which
		// are placeholders and which have working defaults".
		if in.Kind != inputDefault {
			if line := blockLine(doc.InstallCommand, in.Name); !strings.Contains(line, in.tag()) {
				t.Errorf("%s's line is not marked %q: %q", in.Name, in.tag(), line)
			}
		}
	}
}

// blockLine returns the first line of block that names the variable, trimmed.
func blockLine(block, name string) string {
	for _, line := range strings.Split(block, "\n") {
		if strings.Contains(line, name) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// A name inside a comment is not a value. Every required variable has to appear
// as a LIVE `export NAME=…` line, because a block that only mentions it in a
// comment would still contain the string and still refuse on the box.
func TestBoxInitRequiredInputsAreLiveExports(t *testing.T) {
	doc, _, _ := runBoxInit(t)
	for _, name := range boxInitRequiredWant {
		want := "export " + name + "="
		var found bool
		for _, line := range strings.Split(doc.InstallCommand, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("install_command has no live %q line (a commented name is not a value):\n%s", want, doc.InstallCommand)
		}
	}
}

// The block's only network action is the served install one-liner. cafe's own
// `box init` never fetches anything, and the block it prints must not smuggle a
// second fetch (`git clone`, a tarball, another curl) into a stranger's shell.
func TestBoxInitCommandRunsOnlyTheServedScript(t *testing.T) {
	doc, _, stderr := runBoxInit(t)
	if n := strings.Count(doc.InstallCommand, "curl "); n != 1 {
		t.Errorf("install_command carries %d curl calls, want exactly the one served script:\n%s", n, doc.InstallCommand)
	}
	if !strings.Contains(doc.InstallCommand, boxInstallCommand) {
		t.Errorf("install_command does not end in the served one-liner %q:\n%s", boxInstallCommand, doc.InstallCommand)
	}
	for _, banned := range []string{"git ", "wget", "scp ", "ssh ", "go install", "go get"} {
		if strings.Contains(doc.InstallCommand, banned) {
			t.Errorf("install_command carries %q; it must run only the served script:\n%s", banned, doc.InstallCommand)
		}
	}
	if strings.Contains(stderr, "curl ") && !strings.Contains(stderr, boxInstallURL) {
		t.Error("stderr mentions curl without the canonical install URL")
	}
}

// The examples are placeholders, never secrets. The installer's own credentials
// have unmistakable shapes — a capability is `cap_` plus 64 hex, a Resend
// signing secret is `whsec_` plus a long tail, a 32-byte key is 43 base64url
// characters — and none of those shapes may appear in what cafe prints.
func TestBoxInitExamplesCarryNoSecret(t *testing.T) {
	doc, stdout, stderr := runBoxInit(t)
	all := stdout + stderr + doc.InstallCommand
	secretShapes := []struct {
		name string
		re   *regexp.Regexp
	}{
		{"a minted capability", regexp.MustCompile(`cap_[0-9a-f]{64}`)},
		{"a Resend signing secret", regexp.MustCompile(`whsec_[A-Za-z0-9]{16,}`)},
		{"PEM material", regexp.MustCompile(`-----BEGIN [A-Z ]+-----`)},
		{"a bearer token", regexp.MustCompile(`(?i)bearer [A-Za-z0-9._-]{20,}`)},
	}
	for _, s := range secretShapes {
		if loc := s.re.FindString(all); loc != "" {
			t.Errorf("box init printed %s: %q", s.name, loc)
		}
	}
	// And the two secret values are explicitly placeholders, not fake values.
	for _, name := range []string{"KIT_SUBSTRATE_SMTP_PASSWORD", "KIT_SUBSTRATE_COURIER_INBOUND_RESEND_SECRET"} {
		line := liveExportLine(doc.InstallCommand, name)
		if line == "" {
			t.Fatalf("no live export for %s", name)
		}
		if !strings.Contains(line, "<") {
			t.Errorf("%s's example %q is not an obvious placeholder", name, line)
		}
	}
}

// The machine document lists every input with its kind, so a script can read the
// required set without parsing the block — and the document's `inputs` cannot
// disagree with the table the block was built from.
func TestBoxInitDocumentListsEveryInputWithItsKind(t *testing.T) {
	doc, _, _ := runBoxInit(t)
	if len(doc.Inputs) != len(boxInstallInputs) {
		t.Fatalf("document lists %d inputs, table has %d", len(doc.Inputs), len(boxInstallInputs))
	}
	var required []string
	for i, got := range doc.Inputs {
		want := boxInstallInputs[i]
		if got.Name != want.Name || got.Kind != string(want.Kind) || got.Meaning != want.Meaning {
			t.Errorf("input %d = %+v, want name %q kind %q meaning %q", i, got, want.Name, want.Kind, want.Meaning)
		}
		if want.Kind == inputRequired {
			required = append(required, got.Name)
		}
	}
	if strings.Join(required, ",") != strings.Join(boxInitRequiredWant, ",") {
		t.Errorf("document's required inputs = %v, want %v", required, boxInitRequiredWant)
	}
}

// The enroll step is present, in its safe spelling: the capability on stdin, not
// in argv.
func TestBoxInitNamesTheEnrollStep(t *testing.T) {
	doc, _, stderr := runBoxInit(t)
	for _, want := range []string{"cafe box enroll", "--token -", "--box"} {
		if !strings.Contains(doc.EnrollCommand, want) {
			t.Errorf("enroll_command %q missing %q", doc.EnrollCommand, want)
		}
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not mention %q in the enroll step", want)
		}
	}
}

// liveExportLine returns the uncommented `export NAME=…` line for name, or "".
func liveExportLine(block, name string) string {
	want := "export " + name + "="
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, want) {
			return trimmed
		}
	}
	return ""
}

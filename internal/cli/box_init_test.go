package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

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

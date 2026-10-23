package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The skill file is the document an agent reads to learn what cafe can do. It
// shipped with commands that do not exist (`cafe project add-service`), with
// commands that were stubs (`cafe task get`), and with commands that called
// routes ship does not have (`cafe task approve`). Nothing caught it: the
// file is prose, and prose does not fail a build.
//
// This test is the thing that catches it. Every `cafe <command>` in the file
// has to resolve in the registry — and so does every `cafe <command>
// <subcommand>`, because a command that exists is not enough when the
// subcommand under it does not.

// skillCommandToken matches a command invocation in the skill file.
//
// The match has to begin at the start of a line or right after a backtick, so
// a sentence that happens to mention cafe is not read as a command. The
// second group is the subcommand, when the invocation names one.
var skillCommandToken = regexp.MustCompile("(?m)(?:^|`)\\s*cafe\\s+([a-z][a-z0-9-]*)(?:\\s+([a-z][a-z0-9-]*))?")

// skillFilePath is the embedded skill, read from where the embed reads it.
const skillFilePath = "../skills/assets/anywaye/SKILL.md"

// Every command the skill file names has to exist.
func TestTheSkillFileOnlyNamesCommandsThatExist(t *testing.T) {
	data, err := os.ReadFile(skillFilePath)
	if err != nil {
		t.Fatalf("read the skill file: %v", err)
	}
	matches := skillCommandToken.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		t.Fatal("the skill file names no commands at all; the extraction is broken and this test proves nothing")
	}

	commands := Commands()
	for _, m := range matches {
		name, sub := m[1], m[2]
		parent, found := lookupCommand(commands, name)
		if !found {
			t.Errorf("the skill file names %q, which is not a command", name)
			continue
		}
		if sub == "" {
			continue
		}
		if !advertisesSubcommand(parent, sub) {
			t.Errorf("the skill file names %q, and %q has no such subcommand", name+" "+sub, parent.Name)
		}
	}
}

// advertisesSubcommand reports whether a command tells the reader it has the
// subcommand in question.
//
// It reads the help, because the help is what the reader has. A verb the
// help does not mention is a verb nobody can know about, so the skill file
// may not name it either.
func advertisesSubcommand(c *Command, sub string) bool {
	for _, name := range advertisedSubcommands(c) {
		if name == sub {
			return true
		}
	}
	return false
}

// advertisedSubcommands returns the subcommand names a command advertises:
// the "Subcommands:" block of its help, the "Commands:" verb table, and the
// alternatives in its usage line.
//
// The three sources are the three ways this tree writes a verb list down, and
// all three are read rather than parsed out of the code, so a command that
// changes what it advertises changes this with it.
func advertisedSubcommands(c *Command) []string {
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}

	for _, alt := range usageAlternatives(c.Usage) {
		add(alt)
	}

	block := ""
	for _, line := range strings.Split(c.LongHelp, "\n") {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "Subcommands:":
			block = "subcommands"
			continue
		case "Commands:":
			block = "commands"
			continue
		}
		if block == "" {
			continue
		}
		if trimmed == "" {
			block = ""
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			block = ""
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		// The verb table writes the FULL invocation — "secret inventory" —
		// where the subcommand list writes the word on its own.
		if name == c.Name && len(fields) > 1 {
			name = fields[1]
		}
		add(name)
	}
	return names
}

// usageAlternatives returns the `<a|b|c>` alternatives in a usage line.
func usageAlternatives(usage string) []string {
	var names []string
	for _, group := range regexp.MustCompile(`<([^>]+)>`).FindAllStringSubmatch(usage, -1) {
		if !strings.Contains(group[1], "|") {
			continue
		}
		for _, name := range strings.Split(group[1], "|") {
			names = append(names, strings.TrimSpace(name))
		}
	}
	return names
}

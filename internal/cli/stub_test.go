package cli

import (
	"errors"
	"flag"
	"strings"
	"testing"
)

// stubCommand is one row of the stub table: the subcommand, the number of
// positional arguments it wants, and a flag invocation that must be accepted
// before the stub gives up.
type stubCommand struct {
	name  string
	args  int
	flags []string
}

// stubCommands is the single source of truth for which subcommands are stubs.
//
// A stub parses its flags, checks its argument count and then returns
// errNotImplemented. It never half-writes anything. When a subcommand's real
// behavior lands, its row moves out of this table and the command gets a
// behavior test of its own, the way version_test.go and doctor_test.go do.
var stubCommands = []stubCommand{}

// workingCommands are the subcommands that are not stubs because they already
// work.
//
// Most of them are pinned by a test that drives the command itself: the
// ship-backed ones by ship_test.go, which runs them against a server that
// mirrors ship's route table, and the rest by the test file that sits next to
// the command it tests.
//
// The ones that are NOT are untestedCommands, and they are named rather than
// quietly counted: a table that claims more than the tree proves is the same
// drift this file exists to catch.
// A verb that groups others is dispatched by its parent's Run and is never in
// this list, so `env` is here and `env up` is not, and `gen` is here while
// `gen telemetry` is not.
var workingCommands = []string{
	"add", "agent", "backup", "board", "box", "boxd", "contract", "cve", "deploy", "dev", "devbox", "docs", "errors", "doctor", "env", "failure", "gate", "gen", "grafana", "init", "landing", "lock", "login", "logs", "mcp", "metrics", "new", "pipeline", "project", "provider", "publish", "reclaim", "repo", "report", "resolve", "secret", "skills", "start", "task", "update", "version",
}

// untestedCommands are the working commands with no behavior test of their
// own. Each one forwards to another program — instabox, the error store,
// Grafana, Loki, Prometheus — and has nothing of its own to assert.
//
// They are listed so that the claim above stays true, and so that writing
// the missing test is a deletion here rather than a contradiction.
var untestedCommands = []string{
	"devbox", "errors", "grafana", "logs", "metrics",
}

// invocation builds a valid command line for a stub: its flags, then the
// positional arguments it wants.
func (s stubCommand) invocation() []string {
	args := append([]string{}, s.flags...)
	for range s.args {
		args = append(args, "acme")
	}
	return args
}

// The stub must be honest: it reports the sentinel, and the message names the
// command the user typed.
func TestStubReturnsNotImplemented(t *testing.T) {
	for _, stub := range stubCommands {
		t.Run(stub.name, func(t *testing.T) {
			c := lookupCommandOrFail(t, stub.name)

			err := c.Execute(newTestEnv(), stub.invocation())

			if !errors.Is(err, errNotImplemented) {
				t.Fatalf("err = %v, want it to wrap errNotImplemented", err)
			}
			if want := "cafe " + stub.name + ": " + errNotImplemented.Error(); err.Error() != want {
				t.Errorf("err = %q, want %q", err, want)
			}
		})
	}
}

// A stub that was wired is not a usage mistake, so it exits 1 like any other
// failure and says why on stderr.
func TestStubExitsOneWithItsReason(t *testing.T) {
	for _, stub := range stubCommands {
		t.Run(stub.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, testVersion, append([]string{stub.name}, stub.invocation()...)...)

			if code != exitFailure {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, exitFailure, stderr)
			}
			if want := "cafe " + stub.name + ": " + errNotImplemented.Error(); !strings.Contains(stderr, want) {
				t.Errorf("stderr missing %q\ngot:\n%s", want, stderr)
			}
			if stdout != "" {
				t.Errorf("a stub must not write to stdout, got:\n%s", stdout)
			}
		})
	}
}

// The flags a stub declares are real flags: passing one must be parsed, not
// rejected. An undeclared flag exits 2, so exit 1 here proves the flag surface
// is wired.
func TestStubAcceptsItsOwnFlags(t *testing.T) {
	for _, stub := range stubCommands {
		t.Run(stub.name, func(t *testing.T) {
			code, _, stderr := runCLI(t, testVersion, append([]string{stub.name}, stub.invocation()...)...)

			if code == exitUsage {
				t.Errorf("exit code = %d, want the flags to parse (stderr: %s)", code, stderr)
			}
		})
	}
}

// Every flag named in the table must exist on the command, so the table cannot
// drift into testing flags a subcommand no longer declares. Table entries are
// name/value pairs, so only the even indexes name a flag.
func TestStubFlagsAreDeclared(t *testing.T) {
	for _, stub := range stubCommands {
		t.Run(stub.name, func(t *testing.T) {
			fs := lookupCommandOrFail(t, stub.name).FlagSet()
			declared := map[string]bool{}
			fs.VisitAll(func(f *flag.Flag) { declared[f.Name] = true })

			for i := 0; i < len(stub.flags); i += 2 {
				name := strings.TrimLeft(stub.flags[i], "-")
				if !declared[name] {
					t.Errorf("stub %q does not declare -%s, but the table passes %s", stub.name, name, stub.flags[i])
				}
			}
			if len(declared) == 0 {
				t.Errorf("stub %q declares no flags; a wired subcommand exposes its options", stub.name)
			}
		})
	}
}

// A bad argument count is the user's mistake, so it is a usage error and exits
// 2 — checked on both sides of the arity the command wants.
func TestStubRejectsBadArgumentCount(t *testing.T) {
	for _, stub := range stubCommands {
		for _, extra := range []int{-1, 1} {
			got := stub.args + extra
			if got < 0 {
				continue
			}
			t.Run(stub.name+"/"+pluralArgs(got), func(t *testing.T) {
				args := stub.invocation()
				for len(args) > len(stub.flags)+got {
					args = args[:len(args)-1]
				}
				for len(args) < len(stub.flags)+got {
					args = append(args, "acme")
				}

				err := lookupCommandOrFail(t, stub.name).Execute(newTestEnv(), args)

				if !errors.Is(err, errUsage) {
					t.Errorf("err = %v, want a usage error", err)
				}
				if errors.Is(err, errNotImplemented) {
					t.Errorf("a bad argument count must not be reported as unimplemented: %v", err)
				}
			})
		}
	}
}

// The table and the registry must agree, or the table is not the source of
// truth it claims to be. Every subcommand is either a stub or already working.
func TestStubTableMatchesTheRegistry(t *testing.T) {
	stubbed := map[string]bool{}
	for _, stub := range stubCommands {
		if stubbed[stub.name] {
			t.Errorf("%q is listed twice in stubCommands", stub.name)
		}
		stubbed[stub.name] = true
	}

	working := map[string]bool{}
	for _, name := range workingCommands {
		if working[name] {
			t.Errorf("%q is listed twice in workingCommands", name)
		}
		working[name] = true
	}

	for _, name := range commandNames(Commands()) {
		if !stubbed[name] && !working[name] {
			t.Errorf("%q is neither a stub nor a working command: add it to stubCommands or to workingCommands", name)
		}
	}

	for name := range stubbed {
		if _, found := lookupCommand(Commands(), name); !found {
			t.Errorf("stubCommands lists %q, which is not in the registry", name)
		}
	}
	for _, name := range workingCommands {
		if _, found := lookupCommand(Commands(), name); !found {
			t.Errorf("workingCommands lists %q, which is not in the registry", name)
		}
	}
}

// The commands with no behavior test are named, and the naming has to be
// true: each one is a working command, none of them is a stub, and none of
// them is listed twice. Otherwise the exception list grows into a place where
// a command can hide.
func TestUntestedCommandsAreWorkingCommands(t *testing.T) {
	stubbed := map[string]bool{}
	for _, stub := range stubCommands {
		stubbed[stub.name] = true
	}
	working := map[string]bool{}
	for _, name := range workingCommands {
		working[name] = true
	}

	seen := map[string]bool{}
	for _, name := range untestedCommands {
		if seen[name] {
			t.Errorf("%q is listed twice in untestedCommands", name)
		}
		seen[name] = true
		if stubbed[name] {
			t.Errorf("%q is a stub and untested; a stub is not a working command", name)
		}
		if !working[name] {
			t.Errorf("%q is in untestedCommands but not in workingCommands", name)
		}
		if _, found := lookupCommand(Commands(), name); !found {
			t.Errorf("untestedCommands lists %q, which is not in the registry", name)
		}
	}
}

// A stub's help must advertise the flags it will honor, otherwise the flag
// surface is invisible to the person about to type it.
func TestStubHelpRendersFlags(t *testing.T) {
	for _, stub := range stubCommands {
		t.Run(stub.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, testVersion, "help", stub.name)

			if code != exitSuccess {
				t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
			}
			if !strings.Contains(stdout, "cafe "+stub.name) {
				t.Errorf("help does not name the command\ngot:\n%s", stdout)
			}
			if !strings.Contains(stdout, "Flags:") {
				t.Errorf("help does not list the flags\ngot:\n%s", stdout)
			}
		})
	}
}

// Command cafe is the cafaye platform CLI.
//
// It is a thin entrypoint on purpose: it turns the process into an
// internal/cli.Options and exits with the code cli.Run returns. Everything a
// command needs arrives through that struct, so no command depends on package
// state and every one of them is testable in isolation.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/cafaye/cafe/internal/cli"
)

// Build identity, injected at link time:
//
//	go build -ldflags "-X main.version=1.2.3 -X main.commit=$(git rev-parse --short HEAD)" ./cmd/cafe
//
// .goreleaser.yml sets both for release builds. Left empty they resolve to the
// development defaults in internal/cli, so `go run ./cmd/cafe` works.
//
// The agent skill `cafe skills install` writes carries its own stamp, in
// internal/version.Current — a third -X, and one that has to be set as well:
//
//	go build -ldflags "-X github.com/cafaye/cafe/internal/version.Current=1.2.3" ./cmd/cafe
//
// A release that sets only the first two ships a binary that says "cafe
// 1.2.3" next to a skill that says "cli_version: dev", and that stamp is
// exactly what tells a person the two match.
var (
	version = ""
	commit  = ""
)

func main() {
	// This is the one place the signals are read, because it is the one place
	// that has a process. `cafe dev` is the command that cares: it cancels on
	// this to stop waiting and put the stack it started back down, and a run
	// whose teardown can never fire is a run that leaves four containers
	// behind.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// The handler is released before exiting so a signal arriving during a
	// command's own teardown does not interfere with it. `cafe dev` runs its
	// teardown on a context it makes itself, so this is belt and braces rather
	// than the mechanism.
	defer stop()

	os.Exit(cli.Run(cli.Options{
		Version: cli.Version{Semver: version, Commit: commit},
		Args:    os.Args[1:],
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Context: ctx,
	}))
}

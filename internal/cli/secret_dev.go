package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anywaye/cafe/internal/box"
)

// Dev secrets live in a FileRing under the project's keyring directory, filed
// under the "cafe-dev" service so they never collide with box or agent tokens.
// They are local-only, 0600, and gitignored. `cafe secret list` prints names
// only (safe for agents); `get` prints the value (dev only); `set` stores it.
//
// There is no .env file in this flow. cafe dev injects these into the process
// environment, and the manifest's env block declares which ones are needed.

const devSecretService = "cafe-dev"

// secretDevDeps is the wiring for the dev-secret verbs.
type secretDevDeps struct {
	// dir is the project directory (where keyring/ lives).
	dir string
	// ring is the store. Nil means "resolve from dir".
	ring *box.FileRing
}

func (d secretDevDeps) ringFor() box.FileRing {
	if d.ring != nil {
		return *d.ring
	}
	dir := d.dir
	if dir == "" {
		dir = "."
	}
	return box.FileRing{Dir: filepath.Join(dir, "keyring")}
}

// --- cafe secret list -------------------------------------------------------

func newSecretListCommand(deps secretDevDeps) *Command {
	opts := &secretDevListOptions{}
	c := &Command{
		Name:    "secret list",
		Summary: "names of the dev secrets in the local keyring (values never printed)",
		Usage:   "cafe secret list [flags]",
		LongHelp: "Lists the names of secrets stored in the local dev keyring. " +
			"Values are never printed, so this is safe to run in a transcript or hand to an agent. " +
			"Dev secrets live in keyring/ (gitignored, 0600). There is no .env file in this flow.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.dir, "dir", ".", "project directory holding the keyring")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) != 0 {
			return fmt.Errorf("%w: cafe secret list takes no arguments (usage: %s)", errUsage, c.Usage)
		}
		d := deps
		if opts.dir != "" {
			d.dir = opts.dir
		}
		ring := d.ringFor()
		names, err := listDevSecrets(ring)
		if err != nil {
			return err
		}
		out := env.Stdout
		if len(names) == 0 {
			_, _ = fmt.Fprintln(out, "cafe secret: no dev secrets stored. Use `cafe secret set KEY=value`.")
			return nil
		}
		for _, name := range names {
			_, _ = fmt.Fprintln(out, name)
		}
		return nil
	}
	return c
}

// listDevSecrets returns the secret names stored under the dev service.
func listDevSecrets(ring box.FileRing) ([]string, error) {
	entries, err := os.ReadDir(ring.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read keyring: %w", err)
	}
	prefix := devSecretService + "-"
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// Strip service prefix and flatten reversal is lossy, so we store
		// the raw name. For dev secrets the account IS the env var name,
		// flattened by FileRing. We recover by reading the file's... no, we
		// just use the filename. Since env var names are [A-Z_], flatten is
		// identity for them.
		acct := strings.TrimPrefix(name, prefix)
		names = append(names, acct)
	}
	sort.Strings(names)
	return names, nil
}

// --- cafe secret get --------------------------------------------------------

func newSecretGetCommand(deps secretDevDeps) *Command {
	opts := &secretDevGetOptions{}
	c := &Command{
		Name:    "secret get",
		Summary: "print one dev secret's value (dev only — never in prod or CI)",
		Usage:   "cafe secret get <KEY> [flags]",
		LongHelp: "Prints the value of one dev secret from the local keyring. " +
			"Dev secrets are disposable and localhost-scoped, so printing them is acceptable in dev. " +
			"In production, cafe secret get is blocked: boxd has no secret-read path.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.dir, "dir", ".", "project directory holding the keyring")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) != 1 {
			return fmt.Errorf("%w: cafe secret get wants exactly one key (usage: %s)", errUsage, c.Usage)
		}
		key := args[0]
		d := deps
		if opts.dir != "" {
			d.dir = opts.dir
		}
		ring := d.ringFor()
		val, err := ring.Get(devSecretService, key)
		if err != nil {
			return fmt.Errorf("cafe secret get: %w", err)
		}
		_, _ = fmt.Fprint(env.Stdout, val)
		return nil
	}
	return c
}

// --- cafe secret set --------------------------------------------------------

func newSecretSetCommand(deps secretDevDeps) *Command {
	opts := &secretDevSetOptions{}
	c := &Command{
		Name:    "secret set",
		Summary: "store a dev secret in the local keyring",
		Usage:   "cafe secret set KEY=value [flags]",
		LongHelp: "Stores a dev secret in the local keyring (0600, gitignored). " +
			"The value never appears in a committed file. Use KEY=value or KEY value.",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.dir, "dir", ".", "project directory holding the keyring")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) == 0 {
			return fmt.Errorf("%w: cafe secret set wants KEY=value (usage: %s)", errUsage, c.Usage)
		}
		// Accept "KEY=value" or "KEY value".
		var key, value string
		joined := strings.Join(args, " ")
		if idx := strings.Index(joined, "="); idx >= 0 {
			key = strings.TrimSpace(joined[:idx])
			value = joined[idx+1:]
		} else if len(args) == 2 {
			key = args[0]
			value = args[1]
		} else {
			return fmt.Errorf("%w: cafe secret set wants KEY=value (usage: %s)", errUsage, c.Usage)
		}
		if key == "" {
			return fmt.Errorf("%w: cafe secret set: key is empty", errUsage)
		}
		d := deps
		if opts.dir != "" {
			d.dir = opts.dir
		}
		ring := d.ringFor()
		if err := ring.Set(devSecretService, key, value); err != nil {
			return fmt.Errorf("cafe secret set: %w", err)
		}
		_, _ = fmt.Fprintf(env.Stdout, "cafe secret: stored %s\n", key)
		return nil
	}
	return c
}

// io is used for Fprint; keep the import.

type secretDevListOptions struct {
	dir string
}

type secretDevGetOptions struct {
	dir string
}

type secretDevSetOptions struct {
	dir string
}

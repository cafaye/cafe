package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/anywaye/cafe/internal/box"
)

type boxRetargetOptions struct {
	box string
}

func newBoxRetargetCommand(deps boxDeps) *Command {
	opts := &boxRetargetOptions{}
	c := &Command{
		Name:    "box retarget",
		Summary: "re-point an enrolled box at a moved control plane",
		Usage:   "cafe box retarget --box <url>",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.box, "box", "", "the new control-plane origin (https://host[:port] or host:port)")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		url := strings.TrimSpace(opts.box)
		if url == "" {
			url = strings.TrimSpace(os.Getenv(boxURLEnv))
		}
		if url == "" {
			return fmt.Errorf("%w: cafe box retarget wants --box <url> (usage: %s)", errUsage, c.Usage)
		}
		store, err := boxStoreFor(c, env, deps)
		if err != nil {
			return err
		}
		res, err := store.Retarget(url)
		if err != nil {
			if isNotEnrolled(err) {
				_, _ = fmt.Fprintf(env.Stderr, "cafe box retarget: %v\n", err)
				return errReported
			}
			// Validation errors name the field already (control plane "...").
			if strings.Contains(err.Error(), "control plane") {
				return fmt.Errorf("%w: %w (usage: %s)", errUsage, err, c.Usage)
			}
			return err
		}
		// Stderr is the sentence; stdout is the document.
		say(env.Stderr, res.Message)
		return writeJSONDoc(env.Stdout, res)
	}
	return c
}

func isNotEnrolled(err error) bool {
	return err != nil && strings.Contains(err.Error(), box.ErrNotEnrolled.Error())
}

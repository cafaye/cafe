package cli

import (
	"fmt"

	"github.com/anywaye/cafe/internal/features"
)

func newFeaturesCommand() *Command {
	return &Command{
		Name:    "features",
		Summary: "list global feature flags",
		Usage:   "cafe features",
		Run: func(args []string, env *Env) error {
			if err := wantArgs("features", "cafe features", 0, len(args)); err != nil {
				return err
			}
			if env.Features == nil {
				env.Features = features.New(features.Config{})
			}
			for name, on := range env.Features.All(env.Context, nil) {
				state := "off"
				if on {
					state = "on"
				}
				_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\n", name, state)
			}
			return nil
		},
	}
}

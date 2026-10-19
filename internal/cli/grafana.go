package cli

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func newGrafanaCommand() *Command {
	var baseURL string
	return &Command{
		Name:    "grafana",
		Summary: "Print the cafaye Grafana (dashboards) URL",
		Usage:   "cafe grafana [--url URL]",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&baseURL, "url", os.Getenv("CAFAYE_GRAFANA_URL"), "Grafana base URL")
		},
		Run: func(args []string, env *Env) error {
			if strings.TrimSpace(baseURL) == "" {
				baseURL = "http://localhost:15000"
			}
			_, _ = fmt.Fprintf(env.Stdout, "%s\n", strings.TrimRight(baseURL, "/"))
			return nil
		},
	}
}

package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func newMetricsCommand() *Command {
	var (
		baseURL string
		query   string
		tenant  string
	)
	return &Command{
		Name:    "metrics",
		Summary: "Query the anywaye metrics store (Prometheus/Mimir) for a PromQL result",
		Usage:   "cafe metrics [--url URL] [--query PROMQL] [--tenant TENANT]",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&baseURL, "url", os.Getenv("ANYWAYE_PROMETHEUS_URL"), "Prometheus/Mimir base URL")
			fs.StringVar(&query, "query", "up", "PromQL query")
			fs.StringVar(&tenant, "tenant", os.Getenv("ANYWAYE_PROMETHEUS_TENANT"), "tenant id (Mimir)")
		},
		Run: func(args []string, env *Env) error {
			if strings.TrimSpace(baseURL) == "" {
				baseURL = "http://localhost:15902"
			}
			baseURL = strings.TrimRight(baseURL, "/")
			if strings.TrimSpace(tenant) == "" {
				tenant = "single-tenant"
			}
			url := fmt.Sprintf("%s/api/v1/query?query=%s", baseURL, urlEncode(query))
			req, err := http.NewRequestWithContext(ctxOrBackground(env), "GET", url, nil)
			if err != nil {
				return err
			}
			if tenant != "" {
				req.Header.Set("X-Scope-OrgID", tenant)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("cafe metrics: %s: %s", resp.Status, strings.TrimSpace(string(body)))
			}
			// Pretty-print the raw JSON result for readability.
			var pretty map[string]any
			if err := json.Unmarshal(body, &pretty); err != nil {
				_, _ = fmt.Fprintln(env.Stdout, string(body))
				return nil
			}
			b, _ := json.MarshalIndent(pretty, "", "  ")
			_, _ = fmt.Fprintln(env.Stdout, string(b))
			return nil
		},
	}
}

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/cafaye/cafe/internal/mcp"
)

type metricsArgs struct {
	Query  string `json:"query,omitempty"`
	Tenant string `json:"tenant,omitempty"`
}

func (t *mcpTools) metrics() mcp.Tool[metricsArgs, string] {
	return mcp.Tool[metricsArgs, string]{
		Name:        "caf_metrics",
		Description: "Query the cafaye metrics store (Prometheus/Mimir) with a PromQL expression. Returns the raw result. Does not mutate anything.",
		ReadOnly:    true,
		Idempotent:  true,
		Input:       metricsArgs{},
		Handler:     t.runMetrics,
	}
}

func (t *mcpTools) runMetrics(ctx context.Context, args metricsArgs) (string, error) {
	baseURL := strings.TrimSpace(os.Getenv("CAFAYE_PROMETHEUS_URL"))
	if baseURL == "" {
		baseURL = "http://localhost:15902"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	query := strings.TrimSpace(args.Query)
	if query == "" {
		query = "up"
	}
	tenant := strings.TrimSpace(args.Tenant)
	if tenant == "" {
		tenant = strings.TrimSpace(os.Getenv("CAFAYE_PROMETHEUS_TENANT"))
	}
	if tenant == "" {
		tenant = "single-tenant"
	}
	url := fmt.Sprintf("%s/api/v1/query?query=%s", baseURL, urlEncode(query))
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	if tenant != "" {
		req.Header.Set("X-Scope-OrgID", tenant)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("cafe caf_metrics: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	// Validate JSON and return pretty.
	var pretty map[string]any
	if err := json.Unmarshal(body, &pretty); err != nil {
		return string(body), nil
	}
	b, _ := json.MarshalIndent(pretty, "", "  ")
	return string(b), nil
}

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/anywaye/cafe/internal/mcp"
)

type lokiArgs struct {
	Query   string `json:"query,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
	Since   string `json:"since,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type lokiLine struct {
	Timestamp string `json:"timestamp"`
	Line      string `json:"line"`
}

func (t *mcpTools) loki() mcp.Tool[lokiArgs, []lokiLine] {
	return mcp.Tool[lokiArgs, []lokiLine]{
		Name:        "caf_loki",
		Description: "Query the anywaye log store (Loki) for lines, optionally filtered by trace id. Does not mutate anything.",
		ReadOnly:    true,
		Idempotent:  true,
		Input:       lokiArgs{},
		Handler:     t.runLoki,
	}
}

func (t *mcpTools) runLoki(ctx context.Context, args lokiArgs) ([]lokiLine, error) {
	baseURL := strings.TrimSpace(os.Getenv("ANYWAYE_LOKI_URL"))
	if baseURL == "" {
		baseURL = "http://localhost:15901"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	query := strings.TrimSpace(args.Query)
	traceID := strings.TrimSpace(args.TraceID)
	if query == "" {
		if traceID != "" {
			query = `{job=~".+"} |= "` + traceID + `"`
		} else {
			query = `{job=~".+"}`
		}
	}
	since := args.Since
	if since == "" {
		since = "15m"
	}
	dur, err := time.ParseDuration(since)
	if err != nil {
		return nil, fmt.Errorf("cafe caf_loki: bad since: %w", err)
	}
	end := time.Now()
	start := end.Add(-dur)
	limit := args.Limit
	if limit <= 0 {
		limit = 100
	}
	url := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=%d",
		baseURL, urlEncode(query), start.UnixNano(), end.UnixNano(), limit)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cafe caf_loki: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var res struct {
		Data struct {
			Result []struct {
				Values [][]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("cafe caf_loki: decode: %w", err)
	}
	var out []lokiLine
	for _, r := range res.Data.Result {
		for _, v := range r.Values {
			if len(v) < 2 {
				continue
			}
			ts := v[0]
			if n, err := parseNano(ts); err == nil {
				ts = n.UTC().Format(time.RFC3339Nano)
			}
			out = append(out, lokiLine{Timestamp: ts, Line: v[1]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	return out, nil
}

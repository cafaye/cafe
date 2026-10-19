package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

func newLogsCommand() *Command {
	var (
		baseURL string
		query   string
		traceID string
		since   string
		limit   int
	)
	return &Command{
		Name:    "logs",
		Summary: "Query the cafaye log store (Loki) for lines, optionally by trace id",
		Usage:   "cafe logs [--url URL] [--query LOGQL] [--trace-id ID] [--since DURATION] [--limit N]",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&baseURL, "url", os.Getenv("CAFAYE_LOKI_URL"), "Loki base URL")
			fs.StringVar(&query, "query", "", "LogQL query (default: all streams)")
			fs.StringVar(&traceID, "trace-id", "", "filter lines containing this trace id")
			fs.StringVar(&since, "since", "15m", "lookback duration")
			fs.IntVar(&limit, "limit", 100, "max lines")
		},
		Run: func(args []string, env *Env) error {
			if strings.TrimSpace(baseURL) == "" {
				baseURL = "http://localhost:15901"
			}
			baseURL = strings.TrimRight(baseURL, "/")
			if strings.TrimSpace(query) == "" {
				if strings.TrimSpace(traceID) != "" {
					query = `{job=~".+"} |= "` + traceID + `"`
				} else {
					query = `{job=~".+"}`
				}
			}
			dur, err := time.ParseDuration(since)
			if err != nil {
				return fmt.Errorf("%w: cafe logs: bad --since: %w", errUsage, err)
			}
			end := time.Now()
			start := end.Add(-dur)
			url := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=%d",
				baseURL, urlEncode(query), start.UnixNano(), end.UnixNano(), limit)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("cafe logs: %s: %s", resp.Status, strings.TrimSpace(string(body)))
			}
			var res struct {
				Data struct {
					Result []struct {
						Stream map[string]string `json:"stream"`
						Values [][]string        `json:"values"`
					} `json:"result"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &res); err != nil {
				return fmt.Errorf("cafe logs: decode: %w", err)
			}
			type line struct {
				ts   string
				text string
			}
			var lines []line
			for _, r := range res.Data.Result {
				for _, v := range r.Values {
					if len(v) < 2 {
						continue
					}
					ts := v[0]
					// convert nanos to RFC3339
					if n, err := parseNano(ts); err == nil {
						ts = n.UTC().Format(time.RFC3339Nano)
					}
					lines = append(lines, line{ts: ts, text: v[1]})
				}
			}
			sort.Slice(lines, func(i, j int) bool { return lines[i].ts < lines[j].ts })
			if len(lines) == 0 {
				fmt.Fprintln(env.Stdout, "no log lines")
				return nil
			}
			for _, l := range lines {
				fmt.Fprintf(env.Stdout, "%s  %s\n", l.ts, l.text)
			}
			return nil
		},
	}
}

func parseNano(s string) (time.Time, error) {
	// Loki returns nanosecond string
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, n), nil
}

func urlEncode(s string) string {
	// minimal encoding for LogQL
	r := strings.NewReplacer(" ", "%20", "{", "%7B", "}", "%7D", "|", "%7C", "\"", "%22", "=", "%3D", "&", "%26")
	return r.Replace(s)
}

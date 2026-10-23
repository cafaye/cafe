package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func newErrorsCommand() *Command {
	var (
		baseURL string
		token   string
		org     string
		limit   int
	)
	return &Command{
		Name:    "errors",
		Summary: "List unresolved errors from the anywaye error store (GlitchTip)",
		Usage:   "cafe errors [--url URL] [--token TOKEN] [--org ORG] [--limit N]",
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&baseURL, "url", os.Getenv("ANYWAYE_GLITCHTIP_URL"), "GlitchTip base URL")
			fs.StringVar(&token, "token", os.Getenv("ANYWAYE_GLITCHTIP_TOKEN"), "GlitchTip API token")
			fs.StringVar(&org, "org", os.Getenv("ANYWAYE_GLITCHTIP_ORG"), "organization slug (default: first)")
			fs.IntVar(&limit, "limit", 25, "max issues to list")
		},
		Run: func(args []string, env *Env) error {
			if len(args) > 0 && args[0] == "release" {
				if len(args) < 2 {
					return fmt.Errorf("%w: cafe errors release wants a version", errUsage)
				}
				version := args[1]
				org, baseURL2, token2, err := errorStoreConfig()
				if err != nil {
					return err
				}
				if strings.TrimSpace(baseURL) != "" {
					baseURL2 = strings.TrimRight(baseURL, "/")
				}
				if strings.TrimSpace(token) != "" {
					token2 = token
				}
				body := map[string]any{"version": version, "ref": version, "projects": []string{"anywaye"}}
				b, _ := json.Marshal(body)
				req, err := http.NewRequestWithContext(context.Background(), "POST", baseURL2+"/api/0/organizations/"+org+"/releases/", bytes.NewReader(b))
				if err != nil {
					return err
				}
				req.Header.Set("Authorization", "Bearer "+token2)
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return err
				}
				defer func() { _ = resp.Body.Close() }()
				rb, _ := io.ReadAll(resp.Body)
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					return fmt.Errorf("cafe errors: %s: %s", resp.Status, strings.TrimSpace(string(rb)))
				}
				_, _ = fmt.Fprintf(env.Stdout, "registered release %s\n", version)
				return nil
			}
			if len(args) > 0 && (args[0] == "show" || args[0] == "status") {
				sub := args[0]
				if len(args) < 2 {
					return fmt.Errorf("%w: cafe errors %s wants an issue id", errUsage, sub)
				}
				id := args[1]
				org, baseURL2, token2, err := errorStoreConfig()
				_ = org
				if err != nil {
					return err
				}
				if strings.TrimSpace(baseURL) != "" {
					baseURL2 = strings.TrimRight(baseURL, "/")
				}
				if strings.TrimSpace(token) != "" {
					token2 = token
				}
				var raw struct {
					ID        string `json:"id"`
					Title     string `json:"title"`
					Level     string `json:"level"`
					Status    string `json:"status"`
					Count     string `json:"count"`
					FirstSeen string `json:"firstSeen"`
					LastSeen  string `json:"lastSeen"`
					Permalink string `json:"permalink"`
				}
				if err := errorStoreGet(ctxOrBackground(env), baseURL2, token2, "/api/0/issues/"+id+"/", &raw); err != nil {
					return err
				}
				if sub == "status" {
					_, _ = fmt.Fprintf(env.Stdout, "%s  %s\n", raw.ID, raw.Status)
					return nil
				}
				_, _ = fmt.Fprintf(env.Stdout, "id: %s\ntitle: %s\nlevel: %s\nstatus: %s\ncount: %s\nfirstSeen: %s\nlastSeen: %s\npermalink: %s\n", raw.ID, raw.Title, raw.Level, raw.Status, raw.Count, raw.FirstSeen, raw.LastSeen, raw.Permalink)
				return nil
			}
			if strings.TrimSpace(baseURL) == "" {
				baseURL = "http://169.58.108.209:8000"
			}
			if strings.TrimSpace(token) == "" {
				return fmt.Errorf("%w: cafe errors: missing token (set ANYWAYE_GLITCHTIP_TOKEN or --token)", errUsage)
			}
			baseURL = strings.TrimRight(baseURL, "/")

			if strings.TrimSpace(org) == "" {
				orgs, err := fetchJSON[[]struct {
					Slug string `json:"slug"`
				}](baseURL, token, "/api/0/organizations/")
				if err != nil {
					return err
				}
				if len(orgs) == 0 {
					return fmt.Errorf("cafe errors: no organizations found")
				}
				org = orgs[0].Slug
			}

			issues, err := fetchJSON[[]struct {
				ID       string `json:"id"`
				Title    string `json:"title"`
				Level    string `json:"level"`
				Status   string `json:"status"`
				Count    string `json:"count"`
				LastSeen string `json:"lastSeen"`
			}](baseURL, token, "/api/0/organizations/"+org+"/issues/?limit="+fmt.Sprint(limit))
			if err != nil {
				return err
			}

			if len(issues) == 0 {
				_, _ = fmt.Fprintln(env.Stdout, "no issues")
				return nil
			}
			for _, i := range issues {
				_, _ = fmt.Fprintf(env.Stdout, "%s  %-7s  %-10s  %s  (count=%s, lastSeen=%s)\n", i.ID, i.Level, i.Status, i.Title, i.Count, i.LastSeen)
			}
			return nil
		},
	}
}

func fetchJSON[T any](baseURL, token, path string) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(context.Background(), "GET", baseURL+path, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return zero, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, fmt.Errorf("cafe errors: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, fmt.Errorf("cafe errors: decode: %w", err)
	}
	return out, nil
}

// registerGlitchTipRelease registers a release with the anywaye error store so
// GlitchTip can auto-resolve issues fixed in it. It never fails the caller: a
// missing token or a store error is logged to stderr and swallowed, because a
// deploy that succeeded must not be reported as failed over observability.
func registerGlitchTipRelease(version string) {
	token := strings.TrimSpace(os.Getenv("ANYWAYE_GLITCHTIP_TOKEN"))
	if token == "" {
		return
	}
	baseURL := strings.TrimSpace(os.Getenv("ANYWAYE_GLITCHTIP_URL"))
	if baseURL == "" {
		baseURL = "http://169.58.108.209:8000"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	org := strings.TrimSpace(os.Getenv("ANYWAYE_GLITCHTIP_ORG"))
	if org == "" {
		org = "anywaye-jf"
	}
	if version == "" {
		version = "latest"
	}
	body := map[string]any{"version": version, "ref": version, "projects": []string{"anywaye"}}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(context.Background(), "POST", baseURL+"/api/0/organizations/"+org+"/releases/", bytes.NewReader(b))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The contract above says a store error is logged and swallowed, so
		// it is logged here. A release that was not registered is worth
		// seeing, and it is not worth failing a deploy that worked over.
		_, _ = fmt.Fprintf(os.Stderr, "cafe: release %s was not registered: %s\n", version, resp.Status)
	}
}

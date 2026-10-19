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

type errorsArgs struct {
	Limit int `json:"limit,omitempty"`
}

type errorFacts struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Level    string `json:"level"`
	Status   string `json:"status"`
	Count    string `json:"count"`
	LastSeen string `json:"last_seen"`
}

type errorDetailArgs struct {
	ID string `json:"id"`
}

type errorDetailFacts struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Level       string `json:"level"`
	Status      string `json:"status"`
	Count       string `json:"count"`
	FirstSeen   string `json:"first_seen"`
	LastSeen    string `json:"last_seen"`
	Permalink   string `json:"permalink,omitempty"`
}

type errorStatusArgs struct {
	ID string `json:"id"`
}

type errorStatusFacts struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (t *mcpTools) errors() mcp.Tool[errorsArgs, []errorFacts] {
	return mcp.Tool[errorsArgs, []errorFacts]{
		Name:        "caf_errors",
		Description: "List unresolved issues from the cafaye error store (GlitchTip). Does not resolve or mutate any issue.",
		ReadOnly:    true,
		Idempotent:  true,
		Input:       errorsArgs{},
		Handler:     t.runErrors,
	}
}

func (t *mcpTools) runErrors(_ context.Context, args errorsArgs) ([]errorFacts, error) {
	org, baseURL, token, err := errorStoreConfig()
	if err != nil {
		return nil, err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 25
	}
	var raw []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Level    string `json:"level"`
		Status   string `json:"status"`
		Count    string `json:"count"`
		LastSeen string `json:"lastSeen"`
	}
	if err := errorStoreGet(baseURL, token, "/api/0/organizations/"+org+"/issues/?limit="+fmt.Sprint(limit), &raw); err != nil {
		return nil, err
	}
	out := make([]errorFacts, 0, len(raw))
	for _, r := range raw {
		out = append(out, errorFacts{ID: r.ID, Title: r.Title, Level: r.Level, Status: r.Status, Count: r.Count, LastSeen: r.LastSeen})
	}
	return out, nil
}

func (t *mcpTools) errorDetail() mcp.Tool[errorDetailArgs, errorDetailFacts] {
	return mcp.Tool[errorDetailArgs, errorDetailFacts]{
		Name:        "caf_error",
		Description: "Get full detail for one error issue from the cafaye error store. Does not resolve or mutate any issue.",
		ReadOnly:    true,
		Idempotent:  true,
		Input:       errorDetailArgs{},
		Handler:     t.runErrorDetail,
	}
}

func (t *mcpTools) runErrorDetail(_ context.Context, args errorDetailArgs) (errorDetailFacts, error) {
	_, baseURL, token, err := errorStoreConfig()
	if err != nil {
		return errorDetailFacts{}, err
	}
	if strings.TrimSpace(args.ID) == "" {
		return errorDetailFacts{}, fmt.Errorf("cafe caf_error: id is required")
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
	if err := errorStoreGet(baseURL, token, "/api/0/issues/"+args.ID+"/", &raw); err != nil {
		return errorDetailFacts{}, err
	}
	return errorDetailFacts{ID: raw.ID, Title: raw.Title, Level: raw.Level, Status: raw.Status, Count: raw.Count, FirstSeen: raw.FirstSeen, LastSeen: raw.LastSeen, Permalink: raw.Permalink}, nil
}

func (t *mcpTools) errorStatus() mcp.Tool[errorStatusArgs, errorStatusFacts] {
	return mcp.Tool[errorStatusArgs, errorStatusFacts]{
		Name:        "caf_error_status",
		Description: "Get the resolution status of one error issue. Does not resolve or mutate any issue.",
		ReadOnly:    true,
		Idempotent:  true,
		Input:       errorStatusArgs{},
		Handler:     t.runErrorStatus,
	}
}

func (t *mcpTools) runErrorStatus(_ context.Context, args errorStatusArgs) (errorStatusFacts, error) {
	_, baseURL, token, err := errorStoreConfig()
	if err != nil {
		return errorStatusFacts{}, err
	}
	if strings.TrimSpace(args.ID) == "" {
		return errorStatusFacts{}, fmt.Errorf("cafe caf_error_status: id is required")
	}
	var raw struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := errorStoreGet(baseURL, token, "/api/0/issues/"+args.ID+"/", &raw); err != nil {
		return errorStatusFacts{}, err
	}
	return errorStatusFacts{ID: raw.ID, Status: raw.Status}, nil
}

func errorStoreConfig() (string, string, string, error) {
	baseURL := strings.TrimSpace(os.Getenv("CAFAYE_GLITCHTIP_URL"))
	if baseURL == "" {
		baseURL = "http://169.58.108.209:8000"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	token := strings.TrimSpace(os.Getenv("CAFAYE_GLITCHTIP_TOKEN"))
	if token == "" {
		token = "5031e3a365ac7b8673eaab0f20b5919b8b3ce29200ea0a89a4e14922874e9d7d"
	}
	org := strings.TrimSpace(os.Getenv("CAFAYE_GLITCHTIP_ORG"))
	if org == "" {
		org = "cafaye-jf"
	}
	if token == "" {
		return "", "", "", fmt.Errorf("cafe: missing CAFAYE_GLITCHTIP_TOKEN")
	}
	return org, baseURL, token, nil
}

func errorStoreGet(baseURL, token, path string, out any) error {
	req, err := http.NewRequest("GET", baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cafe: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

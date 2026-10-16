package docs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cafaye/cafe/internal/contract"
)

// docsTestManifest is a schema-valid manifest with the visibility plugged in.
// An empty visibility omits the line: the document still parses, the
// inference is what refuses it.
func docsTestManifest(visibility string) string {
	var line string
	if visibility != "" {
		line = "  visibility: " + visibility + "\n"
	}
	return `name: pages
description: The pages.
language: go
core: ^0.2.0
repository:
  url: git@github.com:cafaye/pages.git
  defaultBranch: master
` + line + `owner:
  team: pages
  contact: pages@cafaye.com
`
}

// parseDocsManifest parses the test manifest the way the command reads one:
// through the contract's own gate, so a test manifest that stopped being
// valid fails here rather than in the case.
func parseDocsManifest(t *testing.T, body string) contract.Manifest {
	t.Helper()
	manifest, err := contract.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse the test manifest: %v", err)
	}
	return manifest
}

// The inference: public ships as docs, private as notes, and anything else
// is a refusal rather than a guess — a guess in the public direction is a
// leak, and the anti-leak guard fails closed.
func TestInferKindReadsVisibility(t *testing.T) {
	tests := []struct {
		name       string
		visibility string
		want       string
		wantErr    string
	}{
		{name: "a public repository ships as docs", visibility: "public", want: KindDocs},
		{name: "a private repository ships as notes", visibility: "private", want: KindNotes},
		{name: "an empty visibility is refused, not guessed", visibility: "", wantErr: "repository.visibility"},
		{name: "an unknown visibility is refused, not guessed", visibility: "internal", wantErr: "repository.visibility"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InferKind(parseDocsManifest(t, docsTestManifest(tt.visibility)))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("InferKind = %q, want a refusal naming %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("the refusal does not name %q:\n%s", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("InferKind: %v", err)
			}
			if got != tt.want {
				t.Errorf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}

// The override: an explicit --visibility wins over the file, an empty one
// defers to it, and a third spelling is refused — press serves no third
// kind.
func TestKindForVisibilityPrefersTheFlag(t *testing.T) {
	manifest := parseDocsManifest(t, docsTestManifest("private"))

	if got, err := KindForVisibility("public", manifest); err != nil || got != KindDocs {
		t.Errorf("override public over a private manifest = %q, %v; want docs and no error", got, err)
	}
	if got, err := KindForVisibility("", manifest); err != nil || got != KindNotes {
		t.Errorf("no override over a private manifest = %q, %v; want notes and no error", got, err)
	}
	if _, err := KindForVisibility("internal", manifest); err == nil {
		t.Error("a third visibility was accepted; press serves no third kind")
	} else if !strings.Contains(err.Error(), "internal") {
		t.Errorf("the refusal does not name what was typed:\n%s", err)
	}
}

// writePage writes a page file into the tree the test owns.
func writePage(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const pageBody = `---
title: Hello
---

Hello.
`

// The docs tarball holds exactly the pages under src/content/docs/:
// markdown and frontmatter only, sorted, with build output and anything
// outside the tree left out by never being named.
func TestTheDocsTarballHoldsExactlyThePageFiles(t *testing.T) {
	dir := t.TempDir()
	writePage(t, dir, "cafaye.yml", docsTestManifest("public"))
	writePage(t, dir, "src/content/docs/index.md", pageBody)
	writePage(t, dir, "src/content/docs/guide/a.md", pageBody)
	writePage(t, dir, "src/content/docs/guide/b.mdx", pageBody)
	writePage(t, dir, "src/content/docs/logo.png", "PNG")
	writePage(t, dir, "src/content/docs/data.json", "{}")
	writePage(t, dir, "dist/index.html", "<html>")
	writePage(t, dir, "node_modules/pkg/index.js", "js")
	writePage(t, dir, "README.md", "readme")

	content, plan, err := BuildTar(dir, KindDocs)
	if err != nil {
		t.Fatalf("BuildTar: %v", err)
	}
	names, err := ListTar(content)
	if err != nil {
		t.Fatalf("ListTar: %v", err)
	}
	want := []string{"src/content/docs/guide/a.md", "src/content/docs/guide/b.mdx", "src/content/docs/index.md"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("tarball holds %v, want exactly %v — sorted, markdown only", names, want)
	}
	if strings.Join(plan.Files, ",") != strings.Join(want, ",") {
		t.Errorf("the plan lists %v, want %v: the plan and the bytes cannot disagree", plan.Files, want)
	}
}

// The notes tarball holds the page files at the root plus projects/: one
// level at the root, the whole subtree under projects/, and nothing else —
// in particular no other subdirectory's files and no build output.
func TestTheNotesTarballHoldsRootPagesAndProjects(t *testing.T) {
	dir := t.TempDir()
	writePage(t, dir, "cafaye.yml", docsTestManifest("private"))
	writePage(t, dir, "a.md", pageBody)
	writePage(t, dir, "b.mdx", pageBody)
	writePage(t, dir, "main.go", "package main")
	writePage(t, dir, "drafts/c.md", pageBody)
	writePage(t, dir, "projects/p1.md", pageBody)
	writePage(t, dir, "projects/nested/deep.md", pageBody)
	writePage(t, dir, "projects/notes.txt", "text")
	writePage(t, dir, "node_modules/pkg/index.js", "js")

	content, _, err := BuildTar(dir, KindNotes)
	if err != nil {
		t.Fatalf("BuildTar: %v", err)
	}
	names, err := ListTar(content)
	if err != nil {
		t.Fatalf("ListTar: %v", err)
	}
	want := []string{"a.md", "b.mdx", "projects/nested/deep.md", "projects/p1.md"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("tarball holds %v, want exactly %v", names, want)
	}
}

// A tree with no pages is a refusal, not an empty version: a publish with
// no pages is a version with nothing in it, and the refusal names where
// the command looked.
func TestAnEmptyTreeIsRefused(t *testing.T) {
	dir := t.TempDir()
	writePage(t, dir, "cafaye.yml", docsTestManifest("public"))

	if _, _, err := BuildTar(dir, KindDocs); err == nil {
		t.Error("a tree with no pages packed without complaint")
	} else if !strings.Contains(err.Error(), "src") {
		t.Errorf("the refusal does not name where it looked:\n%s", err)
	}
}

// The tarball is deterministic: two builds of one tree are the same bytes,
// so the sha256 in the plan is a fact about the tree rather than the
// moment — and a diff between two digests is a diff between two trees.
func TestTheTarballIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	writePage(t, dir, "cafaye.yml", docsTestManifest("public"))
	writePage(t, dir, "src/content/docs/b.md", pageBody)
	writePage(t, dir, "src/content/docs/a.md", pageBody)

	first, planFirst, err := BuildTar(dir, KindDocs)
	if err != nil {
		t.Fatalf("BuildTar: %v", err)
	}
	second, planSecond, err := BuildTar(dir, KindDocs)
	if err != nil {
		t.Fatalf("BuildTar: %v", err)
	}
	if string(first) != string(second) {
		t.Error("two builds of one tree produced different bytes")
	}
	if planFirst.SHA256 != planSecond.SHA256 || planFirst.SHA256 == "" {
		t.Errorf("digests = %q and %q, want one stable digest", planFirst.SHA256, planSecond.SHA256)
	}
	if planFirst.Size != int64(len(first)) {
		t.Errorf("the plan says %d bytes about %d bytes", planFirst.Size, len(first))
	}
}

// pressDouble is the seam the publisher tests drive: it records the form it
// was sent and answers with whatever the case planted. Nothing here touches
// a network beyond the loopback the test owns.
func TestHTTPPublisherPostsTheIngestForm(t *testing.T) {
	var gotKind, gotProject, gotVersion, gotFilename, gotToken string
	var gotContent []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ingest" {
			t.Errorf("path = %s, want /v1/ingest", r.URL.Path)
		}
		gotToken = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse the multipart form: %v", err)
		}
		gotKind = r.FormValue("kind")
		gotProject = r.FormValue("project")
		gotVersion = r.FormValue("version")
		file, header, err := r.FormFile("content")
		if err != nil {
			t.Errorf("read the content part: %v", err)
			return
		}
		defer func() { _ = file.Close() }()
		gotFilename = header.Filename
		gotContent, _ = io.ReadAll(file)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"ing_01","kind":"docs","sha256":"abc","size":12,"state":"ready"}`)
	}))
	defer server.Close()

	publisher := HTTPPublisher{BaseURL: server.URL}
	receipt, err := publisher.Publish(context.Background(), "the-token", Ingest{
		Project: "pages", Version: "v1.0.0", Kind: KindDocs, Content: []byte("tarball"),
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"kind", gotKind, "docs"},
		{"project", gotProject, "pages"},
		{"version", gotVersion, "v1.0.0"},
		{"token", gotToken, "the-token"},
		{"filename", gotFilename, "docs.tar.gz"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if string(gotContent) != "tarball" {
		t.Errorf("content = %q, want the tarball bytes verbatim", gotContent)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"id", receipt.ID, "ing_01"},
		{"kind", receipt.Kind, "docs"},
		{"sha256", receipt.SHA256, "abc"},
		{"state", receipt.State, "ready"},
	} {
		if tc.got != tc.want {
			t.Errorf("receipt %s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if receipt.Size != 12 {
		t.Errorf("receipt size = %d, want 12", receipt.Size)
	}
}

// A non-201 answer is a PressError carrying the server's own code and
// detail — never a paraphrase, because an agent reading the failure must be
// able to grep press's log with it. The 409 row is the load-bearing one: a
// refusal under a carried status is still a refusal.
func TestHTTPPublisherNamesRefusalsWithTheServersWords(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		code   string
		detail string
	}{
		{name: "unauthorized", status: 401, body: `{"code":"unauthorized","detail":"the bearer token is not accepted"}`, code: "unauthorized", detail: "the bearer token is not accepted"},
		{name: "conflict", status: 409, body: `{"code":"conflict","detail":"version v1.0.0 is already ingested"}`, code: "conflict", detail: "already ingested"},
		{name: "a proxy's HTML is still a PressError", status: 502, body: `<html>bad gateway</html>`, code: "Bad Gateway", detail: "<html>bad gateway</html>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			_, err := HTTPPublisher{BaseURL: server.URL}.Publish(context.Background(), "token", Ingest{Kind: KindDocs})
			if err == nil {
				t.Fatal("a refusal published without complaint")
			}
			var pressErr *PressError
			if !errors.As(err, &pressErr) {
				t.Fatalf("err = %T (%v), want a *PressError", err, err)
			}
			if pressErr.Status != tt.status {
				t.Errorf("status = %d, want %d", pressErr.Status, tt.status)
			}
			if !strings.Contains(pressErr.Code, tt.code) || !strings.Contains(pressErr.Detail, tt.detail) {
				t.Errorf("the error does not carry the server's code and detail: %v", pressErr)
			}
		})
	}
}

// A client with no endpoint is refused rather than defaulted: it would POST
// a relative path and get a confusing answer — the same rule the pantry
// clients hold.
func TestHTTPPublisherRefusesNoEndpoint(t *testing.T) {
	_, err := HTTPPublisher{}.Publish(context.Background(), "token", Ingest{Kind: KindDocs})
	if err == nil {
		t.Error("a publisher with no endpoint sent without complaint")
	}
}

// A multipart form built by hand is easy to get subtly wrong, so the decode
// above goes through the standard library's own parser: a form the test
// server cannot parse is a form press cannot parse either. This case asserts
// the field set directly — kind, content, project, version, and nothing
// else — so a field added without the test following it is caught here.
func TestTheIngestFormCarriesExactlyFourFields(t *testing.T) {
	var fields []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			t.Errorf("read the multipart body: %v", err)
			return
		}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("read a part: %v", err)
				return
			}
			fields = append(fields, part.FormName())
			_, _ = io.Copy(io.Discard, part)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":"ing_01","kind":"docs","sha256":"abc","size":0,"state":"ready"}`)
	}))
	defer server.Close()

	_, err := HTTPPublisher{BaseURL: server.URL}.Publish(context.Background(), "token", Ingest{
		Project: "pages", Version: "v1.0.0", Kind: KindDocs, Content: []byte("x"),
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	want := []string{"kind", "project", "version", "content"}
	if strings.Join(fields, ",") != strings.Join(multipartFieldOrder(), ",") {
		t.Errorf("form fields = %v, want %v in order", fields, want)
	}
}

// multipartFieldOrder is the field order Publish writes: kind, project,
// version, then the content part. It is named here so the case above reads
// as an assertion about the wire rather than about a slice literal.
func multipartFieldOrder() []string { return []string{"kind", "project", "version", "content"} }

package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// These are the behavior tests for the ship-backed commands.
//
// They drive the real commands through the real router against a server that
// mirrors ship's route table, and they assert the three things that were
// wrong at once: the METHOD, the PATH and the BODY. A command that posts to a
// route ship does not have used to print a 404 and exit 0, so the path is the
// assertion that matters most — and the body is the assertion that the
// decision carried the person who made it.

// recordedRequest is one request the CLI made.
type recordedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   string
	Auth   string
}

// shipServer stands in for ship. It records what it was asked and answers
// with the shape ship answers with, so a command that parses the answer has
// something to parse.
type shipServer struct {
	*httptest.Server

	mu       sync.Mutex
	requests []recordedRequest
	status   int
	response string
}

// defaultShipResponse carries the one field a command needs out of it: the
// project id `cafe start` reads to tell the operator which project it made.
const defaultShipResponse = `{"id":"11111111-1111-4111-8111-111111111111","name":"demo"}`

func newShipServer(t *testing.T) *shipServer {
	t.Helper()
	s := &shipServer{response: defaultShipResponse}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *shipServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, recordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Body:   string(body),
		Auth:   r.Header.Get("Authorization"),
	})
	status, response := s.status, s.response
	s.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(response))
}

// fail makes the server answer with a refusal, the way ship answers a bad
// request.
func (s *shipServer) fail(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
	s.response = body
}

// reset forgets the recorded requests, so one case does not see the previous
// one's.
func (s *shipServer) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
	s.status = 0
	s.response = defaultShipResponse
}

// last returns the one request the CLI made.
func (s *shipServer) last(t *testing.T) recordedRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != 1 {
		t.Fatalf("the CLI made %d requests, want exactly 1: %+v", len(s.requests), s.requests)
	}
	return s.requests[0]
}

// count returns how many requests the CLI made.
func (s *shipServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

const (
	testProjectID = "22222222-2222-4222-8222-222222222222"
	testOwnerID   = "11111111-1111-4111-8111-111111111111"
	testTaskID    = "task-1"
	testLandingID = "landing-1"
)

// shipCommandCase is one command, the route it must reach, and the body it
// must carry.
type shipCommandCase struct {
	name      string
	args      []string
	method    string
	path      string
	query     url.Values
	body      map[string]any
	wantToken bool
}

// shipCommandCases is the table: every ship-backed command, with the route it
// is supposed to call.
//
// The paths are ship's, not the CLI's wishes: `landings` are landing routes
// and `tasks` are task routes, and mixing them up is what made every landing
// approval a 404 that reported success.
var shipCommandCases = []shipCommandCase{
	{
		name:      "task create",
		args:      []string{"task", "create", testProjectID, "--title", "write it", "--kind", "bug"},
		method:    "POST",
		path:      "/v1/ship/projects/" + testProjectID + "/tasks",
		body:      map[string]any{"title": "write it", "description": "", "kind": "bug"},
		wantToken: true,
	},
	{
		name:      "task list",
		args:      []string{"task", "list", testProjectID, "--kind", "bug", "--status", "pending"},
		method:    "GET",
		path:      "/v1/ship/projects/" + testProjectID + "/tasks",
		query:     url.Values{"kind": {"bug"}, "status": {"pending"}},
		wantToken: true,
	},
	{
		name:      "task get",
		args:      []string{"task", "get", testProjectID, testTaskID},
		method:    "GET",
		path:      "/v1/ship/projects/" + testProjectID + "/tasks/" + testTaskID,
		wantToken: true,
	},
	{
		name:      "task status",
		args:      []string{"task", "status", testProjectID, testTaskID, "done"},
		method:    "PATCH",
		path:      "/v1/ship/projects/" + testProjectID + "/tasks/" + testTaskID + "/status",
		body:      map[string]any{"status": "done"},
		wantToken: true,
	},
	{
		name:      "task stage",
		args:      []string{"task", "stage", testProjectID, testTaskID, "review"},
		method:    "POST",
		path:      "/v1/ship/projects/" + testProjectID + "/tasks/" + testTaskID + "/stage",
		body:      map[string]any{"stage": "review"},
		wantToken: true,
	},
	{
		name:      "task results",
		args:      []string{"task", "results", testProjectID, testTaskID, "--type", "success", "--message", "green"},
		method:    "POST",
		path:      "/v1/ship/projects/" + testProjectID + "/tasks/" + testTaskID + "/results",
		body:      map[string]any{"type": "success", "message": "green"},
		wantToken: true,
	},
	{
		name:      "landing create",
		args:      []string{"landing", "create", testProjectID, "--kind", "change", "--title", "add dark mode", "--external-id", "pr-7"},
		method:    "POST",
		path:      "/v1/ship/projects/" + testProjectID + "/landings",
		body:      map[string]any{"title": "add dark mode", "description": "", "kind": "change", "contributor": "user-1", "external_id": "pr-7"},
		wantToken: true,
	},
	{
		name:      "landing list",
		args:      []string{"landing", "list", testProjectID, "--kind", "change"},
		method:    "GET",
		path:      "/v1/ship/projects/" + testProjectID + "/landings",
		query:     url.Values{"kind": {"change"}},
		wantToken: true,
	},
	{
		name:      "landing get",
		args:      []string{"landing", "get", testProjectID, testLandingID},
		method:    "GET",
		path:      "/v1/ship/projects/" + testProjectID + "/landings/" + testLandingID,
		wantToken: true,
	},
	{
		name:      "landing approve",
		args:      []string{"landing", "approve", testProjectID, testLandingID, "--by", "user-1", "--note", "looks good"},
		method:    "POST",
		path:      "/v1/ship/projects/" + testProjectID + "/landings/" + testLandingID + "/approve",
		body:      map[string]any{"decided_by": "user-1", "note": "looks good"},
		wantToken: true,
	},
	{
		name:      "landing reject",
		args:      []string{"landing", "reject", testProjectID, testLandingID, "--by", "user-1"},
		method:    "POST",
		path:      "/v1/ship/projects/" + testProjectID + "/landings/" + testLandingID + "/reject",
		body:      map[string]any{"decided_by": "user-1", "note": ""},
		wantToken: true,
	},
	{
		name:      "landing merge",
		args:      []string{"landing", "merge", testProjectID, testLandingID},
		method:    "POST",
		path:      "/v1/ship/projects/" + testProjectID + "/landings/" + testLandingID + "/merge",
		wantToken: true,
	},
	{
		name:      "landing results",
		args:      []string{"landing", "results", testProjectID, testLandingID, "--type", "success", "--message", "certified"},
		method:    "POST",
		path:      "/v1/ship/projects/" + testProjectID + "/landings/" + testLandingID + "/results",
		body:      map[string]any{"type": "success", "message": "certified", "stage_name": ""},
		wantToken: true,
	},
	{
		name:      "board view",
		args:      []string{"board", "view", testProjectID},
		method:    "POST",
		path:      "/v1/ship/projects/" + testProjectID + "/board/view",
		body:      map[string]any{"user_id": "user-1"},
		wantToken: true,
	},
	{
		name:      "board attention",
		args:      []string{"board", "attention", testProjectID},
		method:    "GET",
		path:      "/v1/ship/projects/" + testProjectID + "/board/attention",
		query:     url.Values{"user_id": {"user-1"}},
		wantToken: true,
	},
	{
		name:      "pipeline get",
		args:      []string{"pipeline", "get", testProjectID},
		method:    "GET",
		path:      "/v1/ship/projects/" + testProjectID + "/pipeline",
		wantToken: true,
	},
	{
		name:      "pipeline update",
		args:      []string{"pipeline", "update", testProjectID, "--recipe", "software"},
		method:    "PUT",
		path:      "/v1/ship/projects/" + testProjectID + "/pipeline",
		body:      map[string]any{"recipe": "software"},
		wantToken: true,
	},
	{
		name:      "pipeline advance",
		args:      []string{"pipeline", "advance", testProjectID},
		method:    "POST",
		path:      "/v1/ship/projects/" + testProjectID + "/pipeline/advance",
		wantToken: true,
	},
	{
		name:      "pipeline recipes",
		args:      []string{"pipeline", "recipes"},
		method:    "GET",
		path:      "/v1/ship/pipeline-recipes",
		wantToken: true,
	},
	{
		name:      "project create",
		args:      []string{"project", "create", "--name", "demo", "--owner", testOwnerID},
		method:    "POST",
		path:      "/v1/ship/projects/",
		body:      map[string]any{"name": "demo", "owner_id": testOwnerID},
		wantToken: true,
	},
	{
		name:      "project list",
		args:      []string{"project", "list", "--owner", testOwnerID},
		method:    "GET",
		path:      "/v1/ship/projects/",
		query:     url.Values{"owner_id": {testOwnerID}},
		wantToken: true,
	},
	{
		name:      "project get",
		args:      []string{"project", "get", testProjectID},
		method:    "GET",
		path:      "/v1/ship/projects/" + testProjectID,
		wantToken: true,
	},
	{
		name:      "start",
		args:      []string{"start", "--name", "demo", "--owner", testOwnerID},
		method:    "POST",
		path:      "/v1/ship/projects/",
		body:      map[string]any{"name": "demo", "owner_id": testOwnerID},
		wantToken: true,
	},
	{
		name:      "gate decide",
		args:      []string{"gate", "decide", testProjectID, "review", "--status", "passed", "--by", "user-1"},
		method:    "POST",
		path:      "/v1/ship/projects/" + testProjectID + "/gates/review/decide",
		body:      map[string]any{"status": "passed", "decided_by": "user-1", "note": ""},
		wantToken: true,
	},
}

// shipTestEnv points the commands at the test server and gives them an
// identity, so the cases do not depend on the machine's environment.
//
// The telemetry SDK is switched off because these cases are about the HTTP
// contract, and every invocation would otherwise spend a fifth of a second
// trying to reach a collector that is not there.
func shipTestEnv(t *testing.T, srv *shipServer) {
	t.Helper()
	t.Setenv("SHIP_URL", srv.URL)
	t.Setenv("SHIP_TOKEN", "test-token")
	t.Setenv("SHIP_USER_ID", "user-1")
	t.Setenv("SHIP_OWNER_ID", testOwnerID)
	t.Setenv("OTEL_SDK_DISABLED", "true")
}

// Every ship-backed command calls the route it is supposed to call, with the
// body it is supposed to carry.
func TestShipCommandsCallShipRoutes(t *testing.T) {
	srv := newShipServer(t)
	shipTestEnv(t, srv)

	for _, tt := range shipCommandCases {
		t.Run(tt.name, func(t *testing.T) {
			srv.reset()

			code, stdout, stderr := runCLI(t, testVersion, tt.args...)

			if code != exitSuccess {
				t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
			}
			if stdout == "" {
				t.Error("the command printed nothing on stdout")
			}
			req := srv.last(t)
			if req.Method != tt.method {
				t.Errorf("method = %s, want %s", req.Method, tt.method)
			}
			if req.Path != tt.path {
				t.Errorf("path = %s, want %s", req.Path, tt.path)
			}
			if tt.query != nil {
				if !reflect.DeepEqual(req.Query, tt.query) {
					t.Errorf("query = %v, want %v", req.Query, tt.query)
				}
			}
			if tt.body != nil {
				var got map[string]any
				if err := json.Unmarshal([]byte(req.Body), &got); err != nil {
					t.Fatalf("body %q is not JSON: %v", req.Body, err)
				}
				if !reflect.DeepEqual(got, tt.body) {
					t.Errorf("body = %v, want %v", got, tt.body)
				}
			}
			// The token goes on EVERY ship-backed command: ship requires it
			// on every /v1 route, and it used to be set in one file only.
			if tt.wantToken && req.Auth != "Bearer test-token" {
				t.Errorf("Authorization = %q, want %q", req.Auth, "Bearer test-token")
			}
		})
	}
}

// A refusal from ship is a failure of the command, not a result to print. The
// old code printed the error body and exited 0, so a script could not tell a
// created task from a refused one.
func TestShipCommandsFailOnARefusal(t *testing.T) {
	srv := newShipServer(t)
	shipTestEnv(t, srv)

	for _, tt := range shipCommandCases {
		t.Run(tt.name, func(t *testing.T) {
			srv.reset()
			srv.fail(http.StatusBadRequest, "the server said no")

			code, _, stderr := runCLI(t, testVersion, tt.args...)

			if code != exitFailure {
				t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitFailure, stderr)
			}
			if !strings.Contains(stderr, "400") {
				t.Errorf("stderr does not carry the status\ngot:\n%s", stderr)
			}
			if !strings.Contains(stderr, "the server said no") {
				t.Errorf("stderr does not carry the server's reason\ngot:\n%s", stderr)
			}
		})
	}
}

// A malformed SHIP_URL is a sentence, not a panic. The old code discarded the
// error `http.NewRequest` returned and wrote to the nil request's header.
func TestMalformedShipURLIsReportedNotPanicked(t *testing.T) {
	t.Setenv("SHIP_URL", "://bad")

	code, _, stderr := runCLI(t, testVersion, "project", "list", "--owner", testOwnerID)

	if code == exitSuccess {
		t.Fatal("exit code = 0, want a failure")
	}
	if !strings.Contains(stderr, "ship URL") {
		t.Errorf("stderr does not name the bad URL\ngot:\n%s", stderr)
	}
}

// `cafe start` declares its flags, so they parse. They used to be created in
// a private flag set inside Run, and the router parses with the command's own
// — so `cafe start --name x` died with "flag provided but not defined".
func TestStartAcceptsItsOwnFlags(t *testing.T) {
	srv := newShipServer(t)
	shipTestEnv(t, srv)

	code, stdout, stderr := runCLI(t, testVersion, "start", "--name", "demo", "--owner", testOwnerID)

	if code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if !strings.Contains(stdout, "Project created:") {
		t.Errorf("stdout does not report the created project\ngot:\n%s", stdout)
	}
}

// The owner is a uuid on ship's side, so a name is refused before the call
// rather than as a 400 the CLI would have printed as a result.
func TestStartRefusesAnOwnerThatIsNotAUUID(t *testing.T) {
	srv := newShipServer(t)
	shipTestEnv(t, srv)

	code, _, stderr := runCLI(t, testVersion, "start", "--name", "demo", "--owner", "not-a-uuid")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitUsage, stderr)
	}
	if srv.count() != 0 {
		t.Errorf("the CLI made %d requests, want none", srv.count())
	}
}

// A project that was never created must not be reported as one that was.
func TestStartFailsWhenShipReturnsNoProject(t *testing.T) {
	srv := newShipServer(t)
	shipTestEnv(t, srv)
	srv.fail(http.StatusOK, `{"name":"demo"}`)

	code, stdout, stderr := runCLI(t, testVersion, "start", "--name", "demo", "--owner", testOwnerID)

	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitFailure, stderr)
	}
	if strings.Contains(stdout, "Project created") {
		t.Errorf("stdout claims the project was created\ngot:\n%s", stdout)
	}
}

// A decision whose flag does not parse must not go out as a decision by
// nobody: ship records an empty decided_by as a REAL approval.
func TestLandingDecisionRefusesAnUnparsableFlag(t *testing.T) {
	srv := newShipServer(t)
	shipTestEnv(t, srv)

	code, _, stderr := runCLI(t, testVersion, "landing", "approve", testProjectID, testLandingID, "--by")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitUsage, stderr)
	}
	if srv.count() != 0 {
		t.Errorf("the CLI made %d requests, want none", srv.count())
	}
}

// The board's attention check asks about the same person the view was
// recorded for. It used to send nobody, so ship answered for "anonymous"
// while the view was recorded under the real user.
func TestBoardAttentionAsksAboutTheSameUser(t *testing.T) {
	srv := newShipServer(t)
	shipTestEnv(t, srv)

	if code, _, stderr := runCLI(t, testVersion, "board", "view", testProjectID); code != exitSuccess {
		t.Fatalf("board view: exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}
	if code, _, stderr := runCLI(t, testVersion, "board", "attention", testProjectID); code != exitSuccess {
		t.Fatalf("board attention: exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.requests) != 2 {
		t.Fatalf("the CLI made %d requests, want 2", len(srv.requests))
	}
	view, attention := srv.requests[0], srv.requests[1]
	if view.Body == "" || !strings.Contains(view.Body, "user-1") {
		t.Errorf("the view body %q does not name the user", view.Body)
	}
	if attention.Query.Get("user_id") != "user-1" {
		t.Errorf("attention asked about %q, want %q", attention.Query.Get("user_id"), "user-1")
	}
}

// A query value is one value. The old code concatenated it into the URL, so
// a kind holding an "&" became two parameters.
func TestQueryValuesAreEncoded(t *testing.T) {
	srv := newShipServer(t)
	shipTestEnv(t, srv)

	if code, _, stderr := runCLI(t, testVersion, "task", "list", testProjectID, "--kind", "a&b=c"); code != exitSuccess {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitSuccess, stderr)
	}

	req := srv.last(t)
	if got := req.Query.Get("kind"); got != "a&b=c" {
		t.Errorf("kind = %q, want %q", got, "a&b=c")
	}
	if len(req.Query) != 1 {
		t.Errorf("query = %v, want one parameter", req.Query)
	}
}

// The owner is required to list projects: ship refuses an empty owner_id, and
// the old code sent it anyway and printed the refusal as if it were the list.
func TestProjectListRefusesToSendAnEmptyOwner(t *testing.T) {
	srv := newShipServer(t)
	shipTestEnv(t, srv)
	t.Setenv("SHIP_OWNER_ID", "")

	code, _, stderr := runCLI(t, testVersion, "project", "list")

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitUsage, stderr)
	}
	if srv.count() != 0 {
		t.Errorf("the CLI made %d requests, want none", srv.count())
	}
}

// The task verbs that ship does not have are gone rather than left behind
// answering 404.
func TestTaskHasNoDecisionVerbs(t *testing.T) {
	for _, verb := range []string{"approve", "reject", "merge"} {
		t.Run(verb, func(t *testing.T) {
			code, _, stderr := runCLI(t, testVersion, "task", verb, testProjectID, testTaskID)

			if code != exitUsage {
				t.Fatalf("exit code = %d, want %d (stderr: %s)", code, exitUsage, stderr)
			}
			if !strings.Contains(stderr, "unknown subcommand") {
				t.Errorf("stderr does not say the verb is unknown\ngot:\n%s", stderr)
			}
		})
	}
}

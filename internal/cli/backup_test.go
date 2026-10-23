package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/anywaye/cafe/internal/backup"
)

// `cafe backup` at the command layer: flags, the prompt, the exit codes, and the
// fact that a refusal is a report on stdout rather than an error on stderr.
//
// The behavior of the cycle itself is asserted in internal/backup through the
// Runner seam. What is asserted here is everything the seam cannot see.

// backupRunner is a backup.Runner. It is a separate type from deploy_test.go's
// recordingRunner rather than a reuse of it, and the reason is that the two suites
// key their failures on different things: deploy keys on the whole command as the
// operator would type it, and backup has to key on the STEP, because four
// different commands arrive under the one subcommand `accessory exec`.
type backupRunner struct {
	mu    sync.Mutex
	ran   [][]string
	dirs  []string
	out   string
	fails map[string]error
	// plan, when set, is what Plan returns instead of the real Kamal plan. The
	// engine-selection cases need the CNPG plan, and a fake plan here would
	// make them tests of the fake.
	plan func(backup.Cover, backup.Config, backup.Options) (backup.Plan, error)
}

// Plan is the real Kamal plan unless a case planted another one: the command
// layer is not the place to prove what the plan is, and a fake plan here would
// make every case below a test of the fake.
func (r *backupRunner) Plan(cover backup.Cover, cfg backup.Config, opts backup.Options) (backup.Plan, error) {
	if r.plan != nil {
		return r.plan(cover, cfg, opts)
	}
	return backup.PlanFor(cover, cfg, opts)
}

func (r *backupRunner) Run(_ context.Context, dir string, step backup.Step, stdout, _ io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ran = append(r.ran, step.Args())
	r.dirs = append(r.dirs, dir)
	switch step.Name {
	case backup.Resolve:
		io.WriteString(stdout, r.out)
	case backup.Preflight:
		io.WriteString(stdout, "2.12.0")
	}
	return r.fails[step.Name]
}

// project writes a project whose two files are identity's real ones, so a case
// that plants a broken pair breaks the REAL pair.
func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, source := range map[string]string{
		"config/deploy.yml":       "testdata/identity/deploy.yml",
		"config/kamal-backup.yml": "testdata/identity/kamal-backup.yml",
	} {
		raw, err := os.ReadFile(filepath.Join("..", "backup", source))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".kamal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".kamal", "secrets"), []byte("RESTIC_PASSWORD=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func fixtureResolved(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "backup", "testdata", "identity", "deploy.resolved.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func runBackup(t *testing.T, dir string, deps backupDeps, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{Stdout: &out, Stderr: &errOut}
	c := newBackupCommand(deps)
	// The command runs against the project the test built rather than the test
	// binary's working directory.
	t.Chdir(dir)
	err := c.Execute(env, append([]string{"--yes"}, args...))
	return out.String(), errOut.String(), err
}

// A refusal is a report. It exits 1 and stderr stays empty, because the refusal
// is the paragraph on stdout and a `cafe: ...` line in the place a CI log greps
// would be a truncated copy of it — the copy without the advice in it.
func TestARefusalIsReportedOnStdoutAndStderrStaysEmpty(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{out: strings.Replace(fixtureResolved(t),
		"      - RESTIC_REPOSITORY\n", "", 1)}

	stdout, stderr, err := runBackup(t, dir, backupDeps{runner: runner}, "--table", "users", "identity")
	if !errors.Is(err, errReported) {
		t.Fatalf("err = %v, want errReported (exit 1, nothing on stderr)", err)
	}
	if stderr != "" {
		t.Errorf("a refusal wrote to stderr:\n%s", stderr)
	}
	if !strings.Contains(stdout, "caf-backup/secret-not-declared") {
		t.Errorf("the refusal does not carry its reason:\n%s", stdout)
	}
	if !strings.Contains(stdout, "RESTIC_REPOSITORY") {
		t.Errorf("the refusal does not name the secret:\n%s", stdout)
	}
}

// A failed cycle is NOT a refusal, and the difference is what the exit means to a
// caller: errReported means "the report is above", anything else means cafe failed
// and its one-line cause belongs on stderr.
func TestAFailedCycleIsNotAReport(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{out: fixtureResolved(t), fails: map[string]error{backup.Drill: errors.New("exit status 1")}}

	stdout, _, err := runBackup(t, dir, backupDeps{runner: runner}, "--table", "users", "identity")
	if errors.Is(err, errReported) {
		t.Error("a failed cycle was reported as a refusal, so a script would look for a configuration to edit")
	}
	if err == nil {
		t.Fatal("a failed drill reported success")
	}
	if !strings.Contains(stdout, "the scratch database is dropped") {
		t.Errorf("the failure report does not say what was cleaned up:\n%s", stdout)
	}
}

// A cycle with no --table is a usage error with exit 2, not a refusal with exit 1,
// because it is cafe being invoked wrongly rather than a configuration being
// wrong.
func TestNoTableIsAUsageErrorAndNotARefusal(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{out: fixtureResolved(t)}

	_, _, err := runBackup(t, dir, backupDeps{runner: runner}, "identity")
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage (exit 2)", err)
	}
	if !strings.Contains(err.Error(), "--table") {
		t.Errorf("the error does not name the flag that fixes it: %v", err)
	}
	if len(runner.ran) != 0 {
		t.Errorf("kamal was run %d times before the usage error", len(runner.ran))
	}
}

// --table is repeatable, and each one reaches the plan. A flag that kept only the
// last value would assert on one table and report on another.
func TestEveryTableReachesThePlan(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{out: fixtureResolved(t)}

	stdout, _, err := runBackup(t, dir, backupDeps{runner: runner},
		"--table", "users", "--table", "sessions", "--dry-run", "identity")
	if err != nil {
		t.Fatalf("a dry run failed: %v\n%s", err, stdout)
	}
	// A dry run only EXECUTES the two read-only steps, so this asserts on what it
	// printed: the printed plan is the one a real run executes, and that is the
	// property worth holding. A flag that kept only the last --table would print
	// one table here and assert on another in a real run.
	if len(runner.ran) != 2 {
		t.Errorf("a dry run executed %d commands, want the two read-only ones: %v", len(runner.ran), runner.ran)
	}
	for _, want := range []string{"users", "sessions"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the printed plan does not assert on %q:\n%s", want, stdout)
		}
	}
}

// A dry run never asks. Somebody who typed --dry-run to see what would happen gets
// the plan, not a question — and in a pipeline with no terminal, asking would hang.
func TestADryRunNeverAsks(t *testing.T) {
	dir := project(t)
	asked := false
	runner := &backupRunner{out: fixtureResolved(t)}

	stdout, _, err := runBackup(t, dir, backupDeps{
		runner:  runner,
		confirm: func(string) (bool, error) { asked = true; return true, nil },
	}, "--table", "users", "--dry-run", "identity")
	if err != nil {
		t.Fatalf("a dry run failed: %v\n%s", err, stdout)
	}
	if asked {
		t.Error("a dry run asked for confirmation, so it would hang in a pipeline with nobody to answer")
	}
}

// The --engine flag selects the CNPG engine, and the selection is the whole
// claim: the dry run prints the kubectl cnpg commands and executes nothing, on
// a machine with no cluster and no kubectl anywhere in sight. No runner is
// injected, so the real wiring — RunnerFor — is what the flag drives.
func TestTheEngineFlagSelectsTheCNPGEngine(t *testing.T) {
	dir := project(t)
	c := newBackupCommand(backupDeps{})
	t.Chdir(dir)
	var out bytes.Buffer
	err := c.Execute(&Env{Stdout: &out, Stderr: &out},
		[]string{"--engine", "cnpg", "--table", "users", "--dry-run", "identity"})
	if err != nil {
		t.Fatalf("a CNPG dry run failed: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"kubectl cnpg version",
		"kubectl cnpg backup identity",
		"kubectl apply -f",
		"kubectl delete cluster identity_drill",
		"apiVersion: postgresql.cnpg.io/v1",
		"nothing was executed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the dry run does not print %q:\n%s", want, out.String())
		}
	}
}

// An unknown engine is a usage error with exit 2, not a silent default: a typo
// in --engine is the operator invoking cafe wrongly, and the wrong answer to it
// is a drill run against the wrong substrate.
func TestAnUnknownEngineIsAUsageError(t *testing.T) {
	dir := project(t)
	// No injected runner, so the real RunnerFor is what validates the flag.
	_, _, err := runBackup(t, dir, backupDeps{}, "--engine", "cnpq", "--table", "users", "identity")
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage (exit 2)", err)
	}
}

// Without --yes it asks once, and the prompt says what it is about — the service,
// the scratch database and the tables, because a prompt that does not is a prompt
// people approve without reading and the thing being approved creates a database
// and drops it.
func TestWithoutYesItAsksOnceAndThePromptNamesWhatItIsAbout(t *testing.T) {
	dir := project(t)
	var prompts []string
	runner := &backupRunner{out: fixtureResolved(t)}

	c := newBackupCommand(backupDeps{
		runner: runner,
		confirm: func(prompt string) (bool, error) {
			prompts = append(prompts, prompt)
			return false, nil
		},
	})
	t.Chdir(dir)
	var out bytes.Buffer
	err := c.Execute(&Env{Stdout: &out, Stderr: &out}, []string{"--table", "users", "--scratch", "scratch_one", "identity"})

	if err == nil {
		t.Fatal("declining the prompt reported success")
	}
	if !errors.Is(err, errReported) {
		t.Errorf("err = %v, want errReported: nothing was taken and the operator said so", err)
	}
	if len(prompts) != 1 {
		t.Fatalf("asked %d times, want exactly 1: %v", len(prompts), prompts)
	}
	for _, want := range []string{"identity", "scratch_one", "users"} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("the prompt does not name %q, so it is a prompt somebody approves without reading: %q", want, prompts[0])
		}
	}
	if !strings.Contains(out.String(), "nothing was taken") {
		t.Errorf("declining said nothing about what did not happen:\n%s", out.String())
	}
	if len(runner.ran) != 0 {
		t.Errorf("kamal was run %d times after the operator said no", len(runner.ran))
	}
}

// A closed stdin is an instruction, not a hang, and the instruction names the
// flag. The same is true of `cafe deploy`, which is why the prompt is one function
// in confirm.go rather than two.
func TestAnUnanswerablePromptFailsWithTheFlagToPass(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{out: fixtureResolved(t)}

	c := newBackupCommand(backupDeps{runner: runner})
	t.Chdir(dir)
	// stdin is /dev/null under `go test`, which is the "nobody to ask" case.
	err := c.Execute(&Env{Stdout: io.Discard, Stderr: io.Discard}, []string{"--table", "users", "identity"})

	if err == nil {
		t.Fatal("an unanswerable prompt reported success")
	}
	if !strings.Contains(err.Error(), "--yes") || !strings.Contains(err.Error(), "cafe backup") {
		t.Errorf("the error does not name the command and the flag to pass: %v", err)
	}
	if errors.Is(err, errReported) {
		t.Error("an unanswerable prompt was reported as a verdict; there is no report, so the cause belongs on stderr")
	}
}

// The flags are the flags, and one invocation's values cannot leak into the next.
// The router builds the command once and a test may execute it twice, so this is a
// property of the wiring rather than of the router.
func TestOneInvocationDoesNotLeakFlagsIntoTheNext(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{out: fixtureResolved(t)}
	c := newBackupCommand(backupDeps{runner: runner})
	t.Chdir(dir)

	var first bytes.Buffer
	if err := c.Execute(&Env{Stdout: &first, Stderr: &first}, []string{"--table", "users", "--dry-run", "--scratch", "scratch_one", "identity"}); err != nil {
		t.Fatalf("the first invocation failed: %v", err)
	}
	if !strings.Contains(first.String(), "scratch_one") {
		t.Errorf("the first invocation did not use its own --scratch:\n%s", first.String())
	}

	runner.ran = nil
	var second bytes.Buffer
	if err := c.Execute(&Env{Stdout: &second, Stderr: &second}, []string{"--table", "users", "--dry-run", "identity"}); err != nil {
		t.Fatalf("the second invocation failed: %v", err)
	}
	if strings.Contains(second.String(), "scratch_one") {
		t.Errorf("the second invocation carried the first one's --scratch:\n%s", second.String())
	}
	if !strings.Contains(second.String(), "identity_drill") {
		t.Errorf("the second invocation did not fall back to the default scratch database:\n%s", second.String())
	}
}

// The environment is an overlay and the credentials are Kamal's, so the whole
// flag has to reach the commands: the plan's `kamal config` and every
// `accessory exec` after it.
func TestTheEnvironmentFlagReachesEveryKamalCommandThatTakesIt(t *testing.T) {
	dir := project(t)
	if err := os.WriteFile(filepath.Join(dir, "config", "deploy.staging.yml"),
		[]byte("env:\n  clear:\n    STAGE: staging\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &backupRunner{out: fixtureResolved(t)}

	stdout, _, err := runBackup(t, dir, backupDeps{runner: runner}, "--dry-run", "--env", "staging", "--table", "users", "identity")
	if err != nil {
		t.Fatalf("a dry run failed: %v\n%s", err, stdout)
	}
	for _, argv := range runner.ran {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "accessory") && !strings.Contains(joined, "--destination staging") {
			t.Errorf("a kamal command that takes a destination does not carry it: %s", joined)
		}
	}
	if !strings.Contains(stdout, "environment staging") {
		t.Errorf("the report does not say which environment it resolved:\n%s", stdout)
	}
}

// A table name that cannot survive two shells is refused by name, and the
// refusal is a usage error rather than a silent quote.
func TestATableNameThatCannotBeCarriedIsRefusedBeforeAnythingRuns(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{out: fixtureResolved(t)}

	_, _, err := runBackup(t, dir, backupDeps{runner: runner}, "--table", "user events", "identity")
	if err == nil {
		t.Fatal("cafe accepted a table name with a space, which is passed to the accessory unquoted")
	}
	if len(runner.ran) != 0 {
		t.Errorf("kamal was run %d times before the name was refused", len(runner.ran))
	}
}

// An empty --table value names no table, and the flag package's own error is the
// right place for it to fail: cafe was invoked wrongly.
func TestAnEmptyTableValueIsAUsageError(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{out: fixtureResolved(t)}

	_, _, err := runBackup(t, dir, backupDeps{runner: runner}, "--table", "  ", "identity")
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
}

// --engine cnpg selects the CNPG engine: the dry run prints the kubectl cnpg
// commands and executes nothing, because a CNPG dry run runs no command at all
// — there is no contract to check and no preflight a dry run would catch by
// running. The fake's plan is the real CNPG plan, so what is printed is the
// plan a real run would execute.
func TestACNPGDryRunThroughTheCommandExecutesNothing(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{
		plan: func(cover backup.Cover, _ backup.Config, opts backup.Options) (backup.Plan, error) {
			return backup.PlanForCNPG(cover, opts)
		},
	}

	stdout, _, err := runBackup(t, dir, backupDeps{runner: runner},
		"--engine", "cnpg", "--table", "users", "--dry-run", "identity")
	if err != nil {
		t.Fatalf("a dry run failed: %v\n%s", err, stdout)
	}
	if len(runner.ran) != 0 {
		t.Errorf("a CNPG dry run executed %d commands, want zero: %v", len(runner.ran), runner.ran)
	}
	for _, want := range []string{
		"kubectl cnpg version",
		"kubectl cnpg backup identity",
		"kubectl wait --for=jsonpath={.status.phase}=completed backup/identity-drill",
		"kubectl apply -f",
		"kubectl cnpg psql identity_drill",
		"kubectl delete cluster identity_drill",
		"nothing was executed",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the dry run does not print %q:\n%s", want, stdout)
		}
	}
}

// An unknown --engine is a usage error and not a default: a typo is not a
// reason to run a drill against the wrong substrate, and the wrong answer to
// it is the one outcome a backup command must never produce by accident.
func TestAnUnknownEngineIsAUsageErrorAndRunsNothing(t *testing.T) {
	dir := project(t)
	runner := &backupRunner{out: fixtureResolved(t)}

	_, _, err := runBackup(t, dir, backupDeps{runner: runner}, "--engine", "bogus", "--table", "users", "identity")
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage (exit 2)", err)
	}
	if len(runner.ran) != 0 {
		t.Errorf("%d commands ran before the usage error", len(runner.ran))
	}
}

// The help a person reads before running a command that drops a database has to
// say the three things that are not obvious from the flags: the drill is the
// point, the production-name refusal is not duplicated here, and a dry run
// refuses too.
func TestTheLongHelpSaysTheThreeThingsThatAreNotObvious(t *testing.T) {
	for _, want := range []string{
		"never been drilled is not a backup",
		"kamal-backup's",
		"anywaye/kit's templates/kamal/drill.sh",
		"refuses exactly what the real run refuses",
	} {
		if !strings.Contains(backupLongHelp, want) {
			t.Errorf("the long help does not say %q", want)
		}
	}
	if strings.Contains(backupLongHelp, "`") {
		t.Error("the long help contains a backtick, which cannot appear inside the raw string it is written as")
	}
}

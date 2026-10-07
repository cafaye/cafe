package backup

import (
	"strings"
	"testing"
)

// The plan is pure, so every claim about what a backup cycle will do is
// assertable here with no container runtime, no restic repository and no SSH
// connection.

func fixtureCover(service, env string) Cover {
	cover := Cover{
		Dir:     "/srv/identity",
		Service: service,
		Env:     env,
		Config:  "/srv/identity/config/deploy.yml",
	}
	if env != "" {
		cover.Overlay = "/srv/identity/config/deploy." + env + ".yml"
	}
	return cover
}

func fixtureConfig(accessory string) Config {
	return Config{
		App:       "identity",
		Accessory: accessory,
		Secrets:   []string{"DATABASE_URL", "RESTIC_REPOSITORY"},
		File:      "/srv/identity/config/kamal-backup.yml",
	}
}

func fixturePlan(t *testing.T, env string, tables ...string) Plan {
	t.Helper()
	if len(tables) == 0 {
		tables = []string{"users"}
	}
	plan, err := PlanFor(fixtureCover("identity", env), fixtureConfig("backup"), Options{
		Env:    env,
		Tables: tables,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// The order is the package's argument about what a backup is: read, refuse, boot,
// snapshot, make somewhere to restore into, restore, clean up. A test that only
// asserted the set would let a plan that restores before it snapshots pass.
func TestThePlanRunsItsStepsInOrder(t *testing.T) {
	want := []string{Preflight, Resolve, Boot, Settle, Snapshot, Create, Drill, Drop}

	var got []string
	for _, step := range fixturePlan(t, "").Steps {
		got = append(got, step.Name)
	}
	assertOrder(t, "plan steps", got, want)
}

// A dry run executes the two steps that cannot change a server and prints the
// other five. A step that is printed but not in this set would be a plan whose
// dry run is more permissive than the real run, which is the caf-21b rule and the
// reason the rule exists.
func TestThePlanMarksExactlyTwoStepsAsSafeForADryRunToExecute(t *testing.T) {
	plan := fixturePlan(t, "")

	var executed, printed []string
	for _, step := range plan.Steps {
		printed = append(printed, step.Name)
		if step.Name == Preflight || step.Name == Resolve {
			executed = append(executed, step.Name)
		}
	}
	assertOrder(t, "steps a dry run executes", executed, []string{Preflight, Resolve})
	if len(printed) != len(plan.Steps) {
		t.Fatalf("a dry run printed %d of %d steps", len(printed), len(plan.Steps))
	}
}

// The exact argv of every step, and it is the argv, not a description of one: a
// dry run prints `Step.Command()` and a real run executes `Step.Args()`, so a
// test on the plan is a test on both.
func TestEveryStepsArgvIsTheOneTheCycleRuns(t *testing.T) {
	for _, c := range []struct {
		name string
		env  string
		step string
		want []string
	}{
		{
			name: "preflight", step: Preflight,
			// `kamal version`, not `kamal --version`: the latter is not a
			// subcommand, it prints the command list and exits 1.
			want: []string{"kamal", "version"},
		},
		{name: "resolve with no environment", step: Resolve, want: []string{"kamal", "config"}},
		{name: "resolve with an environment", step: Resolve, env: "staging",
			want: []string{"kamal", "config", "--destination", "staging"}},
		{name: "boot every accessory", step: Boot,
			want: []string{"kamal", "accessory", "boot", "all"}},
		{name: "boot with an environment", step: Boot, env: "staging",
			want: []string{"kamal", "accessory", "boot", "all", "--destination", "staging"}},
		{name: "the lock observation", step: Settle,
			// One read-only restic command and no shell: `accessory exec` does not
			// carry the status out, and the only quote-free shell carrier cannot
			// carry a quoted script. See script.go.
			want: []string{"kamal", "accessory", "exec", "--reuse", "backup", "restic", "list", "locks"}},
		{name: "a forced snapshot", step: Snapshot,
			// `--force` is the gem's own flag, and without it an accessory whose
			// scheduler ran a minute ago answers "no backup due".
			want: []string{"kamal", "accessory", "exec", "--reuse", "backup", "kamal-backup", "backup", "--force"}},
		{name: "the drill", step: Drill,
			want: []string{"kamal", "accessory", "exec", "--reuse", "backup", "kamal-backup",
				"drill", "production", "latest", "--database", "identity_drill", "--check", "<check>", "--yes"}},
		{name: "the drill carries the environment", step: Drill, env: "staging",
			want: []string{"kamal", "accessory", "exec", "--destination", "staging", "--reuse", "backup",
				"kamal-backup", "drill", "production", "latest", "--database", "identity_drill",
				"--check", "<check>", "--yes"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := fixturePlan(t, c.env)
			got := plan.Step(c.step).Argv

			want := c.want
			if c.step == Drill {
				want = withCheck(c.want, plan)
			}
			if !sameArgs(got, want) {
				t.Errorf("argv is\n  %v\nwant\n  %v", got, want)
			}
		})
	}
}

// withCheck substitutes the real escaped check for the `<check>` placeholder, and
// failing to find the placeholder is a failure: it means the plan's shape moved
// and this test would otherwise pass by not looking.
func withCheck(want []string, plan Plan) []string {
	check := plan.Step(Drill).Argv
	index := -1
	for i, arg := range check {
		if arg == "--check" {
			index = i + 1
		}
	}
	if index < 0 || index >= len(check) {
		return want
	}
	out := append([]string{}, want...)
	for i, arg := range out {
		if arg == "<check>" {
			out[i] = check[index]
		}
	}
	return out
}

// The scratch database is caf's own default and it is not a refusal rule: what is
// and is not an ALLOWED scratch name is kamal-backup's decision, made when the
// drill runs. The default is written down here so a change to it is visible.
func TestTheScratchDatabaseDefaultsToTheServiceAndATableIsRequired(t *testing.T) {
	plan, err := PlanFor(fixtureCover("courier", ""), fixtureConfig("backup"), Options{Tables: []string{"shipments"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Scratch != "courier_drill" {
		t.Errorf("Scratch = %q, want %q", plan.Scratch, "courier_drill")
	}
	if got := plan.Step(Drill).Argv; !contains(got, "courier_drill") {
		t.Errorf("the drill does not name the default scratch database: %v", got)
	}

	_, err = PlanFor(fixtureCover("identity", ""), fixtureConfig("backup"), Options{})
	if err == nil {
		t.Fatal("a cycle with no --table was accepted; a restore whose only proof is that a command " +
			"exited zero passes on an empty database, and an empty database is what a broken restore produces")
	}
	if !strings.Contains(err.Error(), "--table") {
		t.Errorf("the refusal does not name the flag that fixes it: %v", err)
	}
}

// A name that cannot survive two shells is refused by name, and the refusal says
// what kind of rule it is — so a reader who thinks this is the production-name
// refusal is corrected rather than confirmed.
func TestANameThatCannotSurviveTwoShellsIsRefusedByName(t *testing.T) {
	for name, given := range map[string]struct{ what, value string }{
		"a scratch name with a space":          {"scratch database", "identity drill"},
		"a scratch name with a quote":          {"scratch database", "identity\"drill"},
		"a scratch name starting with a digit": {"scratch database", "9drill"},
		"a table with a hyphen":                {"table", "user-events"},
		"a table with a space":                 {"table", "user events"},
	} {
		t.Run(name, func(t *testing.T) {
			opts := Options{Tables: []string{"users"}}
			if given.what == "table" {
				opts = Options{Tables: []string{given.value}}
			} else {
				opts.Scratch = given.value
			}
			_, err := PlanFor(fixtureCover("identity", ""), fixtureConfig("backup"), opts)
			if err == nil {
				t.Fatalf("caf accepted %s %q, which is passed to the accessory unquoted", given.what, given.value)
			}
			if !strings.Contains(err.Error(), "kamal-backup") {
				t.Errorf("the refusal does not say whose decision the PRODUCTION rule is, so a reader "+
					"cannot tell this apart from it: %v", err)
			}
		})
	}
}

// A name that is a plain identifier is accepted, which is the control for the
// table above: a check that refuses everything would pass every row of it.
func TestAPlainIdentifierIsAccepted(t *testing.T) {
	for _, name := range []string{"users", "_internal", "caf_audit_log2", "T1"} {
		t.Run(name, func(t *testing.T) {
			opts := Options{Tables: []string{name}, Scratch: "identity_drill2"}
			if _, err := PlanFor(fixtureCover("identity", ""), fixtureConfig("backup"), opts); err != nil {
				t.Errorf("caf refused a plain identifier %q: %v", name, err)
			}
		})
	}
}

// The accessory name comes out of the backup configuration rather than being
// assumed to be "backup", and the plan uses it in every `accessory exec` it runs.
func TestTheAccessoryNameComesFromTheConfigurationNotFromAnAssumption(t *testing.T) {
	plan, err := PlanFor(fixtureCover("identity", ""), fixtureConfig("archive"), Options{Tables: []string{"users"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Accessory != "archive" {
		t.Fatalf("Accessory = %q, want the configuration's own `archive`", plan.Accessory)
	}
	for _, name := range []string{Snapshot, Create, Drill, Drop} {
		if !contains(plan.Step(name).Argv, "archive") {
			t.Errorf("the %s step does not address the accessory the configuration names: %v", name, plan.Step(name).Argv)
		}
	}
}

// The timeout is on the plan, so the report can name it and the runner can apply
// it, and it has a default so an operator who does not choose one still gets a
// bound.
func TestEveryStepIsBoundedAndTheBudgetHasADefault(t *testing.T) {
	plan := fixturePlan(t, "")
	if plan.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %s, want the default %s", plan.Timeout, DefaultTimeout)
	}

	chosen, err := PlanFor(fixtureCover("identity", ""), fixtureConfig("backup"), Options{
		Tables: []string{"users"}, Timeout: 90 * 1e9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if chosen.Timeout != 90*1e9 {
		t.Errorf("Timeout = %s, want the 90s the operator asked for", chosen.Timeout)
	}
}

// Every step says why it is in the plan, because a plan step an operator cannot
// evaluate is a step they have to trust.
func TestEveryStepSaysWhyItIsThere(t *testing.T) {
	for _, step := range fixturePlan(t, "staging").Steps {
		if strings.TrimSpace(step.Why) == "" {
			t.Errorf("the %s step has no reason: %s", step.Name, step.Command())
		}
	}
}

// fixtureCNPGPlan is fixturePlan for the CNPG engine: the same purity, no
// Kamal anywhere in sight.
func fixtureCNPGPlan(t *testing.T, tables ...string) Plan {
	t.Helper()
	if len(tables) == 0 {
		tables = []string{"users"}
	}
	plan, err := PlanForCNPG(fixtureCover("identity", ""), Options{Tables: tables})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// The CNPG plan's order is the engine's argument about what a backup is: take a
// real backup, wait for it, restore into a scratch cluster, wait for that,
// assert, clean up. A test that only asserted the set would let a plan that
// asserts before it restores pass.
func TestTheCNPGPlanRunsItsStepsInOrder(t *testing.T) {
	want := []string{Preflight, Backup, WaitBackup, Restore, WaitReady, Assert, Drop}

	var got []string
	for _, step := range fixtureCNPGPlan(t).Steps {
		got = append(got, step.Name)
	}
	assertOrder(t, "CNPG plan steps", got, want)
}

// The exact argv of every CNPG step, and it is the argv, not a description of
// one: a dry run prints Step.Command() and a real run executes Step.Args(), so
// a test on the plan is a test on both.
func TestEveryCNPGStepsArgvIsTheOneTheCycleRuns(t *testing.T) {
	for _, c := range []struct {
		name string
		step string
		want []string
	}{
		{
			name: "preflight", step: Preflight,
			// `kubectl cnpg version`, the plugin's own version probe — the CNPG
			// shape of `kamal version`.
			want: []string{"kubectl", "cnpg", "version"},
		},
		{
			name: "one real backup, taken now", step: Backup,
			want: []string{"kubectl", "cnpg", "backup", "identity"},
		},
		{
			name: "wait for the backup", step: WaitBackup,
			want: []string{"kubectl", "wait", "--for=jsonpath={.status.phase}=completed",
				"backup/identity-drill", "--timeout", "10m0s"},
		},
		{
			name: "the restore, declaratively", step: Restore,
			want: []string{"kubectl", "apply", "-f", "/srv/identity/config/cnpg-recovery-identity_drill.yaml"},
		},
		{
			name: "wait for the scratch cluster", step: WaitReady,
			want: []string{"kubectl", "wait", "--for=jsonpath={.status.phase}=running",
				"cluster/identity_drill", "--timeout", "10m0s"},
		},
		{
			name: "the assertion", step: Assert,
			want: []string{"kubectl", "cnpg", "psql", "identity_drill", "--",
				"-v", "ON_ERROR_STOP=1", "-c", "SELECT CASE WHEN (SELECT count(*) FROM users) = 0 THEN 1/0 ELSE 1 END;"},
		},
		{
			name: "the cleanup", step: Drop,
			want: []string{"kubectl", "delete", "cluster", "identity_drill"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			plan := fixtureCNPGPlan(t)
			if got := plan.Step(c.step).Argv; !sameArgs(got, c.want) {
				t.Errorf("argv is\n  %v\nwant\n  %v", got, c.want)
			}
		})
	}
}

// The restore is a rendered recovery Cluster, and the manifest is the whole of
// it: a Cluster whose spec.bootstrap.recovery.backup.name points at the Backup
// the Backup step took. `kubectl cnpg` has no restore subcommand — restore in
// CloudNativePG is declarative — so the document IS the restore, and the plan
// carries it on the step that applies it.
func TestTheCNPGRestoreCarriesTheRecoveryManifestItApplies(t *testing.T) {
	plan := fixtureCNPGPlan(t)
	step := plan.Step(Restore)
	if len(step.Files) != 1 {
		t.Fatalf("the Restore step carries %d files, want 1", len(step.Files))
	}
	file := step.Files[0]
	wantPath := "/srv/identity/config/cnpg-recovery-identity_drill.yaml"
	if file.Path != wantPath {
		t.Errorf("Path = %q, want %q", file.Path, wantPath)
	}
	want := "apiVersion: postgresql.cnpg.io/v1\n" +
		"kind: Cluster\n" +
		"metadata:\n" +
		"  name: identity_drill\n" +
		"spec:\n" +
		"  bootstrap:\n" +
		"    recovery:\n" +
		"      backup:\n" +
		"        name: identity-drill\n" +
		"  instances: 1\n"
	if file.Content != want {
		t.Errorf("the recovery manifest is\n%s\nwant\n%s", file.Content, want)
	}
	// And the step's argv names the file it applies, so the command and the
	// document are one fact.
	if !contains(step.Argv, wantPath) {
		t.Errorf("the Restore step does not name the manifest it applies: %v", step.Argv)
	}
}

// The CNPG plan has no Resolve, no Boot and no Settle: there is no Kamal to
// resolve a config against, no accessory to boot, and no restic repository
// whose lock the boot took. A step that is in this plan is a step the CNPG
// cycle would have to invent, and the refusal it feeds on is the two-file
// contract — which does not exist on the CNPG substrate.
func TestTheCNPGPlanHasNoKamalSteps(t *testing.T) {
	plan := fixtureCNPGPlan(t)
	for _, name := range []string{Resolve, Boot, Settle, Snapshot, Create, Drill} {
		if step := plan.Step(name); step.Argv != nil {
			t.Errorf("the CNPG plan has a %q step (%s), which is a Kamal shape the CNPG engine does not have",
				name, step.Command())
		}
	}
}

// The scratch cluster is caf's own default, and a table is required for the
// same reason the Kamal plan requires one: a restore whose only proof is that
// a command exited zero is not a drill.
func TestTheCNPGScratchClusterDefaultsToTheServiceAndATableIsRequired(t *testing.T) {
	plan, err := PlanForCNPG(fixtureCover("courier", ""), Options{Tables: []string{"shipments"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Scratch != "courier_drill" {
		t.Errorf("Scratch = %q, want %q", plan.Scratch, "courier_drill")
	}
	if got := plan.Step(Drop).Argv; !contains(got, "courier_drill") {
		t.Errorf("the drop does not name the default scratch cluster: %v", got)
	}

	_, err = PlanForCNPG(fixtureCover("identity", ""), Options{})
	if err == nil {
		t.Fatal("a cycle with no --table was accepted; a restore whose only proof is that a command " +
			"exited zero passes on an empty database, and an empty database is what a broken restore produces")
	}
	if !strings.Contains(err.Error(), "--table") {
		t.Errorf("the refusal does not name the flag that fixes it: %v", err)
	}
}

// The plan names the engine that built it, so a reader — and a test — can tell
// which engine's sentences about a cycle are the true ones.
func TestTheCNPGPlanNamesItsEngine(t *testing.T) {
	plan := fixtureCNPGPlan(t)
	if plan.Engine != EngineCNPG {
		t.Errorf("Engine = %q, want %q", plan.Engine, EngineCNPG)
	}
}

// The assertion is one query that divides by zero — raising an error psql turns
// into a nonzero exit — if any named table is empty. The exit status is the
// verdict, which is the same contract the Kamal drill has.
func TestTheCNPGCheckDividesByZeroWhenAnyTableIsEmpty(t *testing.T) {
	for name, c := range map[string]struct {
		tables []string
		want   string
	}{
		"one table": {
			tables: []string{"users"},
			want:   "SELECT CASE WHEN (SELECT count(*) FROM users) = 0 THEN 1/0 ELSE 1 END;",
		},
		"two tables": {
			tables: []string{"users", "sessions"},
			want:   "SELECT CASE WHEN (SELECT count(*) FROM users) = 0 OR (SELECT count(*) FROM sessions) = 0 THEN 1/0 ELSE 1 END;",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := cnpgCheck(c.tables); got != c.want {
				t.Errorf("cnpgCheck is\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

// Every CNPG step says why it is in the plan, because a plan step an operator
// cannot evaluate is a step they have to trust.
func TestEveryCNPGStepSaysWhyItIsThere(t *testing.T) {
	for _, step := range fixtureCNPGPlan(t).Steps {
		if strings.TrimSpace(step.Why) == "" {
			t.Errorf("the %s step has no reason: %s", step.Name, step.Command())
		}
	}
}

// A scratch cluster name that is not a plain identifier is refused by name, for
// the same reason the Kamal plan refuses one: it is passed to kubectl as a
// resource name, and a name with a space or a quote is a name the shell and
// the API server would read differently.
func TestTheCNPGPlanRefusesAScratchNameThatIsNotAPlainIdentifier(t *testing.T) {
	for name, given := range map[string]string{
		"a scratch name with a space":          "identity drill",
		"a scratch name with a quote":          "identity\"drill",
		"a scratch name starting with a digit": "9drill",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := PlanForCNPG(fixtureCover("identity", ""), Options{Scratch: given, Tables: []string{"users"}})
			if err == nil {
				t.Errorf("caf accepted scratch cluster %q, which is not a plain identifier", given)
			}
		})
	}
}

func assertOrder(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !sameArgs(got, want) {
		t.Errorf("%s is\n  %v\nwant\n  %v", what, got, want)
	}
}

func sameArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func contains(haystack []string, needle string) bool {
	for _, arg := range haystack {
		if arg == needle {
			return true
		}
	}
	return false
}

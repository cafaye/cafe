package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/cafaye/caf/internal/contract"
	"github.com/cafaye/caf/internal/dev"
	"github.com/cafaye/caf/internal/gen"
)

// `caf gen` is a verb group, for the same reason `caf contract` is: the words a
// person types and the command that runs them are different strings, and a help
// line or a usage error that reads `caf lint` instead of `caf contract lint` has
// made a person go and look for a command that does not exist.
//
// So each verb below is a Command named with its full invocation. They are
// dispatched from the parent's Run and are never registered in `Commands()`,
// which is why `stub_test.go`'s `workingCommands` lists `gen` and not
// `gen telemetry`.

// genVerb pairs the word a person types with the command that runs it.
type genVerb struct {
	name string
	cmd  *Command
}

func genVerbs() []genVerb {
	return []genVerb{
		{name: "chart", cmd: newGenChartCommand()},
		{name: "telemetry", cmd: newGenTelemetryCommand()},
	}
}

func newGenCommand() *Command {
	verbs := genVerbs()
	c := &Command{
		Name:     "gen",
		Summary:  "generate code and config from cafaye contracts",
		Usage:    "caf gen <chart|telemetry> [flags]",
		LongHelp: genVerbTable(verbs) + genFlagNote,
	}
	c.Run = func(args []string, env *Env) error {
		// Bare `caf gen` is a question ("what can this do?"), not a mistake, so
		// it answers instead of exiting 2 — the same call `caf contract` makes.
		if len(args) == 0 {
			printHelp(env.Stdout, c)
			return nil
		}
		verb, found := lookupGenVerb(verbs, args[0])
		if !found {
			return unknownGenVerb(args[0], verbs)
		}
		return verb.cmd.Execute(env, args[1:])
	}
	return c
}

func lookupGenVerb(verbs []genVerb, name string) (genVerb, bool) {
	for _, verb := range verbs {
		if verb.name == name {
			return verb, true
		}
	}
	return genVerb{}, false
}

// unknownGenVerb names the verbs, because "unknown subcommand" alone leaves the
// person guessing what they could have typed.
func unknownGenVerb(name string, verbs []genVerb) error {
	names := make([]string, 0, len(verbs))
	for _, verb := range verbs {
		names = append(names, verb.name)
	}
	return fmt.Errorf("%w: unknown caf gen target %q (usage: caf gen <%s>)",
		errUsage, name, strings.Join(names, "|"))
}

// genVerbTable is the verb list in a command's help. One string rather than a
// rendering hook, so `caf gen`, `caf gen --help` and `caf help gen` cannot drift
// into three different answers.
func genVerbTable(verbs []genVerb) string {
	var table strings.Builder
	table.WriteString("Targets:\n")
	tw := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	for _, verb := range verbs {
		fmt.Fprintf(tw, "  %s\t%s\n", verb.cmd.Name, verb.cmd.Summary)
	}
	tw.Flush()
	return table.String()
}

// genFlagNote is the prose under the target list, and it says two things a person
// would otherwise have to discover by running the command.
const genFlagNote = `
Everything written is inside -out. Each file is printed in full, so what was
generated is readable without opening it, and the bytes are identical between two
runs over one manifest — which is what makes a diff of two of them a diff of two
manifests rather than a diff of two runs.

Without -force an existing file is refused and nothing is written. That is the
whole point of the flag: an overwrite is a change to a file a person may have
edited, and a generator that silently replaces one is a generator nobody runs
twice.
`

// genOptions is the parsed flag state for `caf gen telemetry`.
type genOptions struct {
	out   string
	force bool
}

func newGenTelemetryCommand() *Command {
	opts := &genOptions{}
	c := &Command{
		Name:    "gen telemetry",
		Summary: "write the OpenTelemetry setup a service needs to honour core's telemetry contract",
		Usage:   "caf gen telemetry [flags]",
		LongHelp: "Reads this project's cafaye.yml — the current directory by default — and\n" +
			"writes three files derived from it and from the cafaye core telemetry\n" +
			"contract vendored in this binary:\n" +
			"\n" +
			"  internal/telemetry/telemetry.go        the setup: the per-signal attribute\n" +
			"                                         allowlists, the prohibition on unbounded\n" +
			"                                         identifiers, the resource attributes, the\n" +
			"                                         span-name bound, the endpoint variable and\n" +
			"                                         the no-op path\n" +
			"  internal/telemetry/telemetry_test.go  the suite for it, generated with it\n" +
			"  telemetry/otel-endpoint.json          the endpoint and no-op declaration, in\n" +
			"                                         the shape core's schema defines and\n" +
			"                                         validated against that schema first\n" +
			"\n" +
			"Every value in those files is READ OUT OF core's schemas rather than written\n" +
			"here, so a core release that changes an allowlist changes what caf emits.\n" +
			"That is why the generated test exists: it is the check that keeps holding\n" +
			"after nobody has run caf gen for a year.\n" +
			"\n" +
			"Go only. kit ships the other five languages' OpenTelemetry templates at\n" +
			"templates/otel/, and a second generator of the same file is how two of them\n" +
			"start disagreeing.\n" +
			"\n" +
			genFlagNote,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.out, "out", ".", "directory to write generated output to")
			fs.BoolVar(&opts.force, "force", false, "overwrite generated files that already exist")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		return runGenTelemetry(opts, env)
	}
	return c
}

// runGenChart is `caf gen chart`: the same three steps as telemetry — read the
// project, generate into memory, write what is missing — with the service's
// environment arriving as flags rather than as a guess.
func runGenChart(opts *genChartOptions, env *Env) error {
	project, err := dev.Load(".")
	if err != nil {
		return fmt.Errorf("caf gen chart: %w", err)
	}

	out, err := gen.Generate("chart", project.Manifest, gen.TargetOptions{Env: opts.env})
	if err != nil {
		// A refusal from the generator is already a sentence aimed at the person
		// who typed the command, so it is passed through rather than rewrapped
		// into something vaguer.
		return fmt.Errorf("caf gen chart: %w", err)
	}

	// Refused before anything is written, so the report is about every file at
	// once rather than about whichever happened to be first.
	existing, err := existingFiles(out.Files, opts.out)
	if err != nil {
		return fmt.Errorf("caf gen chart: %w", err)
	}
	if len(existing) > 0 && !opts.force {
		return errRefusedExistingChart(existing)
	}

	for _, file := range out.Files {
		if err := writeGenerated(opts.out, file); err != nil {
			return fmt.Errorf("caf gen chart: %w", err)
		}
	}

	printGenerated(env.Stdout, out)
	return nil
}

// errRefusedExistingChart is errRefusedExisting with the chart's name in it.
func errRefusedExistingChart(existing []string) error {
	return fmt.Errorf("nothing was written; %s already exist. caf gen chart does not overwrite a "+
		"generated file without being told to, and it cannot tell a generated one from one somebody wrote "+
		"under the same name — so look at %s first, then re-run with -force if they are caf's",
		plural(len(existing), "file"), strings.Join(existing, ", "))
}

// genChartOptions is the parsed flag state for `caf gen chart`.
type genChartOptions struct {
	out   string
	force bool
	env   map[string]string
}

func newGenChartCommand() *Command {
	opts := &genChartOptions{env: map[string]string{}}
	c := &Command{
		Name:    "gen chart",
		Summary: "write the Helm chart a service is deployed from",
		Usage:   "caf gen chart [flags]",
		LongHelp: "Reads this project's cafaye.yml — the current directory by default — and\n" +
			"writes the Helm chart the service is deployed from:\n" +
			"\n" +
			"  chart/Chart.yaml                    the chart's identity\n" +
			"  chart/values.yaml                  the deployment-time half: the image,\n" +
			"                                      the tag, the resources, the env\n" +
			"  chart/templates/deployment.yaml   the Deployment: image, port, env,\n" +
			"                                      readiness probe, resources\n" +
			"  chart/templates/ingress.yaml      the Ingress: how the service is reached,\n" +
			"                                      off by default — gated on values.yaml\n" +
			"  chart/templates/service.yaml      the Service in front of it\n" +
			"  chart/templates/application.yaml  the ArgoCD Application: the\n" +
			"                                      pull-only loop\n" +
			"  chart/README.md                    the loop, the push step, the\n" +
			"                                      secrets, the readiness contract\n" +
			"\n" +
			"The chart is generated-shaped and real: it renders. The readiness probe\n" +
			"is /readyz — the fleet's contract, baked into the template rather than\n" +
			"left as a value, because a readiness path that can be edited is one\n" +
			"that can be pointed at /up. Secrets come by reference: the Deployment\n" +
			"reads a named Secret with envFrom, and no credential reaches this\n" +
			"chart. The image tag is a value and is never `latest`.\n" +
			"\n" +
			"The service's environment arrives as repeated -env flags, because the\n" +
			"manifest carries no env — a service's configuration is its own, and\n" +
			"caf will not guess it:\n" +
			"\n" +
			"  caf gen chart -env DATABASE_URL=... -env API_KEY=...\n" +
			"\n" +
			"Without -env the chart's env block is empty and the values are filled\n" +
			"on the box, per deployment.\n" +
			"\n" +
			genFlagNote,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&opts.out, "out", ".", "directory to write generated output to")
			fs.BoolVar(&opts.force, "force", false, "overwrite generated files that already exist")
			fs.Var(&envFlag{values: &opts.env}, "env", "NAME=VALUE pair for the service's environment; repeatable")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		return runGenChart(opts, env)
	}
	return c
}

// envFlag is a repeatable -env NAME=VALUE flag.
//
// A flag.Value rather than a slice the command splits itself, because the
// parse error belongs at the flag — a pair with no `=` is a usage mistake
// (exit 2), not a generation failure (exit 1), and the two have different
// remedies.
type envFlag struct {
	values *map[string]string
}

func (f *envFlag) String() string { return "" }

func (f *envFlag) Set(pair string) error {
	name, value, found := strings.Cut(pair, "=")
	if !found || name == "" {
		// A plain error, not errUsage: Execute wraps every flag-parse failure
		// in errUsage already, and a second wrap is a sentence nobody reads.
		return fmt.Errorf("-env wants NAME=VALUE, got %q", pair)
	}
	(*f.values)[name] = value
	return nil
}

// runGenTelemetry is the whole command, and it is in one function because the
// three steps are one sequence with no seam in it: read the project, generate
// into memory, write what is missing.
//
// Generating into memory before writing anything is the ordering that matters. A
// generator that wrote each file as it produced it would leave a half-updated tree
// behind when the third file hit an existing file, and the next run would then
// refuse the first two — so an interrupted run becomes a project that needs a
// human to work out what state it is in.
func runGenTelemetry(opts *genOptions, env *Env) error {
	project, err := dev.Load(".")
	if err != nil {
		return fmt.Errorf("caf gen telemetry: %w", err)
	}

	spec, err := contract.Telemetry()
	if err != nil {
		return fmt.Errorf("caf gen telemetry: read the vendored telemetry contract: %w", err)
	}

	out, err := gen.Generate("telemetry", project.Manifest, gen.TargetOptions{Spec: spec})
	if err != nil {
		// A refusal from the generator is already a sentence aimed at the person
		// who typed the command, so it is passed through rather than rewrapped
		// into something vaguer.
		return fmt.Errorf("caf gen telemetry: %w", err)
	}

	// Refused before anything is written, so the report is about every file at
	// once rather than about whichever happened to be first.
	existing, err := existingFiles(out.Files, opts.out)
	if err != nil {
		return fmt.Errorf("caf gen telemetry: %w", err)
	}
	if len(existing) > 0 && !opts.force {
		return errRefusedExisting(existing)
	}

	for _, file := range out.Files {
		if err := writeGenerated(opts.out, file); err != nil {
			return fmt.Errorf("caf gen telemetry: %w", err)
		}
	}

	printGenerated(env.Stdout, out)
	return nil
}

// existingFiles is the subset of generated paths already on disk.
//
// `os.Stat` and not `os.ReadFile`: the question is whether the file is there, and
// a directory is a refusal rather than a write error, so both come back from the
// same check.
func existingFiles(files []gen.File, root string) ([]string, error) {
	var found []string
	for _, file := range files {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(file.Path)))
		switch {
		case err == nil:
			found = append(found, file.Path)
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, fmt.Errorf("check %s: %w", file.Path, err)
		}
	}
	return found, nil
}

// errRefusedExisting says what is in the way and what to do about it.
//
// It says more than "-force", because "-force" alone assumes the reader knows the
// file is generated, and it may not be: `internal/telemetry/` could be a package
// somebody wrote by hand under that name. Overwriting that would destroy work,
// and a generator that can do it silently is a generator nobody runs.
func errRefusedExisting(existing []string) error {
	return fmt.Errorf("nothing was written; %s already exist. caf gen telemetry does not overwrite a "+
		"generated file without being told to, and it cannot tell a generated one from one somebody wrote "+
		"under the same name — so look at %s first, then re-run with -force if they are caf's",
		plural(len(existing), "file"), strings.Join(existing, ", "))
}

// writeGenerated writes one file, creating the directories above it.
func writeGenerated(root string, file gen.File) error {
	path := filepath.Join(root, filepath.FromSlash(file.Path))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, file.Content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// printGenerated prints every file in full, under a header naming the project and
// the core contract it came from.
//
// In full, not a summary, because `caf dev` set the precedent and the reason holds
// here too: a command that generates something nobody can inspect is a command
// nobody can debug, and a document that only lives inside a tool is a document
// nobody can diff. A path and a byte count would tell a reader that three files
// exist and nothing about what is in them.
func printGenerated(w io.Writer, out gen.Output) {
	fmt.Fprintf(w, "caf gen %s wrote %d file(s) from this project's cafaye.yml:\n", out.Target, len(out.Files))
	for _, file := range out.Files {
		fmt.Fprintf(w, "\n===== %s =====\n%s", file.Path, file.Content)
	}
}

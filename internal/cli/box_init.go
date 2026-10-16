package cli

import (
	"fmt"
	"strings"
)

// boxInstallURL is the one canonical install script. The control plane serves
// it at https://cafaye.com/box/install and that is the only maintained copy:
// a second copy vendored in this binary would be a second source of truth that
// can disagree with the server, and the failure is a client and a server running
// different installers.
const boxInstallURL = "https://cafaye.com/box/install"

// boxInstallCommand is the exact one-liner a user runs. curl -fsSL so a 404
// is silent rather than piped into bash, and bash so the script runs.
const boxInstallCommand = "curl -fsSL " + boxInstallURL + " | bash"

// boxEnrollCommand is the step that follows a finished install. It matches
// `cafe box mint`'s own spelling: `--token -` reads the capability from stdin
// (a token in argv is a token in `ps` and in the shell history), and `--box`
// names the box's origin. `cafe box init` cannot know the origin, so it leaves
// it as a placeholder the operator fills in.
const boxEnrollCommand = "cafe box enroll --token - --box <your-box-origin>"

// installInputKind is how the emitted block treats one input: as an assignment
// the operator MUST provide, an optional one, or one that already has a working
// default and should not be set at all unless it is being changed.
type installInputKind string

const (
	// inputRequired: the installer refuses, naming the variable, when it is
	// unset. There is no default and no way to continue without a value.
	inputRequired installInputKind = "required"
	// inputOptional: unset is a supported state (or the input is only needed
	// for a different install shape — a private image, an existing cluster).
	inputOptional installInputKind = "optional"
	// inputDefault: the installer (or the service it configures) has a working
	// default. Setting it changes the default; setting it is never required.
	inputDefault installInputKind = "default"
)

// installInput is one environment variable the installer reads. It is the ONE
// place cafe records the set, because cafe cannot read the installer: the script
// lives in another repository (`cafaye/kit`'s `templates/substrate/install.sh`,
// the bytes the control plane serves at boxInstallURL) and is served over the
// network, which is exactly what `cafe box init` promises never to touch. So the
// table below is copied BY HAND from the installer's own refusals — each entry's
// truth is the `kit_substrate_refuse "<NAME>" …` call in install.sh that names
// it — and a drift between the two is a defect this table is where to fix.
//
// The installer is the authority; this file is the copy. Every `Kind` here is a
// claim about install.sh, and the tests pin the required set so that a variable
// silently dropped from the emitted command fails the gate rather than leaving a
// stranger one refusal short of a working box.
type installInput struct {
	// Name is the environment variable itself (`KIT_SUBSTRATE_…`).
	Name string
	// Kind is required, optional or default — see installInputKind.
	Kind installInputKind
	// Meaning is one line on what the value is for. It goes after a `#` in the
	// block and on its own line in the prose, so it may not contain a newline.
	Meaning string
	// Value is what the emitted block assigns: a placeholder the operator
	// replaces, or a working value. Empty for a `default` input, whose value is
	// the default text below instead.
	Value string
	// Replace is true when Value is an example the operator must replace (a
	// placeholder), false when Value is a working value (the databases list, or
	// a default) that can be left as it is.
	Replace bool
	// Default is the working value for Kind == inputDefault, spelled the way the
	// installer spells it (never invented — read from install.sh's `${VAR:=…}`
	// line or from the comment beside it).
	Default string
}

// boxInstallInputs is every operator input the substrate installer reads, in the
// order a run meets them, so the list is also the order a run is refused in.
//
// AUTHORITY: `cafaye/kit`'s `templates/substrate/install.sh` — the file served at
// boxInstallURL. Required rows mirror its `kit_substrate_refuse "<NAME>"` calls;
// `default` rows mirror its `${KIT_SUBSTRATE_…:=…}` defaults. The comment names
// the authority because cafe cannot import it, which is the whole reason the
// list exists here and not read from one source.
var boxInstallInputs = []installInput{
	// --- step 5: the image pull secret — only when an artifact is private ---
	{
		Name:    "KIT_SUBSTRATE_BOX_ID",
		Kind:    inputOptional,
		Meaning: "this box's enrolled uuid; needed ONLY when an artifact is private, and what the registry capability is bound to",
		Value:   "<the uuid cafe box enroll prints for this box>",
		Replace: true,
	},
	{
		Name:    "KIT_SUBSTRATE_REGISTRY_CAPABILITY",
		Kind:    inputOptional,
		Meaning: "the single-use cap_ token cafe box enroll prints; needed ONLY when an artifact is private — public artifacts demand nothing",
		Value:   "<the cap_… token cafe box enroll prints>",
		Replace: true,
	},

	// --- step 7: the Let's Encrypt ClusterIssuers ---
	{
		Name:    "KIT_SUBSTRATE_ACME_EMAIL",
		Kind:    inputRequired,
		Meaning: "the address Let's Encrypt emails about this box's certificates; example.com is refused, so it must be real",
		Value:   "you@example.com",
		Replace: true,
	},

	// --- step 8: the Postgres Cluster ---
	{
		Name:    "KIT_SUBSTRATE_DATABASES",
		Kind:    inputRequired,
		Meaning: "the comma-separated service names; this is the value the Cluster manifest provisions — keep it unless you changed the manifest",
		Value:   "identity,gateway,courier,parlor",
		Replace: false,
	},

	// --- step 9: courier's externally-sourced settings ---
	{
		Name:    "KIT_SUBSTRATE_SMTP_HOST",
		Kind:    inputRequired,
		Meaning: "your mail relay's host",
		Value:   "smtp.example.com",
		Replace: true,
	},
	{
		Name:    "KIT_SUBSTRATE_SMTP_USERNAME",
		Kind:    inputRequired,
		Meaning: "your mail relay's username",
		Value:   "relay-user",
		Replace: true,
	},
	{
		Name:    "KIT_SUBSTRATE_SMTP_PASSWORD",
		Kind:    inputRequired,
		Meaning: "your mail relay's password — a secret; never commit it, and it is never printed",
		Value:   "<your-smtp-password>",
		Replace: true,
	},
	{
		Name:    "KIT_SUBSTRATE_COURIER_INBOUND_RESEND_SECRET",
		Kind:    inputRequired,
		Meaning: "the webhook signing secret from your mail provider's dashboard (Resend: the endpoint's Signing Secret) — a secret",
		Value:   "<the signing secret from your provider>",
		Replace: true,
	},

	// --- step 12: identity's mail path and OIDC surface (all optional) ---
	{
		Name:    "KIT_SUBSTRATE_APP_BASE_URL",
		Kind:    inputOptional,
		Meaning: "the public origin a browser reaches; set it to give the box a mail path, leave it unset for no mail path",
		Value:   "https://app.example.com",
		Replace: true,
	},
	{
		Name:    "KIT_SUBSTRATE_IDENTITY_OIDC_ISSUER",
		Kind:    inputOptional,
		Meaning: "the URL identity publishes as its issuer; set it with the login UI below, or leave both unset for no OIDC",
		Value:   "https://auth.example.com",
		Replace: true,
	},
	{
		Name:    "KIT_SUBSTRATE_IDENTITY_OIDC_LOGIN_UI_URL",
		Kind:    inputOptional,
		Meaning: "the page the browser is sent to in order to sign in; set it with the issuer above, or neither",
		Value:   "https://app.example.com/sign-in/oidc",
		Replace: true,
	},

	// --- the other install shape: a cluster that already exists ---
	{
		Name:    "KIT_SUBSTRATE_KUBECONFIG",
		Kind:    inputOptional,
		Meaning: "a kubeconfig path, to install into a cluster that ALREADY exists instead of provisioning the host; a fresh box leaves it unset",
		Value:   "</path/to/kubeconfig>",
		Replace: true,
	},

	// --- working defaults: named so nobody sets them by accident ---
	{
		Name:    "KIT_SUBSTRATE_BOX_URL",
		Kind:    inputDefault,
		Meaning: "where a capability is redeemed",
		Default: "https://box.svc.cafaye.com",
	},
	{
		Name:    "KIT_SUBSTRATE_REGISTRY_URL",
		Kind:    inputDefault,
		Meaning: "where a capability is exchanged for a registry credential",
		Default: "https://registry.svc.cafaye.com",
	},
	{
		Name:    "KIT_SUBSTRATE_SMTP_ADAPTER",
		Kind:    inputDefault,
		Meaning: "courier's mail adapter (smtp or postmark)",
		Default: "smtp",
	},
	{
		Name:    "KIT_SUBSTRATE_SMTP_PORT",
		Kind:    inputDefault,
		Meaning: "the SMTP submission port",
		Default: "587",
	},
	{
		Name:    "KIT_SUBSTRATE_POSTGRES_EXTENSIONS",
		Kind:    inputDefault,
		Meaning: "the Postgres extension list",
		Default: "vector",
	},
	{
		Name:    "KIT_SUBSTRATE_IDENTITY_MFA_ENCRYPTION_KEY",
		Kind:    inputDefault,
		Meaning: "identity's MFA key — generated on the box on first creation and read back thereafter",
		Default: "(generated)",
	},
	{
		Name:    "KIT_SUBSTRATE_IDENTITY_OAUTH_ENCRYPTION_KEY",
		Kind:    inputDefault,
		Meaning: "identity's OAuth key — unset keeps the OAuth surface off",
		Default: "(unset)",
	},
	{
		Name:    "KIT_SUBSTRATE_MAIL_FROM",
		Kind:    inputDefault,
		Meaning: "courier's From address — falls back to courier's own default",
		Default: "(courier's default)",
	},
	{
		Name:    "KIT_SUBSTRATE_MAIL_FROM_NAME",
		Kind:    inputDefault,
		Meaning: "courier's From display name — falls back to courier's own default",
		Default: "(courier's default)",
	},
}

// inputsOfKind filters the table. It exists so the renderer and the prose read
// the same table in the same order, and so neither can carry a second list.
func inputsOfKind(kind installInputKind) []installInput {
	out := make([]installInput, 0, len(boxInstallInputs))
	for _, in := range boxInstallInputs {
		if in.Kind == kind {
			out = append(out, in)
		}
	}
	return out
}

// renderInstallCommand builds the copyable block from the table, so the block
// and the table cannot disagree: a variable that is not in the table cannot be
// in the block, and a variable added to the table appears in the block on the
// next line without anyone editing two places.
//
// The shape is `export`s followed by the install one-liner, and the exports are
// what make it work as one paste: `VAR=… curl … | bash` would set the variable
// for CURL only — an environment prefix binds to the command it precedes, and
// the installer runs in the `bash` on the right of the pipe. The `export`s put
// the values in the shell the pipe's `bash` inherits from. This is the shape the
// substrate README's own "How to run it" block uses, for the same reason.
func renderInstallCommand() string {
	var b strings.Builder
	b.WriteString("# cafaye box install. Replace anything in <angle brackets> or on example.com,\n")
	b.WriteString("# keep the rest, then run this whole block in one shell.\n")

	b.WriteString("# Required — the installer refuses, one per run, without each of these:\n")
	for _, in := range inputsOfKind(inputRequired) {
		fmt.Fprintf(&b, "export %s='%s'   # %s\n", in.Name, in.Value, in.Meaning)
	}

	b.WriteString("# Optional — each may be left unset; set only what you need:\n")
	for _, in := range inputsOfKind(inputOptional) {
		fmt.Fprintf(&b, "# export %s='%s'   # %s\n", in.Name, in.Value, in.Meaning)
	}

	b.WriteString("# Working defaults — do NOT set these unless you are changing the default:\n")
	for _, in := range inputsOfKind(inputDefault) {
		fmt.Fprintf(&b, "#   %s=%s   # %s\n", in.Name, in.Default, in.Meaning)
	}

	b.WriteString("\n")
	b.WriteString(boxInstallCommand)
	return b.String()
}

// renderInstallProse is the human walkthrough on stderr: what to provide, then
// the block to paste, then the enroll step. It reads the same table the block
// does, so the two cannot drift.
func renderInstallProse() string {
	var b strings.Builder
	b.WriteString("cafe box init: the box install front door.\n")
	b.WriteString("There is one install script (" + boxInstallURL + ") and this is how to run it with\n")
	b.WriteString("every value it reads. Fill in the placeholders, then run the block in one shell.\n\n")

	b.WriteString("Required inputs — the installer refuses without each one:\n")
	for _, in := range inputsOfKind(inputRequired) {
		fmt.Fprintf(&b, "  %s = %s   (%s)\n", in.Name, in.Value, in.Meaning)
	}

	b.WriteString("\nOptional inputs — leave any of these unset if you do not need it:\n")
	for _, in := range inputsOfKind(inputOptional) {
		fmt.Fprintf(&b, "  %s = %s   (%s)\n", in.Name, in.Value, in.Meaning)
	}

	b.WriteString("\nValues with working defaults — do not set these unless you are changing the default:\n")
	for _, in := range inputsOfKind(inputDefault) {
		fmt.Fprintf(&b, "  %s = %s   (%s)\n", in.Name, in.Default, in.Meaning)
	}

	b.WriteString("\nThe complete command, as one block to paste and edit:\n\n")
	b.WriteString(renderInstallCommand())
	b.WriteString("\n\nWhen the install finishes, enroll this box with the capability the control plane\n")
	b.WriteString("minted for it (the installer prints this step again when it is done):\n\n")
	b.WriteString("  " + boxEnrollCommand + "\n")
	return b.String()
}

// boxInitInputDoc is one input in the machine document.
type boxInitInputDoc struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Meaning string `json:"meaning"`
	// Value is the placeholder or working value the command assigns, and is
	// omitted for a `default` input (whose working value is Default).
	Value string `json:"value,omitempty"`
	// Default is the working value for a `default` input.
	Default string `json:"default,omitempty"`
}

// boxInitDoc is the machine document `cafe box init` prints on stdout. One JSON
// document, because every command in this tree speaks JSON on stdout and prose
// on stderr, and a script that parses the install command should not have to
// scrape a terminal.
type boxInitDoc struct {
	InstallURL     string            `json:"install_url"`
	InstallCommand string            `json:"install_command"`
	EnrollCommand  string            `json:"enroll_command"`
	Inputs         []boxInitInputDoc `json:"inputs"`
	Message        string            `json:"message"`
}

// boxInitDocument is the one document the command prints. It is a function so
// the tests can build the same document the command does without running it
// through the router, and so the table is read exactly once.
func boxInitDocument() boxInitDoc {
	inputs := make([]boxInitInputDoc, 0, len(boxInstallInputs))
	for _, in := range boxInstallInputs {
		inputs = append(inputs, boxInitInputDoc{
			Name:    in.Name,
			Kind:    string(in.Kind),
			Meaning: in.Meaning,
			Value:   in.Value,
			Default: in.Default,
		})
	}
	return boxInitDoc{
		InstallURL:     boxInstallURL,
		InstallCommand: renderInstallCommand(),
		EnrollCommand:  boxEnrollCommand,
		Inputs:         inputs,
		Message:        "the install command is complete once you replace the placeholders; run it, then run the enroll command with the capability the control plane minted for this box — see `cafe box enroll --help` for the token plumbing",
	}
}

const boxInitLongHelp = `The install front door.

There is exactly one maintained install script: the one the control plane serves
at ` + boxInstallURL + `. This command does not carry a second copy and never
fetches one — it GENERATES the front door from that URL, so the one-liner it
prints and the URL the server answers are the same string. A vendored script
would be a second source of truth that can drift, and a drift is a client that
installs one thing while the server documents another.

The installer fails fast: it refuses, naming one environment variable per run,
when a value it needs is missing. A stranger reading a bare one-liner would meet
that refusal with no way to know the whole set — so this command prints the whole
set, parameterized. What it prints, in order:

  1. the inputs the install reads — which are required (the installer refuses
     without them), which are optional (unset is a supported state), and which
     already have a working default — each with a one-line meaning and a safe
     example the operator replaces;
  2. the COMPLETE install command as one copyable block: the exports the
     installer reads, then ` + boxInstallCommand + `;
  3. the enroll step that follows (` + "`" + boxEnrollCommand + "`" + `), which the installer
     prints again when it finishes.

The required set is the installer's own refusals. cafe cannot read the installer
(it is served, not vendored, and this command promises no network), so the set is
recorded in ONE place in this package — the table every line above is rendered
from — with a comment naming ` + "`templates/substrate/install.sh`" + ` as the authority. A
test pins it, so dropping a variable fails the gate rather than leaving an
operator one refusal short of a working box.

Deterministic and safe: no network, no config, no secret. Run it twice and the
bytes are identical, and pasting the install_command into a shell is the intended
use.`

func newBoxInitCommand() *Command {
	c := &Command{
		Name:     "box init",
		Summary:  "print the complete, parameterized box install command and the enroll step that follows",
		Usage:    "cafe box init",
		LongHelp: boxInitLongHelp,
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		doc := boxInitDocument()
		_, _ = fmt.Fprint(env.Stderr, renderInstallProse())
		return writeJSONDoc(env.Stdout, doc)
	}
	return c
}

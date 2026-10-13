package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cafaye/cafe/internal/stack"
)

// boxStackDeps is what the deploy verbs need from the world: where this
// box's stack lives, and the substrate that starts a release on it. It is a
// struct of parameters rather than package-level variables for the reason
// boxDeps is — a test can hand the commands a stack in a temporary directory
// and a runner that records its argv, which is the only way a deploy can be
// driven through the real router without a container runtime.
type boxStackDeps struct {
	// root is the stack's directory: releases, current, history, logs.
	root string
	// ready is the health gate. Nil means the box's own edge on loopback,
	// which is the only honest default: a deploy is not a deploy unless
	// something answered.
	ready func(ctx context.Context, rel stack.Release) error
	// run executes the container-runtime argv. Nil means exec.Command, and a
	// test always sets it.
	run func(ctx context.Context, argv []string) error
}

// substrate is the box's answer to "start this release"

// stackRootEnv is where the stack's directory comes from when no flag names
// one. An environment variable rather than a flag default baked into the
// binary, because the default that ships in a release is the default nobody
// can change without a new release.
const stackRootEnv = "CAF_BOX_STACK"

// defaultStackRoot is the box's own location. It is not under the config
// directory: the release history is the box's PRODUCT, not its settings, and
// it is sized in gigabytes rather than in kilobytes.
const defaultStackRoot = "/srv/cafe/stack"

func (d boxStackDeps) withDefaults() boxStackDeps {
	if d.root == "" {
		d.root = os.Getenv(stackRootEnv)
	}
	if d.root == "" {
		d.root = defaultStackRoot
	}
	return d
}

// substrate is the box's answer to "start this release": pull the pinned
// build, run it with the release's own env file, and let the edge route to it.
//
// # Why argv is built by a pure function and executed by a seam
//
// internal/deploy holds the same split and the reason: a claim about what a
// deploy WILL do has to be assertable without the thing it deploys onto. So
// pullArgv and runArgv are pure and tested against the exact argv they
// produce, and the only piece that touches the world is the runner — which a
// test replaces with one that records.
//
// # The secrets never travel in argv
//
// The release's secret set is installed as a FILE inside the release
// directory and passed as `--env-file`. argv is visible to every other
// process on the box through /proc, so a secret in argv is a secret published
// to everything running on the machine — the same discipline the secret
// package holds, for the same reason.
type substrate struct {
	root string
	// bin is the container runtime. docker first, because that is what the
	// box's bootstrap installs; podman is the named fallback for the boxes
	// that run it.
	bin string
	// listen is the address the box's edge listens on, read out of the edge's
	// own state file. The health gate dials it.
	listen string
	// ready overrides the loopback health gate, for a test.
	ready func(ctx context.Context, rel stack.Release) error
	// run executes the container-runtime argv. Nil means exec.Command, and a
	// test always sets it: the argv is the product, the running of it is the
	// live tier.
	run func(ctx context.Context, argv []string) error
}

// edgeFile is the live host and certificate state the box's edge publishes.
// It is READ here rather than owned here: the edge is the only thing that
// knows what it routes and what certificate it holds, and a second copy of
// that state in the deploy path would be a copy that can disagree with the
// one serving traffic.
type edgeFile struct {
	Listen string `json:"listen"`
	Hosts  map[string]struct {
		Routed       bool      `json:"routed"`
		CertSubject  string    `json:"cert_subject,omitempty"`
		CertNotAfter time.Time `json:"cert_not_after,omitempty"`
	} `json:"hosts"`
}

func edgeStatePath(root string) string { return filepath.Join(root, "edge.json") }

// readEdgeState reads the edge's state. A missing file is a refusal that
// names the path rather than a stack with no hosts: the edge writes it, and
// a box whose edge has not written it has nothing to deploy through.
func readEdgeState(root string) (edgeFile, error) {
	raw, err := os.ReadFile(edgeStatePath(root))
	if err != nil {
		return edgeFile{}, fmt.Errorf("read the edge state at %s: %w — the box's edge writes this file and a deploy without it has nothing to be routed by", edgeStatePath(root), err)
	}
	var e edgeFile
	if err := json.Unmarshal(raw, &e); err != nil {
		return edgeFile{}, fmt.Errorf("read the edge state at %s: %w", edgeStatePath(root), err)
	}
	return e, nil
}

// pullArgv is the pull. The digest always, and the tag only as a label: what
// the box runs is the bytes the digest names, and a pull by tag is how a box
// ends up running something it never intended.
func pullArgv(bin, image string) []string {
	return []string{bin, "image", "pull", image}
}

// runArgv is the run. Three things in it are the design:
//
//   - `--env-file <release>/env` is the secret set, installed by the box at
//     deploy time and never in argv, never in the image.
//   - the release DIRECTORY is what is published, so a rollback switches the
//     edge's upstream and starts nothing.
//   - the container name carries the release id, so a second deploy is a
//     second name and the first release's container is still there to go
//     back to.
func runArgv(bin, releaseDir, id string) []string {
	return []string{
		bin, "run", "--detach", "--name", "cafe-" + id,
		"--env-file", filepath.Join(releaseDir, "env"),
		"--volume", releaseDir + ":/srv/release:ro",
		"cafaye/release:" + id,
	}
}

// Host is the live host state the deploy reads before it stages anything.
func (s *substrate) Host(name string) (stack.Host, bool) {
	e, err := readEdgeState(s.root)
	if err != nil {
		return stack.Host{}, false
	}
	h, ok := e.Hosts[name]
	if !ok {
		return stack.Host{}, false
	}
	return stack.Host{
		Name:         name,
		Routed:       h.Routed,
		CertSubject:  h.CertSubject,
		CertNotAfter: h.CertNotAfter,
	}, true
}

// Serve pulls the pinned build and runs it, with the release's own env file.
func (s *substrate) Serve(ctx context.Context, rel stack.Release) error {
	// The release directory is the SERVICE's own, under the stack root, and it
	// is what the container mounts read-only: a rollback switches the pointer
	// and the release's bytes are untouched.
	dir := filepath.Join(s.root, rel.Service, rel.ID)
	bin, err := s.runtime()
	if err != nil {
		return err
	}
	if s.run != nil {
		if err := s.run(ctx, pullArgv(bin, rel.Image)); err != nil {
			return fmt.Errorf("pull %s: %w", rel.Image, err)
		}
		return s.run(ctx, runArgv(bin, dir, rel.ID))
	}
	run := execRunner()
	if err := run(ctx, pullArgv(bin, rel.Image)); err != nil {
		return fmt.Errorf("pull %s: %w", rel.Image, err)
	}
	if err := run(ctx, runArgv(bin, dir, rel.ID)); err != nil {
		return fmt.Errorf("run release %s: %w", rel.ID, err)
	}
	return nil
}

// runtime is the container-runtime binary, resolved once. A box that has
// neither is refused by name rather than by an exec failure halfway through a
// pull.
func (s *substrate) runtime() (string, error) {
	if s.bin != "" {
		return s.bin, nil
	}
	if bin := firstOnPath("docker", "podman"); bin != "" {
		return bin, nil
	}
	return "", errors.New("no container runtime on PATH: install docker, or run this on a box that has it")
}

// execRunner is the real one. The argv is the product and running it is the
// live tier, which is why the seam exists at all.
func execRunner() func(context.Context, []string) error {
	return func(ctx context.Context, argv []string) error {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return cmd.Run()
	}
}

// Ready is the health gate, and it is a real request: the box's own edge, on
// loopback, with the Host header the public name would carry. A gate that
// asked the container directly would be a gate that never noticed the edge
// stopped routing.
func (s *substrate) Ready(ctx context.Context, rel stack.Release) error {
	if s.ready != nil {
		return s.ready(ctx, rel)
	}
	listen := s.listen
	if listen == "" {
		e, err := readEdgeState(s.root)
		if err != nil {
			return err
		}
		listen = e.Listen
	}
	if listen == "" {
		return errors.New("the edge state names no listen address, so there is nothing to health-check against")
	}
	url := "http://" + normalizeListen(listen) + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Host = rel.Host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s through the edge: %w", rel.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d through the edge", rel.Host, resp.StatusCode)
	}
	return nil
}

// normalizeListen turns the edge's listen address into a dialable one.
// ":8443" is a LISTEN address, not a dial address, and the difference is the
// loopback interface.
func normalizeListen(listen string) string {
	if strings.HasPrefix(listen, ":") {
		return "127.0.0.1" + listen
	}
	return listen
}

func firstOnPath(names ...string) string {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// stackStore opens the stack for the ROOT it was given. The root is one
// service's: `cafe box deploy` joins the service's own directory under it, so
// a rollback of api cannot move auth's traffic.
func stackStore(deps boxStackDeps, service string) (*stack.Store, error) {
	deps = deps.withDefaults()
	if service != "" {
		deps.root = stack.ServiceDir(deps.root, service)
	}
	s, err := stack.Open(deps.root)
	if err != nil {
		return nil, err
	}
	if n, ok := os.LookupEnv("CAF_BOX_KEEP"); ok {
		if k, err := strconv.Atoi(n); err == nil && k > 0 {
			s.Keep = k
		}
	}
	return s, nil
}

// boxStackFlags is the shared flag state of the deploy verbs.
type boxStackFlags struct {
	json bool
}

func (f *boxStackFlags) declare(fs *flag.FlagSet) {
	fs.BoolVar(&f.json, "json", false, "print the machine document on stdout instead of the human one")
}

// boxDeployOpts is `cafe box deploy`'s flag state.
type boxDeployOpts struct {
	boxStackFlags
	service string
	build   string
	tag     string
	host    string
	secrets string
	root    string
}

func newBoxDeployCommand(deps boxDeps) *Command {
	opts := &boxDeployOpts{}
	c := &Command{
		Name:    "box deploy",
		Summary: "ship a pantry build to this box: stage it, switch traffic to it, and roll back if it does not answer",
		Usage:   "cafe box deploy --build <name@sha256:…> --host <name> [--service <name>] [--tag <version>] [--secrets <set>] [--root <dir>] [--json]",
		Flags: func(fs *flag.FlagSet) {
			opts.declare(fs)
			fs.StringVar(&opts.service, "service", "", "the service this build belongs to; default is derived from the build's image name")
			fs.StringVar(&opts.build, "build", "", "the pantry build to ship: an OCI reference pinned by digest, name@sha256:…")
			fs.StringVar(&opts.tag, "tag", "", "the version the build carries, for the record and for a person reading it")
			fs.StringVar(&opts.host, "host", "", "the host name this release answers on through the box's edge")
			fs.StringVar(&opts.secrets, "secrets", "", "the name of the secret set the box installs into this release; never a value")
			fs.StringVar(&opts.root, "root", "", "where this box's stack lives (default $"+stackRootEnv+" or "+defaultStackRoot+")")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if err := wantArgs(c.Name, c.Usage, 0, len(args)); err != nil {
			return err
		}
		req, err := deployRequest(opts)
		if err != nil {
			return err
		}
		sd := deps.stack
		if opts.root != "" {
			sd.root = opts.root
		}
		store, err := stackStore(sd, req.Service)
		if err != nil {
			return err
		}
		box := substrateFor(sd, store)
		rep, err := stack.Deploy(env.Context, store, box, req)
		// A failed deploy is REPORTED rather than swallowed: the document says
		// what is serving now, which after a flip-back is the last good
		// release and not the one that broke.
		if err := writeDoc(env.Stdout, rep, opts.json); err != nil {
			return err
		}
		if err != nil {
			fmt.Fprintf(env.Stderr, "cafe box deploy: %v\n", err)
			if rep.RolledBack {
				fmt.Fprintf(env.Stderr, "cafe box deploy: %s is serving %s again — the deploy failed and the box is intact\n", rep.Release.Service, rep.Release.ID)
			}
			return errReported
		}
		say(env.Stderr, "%s", rep.Detail)
		return nil
	}
	return c
}

// findService is the directory under a stack root that holds one release id.
// It is how a release id names its own service, so `cafe box rollback <id>`
// and `cafe box logs --release <id>` do not each need a service flag.
func findService(root, id string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read the stack root %s: %w", root, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := stack.Open(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		if _, err := s.Release(id); err == nil {
			return e.Name(), nil
		}
	}
	return "", fmt.Errorf("%w: %s", stack.ErrNoSuchRelease, id)
}

// stackServices is what the box has, for a refusal that lists it rather than
// guessing which one the person meant.
func stackServices(deps boxStackDeps) ([]string, error) {
	root := deps.withDefaults().root
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read the stack root %s: %w", root, err)
	}
	var services []string
	for _, e := range entries {
		if e.IsDir() {
			services = append(services, e.Name())
		}
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("no service has ever deployed to this box: nothing to roll back")
	}
	sort.Strings(services)
	return services, nil
}

// releaseLogPath is one release's own log, wherever on the box it lives. It
// walks the services under the stack root because the question a person asks
// is "what did this release do" and not "which service was it", and the
// answer must not depend on their remembering.
func releaseLogPath(deps boxStackDeps, id string) (string, error) {
	service, err := findService(deps.withDefaults().root, id)
	if err != nil {
		return "", err
	}
	s, err := stack.Open(stack.ServiceDir(deps.withDefaults().root, service))
	if err != nil {
		return "", err
	}
	return s.LogPath(id), nil
}

// substrateFor builds the box the deploy drives. It is a method on the deps
// so a test can inject a runner and a gate; the flags never reach it.
//
// The edge state is read from the STACK ROOT rather than from the service's
// own directory, because the edge is the box's — it holds every host it
// routes, for every service — and a service-scoped copy of it would be a
// second copy of the one fact that decides what a deploy may reach.
func substrateFor(deps boxStackDeps, store *stack.Store) *substrate {
	return &substrate{root: filepath.Dir(store.Root), run: deps.run, ready: deps.ready}
}

// deployRequest turns the flags into the request. The service name defaults
// out of the image name, because a person typing `--build ghcr.io/cafaye/api@sha256:…`
// has already said which service it is, and a second flag asking the same
// question is a flag a person gets wrong.
func deployRequest(opts *boxDeployOpts) (stack.Request, error) {
	if strings.TrimSpace(opts.build) == "" {
		return stack.Request{}, fmt.Errorf("%w: cafe box deploy wants --build <name@sha256:…>, the pantry build to ship (usage: %s)", errUsage, "cafe box deploy --build <name@sha256:…> --host <name>")
	}
	if strings.TrimSpace(opts.host) == "" {
		return stack.Request{}, fmt.Errorf("%w: cafe box deploy wants --host <name>, the host the release answers on through the box's edge", errUsage)
	}
	name, digest, ok := strings.Cut(strings.TrimSpace(opts.build), "@")
	if !ok {
		return stack.Request{}, fmt.Errorf("%w: --build %q is not a pinned reference: it wants name@sha256:… — a tag is a name that means something different tomorrow, and rollback is instant only if the artifact it goes back to is still the same bytes", errUsage, opts.build)
	}
	registry, name := splitRegistry(name)
	service := strings.TrimSpace(opts.service)
	if service == "" {
		service = serviceFromImage(name)
	}
	if service == "" {
		return stack.Request{}, fmt.Errorf("%w: --build %q does not name a service and --service was not given", errUsage, opts.build)
	}
	return stack.Request{
		Service: service,
		Host:    opts.host,
		Build: stack.Build{
			Registry: registry,
			Name:     name,
			Tag:      strings.TrimSpace(opts.tag),
			Digest:   digest,
		},
		Secrets: strings.TrimSpace(opts.secrets),
	}, nil
}

// splitRegistry is the one place an OCI reference is taken apart. The registry
// is the first component when it carries a dot or a port, which is the rule
// the OCI distribution spec itself states; everything after it is the
// repository. A split that guessed differently would pull from the wrong
// place, and the guess would be found out on the box rather than here.
func splitRegistry(image string) (registry, repo string) {
	if i := strings.Index(image, "/"); i > 0 {
		first := image[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			return first, image[i+1:]
		}
	}
	return "", image
}

// serviceFromImage is the last path element of an OCI reference, which is the
// service's own name in every template this repository ships.
func serviceFromImage(image string) string {
	parts := strings.Split(image, "/")
	last := parts[len(parts)-1]
	if i := strings.Index(last, ":"); i >= 0 {
		last = last[:i]
	}
	return last
}

// boxRollbackOpts is `cafe box rollback`'s flag state.
type boxRollbackOpts struct {
	boxStackFlags
	service string
	root    string
}

func newBoxRollbackCommand(deps boxDeps) *Command {
	opts := &boxRollbackOpts{}
	c := &Command{
		Name:    "box rollback",
		Summary: "put the previous release back: one symlink, no rebuild, no control plane",
		Usage:   "cafe box rollback [<release-id>] [--service <name>] [--root <dir>] [--json]",
		// Interspersed, because the release id is an optional positional
		// subject: `cafe box rollback sample-stack-1 --service api` is the
		// shape a person types, and it is the same command as the flags first.
		// See box_setup.go for the whole argument.
		Interspersed: true,
		Flags: func(fs *flag.FlagSet) {
			opts.declare(fs)
			fs.StringVar(&opts.service, "service", "", "the service to roll back; without it the service the release id names is used, and an id that names none is a refusal that lists what the box has")
			fs.StringVar(&opts.root, "root", "", "where this box's stack lives (default $"+stackRootEnv+" or "+defaultStackRoot+")")
		},
	}
	c.Run = func(args []string, env *Env) error {
		if len(args) > 1 {
			return wantArgs(c.Name, c.Usage, 1, len(args))
		}
		target := ""
		if len(args) == 1 {
			target = args[0]
		}
		if opts.service == "" && target != "" {
			// A release id names its own service: the box finds it, because a
			// person who deployed api knows the release id and not the
			// directory it lives in.
			found, err := findService(deps.stack.withDefaults().root, target)
			if err == nil {
				opts.service = found
			}
		}
		sd := deps.stack
		if opts.root != "" {
			sd.root = opts.root
		}
		if opts.service == "" && target == "" {
			// A box runs several services and each has its own pointer, so
			// "roll back" is a question with more than one answer. Naming the
			// services the box has is the answer that does not guess.
			services, err := stackServices(sd)
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: cafe box rollback wants --service <name> or a release id; this box has %s", errUsage, strings.Join(services, ", "))
		}
		if opts.service == "" {
			// A release id names its own service: the box finds it, because a
			// person who deployed api knows the release id and not the
			// directory it lives in.
			found, err := findService(sd.withDefaults().root, target)
			if err != nil {
				return fmt.Errorf("%w: cafe box rollback <release-id>: %v", errUsage, err)
			}
			opts.service = found
		}
		store, err := stackStore(sd, opts.service)
		if err != nil {
			return err
		}
		back, rep, err := store.Rollback(target, time.Now())
		if err := writeDoc(env.Stdout, rep, opts.json); err != nil {
			return err
		}
		if err != nil {
			fmt.Fprintf(env.Stderr, "cafe box rollback: %v\n", err)
			return errReported
		}
		say(env.Stderr, "%s", rep.Detail)
		_ = back
		return nil
	}
	return c
}

package cve

import (
	"context"
	"sort"
	"strconv"
	"strings"
)

// Kind is what a component is on the box. It is not decoration: it is what
// decides how a fix is applied, and therefore what the impact paragraph says.
// A node agent is upgraded by running the installer again; a chart is upgraded
// by moving an Argo Application's targetRevision; an image is upgraded by
// moving the tag in a manifest. Three different sentences, and a report that
// said "upgrade k3s" the way it says "upgrade redis" would be a report that
// sent an operator to the wrong place.
type Kind string

const (
	// KindNode is the box's own node agent. One per box, and everything else
	// on the box runs because of it.
	KindNode Kind = "node"
	// KindAddon is a cluster add-on applied as Kubernetes manifests by the
	// installer: cert-manager, CloudNativePG, Argo CD.
	KindAddon Kind = "addon"
	// KindWorkload is a workload applied as a bare manifest with a pinned
	// image tag: Redis, NATS.
	KindWorkload Kind = "workload"
	// KindChart is an Argo Application whose targetRevision the installer
	// pins — the eight Applications under manifests/applications/.
	KindChart Kind = "chart"
	// KindImage is a container image the substrate runs whose version is not
	// pinned anywhere in kit: CNPG's Postgres leaves spec.imageName unset on
	// purpose, and the version that lands is whichever one the operator's
	// release line moved to.
	KindImage Kind = "image"
)

// Component is one installed thing on a box.
//
// Version is kept AS OBSERVED, never normalised in place. The substrate's own
// node agent is pinned as `v1.37.1+k3s1` — a leading v and a +build suffix,
// which is not a core version and which a normalising parser would either
// reject or silently strip. The raw spelling stays here and the normalised
// form is computed where it is compared (see comparable), so a report can
// still print what the box said about itself.
type Component struct {
	// Name is what the component is called, in the spelling the substrate
	// uses: `k3s`, `cert-manager`, `redis`, `identity`.
	Name string
	// Version is the version as observed. Empty means the observation could
	// not read one, which is a fact about the box rather than an absence of
	// the component.
	Version string
	// Kind is how a fix to this component is applied.
	Kind Kind
	// Namespace is the Kubernetes namespace it runs in, for the kinds that
	// have one. Empty for the node agent and the cluster-wide add-ons.
	Namespace string
	// Source is where this entry's version came from, in the form a reader
	// can check: a file and line in the substrate, or the observation that
	// produced it.
	Source string
	// Pinned is whether the substrate pins this component's version. An
	// unpinned component still has a version — the box is running one — but
	// nobody chose it, which is exactly the case a CVE report has to name
	// rather than pretend is a pin.
	Pinned bool
	// Declared is whether this entry came from the substrate catalogue
	// (true) or from observing the box (false). An observed component that
	// is not in the catalogue is not refused — it is a thing on the box with
	// a version, and it can carry an advisory like anything else — but it is
	// marked, because "we do not know what installed this" is the first
	// thing the reader of a security report needs to be told.
	Declared bool
	// Blast is what a fix to this component touches. Empty for an observed
	// component nobody has characterised; the report says so rather than
	// inventing a radius.
	Blast Blast
}

// Blast is what applying a fix to one component touches.
//
// It is the whole reason this package exists. The advisory count is free; the
// sentence that decides whether a customer can be upgraded tonight is this
// one, and it is a fact about the substrate rather than about the advisory.
type Blast struct {
	// Apply is how a fix is applied, in the words the operator uses.
	Apply string
	// Restarts is what stops running while it happens. Empty means nothing
	// else restarts, which for a chart is the interesting half of the
	// answer.
	Restarts []string
}

// Substrate is the component set a substrate box is expected to be running,
// with the version each one is pinned to and where that pin is written.
//
// # Why this is a copy and not a read
//
// cafaye/kit is a different repository. caf cannot open
// templates/substrate/install.sh at run time, and a caf that could would be a
// caf whose answer depended on a checkout being beside it. So the catalogue
// is transcribed here and every row carries the file:line it was read from,
// which is the only defence a copy has: a reader can check it, and a drift
// between the two is findable by opening one file.
//
// The set is exactly what the installer's own header lists in its eleven
// steps (kit/templates/substrate/install.sh, lines 15–35) and what the
// manifests it applies pin. Nothing is added because it seems likely to be
// there: the pull secret of step 5, the ClusterIssuers of step 7 and the
// minted credential of step 11 are credentials and configuration, not
// versioned components, and a component nobody can have an advisory against
// is noise in a security report.
var Substrate = []Component{
	{
		Name:     "k3s",
		Version:  "v1.37.1+k3s1",
		Kind:     KindNode,
		Pinned:   true,
		Declared: true,
		Source:   "kit/templates/substrate/install.sh:169",
		Blast: Blast{
			Apply:    "re-run the substrate installer, which installs k3s by pinning INSTALL_K3S_VERSION",
			Restarts: []string{"every workload on the box — the node agent restart drains it"},
		},
	},
	{
		Name:      "cert-manager",
		Version:   "v1.21.2",
		Kind:      KindAddon,
		Namespace: "cert-manager",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/install.sh:170",
		Blast: Blast{
			Apply:    "re-run the substrate installer, which applies cert-manager's release manifest",
			Restarts: []string{"cert-manager's controller and webhook in namespace cert-manager"},
		},
	},
	{
		Name:      "cloudnative-pg",
		Version:   "v1.30.1",
		Kind:      KindAddon,
		Namespace: "cnpg-system",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/install.sh:171",
		Blast: Blast{
			Apply:    "re-run the substrate installer, which applies CloudNativePG's release manifest",
			Restarts: []string{"the CloudNativePG operator in namespace cnpg-system; running clusters are not restarted by it"},
		},
	},
	{
		Name:      "argocd",
		Version:   "3.5.4",
		Kind:      KindAddon,
		Namespace: "argocd",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/install.sh:172",
		Blast: Blast{
			Apply:    "re-run the substrate installer, which applies Argo CD's core-install manifest",
			Restarts: []string{"Argo CD's application controller, repo server and API server in namespace argocd"},
		},
	},
	{
		Name:      "redis",
		Version:   "7.4.1-alpine",
		Kind:      KindWorkload,
		Namespace: "data",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/manifests/data/redis.yaml:65",
		Blast: Blast{
			Apply:    "bump the image tag in manifests/data/redis.yaml and let Argo sync it",
			Restarts: []string{"the redis deployment in namespace data — one restart, and the data volume survives it"},
		},
	},
	{
		Name:      "nats",
		Version:   "2.10.24-alpine",
		Kind:      KindWorkload,
		Namespace: "data",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/install.sh:179",
		Blast: Blast{
			Apply:    "move NATS_VERSION in install.sh, which substitutes it into the broker manifest at apply time",
			Restarts: []string{"the nats deployment in namespace data, and every publisher that holds an open connection to it"},
		},
	},
	{
		Name:      "nats-box",
		Version:   "0.20.0",
		Kind:      KindWorkload,
		Namespace: "data",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/install.sh:180",
		Blast: Blast{
			Apply:    "move NATS_BOX_VERSION in install.sh, which is the stream-init image",
			Restarts: []string{"nothing long-lived — it is a one-shot job that defines the fleet's stream"},
		},
	},
	{
		Name:      "identity",
		Version:   "0.1.0",
		Kind:      KindChart,
		Namespace: "identity",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/manifests/applications/identity.yaml:13",
		Blast: Blast{
			Apply:    "move the identity Application's targetRevision",
			Restarts: []string{"identity's pods in namespace identity; the other three services are unaffected"},
		},
	},
	{
		Name:      "gateway",
		Version:   "0.1.0",
		Kind:      KindChart,
		Namespace: "gateway",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/manifests/applications/gateway.yaml:13",
		Blast: Blast{
			Apply:    "move the gateway Application's targetRevision",
			Restarts: []string{"gateway's pods in namespace gateway, which is the ingress path for every other service"},
		},
	},
	{
		Name:      "courier",
		Version:   "0.1.0",
		Kind:      KindChart,
		Namespace: "courier",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/manifests/applications/courier.yaml:13",
		Blast: Blast{
			Apply:    "move the courier Application's targetRevision",
			Restarts: []string{"courier's pods in namespace courier; mail in flight is retried rather than lost"},
		},
	},
	{
		Name:      "parlor",
		Version:   "0.1.0",
		Kind:      KindChart,
		Namespace: "parlor",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/manifests/applications/parlor.yaml:13",
		Blast: Blast{
			Apply:    "move the parlor Application's targetRevision",
			Restarts: []string{"parlor's pods in namespace parlor"},
		},
	},
	{
		Name:      "alloy",
		Version:   "1.13.0",
		Kind:      KindChart,
		Namespace: "monitoring",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/manifests/applications/alloy.yaml:13",
		Blast: Blast{
			Apply:    "move the alloy Application's targetRevision",
			Restarts: []string{"alloy's pods in namespace monitoring; the log pipeline pauses and resumes"},
		},
	},
	{
		Name:      "grafana",
		Version:   "10.5.15",
		Kind:      KindChart,
		Namespace: "monitoring",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/manifests/applications/grafana.yaml:13",
		Blast: Blast{
			Apply:    "move the grafana Application's targetRevision",
			Restarts: []string{"grafana's pods in namespace monitoring; dashboards are stored outside them"},
		},
	},
	{
		Name:      "loki",
		Version:   "7.3.0",
		Kind:      KindChart,
		Namespace: "monitoring",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/manifests/applications/loki.yaml:13",
		Blast: Blast{
			Apply:    "move the loki Application's targetRevision",
			Restarts: []string{"loki's pods in namespace monitoring, which is where the box's own logs live"},
		},
	},
	{
		Name:      "prometheus",
		Version:   "29.36.0",
		Kind:      KindChart,
		Namespace: "monitoring",
		Pinned:    true,
		Declared:  true,
		Source:    "kit/templates/substrate/manifests/applications/prometheus.yaml:13",
		Blast: Blast{
			Apply:    "move the prometheus Application's targetRevision",
			Restarts: []string{"prometheus's pods in namespace monitoring; the metrics it has scraped stay in its volume"},
		},
	},
	{
		Name:      "postgres",
		Version:   "",
		Kind:      KindImage,
		Namespace: "data",
		Pinned:    false,
		Declared:  true,
		Source:    "kit/templates/substrate/manifests/data/postgres-cluster.yaml:10",
		Blast: Blast{
			Apply:    "set spec.imageName on the CNPG Cluster, which today is deliberately unset so the operator's own default applies",
			Restarts: []string{"the Postgres cluster in namespace data — a rolling restart of the primary and each replica, and every service that holds a connection to it"},
		},
	},
	{
		Name:      "traefik",
		Version:   "",
		Kind:      KindAddon,
		Namespace: "kube-system",
		Pinned:    false,
		Declared:  true,
		Source:    "kit/templates/substrate/install.sh:108",
		Blast: Blast{
			Apply:    "it ships with the k3s release rather than being pinned, so the fix arrives with the node agent",
			Restarts: []string{"traefik in namespace kube-system, which is the ingress path for every service on the box"},
		},
	},
}

// Observer is the seam that reads a box. One method, because that is the whole
// of what the inventory needs from the world, and an interface for the reason
// internal/boxd's World is one: a test that needed a cluster to prove the
// classification would not be run.
//
// The implementation that satisfies it on a real box is kubectl(1) — the
// substrate is a single-node k3s cluster and kubectl is the one tool on it
// that can answer "what images are running and with what tags" — and it runs
// under a deadline the caller sets, because a box that has stopped answering
// is a box that has stopped answering.
type Observer interface {
	Observe(ctx context.Context, e Emitter) ([]Component, error)
}

// Inventory is the answer to "what is on this box": the declared catalogue,
// what observation produced, and the two divergences that matter.
//
// The two lists are kept apart on purpose. Folding them into one list of
// components would hide the two facts this package exists to surface — a pin
// nobody applied, and an applied version nobody pinned — and both of those
// are security facts rather than tidiness facts. A box running a version the
// substrate does not declare is a box somebody changed by hand, which is
// exactly what a CVE report is read to find out.
type Inventory struct {
	// Box is the enrolled identity of the box this is about.
	Box string
	// Declared is the substrate catalogue, verbatim.
	Declared []Component
	// Observed is what the observer read. Empty when nothing has been
	// observed, which is a fact about the run rather than an empty box.
	Observed []Component
	// Divergences names every place the two disagree, in the order they were
	// found, each naming the component and both versions.
	Divergences []Divergence
}

// Divergence is one disagreement between what the substrate declares and what
// the box is running.
type Divergence struct {
	// Component is the name both sides are talking about.
	Component string
	// Declared is the catalogue's version. Empty when the box is running
	// something the catalogue does not name at all.
	Declared string
	// Observed is the version the box reported. Empty when the observer read
	// no version, which is reported rather than silently matched against the
	// catalogue's pin.
	Observed string
	// Why is the sentence a reader acts on.
	Why string
}

// Compare fills in the divergences between the catalogue and what was
// observed.
//
// It matches by name, not by version, and it says why each mismatch matters
// rather than only that there is one:
//
//   - A catalogue entry nobody observed is a component the substrate installs
//     that this box is not running. That is either a box mid-install or a box
//     somebody took apart, and the report does not guess which.
//   - An observed component nobody declared is reported as a divergence with
//     no declared version: it is a thing on the box with a version and no
//     story about where it came from.
//   - The same component at two versions is reported with both spellings,
//     because `v1.37.1+k3s1` and `1.37.1` are the same node agent and a
//     string comparison would call that a divergence that isn't.
//
// An empty observation returns no divergences rather than a divergence per
// catalogue entry: "nobody looked" is not "everything is missing", and a
// report that said the box had lost sixteen components when the observer
// never ran would be the loudest wrong answer in the package.
func (inv *Inventory) Compare() []Divergence {
	inv.Divergences = nil
	if len(inv.Observed) == 0 {
		return inv.Divergences
	}
	observed := make(map[string]Component, len(inv.Observed))
	for _, c := range inv.Observed {
		observed[c.Name] = c
	}
	for _, want := range inv.Declared {
		got, ok := observed[want.Name]
		if !ok {
			inv.Divergences = append(inv.Divergences, Divergence{
				Component: want.Name,
				Declared:  want.Version,
				Why: "the substrate installs this and the observation did not find it — a box mid-install, " +
					"or a box somebody removed part of",
			})
			continue
		}
		if !sameVersion(want.Version, got.Version) {
			inv.Divergences = append(inv.Divergences, Divergence{
				Component: want.Name,
				Declared:  want.Version,
				Observed:  got.Version,
				Why:       "the box is running a version the substrate does not pin — somebody changed it by hand",
			})
		}
	}
	declared := make(map[string]bool, len(inv.Declared))
	for _, want := range inv.Declared {
		declared[want.Name] = true
	}
	for _, got := range inv.Observed {
		if !declared[got.Name] {
			inv.Divergences = append(inv.Divergences, Divergence{
				Component: got.Name,
				Observed:  got.Version,
				Why:       "running on the box and not in the substrate catalogue — nobody has said what installed it",
			})
		}
	}
	return inv.Divergences
}

// Components is the inventory as one flat list: everything declared, with
// anything observed that the catalogue does not name appended.
//
// This is the list the matcher runs over, and it is deliberately the union: an
// advisory against a component nobody declared is still an advisory against a
// thing running on the box, and the catalogue's job is to characterise the
// substrate, not to set the scope of what can be vulnerable.
func (inv *Inventory) Components() []Component {
	out := make([]Component, 0, len(inv.Declared)+len(inv.Observed))
	out = append(out, inv.Declared...)
	known := make(map[string]bool, len(inv.Declared))
	for _, c := range inv.Declared {
		known[c.Name] = true
	}
	for _, c := range inv.Observed {
		if !known[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// sameVersion reports whether two spellings name the same version.
//
// The substrate's node agent is pinned `v1.37.1+k3s1` and a box reports its
// own version the same way, so this is the identity comparison for the common
// case. It is not a general semver equality: two versions that normalise to
// the same three numbers but differ in their build metadata are different
// builds, and the report would rather over-report a divergence than
// under-report one. What it does do is treat a leading `v` and the `+k3s1`
// suffix the substrate itself writes as the same spelling, because that is
// one fact written two ways rather than two facts.
func sameVersion(declared, observed string) bool {
	if declared == observed {
		return true
	}
	a, aok := comparable(declared)
	b, bok := comparable(observed)
	return aok && bok && a == b
}

// comparable is the three-integer form of a version as the substrate writes
// it: a leading `v` comes off, a `+build` suffix comes off, and anything else
// that is not three numbers fails rather than being coerced.
//
// It fails honestly, and the caller says so. A version caf cannot compare is
// reported as an unknown classification — "we do not know whether this is
// affected, because we could not read the version" — rather than as
// unaffected, which is the one wrong answer that gets a box left unpatched.
func comparable(version string) (string, bool) {
	text := strings.TrimSpace(version)
	if text == "" {
		return "", false
	}
	if i := strings.IndexByte(text, '+'); i >= 0 {
		text = text[:i]
	}
	text = strings.TrimPrefix(text, "v")
	parts := strings.Split(text, ".")
	if len(parts) != 3 {
		return "", false
	}
	for _, part := range parts {
		if part == "" {
			return "", false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return "", false
			}
		}
	}
	return text, true
}

// compareVersions orders two versions by their numeric components, so 10.0.0
// is newer than 9.0.0 — which a string comparison gets backwards. It returns
// 0 for two versions it cannot order, and the classifier treats "cannot
// order" as unknown rather than as equal.
func compareVersions(a, b string) int {
	x, xok := comparable(a)
	y, yok := comparable(b)
	if !xok || !yok {
		return 0
	}
	xs := strings.Split(x, ".")
	ys := strings.Split(y, ".")
	for i := range xs {
		xi, _ := strconv.Atoi(xs[i])
		yi, _ := strconv.Atoi(ys[i])
		switch {
		case xi < yi:
			return -1
		case xi > yi:
			return 1
		}
	}
	return 0
}

// sortedByName returns the components in a stable order, so two runs over one
// box produce the same report. Same argument as caf.lock's no-timestamp rule:
// a diff between two reports has to be a diff between two boxes.
func sortedByName(in []Component) []Component {
	out := make([]Component, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// known reports whether the substrate catalogue names this component.
func known(name string) bool {
	for _, c := range Substrate {
		if c.Name == name {
			return true
		}
	}
	return false
}

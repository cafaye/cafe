package gen

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/cafaye/cafe/internal/contract"
	"github.com/cafaye/cafe/internal/dev"
)

// The paths one `caf gen chart` run writes, relative to the output root.
//
// They are constants rather than strings at the use sites for the reason
// telemetry's are: every path appears in the printed report, in the refusal
// sentences and in the tests, and a path spelled four ways is a path one of
// them will get wrong.
//
// The layout is the chart's own — Chart.yaml at the root, values.yaml beside
// it, templates/ under that — because that is the shape `helm template`
// expects. A generated artifact whose layout the tool that consumes it does
// not recognise is a generator bug no test would catch.
const (
	chartYAMLPath       = "chart/Chart.yaml"
	chartValuesPath     = "chart/values.yaml"
	chartDeploymentPath = "chart/templates/deployment.yaml"
	chartServicePath    = "chart/templates/service.yaml"
	chartIngressPath    = "chart/templates/ingress.yaml"
	chartReadmePath     = "chart/README.md"
)

// chartVersion is the chart's own version and the default image tag.
//
// It is a constant rather than a manifest fact because the manifest carries
// no version of the service — `core` is a constraint on the core contract,
// which is a different thing — and a chart with no version is a chart `helm`
// refuses to package. The tag defaults to it so that the default deploy runs
// the chart's own version, and a release bumps both by editing values.yaml.
const chartVersion = "0.1.0"

// readyzPath is the fleet's readiness path. It is a constant rather than a
// value for the same reason `namespace` is in telemetry.go: no manifest field
// carries it and no schema states it — the fleet's services serve it, the
// substrate's rollout waits on it, and that is the whole of the evidence.
//
// It is baked into the template rather than left in values.yaml because a
// readiness path that can be edited is a readiness path that can be pointed at
// /up: the defect kit's kamal template carried, which produced deploys that
// could never go green and which two services hand-patched around. /healthz is
// the other wrong answer — it answers "the process is up" before the service
// can serve, so a rollout waiting on it hands traffic to a container that
// refuses it.
const readyzPath = "/readyz"

// chart is `caf gen chart`: the pure function.
//
// Six files, and the split between them is the substance of the target:
//
//   - Chart.yaml and values.yaml, the chart's identity and its deployment-time
//     half — the facts a person changes per deployment, as opposed to the
//     facts about the service that only a regeneration changes;
//   - the Deployment, the Service and the Ingress, which are the contract
//     and are not meant to be edited by hand;
//   - the README, because the loop the chart exists for — push, pull, sync —
//     is three commands and a habit, and a habit nobody wrote down is a habit
//     the next person has to rediscover.
//
// The ArgoCD Application is deliberately NOT among them, and the reason is
// load-bearing rather than an omission: the substrate owns the Application —
// one static one per service, OCI-sourced, applied by install.sh — and a
// service repository commits none. A generated Application would be a second
// one of the same name in the same namespace with a different source, and
// the later `kubectl apply` wins: a deploy whose target depends on apply
// order is not a deploy. The decision is recorded in moon/DEPLOYMENT.md,
// "Who owns the Application".
//
// The Ingress is the one contract file that is gated on a value: it is
// always emitted, and renders only when `ingress.enabled` is true. How a
// service is reached is a per-deployment fact — which host — and which
// services are exposed is a per-deployment decision, so the document lives
// in the chart behind the knob rather than in the substrate. Off by default:
// the substrate's first sync happens before any operator has set a host, and
// a chart that suddenly routes traffic is a defect.
//
// What is derived from the manifest, and what is not:
//
//   - the service name, the chart name, the container port (from the language,
//     through the same table `caf dev` uses, so the two cannot disagree), and
//     the namespace;
//   - the image repository, from the service name and the fleet's registry
//     convention — DEPLOYMENT.md: charts ship from a private OCI registry
//     (GHCR), because that is how closed-source charts ride a pull-based
//     system;
//   - the image tag, the env, the resources and the replica count, which the
//     manifest does not carry and which are values with stated defaults
//     rather than guesses baked into the template.
func chart(manifest contract.Manifest, env map[string]string) (Output, error) {
	language := manifest.Language()
	if !slicesContains(supportedLanguages("chart"), language) {
		return refuses("chart", manifest, chartLanguageNote)
	}
	if !manifest.ServesHTTP() {
		return Output{}, fmt.Errorf("caf gen chart: %w: this manifest declares no HTTP surface — `exposes.api` "+
			"is absent — so there is nothing to put behind a Service and a %s probe. caf gen chart writes "+
			"the deploy artifact for a service that serves HTTP; a library or a specification repository "+
			"has nothing to deploy", ErrNoHTTPSurface, readyzPath)
	}

	port, err := dev.LanguagePort(language)
	if err != nil {
		// Unreachable through `caf gen chart` — the language check above reads
		// the same table — and reported rather than skipped because a port
		// nobody can name is a generator bug, not a fact about the project.
		return Output{}, fmt.Errorf("caf gen chart: %w", err)
	}

	files := []File{
		{Path: chartYAMLPath, Content: renderChartYAML(manifest)},
		{Path: chartValuesPath, Content: renderChartValues(manifest, env)},
		{Path: chartDeploymentPath, Content: renderDeployment(manifest, port)},
		{Path: chartServicePath, Content: renderService(manifest)},
		{Path: chartIngressPath, Content: renderIngress(manifest)},
		{Path: chartReadmePath, Content: renderChartReadme(manifest)},
	}

	slices.SortStableFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return Output{Target: "chart", Files: files}, nil
}

// chartLanguageNote is the guidance refuses() gives when a manifest's
// language is one `caf gen chart` does not generate for.
const chartLanguageNote = "a specification repository runs nothing, so there is no container to deploy"

// renderChartYAML writes Chart.yaml: the chart's identity.
func renderChartYAML(manifest contract.Manifest) []byte {
	var w writer

	w.text("# Code generated by `caf gen chart`. DO NOT EDIT.\n")
	w.text("#\n")
	w.text("# The chart's identity. The name is the manifest's `name`, which is also the\n")
	w.text("# repository name and the service's namespace — one name, because a chart\n")
	w.text("# named after anything but the service it deploys is a chart somebody\n")
	w.text("# has to go and look up.\n")
	w.text("apiVersion: v2\n")
	w.text("name: ")
	w.at("%s", manifest.ServiceName())
	w.text("\ntype: application\n")
	w.text("version: ")
	w.at("%s", chartVersion)
	w.text("\nappVersion: ")
	w.at("%q", chartVersion)
	if description := oneLine(manifest.Description()); description != "" {
		w.text("\ndescription: ")
		w.at("%s", description)
	}
	w.text("\n")

	return w.bytes()
}

// renderChartValues writes values.yaml: the deployment-time half.
//
// The header says this file is meant to be edited, and that is the one place
// the chart breaks the "DO NOT EDIT" rule every other generated file follows.
// DEPLOYMENT.md's loop is "a values commit, then wait for sync" — the values
// commit is the only write path into the cluster, and this is the file it
// edits. The templates are the contract; this is the knob.
func renderChartValues(manifest contract.Manifest, env map[string]string) []byte {
	name := manifest.ServiceName()

	var w writer

	w.text("# Generated by `caf gen chart` from this project's cafaye.yml.\n")
	w.text("#\n")
	w.text("# This is the chart's DEPLOYMENT-TIME half, and it is the one file in the\n")
	w.text("# chart meant to be edited: the values commit is the write path of the\n")
	w.text("# pull-based loop (DEPLOYMENT.md — \"a values commit, then wait for\n")
	w.text("# sync\"), and this is the file that commit edits. The templates are the\n")
	w.text("# contract and are not. Re-running `caf gen chart` refuses to overwrite\n")
	w.text("# this file without -force, so an edit is never lost to an accident.\n")
	w.text("#\n")
	w.text("# The defaults below are derived from the manifest where the manifest\n")
	w.text("# carries the fact, and stated here where it does not.\n")
	w.text("\n")

	w.text("# The image the service runs.\n")
	w.text("#\n")
	w.text("# The repository is the fleet's private OCI registry (GHCR), because the\n")
	w.text("# substrate is closed source and a pull-based system needs somewhere to\n")
	w.text("# pull from. The tag is a VALUE and is never `latest`: a floating tag\n")
	w.text("# is a deploy that changes when somebody else pushes, and the loop\n")
	w.text("# exists so that what runs is exactly what the values commit said.\n")
	w.text("# Set it at release time; the default is the chart's own version.\n")
	w.text("image:\n")
	w.text("  repository: ghcr.io/cafaye/")
	w.at("%s", name)
	w.text("\n  tag: ")
	w.at("%q", chartVersion)
	w.text("\n  pullPolicy: IfNotPresent\n")
	w.text("\n")

	w.text("# The read-only pull secret on the box, created once, out of band:\n")
	w.text("#\n")
	w.text("#   kubectl create secret docker-registry ")
	w.at("%s", name)
	w.text("-ghcr \\\n")
	w.text("#     --docker-server=ghcr.io \\\n")
	w.text("#     --docker-username=<deploy-bot> \\\n")
	w.text("#     --docker-password=<read-only-token>\n")
	w.text("#\n")
	w.text("# The chart NAMES the secret and never contains a credential. A\n")
	w.text("# credential that reaches this file is a credential in a git\n")
	w.text("# repository, which is the one thing the pull-based loop exists to\n")
	w.text("# prevent.\n")
	w.text("imagePullSecrets: ")
	w.at("%s", name)
	w.text("-ghcr\n")
	w.text("\n")

	w.text("# The secret holding the service's environment, also created on the box\n")
	w.text("# and also by name only. The Deployment reads it with envFrom, so a\n")
	w.text("# value is rotated by replacing the secret and restarting the pods —\n")
	w.text("# not by editing a generated file.\n")
	w.text("secretName: ")
	w.at("%s", name)
	w.text("-secrets\n")
	w.text("\n")

	w.text("# How many copies of the service run. One is the default for a service\n")
	w.text("# whose state is the cluster's, not the pod's; raise it when the\n")
	w.text("# service scales horizontally.\n")
	w.text("replicas: 1\n")
	w.text("\n")

	w.text("# The Service in front of the container.\n")
	w.text("service:\n")
	w.text("  type: ClusterIP\n")
	w.text("  port: 80\n")
	w.text("\n")

	w.text("# How the service is reached from outside the cluster: an Ingress,\n")
	w.text("# gated on `ingress.enabled`, which is FALSE by default.\n")
	w.text("#\n")
	w.text("# A chart that suddenly routes traffic is a defect: the substrate's\n")
	w.text("# first sync happens before any operator has set a host, and an\n")
	w.text("# Ingress with no host is a silent misroute — the class of defect\n")
	w.text("# kit's /up healthcheck was. The services the fleet exposes (gateway,\n")
	w.text("# parlor) turn the knob on in their values commit; identity and\n")
	w.text("# courier stay internal.\n")
	w.text("#\n")
	w.text("# The edge is Traefik, k3s's default ingress controller, with\n")
	w.text("# cert-manager in front of it (DEPLOYMENT.md:28 — \"Ingress + TLS |\n")
	w.text("# Traefik (k3s default) + cert-manager | declarative, no edge\n")
	w.text("# box\"). The ClusterIssuer is created ONCE PER CLUSTER by the\n")
	w.text("# substrate (a follow-up, not this packet); this block only NAMES\n")
	w.text("# it — the same \"names the secret and never contains a credential\"\n")
	w.text("# discipline the imagePullSecrets block uses. Empty means no TLS: a\n")
	w.text("# secret nothing writes.\n")
	w.text("ingress:\n")
	w.text("  enabled: false\n")
	w.text("  className: traefik\n")
	w.text("  host: \"\"\n")
	w.text("  clusterIssuer: \"\"\n")
	w.text("  tls: true\n")
	w.text("  path: /\n")
	w.text("  pathType: Prefix\n")
	w.text("\n")

	w.text("# The service's environment, as NAME: value pairs.\n")
	w.text("#\n")
	w.text("# The manifest carries no env — a service's configuration is its own,\n")
	w.text("# and caf will not guess it — so this starts empty and is filled per\n")
	w.text("# deployment, or at generation time with repeated -env flags:\n")
	w.text("#\n")
	w.text("#   caf gen chart -env DATABASE_URL=... -env API_KEY=...\n")
	if len(env) == 0 {
		w.text("env: {}\n")
	} else {
		w.text("env:\n")
		for _, key := range sortedKeys(env) {
			w.text("  ")
			w.at("%s", key)
			w.text(": ")
			w.at("%q", env[key])
			w.text("\n")
		}
	}
	w.text("\n")

	w.text("# What the container is allowed to ask for and keep. Requests are what\n")
	w.text("# the scheduler places the pod on the basis of; limits are what it is\n")
	w.text("# killed at. These are starting points for a small service on a shared\n")
	w.text("# box, not a measurement — adjust them to the service's real footprint.\n")
	w.text("resources:\n")
	w.text("  requests:\n")
	w.text("    cpu: 100m\n")
	w.text("    memory: 128Mi\n")
	w.text("  limits:\n")
	w.text("    cpu: 500m\n")
	w.text("    memory: 512Mi\n")
	w.text("\n")

	w.text("# The readiness probe's timing. The PATH is not here: it is /readyz,\n")
	w.text("# the fleet's contract, baked into the template — see the chart README\n")
	w.text("# for why a readiness path that can be edited is a defect waiting.\n")
	w.text("readinessProbe:\n")
	w.text("  initialDelaySeconds: 5\n")
	w.text("  periodSeconds: 10\n")
	w.text("\n")

	w.text("# The namespace the service lands in. One service per namespace, so the\n")
	w.text("# namespace is the unit Argo syncs and the unit a rollback reverts.\n")
	w.text("namespace: ")
	w.at("%s", name)
	w.text("\n")

	return w.bytes()
}

// renderDeployment writes the Deployment: the contract half of the chart.
func renderDeployment(manifest contract.Manifest, port int) []byte {
	name := manifest.ServiceName()

	var w writer

	w.text("# Code generated by `caf gen chart`. DO NOT EDIT.\n")
	w.text("#\n")
	w.text("# The Deployment: the service's image, its container port, its\n")
	w.text("# environment and its readiness probe. The probe's path is the fleet's\n")
	w.text("# contract (/readyz — see the chart README for why it is not a value);\n")
	w.text("# the port is the language's, read from the same table `caf dev`\n")
	w.text("# uses. Edit by changing the manifest and regenerating.\n")
	w.text("apiVersion: apps/v1\n")
	w.text("kind: Deployment\n")
	w.text("metadata:\n")
	w.text("  name: ")
	w.at("%s", name)
	w.text("\n  labels:\n")
	w.text("    app.kubernetes.io/name: ")
	w.at("%s", name)
	w.text("\n    app.kubernetes.io/instance: ")
	w.at("%s", name)
	w.text("\n    app.kubernetes.io/managed-by: caf\n")
	w.text("spec:\n")
	w.text("  replicas: {{ .Values.replicas }}\n")
	w.text("  selector:\n")
	w.text("    matchLabels:\n")
	w.text("      app.kubernetes.io/name: ")
	w.at("%s", name)
	w.text("\n      app.kubernetes.io/instance: ")
	w.at("%s", name)
	w.text("\n")
	w.text("  template:\n")
	w.text("    metadata:\n")
	w.text("      labels:\n")
	w.text("        app.kubernetes.io/name: ")
	w.at("%s", name)
	w.text("\n        app.kubernetes.io/instance: ")
	w.at("%s", name)
	w.text("\n        app.kubernetes.io/managed-by: caf\n")
	w.text("    spec:\n")
	w.text("      imagePullSecrets:\n")
	w.text("        - name: {{ .Values.imagePullSecrets }}\n")
	w.text("      containers:\n")
	w.text("        - name: ")
	w.at("%s", name)
	w.text("\n")
	w.text("          image: \"{{ .Values.image.repository }}:{{ .Values.image.tag }}\"\n")
	w.text("          imagePullPolicy: {{ .Values.image.pullPolicy }}\n")
	w.text("          ports:\n")
	w.text("            - name: http\n")
	w.text("              containerPort: ")
	w.at("%d", port)
	w.text("\n              protocol: TCP\n")
	w.text("          envFrom:\n")
	w.text("            - secretRef:\n")
	w.text("                name: {{ .Values.secretName }}\n")
	w.text("          env:\n")
	w.text("            {{- range $name, $value := .Values.env }}\n")
	w.text("            - name: {{ $name }}\n")
	w.text("              value: {{ $value | quote }}\n")
	w.text("            {{- end }}\n")
	w.text("          readinessProbe:\n")
	w.text("            httpGet:\n")
	w.text("              path: /readyz\n")
	w.text("              port: http\n")
	w.text("            initialDelaySeconds: {{ .Values.readinessProbe.initialDelaySeconds }}\n")
	w.text("            periodSeconds: {{ .Values.readinessProbe.periodSeconds }}\n")
	w.text("          resources:\n")
	w.text("            requests:\n")
	w.text("              cpu: {{ .Values.resources.requests.cpu }}\n")
	w.text("              memory: {{ .Values.resources.requests.memory }}\n")
	w.text("            limits:\n")
	w.text("              cpu: {{ .Values.resources.limits.cpu }}\n")
	w.text("              memory: {{ .Values.resources.limits.memory }}\n")

	return w.bytes()
}

// renderService writes the Service in front of the container.
func renderService(manifest contract.Manifest) []byte {
	name := manifest.ServiceName()

	var w writer

	w.text("# Code generated by `caf gen chart`. DO NOT EDIT.\n")
	w.text("#\n")
	w.text("# The Service in front of the container. ClusterIP, because the service\n")
	w.text("# is reached through the cluster's ingress rather than directly; the\n")
	w.text("# targetPort is the container's named `http` port, so the container\n")
	w.text("# port and this document cannot disagree.\n")
	w.text("apiVersion: v1\n")
	w.text("kind: Service\n")
	w.text("metadata:\n")
	w.text("  name: ")
	w.at("%s", name)
	w.text("\n  labels:\n")
	w.text("    app.kubernetes.io/name: ")
	w.at("%s", name)
	w.text("\n    app.kubernetes.io/instance: ")
	w.at("%s", name)
	w.text("\n    app.kubernetes.io/managed-by: caf\n")
	w.text("spec:\n")
	w.text("  type: {{ .Values.service.type }}\n")
	w.text("  ports:\n")
	w.text("    - name: http\n")
	w.text("      port: {{ .Values.service.port }}\n")
	w.text("      targetPort: http\n")
	w.text("      protocol: TCP\n")
	w.text("  selector:\n")
	w.text("    app.kubernetes.io/name: ")
	w.at("%s", name)
	w.text("\n    app.kubernetes.io/instance: ")
	w.at("%s", name)
	w.text("\n")

	return w.bytes()
}

// renderIngress writes the Ingress: how the service is reached.
//
// The whole document is inside `{{- if .Values.ingress.enabled }}`, so with
// the knob off — the default — `helm template` emits no Ingress at all.
//
// The guard is helm's `fail`, and the asymmetry it creates is the point: an
// unconfigured chart RENDERS (the substrate's first sync happens before any
// operator has set a host), but an enabled chart with no host does NOT — an
// Ingress with no host is a silent misroute, which is the class of defect
// kit's /up healthcheck was.
//
// The backend port is `.Values.service.port`, the Service's own port, so the
// Ingress and the Service cannot disagree about it. The cluster-issuer
// annotation is emitted only when the name is non-empty, because an empty
// annotation is an annotation cert-manager reads as a request it cannot
// serve; the tls block only when tls is true AND the issuer is set, because
// TLS without an issuer is a secret nothing writes. The issuer itself is
// never created here — it is cluster-scoped, created once per cluster by
// the substrate, and the chart only names it.
func renderIngress(manifest contract.Manifest) []byte {
	name := manifest.ServiceName()

	var w writer

	w.text("# Code generated by `caf gen chart`. DO NOT EDIT.\n")
	w.text("#\n")
	w.text("# The Ingress: how the service is reached from outside the cluster.\n")
	w.text("# Off by default and gated on `ingress.enabled` in values.yaml — see\n")
	w.text("# that file for why it is off, and for the issuer the chart names\n")
	w.text("# and never creates.\n")
	w.text("{{- if .Values.ingress.enabled }}\n")
	w.text("{{- if not .Values.ingress.host }}\n")
	w.text("{{- fail \"caf gen chart: ingress.enabled is true and ingress.host is empty — an Ingress with no " +
		"host is a silent misroute; set ingress.host in values.yaml\" }}\n")
	w.text("{{- end }}\n")
	w.text("apiVersion: networking.k8s.io/v1\n")
	w.text("kind: Ingress\n")
	w.text("metadata:\n")
	w.text("  name: ")
	w.at("%s", name)
	w.text("\n  labels:\n")
	w.text("    app.kubernetes.io/name: ")
	w.at("%s", name)
	w.text("\n    app.kubernetes.io/instance: ")
	w.at("%s", name)
	w.text("\n    app.kubernetes.io/managed-by: caf\n")
	w.text("  {{- if .Values.ingress.clusterIssuer }}\n")
	w.text("  annotations:\n")
	w.text("    cert-manager.io/cluster-issuer: {{ .Values.ingress.clusterIssuer }}\n")
	w.text("  {{- end }}\n")
	w.text("\n")
	w.text("spec:\n")
	w.text("  ingressClassName: {{ .Values.ingress.className }}\n")
	w.text("  {{- if and .Values.ingress.tls .Values.ingress.clusterIssuer }}\n")
	w.text("  tls:\n")
	w.text("    - hosts:\n")
	w.text("        - {{ .Values.ingress.host }}\n")
	w.text("      secretName: ")
	w.at("%s", name)
	w.text("-tls\n")
	w.text("  {{- end }}\n")
	w.text("  rules:\n")
	w.text("    - host: {{ .Values.ingress.host }}\n")
	w.text("      http:\n")
	w.text("        paths:\n")
	w.text("          - path: {{ .Values.ingress.path }}\n")
	w.text("            pathType: {{ .Values.ingress.pathType }}\n")
	w.text("            backend:\n")
	w.text("              service:\n")
	w.text("                name: ")
	w.at("%s", name)
	w.text("\n")
	w.text("                port:\n")
	w.text("                  number: {{ .Values.service.port }}\n")
	w.text("{{- end }}\n")

	return w.bytes()
}

// renderChartReadme writes the chart's README: the loop, the push step, the
// secrets and the readiness contract, in the order a person needs them.
func renderChartReadme(manifest contract.Manifest) []byte {
	name := manifest.ServiceName()

	var w writer

	w.text("# Generated by `caf gen chart` from this project's cafaye.yml.\n")
	w.text("\n")
	w.text("# ")
	w.at("%s", name)
	w.text("\n")
	w.text("\n")
	w.text("The Helm chart ")
	w.at("%s", name)
	w.text(" is deployed from. The templates are the\n")
	w.text("contract; `values.yaml` is the deployment-time half — the file the\n")
	w.text("values commit edits.\n")
	w.text("\n")
	w.text("## The loop\n")
	w.text("\n")
	w.text("The chart is deployed by ArgoCD, pull-only, and the Application is\n")
	w.text("not this chart's: the substrate owns it — one static Application\n")
	w.text("per service, OCI-sourced, applied by install.sh (moon/DEPLOYMENT.md,\n")
	w.text("\"Who owns the Application\"). A service repository commits none.\n")
	w.text("The only thing that can change the cluster is a commit: push a\n")
	w.text("values commit, Argo syncs, the box agrees with git. A hand edit\n")
	w.text("self-heals; rollback is `git revert`.\n")
	w.text("\n")
	w.text("## Shipping the chart\n")
	w.text("\n")
	w.text("The chart ships from the fleet's private OCI registry, because the\n")
	w.text("substrate is closed source and a pull-based system needs somewhere to\n")
	w.text("pull from:\n")
	w.text("\n")
	w.text("    helm package chart\n")
	w.text("    helm push chart-")
	w.at("%s", chartVersion)
	w.text(".tgz oci://ghcr.io/cafaye/charts/")
	w.at("%s", name)
	w.text("\n")
	w.text("\n")
	w.text("The box pulls it with the read-only secret `values.yaml` names\n")
	w.text("(`imagePullSecrets`), created on the box once, out of band. The chart\n")
	w.text("names the secret and never contains a credential.\n")
	w.text("\n")
	w.text("## Secrets\n")
	w.text("\n")
	w.text("The service's environment arrives by reference: the Deployment reads\n")
	w.text("the secret `values.yaml` names (`secretName`) with `envFrom`. Create\n")
	w.text("it on the box:\n")
	w.text("\n")
	w.text("    kubectl create secret generic ")
	w.at("%s", name)
	w.text("-secrets --from-literal=...\n")
	w.text("\n")
	w.text("Rotate a value by replacing the secret and restarting the pods — not\n")
	w.text("by editing a generated file.\n")
	w.text("\n")
	w.text("## Reaching the service\n")
	w.text("\n")
	w.text("The chart installs no Ingress until a values commit says so:\n")
	w.text("`ingress.enabled` is `false`, and with it false `helm template` emits\n")
	w.text("no Ingress at all — a chart that suddenly routes traffic is a\n")
	w.text("defect. The services the fleet exposes (gateway, parlor) turn it\n")
	w.text("on in their values commit:\n")
	w.text("\n")
	w.text("    ingress:\n")
	w.text("      enabled: true\n")
	w.text("      host: gateway.example.com\n")
	w.text("\n")
	w.text("`className` is traefik, k3s's default (DEPLOYMENT.md:28). Set\n")
	w.text("`clusterIssuer` to a cert-manager ClusterIssuer — created once per\n")
	w.text("cluster by the substrate, not by the chart — and `tls: true` writes\n")
	w.text("the `spec.tls` block and cert-manager provisions the certificate.\n")
	w.text("Without an issuer there is no TLS: a secret nothing writes.\n")
	w.text("\n")
	w.text("## Readiness\n")
	w.text("\n")
	w.text("The probe is `/readyz`. That is the fleet's contract: every cafaye\n")
	w.text("service serves it, and the rollout waits on it. It is baked into the\n")
	w.text("template rather than left as a value, because a readiness path that\n")
	w.text("can be edited is a readiness path that can be pointed at `/up` — the\n")
	w.text("defect the kamal template had, which produced deploys that could\n")
	w.text("never go green.\n")

	return w.bytes()
}

// oneLine collapses a description to a single line, because Chart.yaml's
// description is one line and a newline inside it would be a parse error
// rather than a long line.
func oneLine(description string) string {
	return strings.Join(strings.Fields(description), " ")
}

// sortedKeys is the env map's keys in name order, because a map iterates in
// an order that changes between runs and a generated file that reshuffles
// between two runs of one manifest is a file nobody commits.
func sortedKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for name := range env {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

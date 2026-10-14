package gen

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
	"text/template"

	"github.com/cafaye/cafe/internal/dev"
	"github.com/goccy/go-yaml"
)

// Tests for `cafe gen chart`.
//
// The thing being defended here is the content of the chart, not the existence
// of its files: a test that asserted "templates/ is non-empty" would pass
// against a chart that could never render, and an unrendered chart is the
// defect this target exists to prevent. So each test below names a property
// the substrate depends on — the readiness path, the secret-by-reference
// rule, the never-`latest` tag — and asserts the bytes that carry it.
//
// The render proof is in-process rather than absent, and that is a change of
// means rather than a weakening. The template is Go's text/template and the
// values are YAML, so `TestTheIngressRendersFromValues` executes the
// generated ingress.yaml against the chart's own values.yaml and unmarshals
// the result — no binary, on every gate. What it cannot prove is that helm
// agrees: `helm template` and `helm lint` stay in the packet's report, and
// the in-process render is the half that runs everywhere.

// chartManifest is a minimal cafaye.yml with an HTTP surface, which is the
// kind of project `cafe gen chart` generates for.
//
// Written as a literal rather than read from this repository's own manifest so
// a change to cafe's `cafaye.yml` cannot quietly move every table in this file.
// cafe's own manifest declares no `exposes` at all — it is a binary, not a
// service — which is itself one of the refusal cases below.
const chartManifest = `name: courier
description: Transactional email and push delivery.
language: go
core: ^0.2.0

exposes:
  api: openapi/openapi.yaml
  events:
    - courier.email.queued

repository:
  url: git@github.com:cafaye/courier.git
  defaultBranch: master
  visibility: public

owner:
  team: courier
  contact: courier@cafaye.com
`

// generateChart runs the chart target over one manifest, because a table that
// called the renderers directly would not notice that `chart` refuses
// something the contract accepts.
func generateChart(t *testing.T, document string, env map[string]string) Output {
	t.Helper()

	out, err := Generate("chart", loadManifest(t, document), TargetOptions{Env: env})
	if err != nil {
		t.Fatalf("cafe gen chart: %v", err)
	}
	return out
}

// findChartFile returns one generated file, and fails the test if it is not
// there — because a test that found nothing and compared nothing is a test
// that passed for the wrong reason.
func findChartFile(t *testing.T, out Output, path string) File {
	t.Helper()

	for _, file := range out.Files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("%s is not among the generated files (%v)", path, out.Paths())
	return File{}
}

// The six files, every time. A generator that emits a seventh is not refused
// here — that is a review decision — but a generator that emits a DIFFERENT SET
// between two runs over one manifest is, and so is one whose order drifts.
func TestTheChartFileSetIsStableAndSorted(t *testing.T) {
	first := generateChart(t, chartManifest, nil)
	second := generateChart(t, chartManifest, nil)

	want := []string{
		"chart/Chart.yaml",
		"chart/README.md",
		"chart/templates/deployment.yaml",
		"chart/templates/ingress.yaml",
		"chart/templates/service.yaml",
		"chart/values.yaml",
	}
	if len(first.Files) != len(want) {
		t.Fatalf("generated %d files, want %d: %v", len(first.Files), len(want), first.Paths())
	}
	for i, path := range want {
		if first.Files[i].Path != path {
			t.Errorf("file %d is %s, want %s (sorted, so the report and the diff are stable)", i, first.Files[i].Path, path)
		}
	}

	for _, a := range first.Files {
		for _, b := range second.Files {
			if a.Path == b.Path && string(a.Content) != string(b.Content) {
				t.Errorf("%s differs between two runs over one manifest", a.Path)
			}
		}
	}
}

// The generator emits no ArgoCD Application, and that is a decision rather
// than an omission: the substrate owns the Application — one static one per
// service, OCI-sourced, applied by install.sh — and a service repository
// commits none. A generated Application would be a SECOND one of the same
// name in the same namespace with a different source, and the later
// `kubectl apply` wins: a deploy whose target depends on apply order is
// not a deploy. The decision is recorded in moon/DEPLOYMENT.md, "Who owns
// the Application".
//
// The assertion is over the real output rather than a hand-written list:
// a list that agreed with a broken generator would pass against one. The
// path check fails if the file comes back under its own name; the content
// check fails if it comes back under any other; the values check fails if
// the targetRevision the Application consumed comes back with it.
func TestTheGeneratorEmitsNoArgoApplication(t *testing.T) {
	out := generateChart(t, chartManifest, nil)

	for _, file := range out.Files {
		if file.Path == "chart/templates/application.yaml" {
			t.Errorf("cafe gen chart emitted %s — the substrate owns the Application "+
				"(moon/DEPLOYMENT.md, \"Who owns the Application\"): one static per service, "+
				"OCI-sourced, applied by install.sh. A generated one is a second Application "+
				"of the same name in the same namespace, and the later apply wins", file.Path)
		}
		if strings.Contains(string(file.Content), "kind: Application") {
			t.Errorf("%s is an ArgoCD Application document — the generator emits none:\n%s",
				file.Path, file.Content)
		}
	}

	values := string(findChartFile(t, out, chartValuesPath).Content)
	if strings.Contains(values, "targetRevision") {
		t.Errorf("values.yaml carries a targetRevision — the ref was the Application's, and the\n"+
			"Application is the substrate's:\n%s", values)
	}
}

// The readiness probe is /readyz, and that is the fleet's contract: every
// cafaye service serves it and the substrate's rollout waits on it.
//
// The test asserts the path is /readyz AND that the two wrong answers are
// absent, because the defect this target exists to prevent is a probe pointed
// at /up — the path kit's kamal template carried, which produced deploys that
// could never go green. A test that only asserted "/readyz is present" would
// pass against a template that carried both.
func TestTheReadinessProbeIsTheFleetContract(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	deployment := string(findChartFile(t, out, chartDeploymentPath).Content)

	if !strings.Contains(deployment, "path: /readyz") {
		t.Errorf("the Deployment's readiness probe is not /readyz:\n%s", deployment)
	}
	for _, wrong := range []string{"/healthz", "/up"} {
		if strings.Contains(deployment, wrong) {
			t.Errorf("the Deployment probes %s, which is the defect the kamal template carried "+
				"— a fresh copy produced a deploy that could never go green:\n%s", wrong, deployment)
		}
	}
}

// Secrets come by reference: the Deployment reads a named Secret with envFrom,
// and no credential reaches values.yaml.
//
// The second half is asserted as "no value in values.yaml looks like a
// credential" rather than "these specific strings are absent", because a
// credential that reached a field nobody thought to check is exactly the case
// the rule exists for. The secret NAME is expected — it is the reference — so
// the check is on values that are not the name of something.
func TestSecretsComeByReferenceAndNeverInline(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	deployment := string(findChartFile(t, out, chartDeploymentPath).Content)
	values := string(findChartFile(t, out, chartValuesPath).Content)

	// The reference: envFrom with a secretRef, not a list of literal values.
	if !strings.Contains(deployment, "envFrom:") || !strings.Contains(deployment, "secretRef:") {
		t.Errorf("the Deployment does not read its environment from a named Secret:\n%s", deployment)
	}

	// The name is a value, and it is the same name in both files — a chart
	// whose Deployment reads a secret values.yaml does not name is a chart
	// whose pods crashloop.
	valuesSecret := "courier-secrets"
	if !strings.Contains(values, valuesSecret) {
		t.Errorf("values.yaml does not name the secret %q the Deployment reads:\n%s", valuesSecret, values)
	}
	if !strings.Contains(deployment, "secretName") && !strings.Contains(deployment, valuesSecret) {
		t.Errorf("the Deployment does not reference the secret values.yaml names:\n%s", deployment)
	}

	// No credential-shaped value in values.yaml. The secret name itself is
	// fine; a password, token or key value is not.
	for _, line := range strings.Split(values, "\n") {
		lower := strings.ToLower(line)
		for _, word := range []string{"password", "token", "apikey", "api_key", "secret"} {
			if strings.Contains(lower, word) && !strings.Contains(lower, "secretname") && !strings.Contains(lower, "imagepullsecrets") {
				// A comment mentioning the word is fine; a value is not.
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "#") {
					continue
				}
				t.Errorf("values.yaml carries a credential-shaped entry %q — the chart names the secret, "+
					"the value lives on the box:\n%s", word, line)
			}
		}
	}
}

// The image tag is a value and is never `latest`: a floating tag is a deploy
// that changes when somebody else pushes, and the pull-only loop exists so
// that what runs is exactly what the values commit said.
//
// The check is on the tag's VALUE line, not the word: values.yaml's own
// comment says "is never `latest`", and a test that failed against a sentence
// explaining the rule would be a test that fails against the rule being kept.
func TestTheImageTagIsNeverLatest(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	values := string(findChartFile(t, out, chartValuesPath).Content)

	tag := ""
	for _, line := range strings.Split(values, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "tag:") {
			tag = strings.Trim(strings.TrimPrefix(trimmed, "tag:"), ` "`)
		}
	}
	if tag == "" {
		t.Fatalf("values.yaml carries no image tag, so the default deploy is whatever "+
			"the repository's default branch builds:\n%s", values)
	}
	if tag == "latest" {
		t.Errorf("values.yaml's image tag is `latest` — a floating tag is a deploy that "+
			"changes when somebody else pushes:\n%s", values)
	}

	// The Deployment composes the image from the two values, so the tag that
	// runs is the one values.yaml names and nothing baked into the template.
	deployment := string(findChartFile(t, out, chartDeploymentPath).Content)
	if !strings.Contains(deployment, `{{ .Values.image.repository }}:{{ .Values.image.tag }}`) {
		t.Errorf("the Deployment does not compose image:tag from values:\n%s", deployment)
	}
}

// Resources are set: requests, which the scheduler places the pod on the basis
// of, and limits, which it is killed at. A chart with neither is a pod the
// scheduler can place anywhere and OOM-kill silently.
func TestResourcesAreSet(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	values := string(findChartFile(t, out, chartValuesPath).Content)
	deployment := string(findChartFile(t, out, chartDeploymentPath).Content)

	for _, doc := range []struct{ name, body string }{{"values.yaml", values}, {"deployment", deployment}} {
		for _, want := range []string{"requests:", "limits:", "cpu:", "memory:"} {
			if !strings.Contains(doc.body, want) {
				t.Errorf("%s carries no %q — a chart with no resources is a pod the scheduler "+
					"can place anywhere and OOM-kill silently:\n%s", doc.name, want, doc.body)
			}
		}
	}
}

// The container port is the language's, read from the same table `cafe dev`
// uses — the two cannot disagree, because a chart whose port is a guess is a
// chart whose Service points at nothing.
func TestTheContainerPortComesFromTheLanguageTable(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	deployment := string(findChartFile(t, out, chartDeploymentPath).Content)

	port, err := dev.LanguagePort("go")
	if err != nil {
		t.Fatalf("read the language port table: %v", err)
	}
	if !strings.Contains(deployment, "containerPort: "+strconv.Itoa(port)) {
		t.Errorf("the Deployment's containerPort is not the language table's %d:\n%s", port, deployment)
	}
}

// The service's name reaches every file the chart is made of: the chart name,
// the Deployment, the Service, the secret names. A chart that
// named its resources after anything but the service it deploys is a chart
// somebody has to go and look up.
func TestTheServiceNameReachesEveryFile(t *testing.T) {
	out := generateChart(t, chartManifest, nil)

	for _, file := range out.Files {
		if !strings.Contains(string(file.Content), "courier") {
			t.Errorf("%s does not carry the service name:\n%s", file.Path, file.Content)
		}
	}
}

// The environment arrives as NAME=VALUE pairs and reaches values.yaml in name
// order, because a map iterates in an order that changes between runs and a
// generated file that reshuffles between two runs of one manifest is a file
// nobody commits.
func TestEnvFlagsReachValuesInNameOrder(t *testing.T) {
	out := generateChart(t, chartManifest, map[string]string{
		"ZED_LAST":  "1",
		"ALPHA_KEY": "2",
		"MID_KEY":   "3",
	})
	values := string(findChartFile(t, out, chartValuesPath).Content)

	alpha := strings.Index(values, "ALPHA_KEY")
	mid := strings.Index(values, "MID_KEY")
	zed := strings.Index(values, "ZED_LAST")
	if alpha == -1 || mid == -1 || zed == -1 {
		t.Fatalf("values.yaml does not carry all three env pairs:\n%s", values)
	}
	if alpha >= mid || mid >= zed {
		t.Errorf("values.yaml's env is not in name order (ALPHA_KEY at %d, MID_KEY at %d, ZED_LAST at %d):\n%s",
			alpha, mid, zed, values)
	}
}

// A manifest with no env flags gets an empty env block, not a guess: a
// service's configuration is its own, and cafe will not invent it.
func TestNoEnvFlagsMeansAnEmptyEnvBlock(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	values := string(findChartFile(t, out, chartValuesPath).Content)

	if !strings.Contains(values, "env: {}") {
		t.Errorf("values.yaml's env block is not empty when no -env flag was given:\n%s", values)
	}
}

// `chart` refuses what it cannot do, and says why — the same shape `telemetry`
// establishes.
//
// The cases are the ones a VALID manifest can reach. chart supports every
// language core's schema allows except `spec`, so the unsupported-language
// branch is currently reachable only for a specification repository — a
// schema-valid `cobol` manifest cannot exist, and a test built on one would
// be testing a document that cannot exist. The branch stays because core
// adding a language is a schema change, not a cafe change.
func TestChartRefusesWhatItCannotDoAndSaysWhy(t *testing.T) {
	for _, row := range []struct {
		name     string
		document string
		sentinel error
		want     []string
	}{
		{
			name: "a spec repository runs nothing",
			document: `name: specs
description: Specifications only.
language: spec
core: ^0.2.0

repository:
  url: git@github.com:cafaye/specs.git
  defaultBranch: master
  visibility: public

owner:
  team: specs
  contact: specs@cafaye.com
`,
			sentinel: ErrUnsupportedLanguage,
			want:     []string{"cafe gen chart", "spec"},
		},
		{
			name: "a library has no HTTP surface",
			document: `name: core
description: A library.
language: go
core: ^0.2.0

repository:
  url: git@github.com:cafaye/core.git
  defaultBranch: master
  visibility: public

owner:
  team: core
  contact: core@cafaye.com
`,
			sentinel: ErrNoHTTPSurface,
			want:     []string{"cafe gen chart", "exposes"},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			_, err := Generate("chart", loadManifest(t, row.document), TargetOptions{})
			if err == nil {
				t.Fatalf("cafe gen chart accepted it and would have written files")
			}
			if !errors.Is(err, row.sentinel) {
				t.Errorf("err = %v, want it to wrap %v", err, row.sentinel)
			}
			for _, want := range row.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to name %q", err, want)
				}
			}
		})
	}
}

// The chart's identity is the manifest's name and a version helm accepts: a
// chart with no version is a chart `helm` refuses to package, and a chart
// named after anything but the service it deploys is a chart somebody has to
// go and look up.
func TestChartYAMLCarriesTheManifestsIdentity(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	chartYAML := string(findChartFile(t, out, chartYAMLPath).Content)

	for _, want := range []string{"name: courier", "version: 0.1.0", "apiVersion: v2"} {
		if !strings.Contains(chartYAML, want) {
			t.Errorf("Chart.yaml does not carry %q:\n%s", want, chartYAML)
		}
	}
}

// The README names the loop the chart exists for — push, pull, sync — because
// a habit nobody wrote down is a habit the next person has to rediscover.
func TestTheReadmeNamesTheLoop(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	readme := string(findChartFile(t, out, chartReadmePath).Content)

	for _, want := range []string{"helm push", "oci://ghcr.io", "/readyz", "envFrom"} {
		if !strings.Contains(readme, want) {
			t.Errorf("the README does not name %q:\n%s", want, readme)
		}
	}
}

// The Service is ClusterIP: the service is reached through the cluster's
// ingress rather than directly, and a NodePort in a generated chart is a
// decision somebody made for one deployment baked into every one.
func TestTheServiceIsClusterIP(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	service := string(findChartFile(t, out, chartServicePath).Content)

	if !strings.Contains(service, "type: {{ .Values.service.type }}") {
		t.Errorf("the Service's type is not a value:\n%s", service)
	}
	values := string(findChartFile(t, out, chartValuesPath).Content)
	if !strings.Contains(values, "type: ClusterIP") {
		t.Errorf("values.yaml's service type is not ClusterIP:\n%s", values)
	}
}

// The pull secret is named, never inlined: the chart carries the name of the
// read-only secret on the box and no credential, which is the whole of the
// closed-source chart's ride on a pull-based system.
func TestThePullSecretIsNamedAndNeverInlined(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	values := string(findChartFile(t, out, chartValuesPath).Content)
	deployment := string(findChartFile(t, out, chartDeploymentPath).Content)

	if !strings.Contains(values, "imagePullSecrets: courier-ghcr") {
		t.Errorf("values.yaml does not name the pull secret:\n%s", values)
	}
	if !strings.Contains(deployment, "imagePullSecrets:") || !strings.Contains(deployment, "{{ .Values.imagePullSecrets }}") {
		t.Errorf("the Deployment does not read the pull secret by name:\n%s", deployment)
	}
}

// Chart.yaml's description is one line, because a newline inside it is a parse
// error rather than a long line — and the manifest's description is the
// sentence the service's own README opens with.
func TestChartYAMLsDescriptionIsOneLine(t *testing.T) {
	document := strings.Replace(chartManifest,
		"description: Transactional email and push delivery.",
		"description: |\n  Transactional email\n  and push delivery.", 1)
	out := generateChart(t, document, nil)
	chartYAML := string(findChartFile(t, out, chartYAMLPath).Content)

	if strings.Contains(chartYAML, "\n  Transactional") {
		t.Errorf("Chart.yaml's description kept its newlines, which is a parse error:\n%s", chartYAML)
	}
	if !strings.Contains(chartYAML, "Transactional email and push delivery.") {
		t.Errorf("Chart.yaml's description lost the manifest's sentence:\n%s", chartYAML)
	}
}

// A generated file that is not the chart's own layout is a generator bug no
// test would catch: every path is under chart/, because that is the shape
// `helm template` expects.
func TestEveryPathIsUnderChart(t *testing.T) {
	out := generateChart(t, chartManifest, nil)

	for _, file := range out.Files {
		if !strings.HasPrefix(file.Path, "chart/") {
			t.Errorf("%s is outside chart/, which is the layout helm expects", file.Path)
		}
	}
}

// The drift gate's counterpart: the chart target's paths are read out of the
// generator rather than written down beside it, so a path the generator gains
// is a path the lock pins. This test is the assertion that stays true when
// the file set changes — it names no paths.
func TestTheChartTargetIsDiscoveredNotListed(t *testing.T) {
	names := TargetNames()
	found := false
	for _, name := range names {
		if name == "chart" {
			found = true
		}
	}
	if !found {
		t.Errorf("TargetNames() = %v, want chart among them — a target nobody can discover is a "+
			"target nobody runs, and a generated file nothing pins is a file that rots", names)
	}
}

// The ingress block is present, commented, and OFF by default.
//
// Off is the load-bearing word. The substrate's first sync happens before any
// operator has set a host, and a chart that suddenly routes traffic is a
// defect — so the default is a chart that renders an Ingress document of
// nothing, and the services the fleet exposes (gateway, parlor) turn the
// knob in their values commit.
//
// The comment is asserted too, and not as decoration: a knob nobody can
// discover is a knob the next person has to rediscover, which is the rule
// the chart's own README exists for, applied to values.yaml. The two facts
// the comment has to carry are the citation (DEPLOYMENT.md:28 — the edge is
// Traefik and cert-manager because that document says so) and the division
// of labor: the ClusterIssuer is cluster-scoped, created once per cluster
// by the substrate, and the chart only NAMES it.
func TestTheIngressValuesBlockIsOffByDefault(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	values := string(findChartFile(t, out, chartValuesPath).Content)

	for _, want := range []string{
		"ingress:",
		"enabled: false",
		"className: traefik",
		`host: ""`,
		`clusterIssuer: ""`,
		"tls: true",
		"path: /",
		"pathType: Prefix",
	} {
		if !strings.Contains(values, want) {
			t.Errorf("values.yaml does not carry %q:\n%s", want, values)
		}
	}

	for _, want := range []string{
		"DEPLOYMENT.md:28",
		"created ONCE PER CLUSTER",
		"only NAMES",
	} {
		if !strings.Contains(values, want) {
			t.Errorf("values.yaml's ingress comment does not carry %q — the citation and the\n"+
				"division of labor are the two facts a reader needs:\n%s", want, values)
		}
	}
}

// ingressDocument is the shape the render proof asserts on: the fields the
// fleet's edge and the substrate's rollout depend on.
type ingressDocument struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string            `yaml:"name"`
		Labels      map[string]string `yaml:"labels"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Spec struct {
		IngressClassName string `yaml:"ingressClassName"`
		TLS              []struct {
			Hosts      []string `yaml:"hosts"`
			SecretName string   `yaml:"secretName"`
		} `yaml:"tls"`
		Rules []struct {
			Host string `yaml:"host"`
			HTTP struct {
				Paths []struct {
					Path     string `yaml:"path"`
					PathType string `yaml:"pathType"`
					Backend  struct {
						Service struct {
							Name string `yaml:"name"`
							Port struct {
								Number int `yaml:"number"`
							} `yaml:"port"`
						} `yaml:"service"`
					} `yaml:"backend"`
				} `yaml:"paths"`
			} `yaml:"http"`
		} `yaml:"rules"`
	} `yaml:"spec"`
}

// chartValues parses the chart's own values.yaml, so a render proof starts
// from the defaults the generator wrote rather than from a second copy of
// them written in the test: the values block and the template are proved to
// agree, and flipping a default in the generator moves the test.
func chartValues(t *testing.T, out Output) map[string]any {
	t.Helper()

	values := map[string]any{}
	if err := yaml.Unmarshal(findChartFile(t, out, chartValuesPath).Content, &values); err != nil {
		t.Fatalf("the generated values.yaml does not parse: %v", err)
	}
	return values
}

// renderIngressTemplate executes the generated ingress.yaml as a Go
// text/template against one values set, the way `helm template` would.
//
// The one helm-specific function, `fail`, is registered here with the
// behavior helm gives it: it stops the render and its message becomes the
// error `Execute` returns, which is what the no-host guard relies on. Every
// other function the template uses (`if`, `not`, `and`) is Go's own.
func renderIngressTemplate(t *testing.T, body string, values map[string]any) (string, error) {
	t.Helper()

	tmpl, err := template.New("ingress.yaml").Funcs(template.FuncMap{
		"fail": func(message string) (string, error) { return "", errors.New(message) },
	}).Parse(body)
	if err != nil {
		t.Fatalf("the generated ingress.yaml does not parse as a template: %v", err)
	}

	var out bytes.Buffer
	err = tmpl.Execute(&out, map[string]any{"Values": values})
	return out.String(), err
}

// The Ingress renders from values, in-process: the three cases the knob has,
// each asserted on the rendered document rather than on the template's bytes.
//
// The header above used to say the render proof was deliberately absent
// because `helm template` needs a binary no test may start. That reason is
// real — but it is not the whole of the proof. The template is Go's
// text/template and the values are YAML, so the RENDER executes here against
// the chart's own values.yaml, and the result is unmarshalled and asserted
// field by field. Helm's own output is in the packet's report; this is the
// half that runs on every gate, and it is a behavior test of the
// generator's contract rather than a byte-grep:
//
//   - enabled renders the Ingress — the class, the host, the backend service
//     and port, the cluster-issuer annotation, the tls block;
//   - disabled renders NOTHING — the whole document is inside the gate, so
//     an unconfigured chart emits no Ingress at all;
//   - enabled with no host FAILS the render — an Ingress with no host is a
//     silent misroute, the class of defect kit's /up healthcheck was.
func TestTheIngressRendersFromValues(t *testing.T) {
	out := generateChart(t, chartManifest, nil)
	body := string(findChartFile(t, out, chartIngressPath).Content)

	t.Run("enabled renders the Ingress", func(t *testing.T) {
		values := chartValues(t, out)
		values["ingress"] = map[string]any{
			"enabled":       true,
			"className":     "traefik",
			"host":          "courier.example.com",
			"clusterIssuer": "letsencrypt-prod",
			"tls":           true,
			"path":          "/",
			"pathType":      "Prefix",
		}

		rendered, err := renderIngressTemplate(t, body, values)
		if err != nil {
			t.Fatalf("the enabled render failed: %v", err)
		}

		var doc ingressDocument
		if err := yaml.Unmarshal([]byte(rendered), &doc); err != nil {
			t.Fatalf("the enabled render is not a YAML document: %v\n%s", err, rendered)
		}

		if doc.APIVersion != "networking.k8s.io/v1" {
			t.Errorf("apiVersion = %q, want networking.k8s.io/v1", doc.APIVersion)
		}
		if doc.Kind != "Ingress" {
			t.Errorf("kind = %q, want Ingress", doc.Kind)
		}
		if doc.Metadata.Name != "courier" {
			t.Errorf("metadata.name = %q, want the service's name", doc.Metadata.Name)
		}
		for key, want := range map[string]string{
			"app.kubernetes.io/name":       "courier",
			"app.kubernetes.io/instance":   "courier",
			"app.kubernetes.io/managed-by": "cafe",
		} {
			if got := doc.Metadata.Labels[key]; got != want {
				t.Errorf("label %s = %q, want %q — the Ingress and the Service must carry the same labels",
					key, got, want)
			}
		}
		if doc.Spec.IngressClassName != "traefik" {
			t.Errorf("ingressClassName = %q, want traefik — k3s's default, per DEPLOYMENT.md:28",
				doc.Spec.IngressClassName)
		}
		if got := doc.Metadata.Annotations["cert-manager.io/cluster-issuer"]; got != "letsencrypt-prod" {
			t.Errorf("the cluster-issuer annotation = %q, want letsencrypt-prod — the chart names the\n"+
				"issuer and cert-manager serves it", got)
		}
		if len(doc.Spec.TLS) != 1 {
			t.Fatalf("spec.tls has %d entries, want 1 — TLS without an issuer is a secret nothing writes",
				len(doc.Spec.TLS))
		}
		if doc.Spec.TLS[0].SecretName != "courier-tls" {
			t.Errorf("tls.secretName = %q, want courier-tls", doc.Spec.TLS[0].SecretName)
		}
		if len(doc.Spec.TLS[0].Hosts) != 1 || doc.Spec.TLS[0].Hosts[0] != "courier.example.com" {
			t.Errorf("tls.hosts = %v, want [courier.example.com] — the tls host and the rule host are one fact",
				doc.Spec.TLS[0].Hosts)
		}
		if len(doc.Spec.Rules) != 1 {
			t.Fatalf("spec.rules has %d entries, want 1", len(doc.Spec.Rules))
		}
		rule := doc.Spec.Rules[0]
		if rule.Host != "courier.example.com" {
			t.Errorf("rule host = %q, want courier.example.com", rule.Host)
		}
		if len(rule.HTTP.Paths) != 1 {
			t.Fatalf("the rule has %d paths, want 1", len(rule.HTTP.Paths))
		}
		path := rule.HTTP.Paths[0]
		if path.Path != "/" || path.PathType != "Prefix" {
			t.Errorf("path = %s (%s), want / (Prefix)", path.Path, path.PathType)
		}
		if path.Backend.Service.Name != "courier" {
			t.Errorf("backend service = %q, want the Service's name", path.Backend.Service.Name)
		}
		if path.Backend.Service.Port.Number != 80 {
			t.Errorf("backend port = %d, want 80 — the Service's own port, so the Ingress and the\n"+
				"Service cannot disagree", path.Backend.Service.Port.Number)
		}
	})

	t.Run("disabled renders nothing", func(t *testing.T) {
		// The chart's own values.yaml, unmodified: enabled is false there.
		values := chartValues(t, out)

		rendered, err := renderIngressTemplate(t, body, values)
		if err != nil {
			t.Fatalf("the disabled render failed: %v", err)
		}

		// "Nothing" means no Ingress DOCUMENT. The header comment is static
		// text outside the gate and is always emitted; what the gate emits
		// is nothing, so every non-empty line left is a comment and there is
		// no document to unmarshal.
		for _, line := range strings.Split(rendered, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				t.Errorf("ingress.enabled is false and the render emitted more than the header comment —\n"+
					"a chart that suddenly routes traffic is a defect:\n%s", rendered)
				break
			}
		}
	})

	t.Run("enabled without a host fails the render", func(t *testing.T) {
		values := chartValues(t, out)
		values["ingress"] = map[string]any{
			"enabled":       true,
			"className":     "traefik",
			"host":          "",
			"clusterIssuer": "",
			"tls":           true,
			"path":          "/",
			"pathType":      "Prefix",
		}

		_, err := renderIngressTemplate(t, body, values)
		if err == nil {
			t.Error("the render succeeded with ingress.enabled true and ingress.host empty — an Ingress\n" +
				"with no host is a silent misroute, which is the class of defect kit's /up healthcheck was")
		}
	})
}

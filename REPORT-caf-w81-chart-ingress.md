# REPORT — caf-w81: the chart can be reached (the deploy artifact gains its Ingress)

**Branch:** `worker/caf-w81-chart-ingress`
**Base commit:** `1b3cb4925adf13db85abbd914bb582701af071f0` — *Merge worker/caf-w72-genchart: caf gen chart — the verb, the chart, and the registry story*

## What was found on arrival, and what this run did about it

The worktree held two commits from the previous attempt (`ed05855`, `60247f2`):
the generator, the values block, the README section, the in-process render test,
and the CLI help row. The tree was clean. **The gate was red**, and the previous
attempt had not said so:

- `gate.yml`'s suite floor was still 793 against a tree declaring 803 top-level
  tests. `TestTheGateFloorIsNotBelowTheSuiteCafClaimsToHave` failed on arrival:
  *"gate.yml floor 793, tree declares 803 top-level tests, so the gate runs 795
  and skips 8"* — the identity `declared - minimum == expectedSkips` (803 − 793
  = 10 ≠ 8) broken in both directions, exactly as the ratchet is designed to
  catch. The subtests floor (966 against 970 measured) was stale by the file's
  own convention.
- `caf.lock` still pinned the old `gate.yml` hash, so `caf lock --verify` was
  red too.

This run raised both floors with their rationale paragraphs (suite 793 → 795,
subtests 966 → 969, measured 970 and set one lower per the file's rule),
re-ran `go run ./cmd/caf lock .` (12 files re-pinned, verify green), and fixed
one cosmetic defect the previous attempt left: the `gen chart` help table's
ingress row was misaligned (10 spaces where 6 align it with the column). No
existing assertion was weakened, deleted, or skipped; all 22 byte-level
assertions from before the packet are intact and green.

## 1. The values block, verbatim

Written by `renderChartValues` (`internal/gen/chart.yaml` emitter, `chart.go`),
between the `service:` block and the `env:` block:

```yaml
# How the service is reached from outside the cluster: an Ingress,
# gated on `ingress.enabled`, which is FALSE by default.
#
# A chart that suddenly routes traffic is a defect: the substrate's
# first sync happens before any operator has set a host, and an
# Ingress with no host is a silent misroute — the class of defect
# kit's /up healthcheck was. The services the fleet exposes (gateway,
# parlor) turn the knob on in their values commit; identity and
# courier stay internal.
#
# The edge is Traefik, k3s's default ingress controller, with
# cert-manager in front of it (DEPLOYMENT.md:28 — "Ingress + TLS |
# Traefik (k3s default) + cert-manager | declarative, no edge
# box"). The ClusterIssuer is created ONCE PER CLUSTER by the
# substrate (a follow-up, not this packet); this block only NAMES
# it — the same "names the secret and never contains a credential"
# discipline the imagePullSecrets block uses. Empty means no TLS: a
# secret nothing writes.
ingress:
  enabled: false
  className: traefik
  host: ""
  clusterIssuer: ""
  tls: true
  path: /
  pathType: Prefix
```

## 2. The seventh file

`chart/templates/ingress.yaml` — `chartIngressPath` (`chart.go:30`), emitted by
`renderIngress` (`chart.go:463`), in the `files` slice beside the other six.
The whole document is inside `{{- if .Values.ingress.enabled }}`; the guard is
helm's `fail` on enabled-without-a-host; the cluster-issuer annotation is
emitted only when the name is non-empty; `spec.tls` only when `tls` is true
AND the issuer is set; the backend port is `.Values.service.port`, the
Service's own port.

## 3. The render test

`TestTheIngressRendersFromValues` (`internal/gen/chart_test.go:650`) parses the
generated `ingress.yaml` as a Go `text/template` (registering helm's `fail`
with helm's behaviour — it stops the render and its message becomes the
`Execute` error), executes it against values unmarshalled from the chart's own
`values.yaml` (so a flipped default in the generator moves the test), and
unmarshals the result as YAML via `github.com/goccy/go-yaml` — the same
library `internal/ci` and `internal/backup` already parse YAML with. No binary
starts. Three cases:

- **enabled renders the Ingress** — values: `enabled: true`, `host:
  courier.example.com`, `clusterIssuer: letsencrypt-prod`, `tls: true`. Asserts
  on the unmarshalled document: apiVersion/kind, name, the three
  `app.kubernetes.io` labels, `ingressClassName: traefik`, the
  `cert-manager.io/cluster-issuer` annotation, one tls entry
  (`secretName: courier-tls`, hosts `[courier.example.com]`), one rule on the
  host, one path `/` Prefix, backend service `courier` port **80** — the
  Service's own port.
- **disabled renders nothing** — the chart's own values.yaml, unmodified. Every
  non-empty line of the render is a comment; there is no document to unmarshal.
- **enabled without a host fails the render** — `Execute` returns the guard's
  error.

### The red-first proofs (each run by this worker, then restored)

1. **The default is load-bearing.** Flipped `enabled: false` → `true` in
   `renderChartValues`. Red: `TestTheIngressValuesBlockIsOffByDefault` AND
   `TestTheIngressRendersFromValues/disabled_renders_nothing` (the chart's own
   values now render an Ingress). Restored.
2. **The guard is load-bearing.** Deleted the `{{- if not .Values.ingress.host
   }}` / `{{- fail … }}` block. Red: `…/enabled_without_a_host_fails_the_render`
   (the render succeeded). Restored.
3. **The gate is load-bearing.** Removed `{{- if .Values.ingress.enabled }}` and
   its `{{- end }}`. Red: `…/disabled_renders_nothing`. Restored.
4. **The render proof is stronger than a byte-grep** (the bug the previous
   attempt's commit message records, re-run here to confirm the proof catches
   it): moved the cluster-issuer annotation from `metadata:` to `spec:`. Red:
   `…/enabled_renders_the_Ingress` — *"the cluster-issuer annotation = "",
   want letsencrypt-prod"*. A `strings.Contains` byte test passes against those
   bytes; the in-process render does not, because Kubernetes drops an
   annotation under `spec:` and cert-manager never sees it. Restored.

## 4. `helm template` and `helm lint`, verbatim

Generated from a scratch manifest (`name: gateway`, language `go`, an HTTP
surface) with `caf gen chart`; helm v4.3.0 at `/usr/local/bin/helm`.

### `helm template gateway chart` (defaults — `ingress.enabled=false`)

The ingress source emits its header comment and **no Ingress document**
(`grep -c 'kind: Ingress'` → 0). Full output:

```yaml
---
# Source: gateway/templates/service.yaml
# Code generated by `caf gen chart`. DO NOT EDIT.
#
# The Service in front of the container. ClusterIP, because the service
# is reached through the cluster's ingress rather than directly; the
# targetPort is the container's named `http` port, so the container
# port and this document cannot disagree.
apiVersion: v1
kind: Service
metadata:
  name: gateway
  labels:
    app.kubernetes.io/name: gateway
    app.kubernetes.io/instance: gateway
    app.kubernetes.io/managed-by: caf
spec:
  type: ClusterIP
  ports:
    - name: http
      port: 80
      targetPort: http
      protocol: TCP
  selector:
    app.kubernetes.io/name: gateway
    app.kubernetes.io/instance: gateway

---
# Source: gateway/templates/deployment.yaml
# Code generated by `caf gen chart`. DO NOT EDIT.
#
# The Deployment: the service's image, its container port, its
# environment and its readiness probe. The probe's path is the fleet's
# contract (/readyz — see the chart README for why it is not a value);
# the port is the language's, read from the same table `caf dev`
# uses. Edit by changing the manifest and regenerating.
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gateway
  labels:
    app.kubernetes.io/name: gateway
    app.kubernetes.io/instance: gateway
    app.kubernetes.io/managed-by: caf
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: gateway
      app.kubernetes.io/instance: gateway
  template:
    metadata:
      labels:
        app.kubernetes.io/name: gateway
        app.kubernetes.io/instance: gateway
        app.kubernetes.io/managed-by: caf
    spec:
      imagePullSecrets:
        - name: gateway-ghcr
      containers:
        - name: gateway
          image: "ghcr.io/cafaye/gateway:0.1.0"
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: 8080
              protocol: TCP
          envFrom:
            - secretRef:
                name: gateway-secrets
          env:
          readinessProbe:
            httpGet:
              path: /readyz
              port: http
            initialDelaySeconds: 5
            periodSeconds: 10
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 500m
              memory: 512Mi

---
# Source: gateway/templates/ingress.yaml
# Code generated by `caf gen chart`. DO NOT EDIT.
#
# The Ingress: how the service is reached from outside the cluster.
# Off by default and gated on `ingress.enabled` in values.yaml — see
# that file for why it is off, and for the issuer the chart names
# and never creates.

---
# Source: gateway/templates/application.yaml
# Code generated by `caf gen chart`. DO NOT EDIT.
#
# The ArgoCD Application: the pull-only loop. Argo watches the
# repository the manifest declares and syncs this chart when it
# changes; the only thing that can change the cluster is a commit.
# A hand edit self-heals; rollback is `git revert`.
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: gateway
  namespace: argocd
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: git@github.com:cafaye/gateway.git
    targetRevision: master
    path: chart
  destination:
    server: https://kubernetes.default.svc
    namespace: gateway
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
```

### `helm template gateway chart --set ingress.enabled=true --set ingress.host=gateway.example.com --set ingress.clusterIssuer=letsencrypt-prod`

The Ingress document, as helm renders it (the rest of the output is the same
Service/Deployment/Application as above):

```yaml
---
# Source: gateway/templates/ingress.yaml
# Code generated by `caf gen chart`. DO NOT EDIT.
#
# The Ingress: how the service is reached from outside the cluster.
# Off by default and gated on `ingress.enabled` in values.yaml — see
# that file for why it is off, and for the issuer the chart names
# and never creates.
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: gateway
  labels:
    app.kubernetes.io/name: gateway
    app.kubernetes.io/instance: gateway
    app.kubernetes.io/managed-by: caf
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod

spec:
  ingressClassName: traefik
  tls:
    - hosts:
        - gateway.example.com
      secretName: gateway-tls
  rules:
    - host: gateway.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: gateway
                port:
                  number: 80
```

### The guard under real helm

`helm template gateway chart --set ingress.enabled=true` (no host):

```
Error: execution error at (gateway/templates/ingress.yaml:9:4): caf gen chart: ingress.enabled is true and ingress.host is empty — an Ingress with no host is a silent misroute; set ingress.host in values.yaml

Use --debug flag to render out invalid YAML
```

### `helm lint chart` (both cases)

```
==> Linting chart
[INFO] Chart.yaml: icon is recommended

1 chart(s) linted, 0 chart(s) failed
```

Identical for the enabled case (same output, exit 0).

## 5. What was ruled out

- **A `Certificate` object instead of the annotation.** cert-manager can issue a
  `Certificate` directly, but the Ingress annotation is the declarative route:
  the Ingress names the issuer, cert-manager provisions and renews the secret,
  and the chart never carries a key. A `Certificate` would re-state the host and
  DNS names the Ingress already carries — a second copy of "which host" that
  can drift from the first — and would put the certificate's renewal lifecycle in
  the chart rather than at the edge. The annotation is also what
  DEPLOYMENT.md:28's "declarative, no edge box" points at.
- **An Ingress per-namespace in the substrate instead of per-chart.** The
  substrate is shared by all four services; an Ingress there would be a
  cluster-wide document naming service hosts — a second, earlier copy of a
  per-deployment fact, written before any operator has set a host. Per-chart
  keeps the decision in the service's own values commit, which is the pull-based
  loop's only write path. The substrate's job is the controller and the issuer,
  not the routes.
- **On by default** (a chart that suddenly routes traffic is a defect) and
  **requiring a host to render** (the substrate's first sync happens before any
  operator has set one). Both refused in the values comment and the template.

## 6. The gate

```
./bin/prime        # go mod download && go build ./... && go test -v ./...
```

Result (warm, this tree):

```
caf: 16 packages, 795 top-level passes, 970 subtest passes, 0 failures, 8 skips
caf: live tier: 0 of 7 executed; not enabled, so the 7 live tests skipped rather than ran.
```

`go vet ./...` clean, `gofmt -l .` prints nothing. `go run ./cmd/caf lock
--verify .` → *"caf.lock matches 12 pinned file(s)"*. `tests/gate-declaration-self-test.sh`
→ 21 breakages each went red and named their finding. The floors moved in this
packet: suite 793 → 795 (803 declared, 8 skips), subtests 966 → 969 (970
measured, set one lower per the file's rule), with rationale paragraphs in
`gate.yml`'s voice; `caf.lock` re-pinned in the same commit.

## 7. What is still manual

- **No chart is published yet.** `helm push oci://ghcr.io/cafaye/charts/…` has
  not happened for any service, so **no existing chart's `values.yaml` needs
  migrating** — the first values commits that turn the knob on are gateway's
  and parlor's, against charts generated from now on.
- **The ClusterIssuer is not created.** It is cluster-scoped and belongs to the
  substrate (a follow-up, not this packet). Until it exists, setting
  `clusterIssuer` names something that isn't there: cert-manager logs "issuer
  not found" and no certificate is issued. The chart's values comment says so.
- **The two secrets are still hand-made on the box** (`gateway-ghcr`,
  `gateway-secrets`), as the chart's README documents.
- **The helm output above is report evidence, not gate evidence.** The
  in-process render is the half that runs on every gate; `helm template` and
  `helm lint` stay here, per the packet.

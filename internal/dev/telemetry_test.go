package dev

import (
	"strings"
	"testing"
)

// caf dev renders the services a developer runs, and a service in that document
// has no way to reach the collector unless the document says where the collector
// is. The variable that says it is `<SERVICE>_OTEL_ENDPOINT` — core's
// otel-endpoint.schema.json calls that variable "the ONLY contract" — and before
// this packet `caf/internal/dev` did not mention it, so every service started by
// `caf dev` was left to whatever default its own code happened to carry.
//
// Measured consequence, and it is not "telemetry is off": a service whose default
// is core's shipped collector (`http://otel-collector:4317`) still exported, but
// only because it guessed. The moment a developer ran `caf dev` against a stack
// where the collector is on a different network or absent, the variable was the
// only place that could have said so and it was empty.
//
// The table is the four cases the contract distinguishes, and each one is a
// different sentence in the rendered document.
func TestPlanRendersTheTelemetryEndpointForEveryService(t *testing.T) {
	stack := mustPlan(t, twoDependencyManifest, twoServiceRegistry(), Options{Build: buildGo})

	for _, name := range []string{"stack", "alpha", "beta"} {
		env := envOf(service(t, stack, name))
		variable := strings.ToUpper(name) + "_OTEL_ENDPOINT"
		if got := env[variable]; got != "http://otel-collector:4317" {
			t.Errorf("%s %s = %q, want the collector that ships with the local stack. "+
				"core's endpoint contract names this variable as the only way a service is pointed at a "+
				"backend, and a rendered document that omits it leaves every service to its own default",
				name, variable, got)
		}
	}
}

// The rendered document is the artifact a developer reads, so the variable has
// to be IN it and not only on the plan's data structures. A planner that computed
// the right environment and a renderer that dropped it would pass the test above
// and produce a compose file with no telemetry in it.
func TestTheRenderedDocumentCarriesTheEndpointVariable(t *testing.T) {
	stack := mustPlan(t, twoDependencyManifest, twoServiceRegistry(), Options{Build: buildGo})

	block := between(t, stack.Compose, "  alpha:\n", "\n  beta:")
	want := "      ALPHA_OTEL_ENDPOINT: http://otel-collector:4317\n"
	if !strings.Contains(block, want) {
		t.Errorf("alpha's rendered block does not carry the endpoint variable:\n%s", block)
	}
}

// A registry entry describes a service's own configuration, so its value has to
// win — a self-hoster pointing one service at their own backend, which is the
// whole escape hatch core's schema describes. If caf's local default outranked
// the catalog, the escape hatch would be a comment.
func TestARegistryEndpointValueOutranksCafsDefault(t *testing.T) {
	registry := twoServiceRegistry()
	registry["alpha"] = Entry{
		Name:        "alpha",
		Image:       "ghcr.io/cafaye/alpha:1.2.3",
		Port:        8081,
		Environment: []Env{{Name: "ALPHA_OTEL_ENDPOINT", Value: "https://otlp.example.com:4318"}},
	}

	stack := mustPlan(t, twoDependencyManifest, registry, Options{Build: buildGo})

	env := envOf(service(t, stack, "alpha"))
	if got := env["ALPHA_OTEL_ENDPOINT"]; got != "https://otlp.example.com:4318" {
		t.Errorf("alpha ALPHA_OTEL_ENDPOINT = %q, want the value its own catalog entry declared. "+
			"caf's value is a default for a service nobody described, not an override of one that was", got)
	}
	// Scoped to alpha's own block: a document-wide search would be red for the
	// right behaviour, because beta and the project service carry caf's default
	// beside it.
	if block := between(t, stack.Compose, "  alpha:\n", "\n  beta:"); strings.Contains(block, "otel-collector") {
		t.Errorf("alpha's rendered block still carries caf's default collector:\n%s", block)
	}
}

// Local infrastructure is a third-party image, not a cafaye service. postgres and
// redis have no `cafaye.yml`, no `*_OTEL_ENDPOINT` contract and no collector to
// report to, and handing them one is a variable nothing reads.
func TestLocalInfrastructureIsHandedNoTelemetryEndpoint(t *testing.T) {
	stack := mustPlan(t, twoDependencyManifest, twoServiceRegistry(), Options{Build: buildGo})

	for _, name := range []string{"postgres", "redis"} {
		for variable := range envOf(service(t, stack, name)) {
			if strings.HasSuffix(variable, "_OTEL_ENDPOINT") {
				t.Errorf("%s was handed %s. It is a third-party image with no cafaye identity, and a "+
					"variable it does not read is a line in the document a reader has to account for",
					name, variable)
			}
		}
	}
}

// `-no-infra` is about postgres and redis, and nothing else. It is not "no local
// third-party containers", and widening it to cover the collector would make a
// flag named after the infrastructure quietly turn telemetry off — which is a
// switch a person has to go looking for in a compose file to discover they set.
func TestNoInfraStillRendersTheTelemetryEndpoint(t *testing.T) {
	stack := mustPlan(t, twoDependencyManifest, twoServiceRegistry(), Options{Build: buildGo, NoInfra: true})

	if got := envOf(service(t, stack, "alpha"))["ALPHA_OTEL_ENDPOINT"]; got == "" {
		t.Errorf("alpha carries no ALPHA_OTEL_ENDPOINT under -no-infra. That flag names postgres and redis; " +
			"a service pointed at a collector is a fact about the service, not a piece of local infrastructure")
	}
}

// The variable name is core's pattern applied to the service name, and the dash
// is the whole reason it is derived rather than typed: `internal/gen`'s
// `endpointVariable` drops it, because core's pattern admits no dash, so a
// repository named `my-service` reads `MYSERVICE_OTEL_ENDPOINT`. caf dev and
// `caf gen telemetry` have to agree on that spelling or a kebab-case service is
// handed a variable its own generated code does not read — which is the same
// defect this repository already found once, in the drift gate measuring courier
// against courier.
func TestTheEndpointVariableNameIsDerivedTheSameWayTheGeneratorDerivesIt(t *testing.T) {
	const kebab = `name: my-service
description: The service under development.
language: go
core: ^0.2.0
exposes:
  api: openapi/openapi.yaml
repository:
  url: git@github.com:cafaye/my-service.git
owner:
  team: stack
`
	stack := mustPlan(t, kebab, Catalog{}, Options{Build: buildGo})

	env := envOf(service(t, stack, "my-service"))
	if got := env["MYSERVICE_OTEL_ENDPOINT"]; got != "http://otel-collector:4317" {
		t.Errorf("my-service carries MYSERVICE_OTEL_ENDPOINT = %q. core's endpoint pattern "+
			"^[A-Z][A-Z0-9]*_OTEL_ENDPOINT$ admits no dash, and caf gen telemetry drops it for the same "+
			"reason and in the same way — two derivations that disagree leave a service handed a variable "+
			"nothing reads", got)
	}
}

// twoServiceRegistry is alphaRegistry plus beta, for the cases that need more
// than one registry-resolved service on the stack.
func twoServiceRegistry() Catalog {
	registry := alphaRegistry()
	registry["beta"] = Entry{
		Name:  "beta",
		Image: "ghcr.io/cafaye/beta:0.4.0",
		Port:  8082,
	}
	return registry
}

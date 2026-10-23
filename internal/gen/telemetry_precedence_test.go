package gen

import (
	"strings"
	"testing"
)

// The documented precedence is a sentence in a comment in a file nobody compiles
// until they adopt it, and a comment is not a contract. It IS one here, and this
// is the test that makes it one:
//
//   - the cases core's endpoint contract distinguishes are named as rows, not as
//     prose, so a service that reads the table knows what happens in each;
//   - the both-set-and-conflicting row asserts the anywaye variable wins, because
//     that is the only row whose answer is not obvious from the reading.
//
// The second point is the substantive one. The two single-variable rows and the
// agreeing row all say "the variable that is set is the variable used", which any
// implementation satisfies by accident. The conflicting row is the only one that
// can distinguish a resolution WITH a precedence from one without.
//
// It is asserted over the emitted BYTES rather than by running them, because a
// generated Go file is not compiled until a service adopts it, and this repository
// compiles only its own copy. So the generated file has to carry the table itself
// — `cafe gen telemetry` emits the suite beside the setup for exactly this reason.
func TestTheGeneratedSetupCarriesTheEndpointPrecedenceItDocuments(t *testing.T) {
	out := generate(t, manifestFor)

	source := string(findFile(t, out, goSourcePath).Content)
	suite := string(findFile(t, out, goTestPath).Content)

	for _, want := range []string{
		"COURIER_OTEL_ENDPOINT",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("the generated setup does not mention %s", want)
		}
	}
	if !strings.Contains(source, "wins over") {
		t.Errorf("the generated setup does not state which variable wins. A contract with an " +
			"undocumented precedence order is a bug report")
	}

	// The suite's table, which is where a service learns the four cases rather than
	// inferring them from the function body.
	for _, want := range []string{
		"func TestEndpointPrefersTheAnywayeName",
		"t.Setenv(EndpointVariable, row.anywaye)",
		`t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", row.standard)`,
	} {
		if !strings.Contains(suite, want) {
			t.Errorf("the generated suite does not contain %q, so a service adopting this file has "+
				"no executable statement of the precedence", want)
		}
	}

	// The conflicting row, which is the only one that can fail.
	//
	// Both a differing address AND a differing scheme, because those are decided by
	// different code in every OTel SDK this fleet uses: the address by whatever the
	// caller passes, and the scheme by whatever the SDK's own environment read
	// produced. A table that asserted only the address is green against a setup that
	// exports in the clear — MEASURED, in courier's `otel_export_test.exs` and in
	// this repository's own `internal/emitter`, where the address half of the
	// precedence held and the scheme half did not.
	// The literals are the gofmt-canonical spacing of the emitted rows rather than
	// the spacing in the generator's source, because the emitted file is what a
	// service reads and `cafe gen telemetry` writes it through gofmt. A test that
	// matched the generator's own alignment would go red the moment a row above
	// this one changed width, which is a test of the wrong thing.
	for _, want := range []string{
		`"both set and conflicting: the anywaye name wins, address and scheme"`,
		`"https://anywaye.example:4318"`,
		`"http://generic.example:4318"`,
	} {
		if !strings.Contains(suite, want) {
			t.Errorf("the generated suite has no conflicting row carrying %q. The single-variable "+
				"rows and the agreeing row are satisfied by a resolution with no precedence at all; "+
				"only the conflicting one cannot be", want)
		}
	}
}

// The scheme needs its own statement rather than being folded into the address
// row, because the two are decided by different code in every OTel SDK this fleet
// uses. A table that asserted the address would be green against a setup that
// exports in the clear, which is the failure this packet measured rather than one
// it imagined.
func TestTheGeneratedSetupSaysTheSchemeIsPartOfTheContract(t *testing.T) {
	source := strings.ToLower(string(findFile(t, generate(t, manifestFor), goSourcePath).Content))

	for _, want := range []string{"scheme", "plaintext"} {
		if !strings.Contains(source, want) {
			t.Errorf("the generated setup never mentions %q, so the file says where to send telemetry "+
				"and not whether the bytes are encrypted. `http://otel-collector:4317` is plaintext "+
				"gRPC, and a reader who assumes otherwise configures TLS at a collector that does not "+
				"speak it — which exports nothing and says nothing", want)
		}
	}
}

// And the emitted suite's conflicting row has to be one a service can run: the
// generated `Endpoint()` reads `os.Getenv` directly, so a row that set only one
// of the two variables would be decided by whatever the machine running it had.
//
// This is asserted over the bytes for the same reason as the table above, and it
// is separate because it is a different failure: a generator that emitted the
// right rows in a test that cannot isolate them produces a green suite on every
// machine and a service that still resolves the wrong way.
func TestTheGeneratedPrecedenceTableUnsetsBothVariablesInEveryRow(t *testing.T) {
	suite := string(findFile(t, generate(t, manifestFor), goTestPath).Content)

	// `t.Setenv(name, row.anywaye)` with `row.anywaye` empty in the unset rows is
	// what isolates them: t.Setenv restores on cleanup, so each row sees exactly
	// what its own fields say. The generator emits the unset value explicitly
	// rather than skipping the call, and that is the difference.
	if !strings.Contains(suite, `t.Setenv(EndpointVariable, row.anywaye)`) ||
		!strings.Contains(suite, `t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", row.standard)`) {
		t.Error("the generated precedence table does not set both variables in every row, so a row " +
			"decides on whatever the machine running it had set")
	}

	// Scoped to the precedence test's own body, because the generated suite has
	// several tables and a count over the whole file would be measuring them all.
	const fn = "func TestEndpointPrefersTheAnywayeName"
	start := strings.Index(suite, fn)
	if start < 0 {
		t.Fatal("the generated suite has no TestEndpointPrefersTheAnywayeName")
	}
	body := suite[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	if got := strings.Count(body, "t.Run(row.name"); got != 1 {
		t.Errorf("the generated precedence test runs its table %d times; the four cases are one "+
			"table with one runner, and a second one is a second definition of what a row means", got)
	}
	// Every case in it, so "the table isolates its rows" is not an assertion about
	// one row that happens to be written correctly.
	if got := strings.Count(body, "name: "); got < 4 {
		t.Errorf("the generated precedence test has %d named cases, want at least 4: "+
			"only-standard, only-service, both-agree and both-conflict are four different answers",
			got)
	}
}

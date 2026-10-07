package backup

import (
	"errors"
	"testing"
)

// RunnerFor is the engine selection, and it is the whole of how the second
// engine is reached: the empty name is the default, which is Kamal because the
// fleet runs Kamal today, and an unknown name is a usage error rather than a
// silent default — a typo in --engine is not a reason to run a drill against
// the wrong substrate, which is the one outcome a backup command must never
// produce by accident.
func TestRunnerForSelectsTheEngineTheConfigurationNames(t *testing.T) {
	for _, c := range []struct {
		name    string
		engine  string
		want    string
		wantErr bool
	}{
		{name: "the default is Kamal", engine: "", want: "kamal"},
		{name: "Kamal by name", engine: EngineKamal, want: "kamal"},
		{name: "CNPG by name", engine: EngineCNPG, want: "cnpg"},
		{name: "an unknown engine is refused", engine: "bogus", wantErr: true},
		{name: "engine names are exact", engine: "Kamal", wantErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			runner, err := RunnerFor(c.engine)
			if c.wantErr {
				if err == nil {
					t.Fatalf("RunnerFor(%q) returned a runner, want a usage error", c.engine)
				}
				if !errors.Is(err, ErrUsage) {
					t.Errorf("err = %v, want the usage sentinel so the caller exits 2", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("RunnerFor(%q): %v", c.engine, err)
			}
			got := EngineKamal
			if _, ok := runner.(*CNPGRunner); ok {
				got = EngineCNPG
			}
			if got != c.want {
				t.Errorf("RunnerFor(%q) returned the %s engine, want %s", c.engine, got, c.want)
			}
		})
	}
}

package deps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectGemfile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Gemfile"), []byte("gem 'rails'\ngem 'pg'\n"), 0644)
	got := Detect(dir)
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestDetectComposer(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"require":{"php":">=8.1","laravel/framework":"^10.0","ext-mbstring":"*"}}`), 0644)
	got := Detect(dir)
	if len(got) != 1 || got[0] != "laravel/framework" {
		t.Fatalf("got %v", got)
	}
}

func TestDetectPackageJson(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{"react":"^18","next":"^14"}}`), 0644)
	got := Detect(dir)
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestDetectEmpty(t *testing.T) {
	dir := t.TempDir()
	got := Detect(dir)
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

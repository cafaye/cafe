package skills

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cafaye/cafe/internal/version"
)

const (
	skillRelativePath = ".agents/skills/cafaye/SKILL.md"
	booksDirEnv       = "CAFAYE_BOOKS_DIR"
)

//go:embed assets/cafaye/SKILL.md
var cafayeSkillBody string

type InstallResult struct {
	Path    string
	Updated bool
}

func defaultBooksDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv(booksDirEnv)); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home, nil
}

func DefaultBooksDir() (string, error) {
	return defaultBooksDir()
}

func renderSkill() string {
	header := fmt.Sprintf(
		"<!-- managed-by: cafe-cli | cli_version: %s -->\n\n",
		version.Current,
	)
	return header + strings.TrimSpace(cafayeSkillBody) + "\n"
}

func installAtRoot(root string) (InstallResult, error) {
	target := filepath.Join(root, skillRelativePath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return InstallResult{}, err
	}

	content := renderSkill()
	prev, err := os.ReadFile(target)
	if err == nil && string(prev) == content {
		return InstallResult{Path: target, Updated: false}, nil
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{Path: target, Updated: true}, nil
}

func EnsureDefaultInstalled() (InstallResult, error) {
	root, err := defaultBooksDir()
	if err != nil {
		return InstallResult{}, err
	}
	return installAtRoot(root)
}

func InstallForRoot(root string) (InstallResult, error) {
	if strings.TrimSpace(root) == "" {
		return InstallResult{}, fmt.Errorf("root path cannot be empty")
	}
	return installAtRoot(root)
}
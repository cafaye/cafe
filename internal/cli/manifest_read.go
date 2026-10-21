package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cafaye/cafe/internal/contract"
)

// readManifest reads and validates one project's cafaye.yml. The validation is
// the contract linter's own, because cafe has no second opinion about what a
// valid manifest is.
func readManifest(project string) (contract.Manifest, error) {
	path := filepath.Join(project, contract.ManifestFileName())
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return contract.Manifest{}, fmt.Errorf("no %s in %s; a project declares its service in one (run %q to create it, or pass the project directory as the argument)",
				contract.ManifestFileName(), project, "cafe init")
		}
		return contract.Manifest{}, fmt.Errorf("read %s: %w", path, err)
	}
	manifest, finding := contract.CheckData(path, data)
	if !finding.OK() {
		return contract.Manifest{}, fmt.Errorf("%s", finding.String())
	}
	return manifest, nil
}

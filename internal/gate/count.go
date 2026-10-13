package gate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// ExpectedSkips is how many top-level tests the declared gate expects NOT to
// run, and it is the entire margin the ratchet allows:
//
//	declared - minimum == expectedSkips
//
// The eight, all structural rather than machine-dependent: the seven live tests
// (gated on CAF_LIVE_DOCKER / CAF_LIVE_RYUK / CAF_LIVE_KAMAL) and
// TestHolderChildReservesAndExits, which the parent re-execs and which skips in
// the parent run. They skip in every run of the declared gate, on every machine,
// by construction — which is what distinguishes them from the fifteen
// machine-capability skips in this tree ("this machine does not serve tcp6")
// that do not fire here and that a floor must still be entitled to count.
//
// It lives here, beside the counter the ratchet's left side is measured with,
// so the writer and the check that reads its output cannot disagree about the
// margin: `internal/ci` imports this constant rather than spelling 8 a second
// time.
const ExpectedSkips = 8

// CountTestFuncs returns how many top-level `func TestX(t *testing.T)` the tree
// declares.
//
// It is a STATIC count, and it is worth being clear about what it therefore does
// not prove: it cannot see a test behind a `//go:build` tag that is compiled out
// on this platform. Today the tagged files in this tree hold helpers and no
// `Test` function, so the count is the same on every platform.
func CountTestFuncs(root string) (int, error) {
	declared := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if NotASourceTree(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		declared += countTestFuncsInFile(path)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return declared, nil
}

// NotASourceTree is whether a directory holds no cafe test source, whatever it
// is called.
//
// `testdata` is here because the go toolchain excludes it from every package, and
// that is not a convention a caller is free to ignore. It stopped being
// theoretical when `internal/gen` began keeping its drift gate's goldens there as
// real Go files: the golden `telemetry_test.go` declares twelve top-level tests,
// `go test` runs none of them because they are not part of any package, and a
// count that included them came out twelve above the gate's.
//
// The other four names are for the ordinary reasons: version control, two
// package managers' dependency trees, and a vendored build.
func NotASourceTree(name string) bool {
	switch name {
	case ".git", "node_modules", "dist", "vendor", "testdata":
		return true
	}
	return false
}

// IsTestFunc is a top-level `func TestX(t *testing.T)`.
//
// The parameter type is the whole test. A name prefix is not enough, and this
// tree has the counterexample: `internal/ports/live_test.go` declares
// `func TestMain(m *testing.M)`, which is not a test `go test` prints a
// `--- PASS:` line for, and counting it would put a floor in `gate.yml` that no
// run could satisfy — a floor that is a wall, not a detector.
func IsTestFunc(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
		return false
	}
	params := fn.Type.Params
	if params == nil || len(params.List) != 1 {
		return false
	}
	star, ok := params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	// `testing.T` is a selector expression, not one identifier: the pointer to
	// the `T` of the `testing` package.
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "T"
}

// countTestFuncsInFile counts the test functions in one file. An unparseable file
// is a compile error the gate reports far more loudly than this could, and
// counting it would send the reader after a ratchet problem that does not exist —
// so it counts as none.
func countTestFuncsInFile(path string) int {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return 0
	}
	declared := 0
	for _, decl := range parsed.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && IsTestFunc(fn) {
			declared++
		}
	}
	return declared
}

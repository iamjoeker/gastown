package testenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// testMainDecl matches a TestMain definition at the start of a line, which is
// the only place Go allows one.
var testMainDecl = regexp.MustCompile(`(?m)^func TestMain\(`)

// TestEveryTestPackageGuardsProductionDolt is the enforcement half of the
// guard. GuardProductionDolt only protects a package that calls it, and Go has
// no way to inherit a TestMain, so without this check the tree drifts back
// toward the state that created six databases on the live server on
// 2026-08-18: 11 of 75 test packages isolated, the rest defaulting to :3307.
//
// It fails on two shapes:
//
//   - a package with tests and no TestMain at all, which inherits the default
//     one and therefore the production port;
//   - a file that declares a TestMain without calling GuardProductionDolt,
//     which is how a build-tagged variant (see internal/cmd, which has one
//     TestMain per tag set) silently opts a whole suite back out.
//
// The fix in both cases is the file the generator writes:
//
//	func TestMain(m *testing.M) {
//	    testenv.GuardProductionDolt()
//	    os.Exit(m.Run())
//	}
//
// SCOPE, which this check used to leave unstated. It walks every module in the
// repository, not just the one it lives in. Nested modules were skipped once,
// on the reasoning that they are "built and tested on their own terms" — but
// nothing tested them on those terms: `go test ./...` from the repo root
// reports "matched no packages" for a nested module, and `make test` is that
// command, so plugins/dolt-snapshots was covered by no gate at all while this
// check reported complete coverage of the repo (gt-05p1). A check that cannot
// see something has to say so; this one descends instead, because the scan is
// textual and a module boundary does not stop a filesystem walk.
//
// What a nested module cannot do is import testenv — internal/ is unimportable
// across a module boundary. It satisfies the same contract with its own
// GuardProductionDolt; see plugins/dolt-snapshots/testmain_guard_test.go, whose
// port constants TestNestedModuleGuardsMatchThisPackage holds against this
// package's.
func TestEveryTestPackageGuardsProductionDolt(t *testing.T) {
	root := moduleRoot(t)

	var unguarded, missing, nested []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if skipDir(root, path, d.Name()) {
			return filepath.SkipDir
		}

		rel, _ := filepath.Rel(root, path)
		if path != root && isModuleRoot(path) {
			nested = append(nested, rel)
		}

		scan := scanTestFiles(t, path)
		if !scan.hasTests {
			return nil
		}
		if !scan.hasTestMain {
			missing = append(missing, rel)
			return nil
		}
		for _, f := range scan.unguarded {
			unguarded = append(unguarded, filepath.Join(rel, f))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	// Stated, not assumed: these are the modules `go test ./...` does not
	// reach and this walk does. If the list is ever empty the boundary is the
	// root module and nothing is hidden behind a go.mod.
	if len(nested) > 0 {
		t.Logf("nested modules covered by this check but NOT by `go test ./...` from the repo root:\n  %s",
			strings.Join(nested, "\n  "))
	}

	if len(missing) > 0 {
		t.Errorf("packages with tests but no TestMain (they inherit the production Dolt port):\n  %s\n"+
			"a package in a nested module cannot import testenv; it declares its own "+
			"GuardProductionDolt instead (see plugins/dolt-snapshots/testmain_guard_test.go)",
			strings.Join(missing, "\n  "))
	}
	if len(unguarded) > 0 {
		t.Errorf("TestMain declarations that never call GuardProductionDolt:\n  %s",
			strings.Join(unguarded, "\n  "))
	}
}

// dirScan describes one directory's test files: whether it has any, whether
// any of them declares a TestMain, and which of those declarations do not
// guard. The three are distinct — a package with a guarded TestMain has
// hasTestMain set and unguarded empty, which is the only passing shape.
type dirScan struct {
	hasTests    bool
	hasTestMain bool
	unguarded   []string
}

func scanTestFiles(t *testing.T, dir string) dirScan {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var scan dirScan
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}
		scan.hasTests = true

		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", filepath.Join(dir, name), err)
		}
		src := string(data)
		if !testMainDecl.MatchString(src) {
			continue
		}
		scan.hasTestMain = true
		// Unqualified inside this package, qualified everywhere else.
		if !strings.Contains(src, "GuardProductionDolt()") {
			scan.unguarded = append(scan.unguarded, name)
		}
	}
	return scan
}

// skipDir reports whether a directory is outside the scope of this check:
// testdata and fixture trees, and vendored or generated dependency trees.
//
// A nested go.mod is deliberately NOT on this list. It was, and that is the
// whole of gt-05p1: the exclusion was invisible in the result, so a module
// nothing else tested read as covered.
func skipDir(root, path, name string) bool {
	if path == root {
		return false
	}
	switch name {
	case "testdata", "vendor", "node_modules", ".git":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// isModuleRoot reports whether a directory holds a go.mod, and so begins a
// module of its own.
func isModuleRoot(path string) bool {
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

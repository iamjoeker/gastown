package testutil

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenImports are packages testutil must never import, and why.
//
// testutil is imported by the test files of the packages it helps, so any
// import it takes on becomes a cycle the moment that package's own tests want
// a helper. The compiler does catch such a cycle — but only for the tag sets
// it is asked to build, and the test file that closes this one is behind
// //go:build integration. That is how internal/doltserver's entire
// integration suite sat unbuildable while every default-tag gate stayed green
// (gt-pp8o). This check answers under the default tags, where everything runs.
//
// The fix when it fires is not to drop the caller's helper: move the helper
// into a leaf package under internal/testutil/ that no test package imports
// back, as internal/testutil/doltreap does for doltserver.
var forbiddenImports = map[string]string{
	"github.com/steveyegge/gastown/internal/doltserver": "internal/doltserver's integration tests import testutil; use internal/testutil/doltreap instead",
}

func TestTestutilImportsNothingThatImportsItBack(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}

	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		// Test files are free to import anything: they are compiled into
		// this package's own test binary, not into what others import.
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// Every file is parsed whatever its build tags — the invariant has
		// to hold on every platform, not just the one running this test.
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquoting import %s: %v", name, spec.Path.Value, err)
			}
			if reason, bad := forbiddenImports[path]; bad {
				t.Errorf("%s imports %s: %s", filepath.Join("internal/testutil", name), path, reason)
			}
		}
	}
}

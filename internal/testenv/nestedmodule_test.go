package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// guardDecl matches the declaration of a guard entry point, which is what makes
// a file a nested module's copy of this package rather than an ordinary test.
var guardDecl = regexp.MustCompile(`(?m)^func GuardProductionDolt\(`)

// guardedConstants are the values a nested module's copy must agree with this
// package on, and the regexp that reads each one back out of source.
//
// Only the two ports are held. The variable list deliberately is not: a nested
// module guards the variables its own resolvers read, which is not necessarily
// this package's list — plugins/dolt-snapshots reads DOLT_PORT, which nothing
// in the main module does, and reads none of the BEADS_ names, which this
// package guards because gastown code and the bd subprocesses it spawns do.
// Requiring a verbatim copy of the list would have left the one route unique to
// that binary open.
var guardedConstants = []struct {
	name string
	want int
}{
	{"ProductionDoltPort", ProductionDoltPort},
	{"GuardedDoltPort", GuardedDoltPort},
}

// constDecl builds the pattern that reads a named port constant back out of
// source. It is deliberately loose about the surrounding form — leading
// indentation for a declaration inside a const block, an optional `const`
// keyword, an optional explicit type — because the value is the thing under
// test. A check that accepted only one spelling would fail a correct copy and
// send whoever wrote it looking for a drift that was not there.
func constDecl(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^\s*(?:const\s+)?` + name + `\s*(?:[\w.]+\s*)?=\s*(\d+)\b`)
}

// TestNestedModuleGuardsMatchThisPackage ties every reimplementation of the
// Dolt-port guard to the original.
//
// A module that does not require gastown cannot import testenv — internal/ does
// not cross a module boundary — so a nested module with tests satisfies
// TestEveryTestPackageGuardsProductionDolt by declaring its own
// GuardProductionDolt. That buys coverage at the cost of a second
// implementation, and the port numbers are the part where drift is silent in
// the worst direction: if production moved off 3307 and a copy did not follow,
// its tests would redirect away from a port nothing uses while writing happily
// to the new production one, with every gate green.
//
// This is the nested-module twin of TestProductionPortMatchesGuard in
// internal/doltserver, which holds this package's own copy against
// doltserver.DefaultPort.
func TestNestedModuleGuardsMatchThisPackage(t *testing.T) {
	root := moduleRoot(t)

	guards, err := findNestedGuards(root)
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}

	for _, file := range guards {
		rel, _ := filepath.Rel(root, file)
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		src := string(data)

		for _, c := range guardedConstants {
			m := constDecl(c.name).FindStringSubmatch(src)
			if m == nil {
				t.Errorf("%s declares GuardProductionDolt but no %s constant; "+
					"declare `const %s = %d` so drift from testenv.%s is detectable",
					rel, c.name, c.name, c.want, c.name)
				continue
			}
			got, err := strconv.Atoi(m[1])
			if err != nil {
				t.Errorf("%s: %s = %q, not a number", rel, c.name, m[1])
				continue
			}
			if got != c.want {
				t.Errorf("%s: %s = %d, testenv.%s = %d; "+
					"the copy has drifted from the guard it duplicates",
					rel, c.name, got, c.name, c.want)
			}
		}
	}
}

// findNestedGuards returns the test files in modules other than the one at root
// that declare a GuardProductionDolt of their own.
//
// It returns no error for finding none: a repository whose only module is the
// root one has nothing to hold together, and this test passes vacuously. That
// is the same state the check is meant to survive — it starts mattering when
// somebody adds the next nested module, which is exactly when nobody is
// thinking about the Dolt port.
func findNestedGuards(root string) ([]string, error) {
	var guards []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(root, path, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		// Files in the root module import this package instead of copying it,
		// so only a nested module can hold a second implementation.
		if !insideNestedModule(root, filepath.Dir(path)) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		if guardDecl.Match(data) {
			guards = append(guards, path)
		}
		return nil
	})
	return guards, err
}

// insideNestedModule reports whether dir belongs to a module other than the one
// rooted at root, by walking up to the nearest go.mod.
func insideNestedModule(root, dir string) bool {
	for dir != root && strings.HasPrefix(dir, root) {
		if isModuleRoot(dir) {
			return true
		}
		dir = filepath.Dir(dir)
	}
	return false
}

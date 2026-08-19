package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/doctor"
	"github.com/steveyegge/gastown/internal/doltserver"
)

// TestDoctorFixAgreesWithCleanupBalk is the regression test for gt-baj6: two
// commands reach the same bulk deletion of the live data plane, and the guards
// gt-xvh and gt-ti84 installed were on only one of them. `gt doctor --fix`
// force-deleted every orphan with no threshold check of any kind, so the six
// test fixtures that prompted gt-xhjb would have been deleted by a command whose
// name promises repair, without a single warning.
//
// One town, both surfaces, one answer.
func TestDoctorFixAgreesWithCleanupBalk(t *testing.T) {
	townRoot := newBalkTestTown(t)

	// What `gt dolt cleanup` does with this town.
	orphans, err := doltserver.FindOrphanedDatabases(townRoot)
	if err != nil {
		t.Fatalf("FindOrphanedDatabases: %v", err)
	}
	allDBs, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	// Control: the fixture only reproduces the defect at a ratio the cleanup
	// path refuses. If detection stopped flagging the six fixtures, both
	// surfaces would proceed and this test would pass for the wrong reason.
	balk := evaluateCleanupBalks(townRoot, orphans, allDBs, false)
	if balk == nil {
		t.Fatalf("fixture no longer reproduces the defect: cleanup would proceed with %d of %d flagged", len(orphans), len(allDBs))
	}
	if balk.Kind != doltserver.BalkOrphanRatio {
		t.Fatalf("expected the ratio balk, got kind %v", balk.Kind)
	}

	// What `gt doctor --fix` does with the same town.
	check := doctor.NewDoltOrphanedDatabaseCheck()
	ctx := &doctor.CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)
	if result.Status == doctor.StatusOK {
		t.Fatalf("doctor must see the same orphans cleanup does, got OK: %s", result.Message)
	}

	before := doltDataEntries(t, townRoot)

	if fixErr := check.Fix(ctx); fixErr == nil {
		t.Error("gt doctor --fix must refuse where gt dolt cleanup refuses, not delete")
	} else if !strings.Contains(fixErr.Error(), "refusing to remove") {
		t.Errorf("doctor's refusal must say it refused, got: %v", fixErr)
	}

	after := doltDataEntries(t, townRoot)
	if len(after) != len(before) {
		t.Fatalf("gt doctor --fix deleted %d databases the cleanup path refuses to touch", len(before)-len(after))
	}
	for _, o := range orphans {
		if _, statErr := os.Stat(filepath.Join(townRoot, ".dolt-data", o.Name)); statErr != nil {
			t.Errorf("%s was deleted by gt doctor --fix: %v", o.Name, statErr)
		}
	}
}

func doltDataEntries(t *testing.T, townRoot string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(townRoot, ".dolt-data"))
	if err != nil {
		t.Fatalf("reading .dolt-data: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

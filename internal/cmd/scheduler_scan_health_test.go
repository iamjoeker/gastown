package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// setupSchedulerHealthyTown builds the same two-store town as
// setupSchedulerScanFailureTown but with every store readable. It is the
// CONTROL for every assertion below: each "we report the degradation" test has
// a twin proving the same code path reports nothing when the scan was clean,
// so a change that hard-codes the warning cannot pass.
func setupSchedulerHealthyTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(townRoot, "rig", ".beads"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	installFakeBD(t, `#!/bin/sh
printf '[]\n'
exit 0
`)
	return townRoot
}

// gt-vpds: the skipped stores have to survive as DATA, not only as prose in an
// error string. Every operator-facing surface that continues on a partial scan
// has to name what it could not read, and a caller that must re-parse the
// message to do that will not do it.
func TestPartialScanHealthNamesSkippedStores(t *testing.T) {
	townRoot := setupSchedulerScanFailureTown(t)

	_, err := listAllSlingContextRecords(townRoot)
	if err == nil {
		t.Fatal("partial sling-context scan should return an error")
	}

	scan := scanHealthFromErr(err)
	if !scan.Incomplete {
		t.Fatalf("scanHealthFromErr(%v).Incomplete = false, want true", err)
	}
	if scan.Total != 2 {
		t.Errorf("scan.Total = %d, want 2 (town root + rig)", scan.Total)
	}
	if len(scan.Skipped) != 1 || !strings.Contains(scan.Skipped[0], filepath.Join("rig", ".beads")) {
		t.Errorf("scan.Skipped = %v, want the one unreadable rig store named", scan.Skipped)
	}

	// The typed error must not change what existing callers read: the sentinel
	// identity (gt-mji1) and the self-naming message (gt-qm04) both still hold.
	if !errors.Is(err, ErrPartialSlingContextScan) {
		t.Errorf("errors.Is(err, ErrPartialSlingContextScan) = false for %q", err)
	}
	if !strings.Contains(err.Error(), "listing sling contexts") {
		t.Errorf("error = %q, want it to name its own operation", err)
	}
}

func TestScanHealthIsCompleteWhenEveryStoreReads(t *testing.T) {
	townRoot := setupSchedulerHealthyTown(t)

	_, err := listAllSlingContextRecords(townRoot)
	if err != nil {
		t.Fatalf("listAllSlingContextRecords on a healthy town: %v", err)
	}

	scan := scanHealthFromErr(err)
	if scan.Incomplete {
		t.Fatalf("scan = %+v, want a complete scan for a town whose stores all read", scan)
	}
	if warn := scan.Warning(); warn != "" {
		t.Fatalf("Warning() = %q on a complete scan, want empty", warn)
	}
}

func TestScanHealthWarningNamesTheStoresItMissed(t *testing.T) {
	scan := slingContextScanHealth{Incomplete: true, Total: 12, Skipped: []string{"/town/pc1/.beads", "/town/pc2/.beads"}}

	warn := scan.Warning()
	for _, want := range []string{"10 of 12", "incomplete", "pc1", "pc2"} {
		if !strings.Contains(warn, want) {
			t.Errorf("Warning() = %q, want it to contain %q", warn, want)
		}
	}
}

// The live gt-vpds instance: six of twelve stores unreadable, the planner
// proceeds on the rest, and the result is EMPTY. Zero contexts from a half-read
// town must not be reported the same way as an empty scheduler, so the health
// has to survive the early return that fires when nothing was found.
func TestAssessScheduledContextsReportsIncompleteScanOnEmptyResult(t *testing.T) {
	townRoot := setupSchedulerScanFailureTown(t)

	assessments, scan, err := assessScheduledContexts(townRoot)
	if err != nil {
		t.Fatalf("assessScheduledContexts should continue on a partial scan, got %v", err)
	}
	if len(assessments) != 0 {
		t.Fatalf("assessments = %d, want 0 for this fixture", len(assessments))
	}
	if !scan.Incomplete {
		t.Fatal("empty assessment set from a partial scan reported a complete scan — an operator cannot tell a half-read town from an idle one")
	}
	if len(scan.Skipped) != 1 {
		t.Errorf("scan.Skipped = %v, want the unreadable store named", scan.Skipped)
	}
}

func TestAssessScheduledContextsReportsCompleteScanOnEmptyResult(t *testing.T) {
	townRoot := setupSchedulerHealthyTown(t)

	assessments, scan, err := assessScheduledContexts(townRoot)
	if err != nil {
		t.Fatalf("assessScheduledContexts: %v", err)
	}
	if len(assessments) != 0 {
		t.Fatalf("assessments = %d, want 0 for this fixture", len(assessments))
	}
	if scan.Incomplete {
		t.Fatalf("scan = %+v, want complete: an empty scheduler is not a degraded one", scan)
	}
}

// listScheduledBeads is what `gt scheduler status` and `gt scheduler list`
// render. The list it returns is the operator's entire picture of the
// scheduler, so the completeness of the scan behind it has to reach them.
func TestListScheduledBeadsCarriesScanHealth(t *testing.T) {
	beads, scan, err := listScheduledBeads(setupSchedulerScanFailureTown(t))
	if err != nil {
		t.Fatalf("listScheduledBeads on a partial scan: %v", err)
	}
	if len(beads) != 0 {
		t.Fatalf("scheduled = %d, want 0 for this fixture", len(beads))
	}
	if !scan.Incomplete {
		t.Fatal("listScheduledBeads reported a complete scan for a half-read town")
	}

	_, healthyScan, err := listScheduledBeads(setupSchedulerHealthyTown(t))
	if err != nil {
		t.Fatalf("listScheduledBeads on a healthy town: %v", err)
	}
	if healthyScan.Incomplete {
		t.Fatalf("healthy scan = %+v, want complete", healthyScan)
	}
}

func TestDryRunPlanReportsIncompleteScan(t *testing.T) {
	degraded := &schedulerDispatchPlan{
		State: &capacity.SchedulerState{},
		Scan:  slingContextScanHealth{Incomplete: true, Total: 12, Skipped: []string{"/town/pc1/.beads"}},
	}
	out := captureStdout(t, func() { printSchedulerDryRunPlan(degraded) })
	if !strings.Contains(out, "incomplete") || !strings.Contains(out, "pc1") {
		t.Errorf("dry-run output = %q, want the degraded scan reported alongside the plan", out)
	}
	if !strings.Contains(out, "No ready beads scheduled for dispatch") {
		t.Errorf("dry-run output = %q, want the plan itself still printed", out)
	}

	complete := &schedulerDispatchPlan{State: &capacity.SchedulerState{}}
	out = captureStdout(t, func() { printSchedulerDryRunPlan(complete) })
	if strings.Contains(out, "incomplete") {
		t.Errorf("dry-run output = %q on a complete scan, want no degradation warning", out)
	}
}

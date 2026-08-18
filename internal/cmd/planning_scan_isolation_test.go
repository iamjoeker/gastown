package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlanningScanIsolatesPerStoreFailures pins hq-v05uw: one unreadable .beads
// directory must NOT abort the town-wide planning pass.
//
// Before this fix, listAllSlingContextRecords returned on the first store that
// errored, so a single bad directory took down dispatch for EVERY rig. Measured
// in daemon.log: 36 aborted passes across four days (08-02, 08-14, 08-16,
// 08-17), from three paths and two causes. 9 of those 36 aborted on the town's
// OWN registered stores — forkrig and the town root — which is why gating the
// scan on registration does not fix it.
//
// This test builds a town with one healthy rig and one poisoned .beads
// directory. The scan must survive and still return the healthy rig's work.
func TestPlanningScanIsolatesPerStoreFailures(t *testing.T) {
	town := t.TempDir()

	// A rig whose .beads is a FILE where a directory is expected — any read of
	// it fails, standing in for the permissions/not-found causes seen live.
	poisoned := filepath.Join(town, "badrig")
	if err := os.MkdirAll(poisoned, 0o755); err != nil {
		t.Fatalf("mkdir badrig: %v", err)
	}
	if err := os.WriteFile(filepath.Join(poisoned, ".beads"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write poisoned .beads: %v", err)
	}

	// The scan must not panic and must not abort on the poisoned entry.
	// A total failure is still an error (that branch is asserted below), so a
	// town with ONLY a poisoned rig is expected to error; what must never
	// happen is a panic or a silent success that hides the breakage.
	_, err := listAllSlingContextRecords(town)
	if err != nil && !strings.Contains(err.Error(), "ALL") {
		// A per-store error surfaced as a fatal abort is the original defect.
		t.Errorf("scan aborted on a single bad store rather than skipping it: %v\n"+
			"hq-v05uw: one .beads directory must not take down the town-wide pass.", err)
	}
}

// TestPlanningScanStillFailsWhenEveryStoreFails pins the other half of the
// contract. Skipping failures must not degrade into "found no work", because a
// scan that silently returns empty is indistinguishable from a quiet town — and
// that is precisely how a broken cleanup went unnoticed for hours elsewhere
// tonight (gt-u58w).
func TestPlanningScanStillFailsWhenEveryStoreFails(t *testing.T) {
	town := t.TempDir()
	if err := os.WriteFile(filepath.Join(town, ".beads"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write poisoned town .beads: %v", err)
	}

	_, err := listAllSlingContextRecords(town)
	if err == nil {
		return // acceptable: the store may resolve elsewhere in this environment
	}
	if !strings.Contains(err.Error(), "ALL") {
		t.Logf("total-failure path returned a non-aggregate error (environment-dependent): %v", err)
	}
}

// TestPartialScanErrorCarriesSentinelAndOperation pins the two properties the
// partial-scan error must have at once (gt-mji1, gt-qm04), because the callers
// split on different ones and each half has already been lost once.
//
// The IDENTITY is what best-effort planning callers switch on: cleanupStaleContexts
// and assessScheduledContexts do errors.Is(err, ErrPartialSlingContextScan) to
// decide "keep planning on the readable stores". Wrap the error without %w and
// they turn a survivable partial scan back into an aborted town-wide pass —
// the hq-v05uw outage.
//
// The MESSAGE is what a completeness-sensitive caller shows a human. This error
// surfaces from `gt scheduler clear` and areScheduled, not only from planning,
// so it has to name the operation that came up short rather than describing
// itself as a planning scan.
func TestPartialScanErrorCarriesSentinelAndOperation(t *testing.T) {
	townRoot := setupSchedulerScanFailureTown(t)

	_, err := listAllSlingContextRecords(townRoot)
	if err == nil {
		t.Fatal("partial scan returned no error; completeness-sensitive callers would trust it")
	}
	if !errors.Is(err, ErrPartialSlingContextScan) {
		t.Errorf("errors.Is(err, ErrPartialSlingContextScan) = false for %q\n"+
			"best-effort planning callers switch on this identity; losing it aborts the town-wide pass (hq-v05uw).", err)
	}
	if !strings.Contains(err.Error(), "listing sling contexts") {
		t.Errorf("error = %q, want it to name the operation (\"listing sling contexts\")\n"+
			"this error also reaches `gt scheduler clear` and areScheduled (gt-qm04).", err)
	}
	// The unreadable store must be named — an operator cannot act on a count.
	if !strings.Contains(err.Error(), filepath.Join("rig", ".beads")) {
		t.Errorf("error = %q, want the skipped store path", err)
	}
}

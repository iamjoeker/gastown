package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupPartialScanFailureTown builds a town with three stores — the town root,
// a healthy rig holding one open sling context, and a rig whose store cannot be
// read — so a scan over it exercises the genuine PARTIAL failure case: some
// stores readable, at least one not.
//
// The earlier version of this test poisoned the ONLY store, so it passed
// through the total-failure branch and never exercised isolation at all.
func setupPartialScanFailureTown(t *testing.T) string {
	t.Helper()
	town := t.TempDir()
	for _, dir := range []string{
		filepath.Join(town, ".beads"),
		filepath.Join(town, "goodrig", ".beads"),
		filepath.Join(town, "badrig", ".beads"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	installFakeBD(t, `#!/bin/sh
case "$BEADS_DIR" in
  */badrig/.beads) echo "scan failed" >&2; exit 7 ;;
  */goodrig/.beads) printf '[{"id":"gt-ctx-good","title":"ctx","status":"open","priority":1}]\n'; exit 0 ;;
  *) printf '[]\n'; exit 0 ;;
esac
`)
	return town
}

// TestPlanningScanIsolatesPerStoreFailures pins hq-v05uw: one unreadable .beads
// directory must NOT abort the town-wide planning pass.
//
// Before that fix, the scan returned on the first store that errored, so a
// single bad directory took down dispatch for EVERY rig. Measured in
// daemon.log: 36 aborted passes across four days (08-02, 08-14, 08-16, 08-17),
// from three paths and two causes. 9 of those 36 aborted on the town's OWN
// registered stores — forkrig and the town root — which is why gating the scan
// on registration does not fix it.
//
// The scan must survive the poisoned store and still return the healthy rig's
// work, so best-effort planning callers have something to plan with.
func TestPlanningScanIsolatesPerStoreFailures(t *testing.T) {
	town := setupPartialScanFailureTown(t)

	records, err := listAllSlingContextRecords(town)
	if err != nil && !errors.Is(err, ErrPartialSlingContextScan) {
		t.Fatalf("scan aborted on a single bad store rather than skipping it: %v\n"+
			"hq-v05uw: one .beads directory must not take down the town-wide pass.", err)
	}
	if len(records) != 1 || records[0].issue.ID != "gt-ctx-good" {
		t.Fatalf("records = %+v, want the healthy rig's one context returned alongside the error", records)
	}
}

// TestPlanningScanReportsPartialFailureToCallers pins gt-mji1: isolating a
// per-store failure must not make the result LOOK complete. Callers that need
// completeness (areScheduled, scheduler clear) cannot tell "no such context"
// from "the store holding it was unreadable", so a partial scan has to be
// distinguishable from a clean one — by sentinel, and by an error that names
// the operation and which stores were skipped.
func TestPlanningScanReportsPartialFailureToCallers(t *testing.T) {
	town := setupPartialScanFailureTown(t)

	_, err := listAllSlingContextRecords(town)
	if err == nil {
		t.Fatal("partial scan reported success; a completeness-sensitive caller would trust an incomplete view")
	}
	if !errors.Is(err, ErrPartialSlingContextScan) {
		t.Fatalf("error = %v, want ErrPartialSlingContextScan so best-effort callers can distinguish it", err)
	}
	if !strings.Contains(err.Error(), "listing sling contexts") {
		t.Fatalf("error = %q, want it to name the operation so callers need not re-wrap it", err.Error())
	}
	if !strings.Contains(err.Error(), filepath.Join("badrig", ".beads")) {
		t.Fatalf("error = %q, want the skipped store named so an operator knows what to repair", err.Error())
	}
}

// TestPlanningScanStillFailsWhenEveryStoreFails pins the other half of the
// contract. Skipping failures must not degrade into "found no work", because a
// scan that silently returns empty is indistinguishable from a quiet town — and
// that is precisely how a broken cleanup went unnoticed for hours elsewhere
// (gt-u58w). A total failure is NOT the recoverable partial case, so it must
// not carry the sentinel that tells planning callers to continue.
func TestPlanningScanStillFailsWhenEveryStoreFails(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	installFakeBD(t, `#!/bin/sh
echo "scan failed" >&2
exit 7
`)

	records, err := listAllSlingContextRecords(town)
	if err == nil {
		t.Fatal("scan succeeded with every store unreadable; an empty result would read as a quiet town")
	}
	if errors.Is(err, ErrPartialSlingContextScan) {
		t.Fatalf("error = %v, want a total failure, not the partial sentinel that tells planning to continue", err)
	}
	if !strings.Contains(err.Error(), "ALL") {
		t.Fatalf("error = %q, want the aggregate total-failure error", err.Error())
	}
	if len(records) != 0 {
		t.Fatalf("records = %+v, want none on total failure", records)
	}
}

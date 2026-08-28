package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
)

// gt-ygb7: gt-ad32 taught the sling entry paths to follow a moved bead's live
// row, but the deferred queue behind them kept routing by id prefix. These
// exercise the scheduler-side halves — the batch lookup every scheduler surface
// reads from, and the cross-rig guard that runs at dispatch time.

// withRigPrefixes adds a rigs.json to an owner-town fixture so rigBeadsPrefix
// answers, which is what makes the cross-rig prefix guard live rather than
// degrading open.
func withRigPrefixes(t *testing.T, townRoot string, prefixes map[string]string) {
	t.Helper()
	rigsConfig := &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs:    map[string]config.RigEntry{},
	}
	for rigName, prefix := range prefixes {
		rigsConfig.Rigs[rigName] = config.RigEntry{
			AddedAt:     time.Now(),
			BeadsConfig: &config.BeadsConfig{Prefix: prefix},
		}
	}
	if err := os.MkdirAll(filepath.Join(townRoot, constants.DirMayor), 0755); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveRigsConfig(constants.MayorRigsPath(townRoot), rigsConfig); err != nil {
		t.Fatalf("save rigs.json: %v", err)
	}
}

// The reproduction: dn-cqu is closed in duly_noted and open in gastown. The
// prefix-routed batch lookup reports it closed, which is what cleanupStaleContexts
// reads as "stale-work-bead" before closing the context out from under the queue.
func TestAdoptMovedWorkBeadRowsFollowsTheLiveRow(t *testing.T) {
	townRoot := newOwnerTown(t)
	stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Title: "source copy", Status: "closed"},
		"gastown":    {Title: "the real work", Status: "open", Labels: []string{"gt:p1"}},
	})

	// What prefix routing produced before this call.
	rows := map[string]beadStatusInfo{
		"dn-cqu": {Status: "closed", Title: "source copy"},
	}
	adoptMovedWorkBeadRows(townRoot, []string{"dn-cqu"}, rows)

	got := rows["dn-cqu"]
	if got.Status != "open" {
		t.Errorf("Status = %q, want %q — the scheduler reaps any context whose work bead is not open", got.Status, "open")
	}
	if got.Title != "the real work" {
		t.Errorf("Title = %q, want the live row's title", got.Title)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "gt:p1" {
		t.Errorf("Labels = %v, want the live row's labels", got.Labels)
	}

	// Readiness must agree, or the row is repaired and still never dispatched.
	if !isScheduledWorkBeadReady("dn-cqu", got, true, map[string]bool{}, map[string]bool{}) {
		t.Error("adopted row is still not ready for dispatch")
	}

	// The answer has to reach the bd calls that follow in this process.
	if _, ok := beads.LookupBeadStore("dn-cqu"); !ok {
		t.Error("expected the live store to be registered for later reads and writes")
	}
}

// The control that keeps stale-context cleanup working: a bead genuinely closed
// everywhere must stay closed. If this passed by adopting anything non-open,
// completed work would never leave the queue.
func TestAdoptMovedWorkBeadRowsLeavesFinishedWorkClosed(t *testing.T) {
	townRoot := newOwnerTown(t)
	stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Title: "genuinely done", Status: "closed"},
	})

	rows := map[string]beadStatusInfo{"dn-cqu": {Status: "closed", Title: "genuinely done"}}
	adoptMovedWorkBeadRows(townRoot, []string{"dn-cqu"}, rows)

	if rows["dn-cqu"].Status != "closed" {
		t.Errorf("Status = %q, want closed — a finished bead must still be reaped", rows["dn-cqu"].Status)
	}
	if _, ok := beads.LookupBeadStore("dn-cqu"); ok {
		t.Error("a genuinely closed bead must not register an override")
	}
}

// The happy path must not pay for the fix: a live row in the prefix store is
// the answer, and no other store is read.
func TestAdoptMovedWorkBeadRowsSkipsLiveRows(t *testing.T) {
	townRoot := newOwnerTown(t)
	scans := stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Title: "still filed here", Status: "open"},
	})

	rows := map[string]beadStatusInfo{"dn-cqu": {Status: "open", Title: "still filed here"}}
	adoptMovedWorkBeadRows(townRoot, []string{"dn-cqu"}, rows)

	if rows["dn-cqu"].Title != "still filed here" {
		t.Errorf("a live prefix row was overwritten: %+v", rows["dn-cqu"])
	}
	if *scans != 0 {
		t.Errorf("scanned %d other stores for a bead whose prefix store is live", *scans)
	}
}

// The second, independent blocker: even with the row repaired, dispatch refused
// the bead on its prefix.
func TestValidatePendingBeadDispatchAcceptsMovedBead(t *testing.T) {
	townRoot := newOwnerTown(t)
	withRigPrefixes(t, townRoot, map[string]string{"gastown": "gt", "duly_noted": "dn"})
	stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Status: "closed"},
		"gastown":    {Status: "open"},
	})

	b := capacity.PendingBead{WorkBeadID: "dn-cqu", TargetRig: "gastown"}

	// The guard the fix has to clear, stated as the fixture rather than assumed:
	// gastown's registered prefix does not accept this id.
	if capacity.AcceptsPrefix("gt", b.WorkBeadID) {
		t.Fatal("fixture is wrong: the prefix guard must reject dn-cqu for a gt- rig")
	}

	if err := validatePendingBeadForDispatch(townRoot, b, false); err != nil {
		t.Errorf("dispatch refused the rig holding the live row: %v", err)
	}
}

// The refusal must survive for beads that really do belong to another rig,
// otherwise the guard has been removed rather than corrected.
func TestValidatePendingBeadDispatchStillRefusesCrossRig(t *testing.T) {
	townRoot := newOwnerTown(t)
	withRigPrefixes(t, townRoot, map[string]string{"gastown": "gt", "duly_noted": "dn"})
	stubBeadRows(t, townRoot, map[string]*beadInfo{
		"duly_noted": {Status: "open"},
	})

	b := capacity.PendingBead{WorkBeadID: "dn-cqu", TargetRig: "gastown"}
	err := validatePendingBeadForDispatch(townRoot, b, false)
	if !errors.Is(err, capacity.ErrCrossRigPrefix) {
		t.Errorf("err = %v, want ErrCrossRigPrefix for a bead whose live row is in duly_noted", err)
	}
}

// A bead in no store at all must not be adopted into the target rig by the
// ownership exception.
func TestBeadOwnedByRigUnknownBead(t *testing.T) {
	townRoot := newOwnerTown(t)
	stubBeadRows(t, townRoot, map[string]*beadInfo{})

	if beadOwnedByRig(townRoot, "gastown", "dn-cqu", nil) {
		t.Error("a bead that exists nowhere must not be claimed by the target rig")
	}
	if beadOwnedByRig(townRoot, "", "dn-cqu", nil) {
		t.Error("an unnamed rig must never own a bead")
	}
}

// The queue reader and the "already scheduled" line have to agree about what
// counts as queued: scheduledBeadInfoFromWork drops any bead that is not open,
// so scheduleBead must not answer a re-sling of one with a tick.
func TestSchedulerListDropsNonOpenWorkBeads(t *testing.T) {
	fields := &capacity.SlingContextFields{WorkBeadID: "dn-cqu", TargetRig: "gastown"}
	for _, status := range []string{"closed", "tombstone", string(beads.IssueStatusHooked)} {
		if _, ok := scheduledBeadInfoFromWork("ctx", fields, beadStatusInfo{Status: status}, true, true); ok {
			t.Errorf("status %q is listed as scheduled; scheduleBead's no-op message assumes it is not", status)
		}
	}
	if _, ok := scheduledBeadInfoFromWork("ctx", fields, beadStatusInfo{Status: "open"}, true, true); !ok {
		t.Error("an open work bead must be listed as scheduled")
	}
}

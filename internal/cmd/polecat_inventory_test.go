package cmd

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

func TestPolecatSessionSet(t *testing.T) {
	setupPolecatTestRegistry(t)
	sessions := newPolecatSessionSet([]string{
		"gt-thunder",
		"gt-crew-dom",
		"gp-mirelurk",
		"not-a-polecat",
	})

	if got, ok := sessions.lookup("gastown", "thunder"); !ok || got != "gt-thunder" {
		t.Fatalf("lookup gastown/thunder = %q, %v", got, ok)
	}
	if _, ok := sessions.lookup("gastown", "dom"); ok {
		t.Fatal("crew session should not be indexed as polecat")
	}
	if got := sessions.namesForRig("gastown"); len(got) != 1 || got[0] != "gt-thunder" {
		t.Fatalf("namesForRig(gastown) = %v", got)
	}
}

func TestBuildPolecatInventoryItem(t *testing.T) {
	setupPolecatTestRegistry(t)
	sessions := newPolecatSessionSet([]string{"gt-running"})
	tests := []struct {
		name         string
		polecatName  string
		fields       *beads.AgentFields
		activeWork   *beads.Issue
		wantState    polecat.State
		wantIssue    string
		wantVerdict  string
		wantReusable bool
		wantRecovery bool
		wantCapacity bool
		wantBlocker  string // substring that must appear in some blocker
	}{
		{
			// Nothing blocks and nothing was checked. This constructor reads
			// beads only, so it reports UNVERIFIED instead of the reuse gate's
			// SAFE_TO_NUKE/reusable — the two used to be indistinguishable in
			// the output while the gate refused polecats this surface listed as
			// reusable (gt-49dp).
			name:         "clean idle is unverified without git facts",
			polecatName:  "idle",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
			wantState:    polecat.StateIdle,
			wantVerdict:  polecat.WorkstateVerdictUnverified,
			wantReusable: false,
		},
		{
			name:         "hooked running is working capacity",
			polecatName:  "running",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
			activeWork:   &beads.Issue{ID: "gt-hook", Status: string(beads.IssueStatusHooked), Assignee: "gastown/polecats/running"},
			wantState:    polecat.StateWorking,
			wantIssue:    "gt-hook",
			wantVerdict:  polecat.WorkstateVerdictWorking,
			wantCapacity: true,
		},
		{
			name:         "open stopped is stalled capacity",
			polecatName:  "stopped",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
			activeWork:   &beads.Issue{ID: "gt-open", Status: string(beads.StatusOpen), Assignee: "gastown/polecats/stopped"},
			wantState:    polecat.StateStalled,
			wantIssue:    "gt-open",
			wantVerdict:  polecat.WorkstateVerdictNeedsRecovery,
			wantRecovery: true,
			wantCapacity: true,
		},
		{
			name:         "deferred protects without capacity",
			polecatName:  "deferred",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
			activeWork:   &beads.Issue{ID: "gt-deferred", Status: string(beads.StatusDeferred), Assignee: "gastown/polecats/deferred"},
			wantState:    polecat.StateIdle,
			wantIssue:    "gt-deferred",
			wantVerdict:  polecat.WorkstateVerdictNeedsRecovery,
			wantRecovery: true,
		},
		{
			name:         "hook fallback protects without capacity",
			polecatName:  "hookonly",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean), HookBead: "gt-old"},
			wantState:    polecat.StateIdle,
			wantVerdict:  polecat.WorkstateVerdictNeedsRecovery,
			wantRecovery: true,
		},
		{
			// A pause blocks reuse but is NOT a recovery condition, and this
			// surface runs no git — so it reports UNVERIFIED rather than the
			// NEEDS_RECOVERY it used to assert without measuring. The blocker
			// assertion is the load-bearing half: the whole defect in gt-fbgq was
			// agent_state going unreported by the surfaces that classify, and a
			// build that dropped the field entirely would also answer UNVERIFIED
			// here. Only check-recovery, which measures, upgrades this to
			// NEEDS_STATE_CLEAR.
			name:        "paused agent state blocks reuse and stays named",
			polecatName: "paused",
			fields:      &beads.AgentFields{AgentState: string(beads.AgentStatePaused), CleanupStatus: string(polecat.CleanupClean)},
			wantState:   polecat.StateIdle,
			wantVerdict: polecat.WorkstateVerdictUnverified,
			wantBlocker: "agent_state=paused",
		},
		{
			name:        "active mr is pending non capacity",
			polecatName: "pendingmr",
			fields:      &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean), ActiveMR: "gt-mr"},
			wantState:   polecat.StateIdle,
			wantVerdict: polecat.WorkstateVerdictPendingMR,
		},
		{
			name:         "done without active mr and clean cleanup is unverified",
			polecatName:  "done",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateDone), CleanupStatus: string(polecat.CleanupClean)},
			wantState:    polecat.StateDone,
			wantVerdict:  polecat.WorkstateVerdictUnverified,
			wantReusable: false,
		},
		{
			name:         "done without active mr blocks reuse when cleanup is dirty",
			polecatName:  "donedirty",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateDone), CleanupStatus: string(polecat.CleanupUnpushed)},
			wantState:    polecat.StateDone,
			wantVerdict:  polecat.WorkstateVerdictNeedsRecovery,
			wantRecovery: true,
			wantCapacity: true,
		},
		{
			name:        "done with active mr remains pending",
			polecatName: "donepending",
			fields:      &beads.AgentFields{AgentState: string(beads.AgentStateDone), CleanupStatus: string(polecat.CleanupClean), ActiveMR: "gt-mr"},
			wantState:   polecat.StateDone,
			wantVerdict: polecat.WorkstateVerdictPendingMR,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := buildPolecatInventoryItem("gastown", tt.polecatName, tt.fields, tt.activeWork, sessions, nil)
			if item.State != tt.wantState || item.Issue != tt.wantIssue || item.Disposition.Verdict != tt.wantVerdict || item.Disposition.Reusable != tt.wantReusable || item.Disposition.NeedsRecovery != tt.wantRecovery || item.Disposition.CountsTowardCapacity != tt.wantCapacity {
				t.Fatalf("item = %+v disposition=%+v", item, item.Disposition)
			}
			if tt.wantBlocker != "" && !slices.ContainsFunc(item.Disposition.Blockers, func(b string) bool {
				return strings.Contains(b, tt.wantBlocker)
			}) {
				t.Fatalf("blockers %q do not name %q", item.Disposition.Blockers, tt.wantBlocker)
			}
		})
	}
}

func TestBuildPolecatInventoryItemActiveWorkLookupErrorFailsClosed(t *testing.T) {
	item := buildPolecatInventoryItemFromEvidence(
		"gastown",
		"lookup",
		&beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
		polecatActiveWorkLookupError(errors.New("bd failed")),
		polecatSessionSet{},
		nil,
	)

	if item.Disposition.Reusable || item.Disposition.SafeToNuke || !item.Disposition.NeedsRecovery || item.Disposition.CountsTowardCapacity {
		t.Fatalf("lookup error disposition = %+v", item.Disposition)
	}
	if item.Disposition.Reason != "active-work" {
		t.Fatalf("reason = %q, want active-work", item.Disposition.Reason)
	}
	if len(item.Disposition.Blockers) != 1 || !strings.Contains(item.Disposition.Blockers[0], "lookup_error") {
		t.Fatalf("blockers = %v, want lookup_error", item.Disposition.Blockers)
	}
}

// TestBuildPolecatInventoryItemSurfacesRefusedMR is the gt-46rk regression.
//
// The witness read `gt polecat list --json` after a POLECAT_DONE and got
// verdict=SAFE_TO_NUKE needs_mq_submit=false for a polecat whose pushed branch
// had never entered the merge queue, because gt done had deliberately refused
// to open an MR against a source bead the agent had already closed. The refusal
// existed only in a mail. Recycling force-deletes branches, so that verdict was
// the destructive answer to a question this surface had never asked.
func TestBuildPolecatInventoryItemSurfacesRefusedMR(t *testing.T) {
	setupPolecatTestRegistry(t)
	fields := &beads.AgentFields{
		AgentState:      string(beads.AgentStateDone),
		CleanupStatus:   string(polecat.CleanupClean),
		Branch:          "polecat/dag/bd-uh0",
		LastSourceIssue: "bd-uh0",
		MRRefused:       true,
	}

	item := buildPolecatInventoryItem("gastown", "dag", fields, nil, polecatSessionSet{}, nil)

	if item.Disposition.Verdict != polecat.WorkstateVerdictNeedsMQSubmit {
		t.Fatalf("verdict = %q, want %q (disposition=%+v)",
			item.Disposition.Verdict, polecat.WorkstateVerdictNeedsMQSubmit, item.Disposition)
	}
	if !item.Disposition.NeedsMQSubmit || item.Disposition.SafeToNuke || item.Disposition.Reusable {
		t.Fatalf("refused polecat must not read safe/reusable: %+v", item.Disposition)
	}
	// gt-mkpm: this surface consults no merge queue, so it says so. The verdict
	// and every flag above are unchanged — only the words admit that the question
	// "does the branch still hold unsubmitted work?" was never asked here.
	if item.Disposition.MQStatus != "refused_closed_source_unchecked" {
		t.Fatalf("mq_status = %q, want refused_closed_source_unchecked", item.Disposition.MQStatus)
	}
	if item.Disposition.ReuseStatus != polecat.ReuseStatusMQUnchecked {
		t.Fatalf("reuse_status = %q, want %q — the did-not-look road must not wear the measured string",
			item.Disposition.ReuseStatus, polecat.ReuseStatusMQUnchecked)
	}

	// Control: the same polecat without the refusal record is the ordinary
	// completed polecat, and must NOT take the refusal path. If this arm ever
	// starts failing too, the test above is passing for the wrong reason.
	//
	// It reads UNVERIFIED rather than SAFE_TO_NUKE because this constructor runs
	// no git and no merge-queue check — the refusal is bead-recorded and fires
	// anywhere, but clearing it does not turn a bead-only surface into one that
	// looked (gt-49dp). The discrimination the control exists for is intact:
	// UNVERIFIED is not NEEDS_MQ_SUBMIT, and it still refuses reuse.
	clean := *fields
	clean.MRRefused = false
	control := buildPolecatInventoryItem("gastown", "dag", &clean, nil, polecatSessionSet{}, nil)
	if control.Disposition.Verdict != polecat.WorkstateVerdictUnverified {
		t.Fatalf("control verdict = %q, want UNVERIFIED (disposition=%+v)",
			control.Disposition.Verdict, control.Disposition)
	}
	if control.Disposition.NeedsMQSubmit || control.Disposition.MQStatus == "refused_closed_source" {
		t.Fatalf("control must not take the refusal path: %+v", control.Disposition)
	}
}

// TestBuildPolecatInventoryItemRescueMRClearsRefusal covers the recovery half:
// once someone submits the stranded branch by hand, the branch index sees the
// MR even though nothing wrote active_mr onto this polecat's agent bead.
func TestBuildPolecatInventoryItemRescueMRClearsRefusal(t *testing.T) {
	setupPolecatTestRegistry(t)
	fields := &beads.AgentFields{
		AgentState:    string(beads.AgentStateDone),
		CleanupStatus: string(polecat.CleanupClean),
		Branch:        "polecat/dag/bd-uh0",
		MRRefused:     true,
	}
	index := &polecatBranchMRIndex{
		openMR:    map[string]string{"polecat/dag/bd-uh0": "bd-wisp-4v7m"},
		submitted: map[string]bool{"polecat/dag/bd-uh0": true},
	}

	item := buildPolecatInventoryItem("gastown", "dag", fields, nil, polecatSessionSet{}, index)

	// An open MR against a live branch is a preserve order, not permission to
	// nuke: recycling would force-delete the branch the MR points at.
	if item.Disposition.Verdict != polecat.WorkstateVerdictPendingMR {
		t.Fatalf("verdict = %q, want PENDING_MR (disposition=%+v)", item.Disposition.Verdict, item.Disposition)
	}
	if item.ActiveMR != "bd-wisp-4v7m" {
		t.Fatalf("active_mr = %q, want bd-wisp-4v7m — the branch index is the only thing that knows", item.ActiveMR)
	}
	if item.Disposition.SafeToNuke {
		t.Fatalf("polecat with an open MR must not read safe to nuke: %+v", item.Disposition)
	}

	// Merged and closed: no longer pending, and the refusal is discharged
	// because the work demonstrably reached the queue.
	closedIndex := &polecatBranchMRIndex{
		openMR:    map[string]string{},
		submitted: map[string]bool{"polecat/dag/bd-uh0": true},
	}
	merged := buildPolecatInventoryItem("gastown", "dag", fields, nil, polecatSessionSet{}, closedIndex)
	// Discharging the refusal is as far as this surface can get. It ran no git
	// and no merge-queue check, so it reports UNVERIFIED rather than borrowing
	// the reuse gate's SAFE_TO_NUKE — which is the point: the refusal being
	// cleared is not the same claim as the polecat being safe (gt-49dp).
	if merged.Disposition.Verdict != polecat.WorkstateVerdictUnverified {
		t.Fatalf("merged verdict = %q, want UNVERIFIED (disposition=%+v)",
			merged.Disposition.Verdict, merged.Disposition)
	}
	if merged.Disposition.NeedsMQSubmit || merged.Disposition.Reason == "mq-refused-closed-source" {
		t.Fatalf("rescue MR must discharge the recorded refusal: %+v", merged.Disposition)
	}
	if merged.Disposition.SafeToNuke || merged.Disposition.Reusable {
		t.Fatalf("a surface that ran no git check must not authorize anything: %+v", merged.Disposition)
	}
}

// TestBuildPolecatInventoryItemHandedOff is the gt-mkpm regression.
//
// gastown/chrome: session gone, work bead still hooked, MR gt-wisp-1cmci OPEN in
// the refinery queue, refinery up. `gt polecat list` said "stalled" — the word
// for a session that died mid-work. beads/ace read the same way on a different
// rig, observed by a different witness, and BOTH flipped to "done" on the merge
// of their own MR with nothing else changed. So the divergence window is exactly
// the in-flight-MR window: it opens when the polecat hands off and closes when
// the refinery lands the work, which is precisely when a witness is most likely
// to be looking. A third observer took a reading inside the window and filed it
// as evidence for an unrelated pool-leak bead.
func TestBuildPolecatInventoryItemHandedOff(t *testing.T) {
	setupPolecatTestRegistry(t)
	fields := &beads.AgentFields{
		AgentState:    string(beads.AgentStateIdle),
		CleanupStatus: string(polecat.CleanupClean),
		Branch:        "polecat/chrome/gt-0g5r",
	}
	hooked := &beads.Issue{ID: "gt-0g5r", Status: string(beads.IssueStatusHooked), Assignee: "gastown/polecats/chrome"}
	openIndex := &polecatBranchMRIndex{
		openMR:    map[string]string{"polecat/chrome/gt-0g5r": "gt-wisp-1cmci"},
		submitted: map[string]bool{"polecat/chrome/gt-0g5r": true},
	}

	// Sessions is empty: the session is gone. That plus a hooked bead is what
	// used to be enough to print "stalled".
	item := buildPolecatInventoryItem("gastown", "chrome", fields, hooked, polecatSessionSet{}, openIndex)

	if item.State != polecat.StateHandedOff {
		t.Fatalf("state = %q, want %q — its work is sitting in the queue", item.State, polecat.StateHandedOff)
	}
	// The MR must be NAMED, not merely implied by the state word. This is what a
	// reader quotes, and "stalled" carried no pointer to the thing in flight.
	if !slices.ContainsFunc(item.Disposition.Blockers, func(b string) bool {
		return strings.Contains(b, "gt-wisp-1cmci")
	}) {
		t.Fatalf("blockers %v must name the in-flight MR", item.Disposition.Blockers)
	}
	// Not a waiver: the hook is still set and still reported. Handed-off changes
	// the WORD, not what blocks.
	if !slices.ContainsFunc(item.Disposition.Blockers, func(b string) bool {
		return strings.Contains(b, "gt-0g5r")
	}) {
		t.Fatalf("blockers %v must still name the hooked bead", item.Disposition.Blockers)
	}
	if item.Disposition.SafeToNuke || item.Disposition.Reusable {
		t.Fatalf("handed-off must never read safe or reusable: %+v", item.Disposition)
	}

	// THE CONTROL, and it is the arm that matters: a polecat whose session died
	// with NO merge request for its branch is genuinely stalled and must keep
	// saying so. Promoting on absence of evidence would be the same defect
	// pointing the other way — and this direction talks a witness out of looking
	// at a polecat that really did die.
	noMRIndex := &polecatBranchMRIndex{openMR: map[string]string{}, submitted: map[string]bool{}}
	stalled := buildPolecatInventoryItem("gastown", "chrome", fields, hooked, polecatSessionSet{}, noMRIndex)
	if stalled.State != polecat.StateStalled {
		t.Fatalf("state = %q, want stalled — no MR was found for this branch", stalled.State)
	}
	if stalled.Disposition.Verdict != polecat.WorkstateVerdictNeedsRecovery {
		t.Fatalf("a truly stalled polecat must still need recovery: %+v", stalled.Disposition)
	}

	// Second control: an UNCONSULTED queue (nil index) is not proof of anything
	// either. This is the reading every surface gets when the merge-queue listing
	// fails, and it must fall back to stalled rather than to the success word.
	unconsulted := buildPolecatInventoryItem("gastown", "chrome", fields, hooked, polecatSessionSet{}, nil)
	if unconsulted.State != polecat.StateStalled {
		t.Fatalf("state = %q, want stalled — the queue was never consulted", unconsulted.State)
	}
}

// TestBuildPolecatInventoryItemDeadSessionUnderOpenMR is the gt-9f67
// regression, and it runs on the SAME fixture as the gt-mkpm test above.
//
// That is the point of it. gt-mkpm established that a polecat whose session is
// gone with its work in the queue must be called "handed-off" and not "stalled",
// and that is still right — nothing here changes the word. What gt-mkpm did not
// separate is the two ways a polecat arrives in that shape, because in every
// bead fact they are identical:
//
//	ran gt done   -> hook CLEARED on the way out, open MR is the trace of success
//	died mid-work -> hook STILL SET, open MR was submitted on its behalf
//
// The second is the one gastown/chrome was in, and PENDING_MR / leave-alone told
// a witness to leave a dead agent alone for as long as its MR was in flight. The
// submission that produced that MR is itself the standing remedy for a convoy
// deadlock, so the remedy for one defect was arming the other.
//
// The word stays handed-off. The VERDICT stops being leave-alone.
func TestBuildPolecatInventoryItemDeadSessionUnderOpenMR(t *testing.T) {
	setupPolecatTestRegistry(t)
	fields := &beads.AgentFields{
		AgentState:    string(beads.AgentStateIdle),
		CleanupStatus: string(polecat.CleanupClean),
		Branch:        "polecat/chrome/gt-0g5r",
	}
	hooked := &beads.Issue{ID: "gt-0g5r", Status: string(beads.IssueStatusHooked), Assignee: "gastown/polecats/chrome"}
	openIndex := func() *polecatBranchMRIndex {
		return &polecatBranchMRIndex{
			openMR:    map[string]string{"polecat/chrome/gt-0g5r": "gt-wisp-1cmci"},
			submitted: map[string]bool{"polecat/chrome/gt-0g5r": true},
		}
	}

	// An enumerated session listing that does not contain this polecat. Both
	// production callers abort outright when `tmux list-sessions` fails, so a
	// non-nil set is a real enumeration and this absence is a real absence.
	dead := buildPolecatInventoryItem("gastown", "chrome", fields, hooked, polecatSessionSet{}, openIndex())

	if dead.Disposition.Verdict != polecat.WorkstateVerdictNeedsRecovery {
		t.Fatalf("verdict = %q, want NEEDS_RECOVERY — the session is gone and the hook is still set: %+v",
			dead.Disposition.Verdict, dead.Disposition)
	}
	if dead.Disposition.Reason != polecat.WorkstateReasonStalledPendingMR {
		t.Fatalf("reason = %q, want %q", dead.Disposition.Reason, polecat.WorkstateReasonStalledPendingMR)
	}
	// The dead session must be NAMED. A verdict that escalates for the right
	// reason but reports only the hook sends the reader looking for a stuck bead
	// instead of a dead agent, and every one of the three readings in gt-9f67
	// omitted the session entirely.
	if !slices.ContainsFunc(dead.Disposition.Blockers, func(b string) bool {
		return strings.Contains(b, "session_presence=absent")
	}) {
		t.Fatalf("blockers %v must name the dead session", dead.Disposition.Blockers)
	}
	// gt-mkpm's requirements are unchanged: the word, and both other blockers.
	if dead.State != polecat.StateHandedOff {
		t.Fatalf("state = %q, want %q — its work really is sitting in the queue", dead.State, polecat.StateHandedOff)
	}
	for _, want := range []string{"gt-wisp-1cmci", "gt-0g5r"} {
		if !slices.ContainsFunc(dead.Disposition.Blockers, func(b string) bool {
			return strings.Contains(b, want)
		}) {
			t.Fatalf("blockers %v must still name %s", dead.Disposition.Blockers, want)
		}
	}

	// THE CONTROL, and it isolates exactly one field. A nil session set is the
	// UNMEASURED road — nobody enumerated, so nothing has been shown about
	// whether this agent is alive. Every other input is byte-identical to the
	// case above, and the verdict must go back to leave-alone.
	//
	// Without this arm the test above would pass just as well if the guard fired
	// on the hook alone, which would revert gt-mkpm.
	unmeasured := buildPolecatInventoryItem("gastown", "chrome", fields, hooked, nil, openIndex())
	if unmeasured.Disposition.Verdict != polecat.WorkstateVerdictPendingMR {
		t.Fatalf("verdict = %q, want PENDING_MR — liveness was never measured, so nothing was ruled out: %+v",
			unmeasured.Disposition.Verdict, unmeasured.Disposition)
	}

	// Second control: a polecat that DID complete. Same dead session, same open
	// MR, no hook — which is what `gt done` leaves behind, and what the whole
	// reuse pool looks like in its steady state. It must stay leave-alone, or
	// every finished polecat escalates.
	completed := buildPolecatInventoryItem("gastown", "chrome", fields, nil, polecatSessionSet{}, openIndex())
	if completed.Disposition.Verdict != polecat.WorkstateVerdictPendingMR {
		t.Fatalf("verdict = %q, want PENDING_MR — a cleared hook is what completion looks like: %+v",
			completed.Disposition.Verdict, completed.Disposition)
	}
}

// TestBuildPolecatInventoryItemResolvesRecordedActiveMR is the gt-hx10
// regression.
//
// Every polecat carrying an active_mr reported its blocker as
// `active_mr=<id> status=unknown` — 19 of 19 town-wide, across two stores, and
// including a wisp observed seconds after creation. The string was a literal:
// this surface never read the MR's status at all. The cost is not the wrong
// word. A pointer at an MR that closed a day ago and a pointer at one filed
// seconds ago produced byte-identical output and the same PENDING_MR verdict,
// so the polecat whose MR was gone could never be told from the one whose MR
// was live, and it stayed out of the reuse pool permanently.
func TestBuildPolecatInventoryItemResolvesRecordedActiveMR(t *testing.T) {
	setupPolecatTestRegistry(t)

	// byID is what the index's reader answers from without a process call. In
	// production the queue listing fills it with MR beads and source issues fall
	// through to bd; here it stands in for both so the classifier can be
	// exercised without a store.
	newIndex := func(beadsByID ...*beads.Issue) *polecatBranchMRIndex {
		index := &polecatBranchMRIndex{
			openMR:    map[string]string{},
			submitted: map[string]bool{},
			byID:      map[string]*beads.Issue{},
		}
		for _, issue := range beadsByID {
			index.byID[issue.ID] = issue
		}
		return index
	}
	doneFields := func() *beads.AgentFields {
		return &beads.AgentFields{
			AgentState:      string(beads.AgentStateDone),
			CleanupStatus:   string(polecat.CleanupClean),
			Branch:          "polecat/slit/bd-6n5",
			ActiveMR:        "bd-wisp-6td",
			LastSourceIssue: "bd-6n5",
		}
	}

	tests := []struct {
		name        string
		index       *polecatBranchMRIndex
		wantVerdict string
		wantBlocker string
	}{
		{
			// The 17 gastown polecats: correctly pending, and now they say why.
			name: "open mr reports its real status",
			index: newIndex(
				&beads.Issue{ID: "bd-wisp-6td", Status: "open", Description: "branch: polecat/slit/bd-6n5\nsource_issue: bd-6n5\n"},
			),
			wantVerdict: polecat.WorkstateVerdictPendingMR,
			wantBlocker: "active_mr=bd-wisp-6td status=open",
		},
		{
			// beads/slit: MR closed by the 24h sweep, work bead closed with it.
			// Nothing is at risk and nothing else will ever revisit this field.
			name: "closed mr with terminal source stops blocking",
			index: newIndex(
				&beads.Issue{ID: "bd-wisp-6td", Status: "closed", Description: "branch: polecat/slit/bd-6n5\nsource_issue: bd-6n5\n"},
				&beads.Issue{ID: "bd-6n5", Status: "closed"},
			),
			// The active-MR blocker is gone, which is what this case is about.
			// What remains is this surface admitting it never ran git or the
			// merge-queue check — it stops blocking without claiming safety
			// (gt-49dp).
			wantVerdict: polecat.WorkstateVerdictUnverified,
			wantBlocker: "reuse_facts=unmeasured (no git or merge-queue check was run for this polecat)",
		},
		{
			// Fail-closed control. Same closed MR, but the work it was carrying
			// is still open — a rejected or reaped MR, not a merged one — so the
			// branch may hold the only copy and the polecat must stay blocked.
			name: "closed mr with live source keeps blocking",
			index: newIndex(
				&beads.Issue{ID: "bd-wisp-6td", Status: "closed", Description: "branch: polecat/slit/bd-6n5\nsource_issue: bd-6n5\n"},
				&beads.Issue{ID: "bd-6n5", Status: "open"},
			),
			wantVerdict: polecat.WorkstateVerdictPendingMR,
			wantBlocker: "active_mr=bd-wisp-6td status=closed source_issue=bd-6n5 source_status=open",
		},
		{
			// The queue was consulted and the MR is not in it — reaped. Absent
			// proof the source is terminal, that is still a blocker.
			name:        "missing mr is not an absence of risk",
			index:       newIndex(),
			wantVerdict: polecat.WorkstateVerdictPendingMR,
			wantBlocker: "active_mr=bd-wisp-6td status=missing source_issue=bd-6n5 source_status=missing",
		},
		{
			// The queue listing failed. "We never looked" must not be dressed up
			// as a status, and must never resolve toward safe.
			name:        "unconsulted queue reports unverified",
			index:       nil,
			wantVerdict: polecat.WorkstateVerdictPendingMR,
			wantBlocker: "active_mr=bd-wisp-6td status=unverified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			item := buildPolecatInventoryItem("beads", "slit", doneFields(), nil, polecatSessionSet{}, tt.index)
			if item.Disposition.Verdict != tt.wantVerdict {
				t.Fatalf("verdict = %q, want %q (disposition=%+v)", item.Disposition.Verdict, tt.wantVerdict, item.Disposition)
			}
			if tt.wantBlocker == "" {
				if len(item.Disposition.Blockers) != 0 {
					t.Fatalf("blockers = %v, want none", item.Disposition.Blockers)
				}
				return
			}
			if len(item.Disposition.Blockers) != 1 || item.Disposition.Blockers[0] != tt.wantBlocker {
				t.Fatalf("blockers = %v, want [%q]", item.Disposition.Blockers, tt.wantBlocker)
			}
			if strings.Contains(item.Disposition.Blockers[0], "status=unknown") {
				t.Fatalf("blocker still reports the literal status=unknown: %q", item.Disposition.Blockers[0])
			}
		})
	}
}

// TestBuildPolecatInventoryItemStaleActiveMRYieldsToLiveBranchMR covers the
// composition of gt-hx10 with gt-46rk: once a recorded active_mr is proven
// stale, the branch index gets its turn, and an MR someone else submitted for
// the same branch still preserves the polecat.
func TestBuildPolecatInventoryItemStaleActiveMRYieldsToLiveBranchMR(t *testing.T) {
	setupPolecatTestRegistry(t)
	index := &polecatBranchMRIndex{
		openMR:    map[string]string{"polecat/slit/bd-6n5": "bd-wisp-live"},
		submitted: map[string]bool{"polecat/slit/bd-6n5": true},
		byID: map[string]*beads.Issue{
			"bd-wisp-6td": {ID: "bd-wisp-6td", Status: "closed", Description: "branch: polecat/slit/bd-6n5\nsource_issue: bd-6n5\n"},
			"bd-6n5":      {ID: "bd-6n5", Status: "closed"},
		},
	}
	fields := &beads.AgentFields{
		AgentState:      string(beads.AgentStateDone),
		CleanupStatus:   string(polecat.CleanupClean),
		Branch:          "polecat/slit/bd-6n5",
		ActiveMR:        "bd-wisp-6td",
		LastSourceIssue: "bd-6n5",
	}

	item := buildPolecatInventoryItem("beads", "slit", fields, nil, polecatSessionSet{}, index)

	if item.Disposition.Verdict != polecat.WorkstateVerdictPendingMR {
		t.Fatalf("verdict = %q, want PENDING_MR (disposition=%+v)", item.Disposition.Verdict, item.Disposition)
	}
	if item.ActiveMR != "bd-wisp-live" {
		t.Fatalf("active_mr = %q, want bd-wisp-live — the stale pointer must not mask the live MR", item.ActiveMR)
	}
}

func TestNewPolecatBranchMRIndexPrefersOpenMR(t *testing.T) {
	// Two MRs for the same branch: an earlier rejected one and a live one.
	// hasMR must be true for both cases; openMRFor must name only the open one.
	index := &polecatBranchMRIndex{openMR: map[string]string{}, submitted: map[string]bool{}}
	for _, issue := range []*beads.Issue{
		{ID: "mr-old", Status: "closed", Description: "branch: polecat/a/x\ntarget: main\n"},
		{ID: "mr-new", Status: "open", Description: "branch: polecat/a/x\ntarget: main\n"},
		{ID: "mr-other", Status: "closed", Description: "branch: polecat/b/y\ntarget: main\n"},
	} {
		mrFields := beads.ParseMRFields(issue)
		index.submitted[mrFields.Branch] = true
		if issue.Status != "closed" && index.openMR[mrFields.Branch] == "" {
			index.openMR[mrFields.Branch] = issue.ID
		}
	}
	if got := index.openMRFor("polecat/a/x"); got != "mr-new" {
		t.Fatalf("openMRFor = %q, want mr-new", got)
	}
	if got := index.openMRFor("polecat/b/y"); got != "" {
		t.Fatalf("closed-only branch openMRFor = %q, want empty", got)
	}
	if !index.hasMR("polecat/b/y") {
		t.Fatal("a closed MR still proves the branch was submitted")
	}
	// A nil index means "not consulted" and must never answer confidently.
	var absent *polecatBranchMRIndex
	if absent.hasMR("polecat/a/x") || absent.openMRFor("polecat/a/x") != "" {
		t.Fatal("nil index must report unknown, not a negative")
	}
}

func TestPolecatSummaryIssueRankPrefersActiveWork(t *testing.T) {
	ordered := []*beads.Issue{
		{ID: "hook", Status: string(beads.IssueStatusHooked)},
		{ID: "progress", Status: string(beads.StatusInProgress)},
		{ID: "open", Status: string(beads.StatusOpen)},
		{ID: "blocked", Status: string(beads.StatusBlocked)},
		{ID: "deferred", Status: string(beads.StatusDeferred)},
	}
	for i := 1; i < len(ordered); i++ {
		if polecatSummaryIssueRank(ordered[i-1]) >= polecatSummaryIssueRank(ordered[i]) {
			t.Fatalf("rank(%s) should be before rank(%s)", ordered[i-1].Status, ordered[i].Status)
		}
	}
}

func TestPolecatNameFromAssignee(t *testing.T) {
	tests := []struct {
		assignee string
		wantName string
		wantOK   bool
	}{
		{assignee: "gastown/polecats/thunder", wantName: "thunder", wantOK: true},
		{assignee: "other/polecats/thunder"},
		{assignee: "gastown/crew/dom"},
		{assignee: "gastown/polecats/"},
		{assignee: "gastown/polecats/a/b"},
	}
	for _, tt := range tests {
		got, ok := polecatNameFromAssignee("gastown", tt.assignee)
		if got != tt.wantName || ok != tt.wantOK {
			t.Fatalf("polecatNameFromAssignee(%q) = %q, %v", tt.assignee, got, ok)
		}
	}
}

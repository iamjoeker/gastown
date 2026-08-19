package cmd

import (
	"errors"
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
	}{
		{
			name:         "clean idle reusable",
			polecatName:  "idle",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean)},
			wantState:    polecat.StateIdle,
			wantVerdict:  polecat.WorkstateVerdictSafeToNuke,
			wantReusable: true,
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
			name:         "paused agent state protects without capacity",
			polecatName:  "paused",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStatePaused), CleanupStatus: string(polecat.CleanupClean)},
			wantState:    polecat.StateIdle,
			wantVerdict:  polecat.WorkstateVerdictNeedsRecovery,
			wantRecovery: true,
		},
		{
			name:        "active mr is pending non capacity",
			polecatName: "pendingmr",
			fields:      &beads.AgentFields{AgentState: string(beads.AgentStateIdle), CleanupStatus: string(polecat.CleanupClean), ActiveMR: "gt-mr"},
			wantState:   polecat.StateIdle,
			wantVerdict: polecat.WorkstateVerdictPendingMR,
		},
		{
			name:         "done without active mr and clean cleanup is reusable",
			polecatName:  "done",
			fields:       &beads.AgentFields{AgentState: string(beads.AgentStateDone), CleanupStatus: string(polecat.CleanupClean)},
			wantState:    polecat.StateDone,
			wantVerdict:  polecat.WorkstateVerdictSafeToNuke,
			wantReusable: true,
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
	if item.Disposition.MQStatus != "refused_closed_source" {
		t.Fatalf("mq_status = %q, want refused_closed_source", item.Disposition.MQStatus)
	}

	// Control: the same polecat without the refusal record is the ordinary
	// completed polecat this surface has always called safe. If this arm ever
	// starts failing too, the test above is passing for the wrong reason.
	clean := *fields
	clean.MRRefused = false
	control := buildPolecatInventoryItem("gastown", "dag", &clean, nil, polecatSessionSet{}, nil)
	if control.Disposition.Verdict != polecat.WorkstateVerdictSafeToNuke {
		t.Fatalf("control verdict = %q, want SAFE_TO_NUKE (disposition=%+v)",
			control.Disposition.Verdict, control.Disposition)
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
	if merged.Disposition.Verdict != polecat.WorkstateVerdictSafeToNuke {
		t.Fatalf("merged verdict = %q, want SAFE_TO_NUKE (disposition=%+v)",
			merged.Disposition.Verdict, merged.Disposition)
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
			wantVerdict: polecat.WorkstateVerdictSafeToNuke,
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

package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// aliveSessions builds a sessionAlive lookup from a fixed set of live names.
func aliveSessions(names ...string) func(string) bool {
	live := map[string]bool{}
	for _, n := range names {
		live[n] = true
	}
	return func(name string) bool { return live[name] }
}

func queuedMRs(ids ...string) func(string) bool {
	queued := map[string]bool{}
	for _, id := range ids {
		queued[id] = true
	}
	return func(id string) bool { return queued[id] }
}

// TestClassifyTrackedIssue_DeferredIsNotReady pins the first half of gt-bel1:
// a deferred bead is waiting on a stated condition, and the old code read it as
// dispatchable work, which is how bd-sdj was slung five times.
func TestClassifyTrackedIssue_DeferredIsNotReady(t *testing.T) {
	got := classifyTrackedIssue(
		trackedIssueInfo{ID: "bd-sdj", Status: "deferred"},
		trackedIssueEnv{},
	)
	if got != dispoDeferred {
		t.Fatalf("classifyTrackedIssue(deferred) = %q, want %q", got, dispoDeferred)
	}
	if isReadyIssue(trackedIssueInfo{ID: "bd-sdj", Status: "deferred"}, nil) {
		t.Fatal("a deferred bead must never be reported as ready for dispatch")
	}
}

// TestClassifyTrackedIssue_DeferredOutranksMechanicalState checks the ordering:
// deferral is the stated intent, so it is the answer even when the bead is also
// blocked or already scheduled. Reporting "blocked" would send a reader to fix
// the blocker; nothing about the blocker is what is holding the bead.
func TestClassifyTrackedIssue_DeferredOutranksMechanicalState(t *testing.T) {
	env := trackedIssueEnv{scheduled: map[string]bool{"bd-sdj": true}}
	got := classifyTrackedIssue(
		trackedIssueInfo{ID: "bd-sdj", Status: "deferred", Blocked: true},
		env,
	)
	if got != dispoDeferred {
		t.Fatalf("classifyTrackedIssue() = %q, want %q", got, dispoDeferred)
	}
}

// TestClassifyTrackedIssue_SubmittedPolecatIsNotAbandoned pins the second half
// of gt-bel1: a polecat that pushed, submitted an MR, and exited leaves a hooked
// bead with a dead session behind. That is the ordinary end of a SUCCESSFUL run,
// and reading it as abandonment is the same inference gt-0g5r fixed in the
// stuck-agent dog.
func TestClassifyTrackedIssue_SubmittedPolecatIsNotAbandoned(t *testing.T) {
	tracked := trackedIssueInfo{
		ID:       "gt-f0b3",
		Status:   "hooked",
		Assignee: "gastown/polecats/chrome",
	}

	// Session dead, MR open in the queue → in-queue, not stranded.
	inQueue := classifyTrackedIssue(tracked, trackedIssueEnv{
		sessionAlive: aliveSessions(),
		queuedMR:     queuedMRs("gt-f0b3"),
	})
	if inQueue != dispoInQueue {
		t.Fatalf("submitted-and-awaiting-merge = %q, want %q", inQueue, dispoInQueue)
	}

	// Session dead and nothing queued → genuinely orphaned, still ready.
	orphan := classifyTrackedIssue(tracked, trackedIssueEnv{
		sessionAlive: aliveSessions(),
		queuedMR:     queuedMRs(),
	})
	if orphan != dispoReady {
		t.Fatalf("dead worker with no MR = %q, want %q", orphan, dispoReady)
	}
}

// TestClassifyTrackedIssue_LiveSessionIsWorking covers the case the live scan
// hit on gt-bel1's own convoy: an issue actively being worked reported 0 ready,
// which the surface then called "needs agent review".
func TestClassifyTrackedIssue_LiveSessionIsWorking(t *testing.T) {
	tracked := trackedIssueInfo{
		ID:       "gt-bel1",
		Status:   "hooked",
		Assignee: "gastown/polecats/brahmin",
	}
	sessionName, _ := assigneeToSessionName(tracked.Assignee)
	if sessionName == "" {
		t.Skip("assigneeToSessionName cannot resolve a session name in this environment")
	}

	got := classifyTrackedIssue(tracked, trackedIssueEnv{
		sessionAlive: aliveSessions(sessionName),
		queuedMR:     queuedMRs(),
	})
	if got != dispoWorking {
		t.Fatalf("live-session issue = %q, want %q", got, dispoWorking)
	}
}

func TestClassifyTrackedIssue_Dispositions(t *testing.T) {
	tests := []struct {
		name string
		in   trackedIssueInfo
		env  trackedIssueEnv
		want string
	}{
		{"closed", trackedIssueInfo{Status: "closed"}, trackedIssueEnv{}, dispoClosed},
		{"tombstone", trackedIssueInfo{Status: "tombstone"}, trackedIssueEnv{}, dispoClosed},
		{"unresolved", trackedIssueInfo{Status: trackedStatusUnknown}, trackedIssueEnv{}, dispoUnknown},
		{"blank status", trackedIssueInfo{Status: " "}, trackedIssueEnv{}, dispoUnknown},
		{"blocked", trackedIssueInfo{Status: "open", Blocked: true}, trackedIssueEnv{}, dispoBlocked},
		{
			name: "scheduled",
			in:   trackedIssueInfo{ID: "gt-1", Status: "open"},
			env:  trackedIssueEnv{scheduled: map[string]bool{"gt-1": true}},
			want: dispoScheduled,
		},
		{"open unassigned", trackedIssueInfo{Status: "open"}, trackedIssueEnv{}, dispoReady},
		{"orphaned molecule", trackedIssueInfo{Status: "in_progress"}, trackedIssueEnv{}, dispoReady},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyTrackedIssue(tc.in, tc.env); got != tc.want {
				t.Fatalf("classifyTrackedIssue() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConvoyReason(t *testing.T) {
	tests := []struct {
		name     string
		tracked  int
		evidence map[string]int
		want     string
	}{
		{"empty", 0, map[string]int{}, strandedReasonEmpty},
		{"ready work wins", 3, map[string]int{dispoReady: 1, dispoDeferred: 2}, strandedReasonFeedable},
		{"all closed", 2, map[string]int{dispoClosed: 2}, strandedReasonComplete},
		{"blocked needs review", 1, map[string]int{dispoBlocked: 1}, strandedReasonNeedsReview},
		{"unroutable needs review", 1, map[string]int{dispoUnknown: 1}, strandedReasonNeedsReview},
		{"town bead needs review", 1, map[string]int{dispoNotSlingable: 1}, strandedReasonNeedsReview},
		{"only deferred waits", 1, map[string]int{dispoDeferred: 1}, strandedReasonWaiting},
		{"only working waits", 1, map[string]int{dispoWorking: 1}, strandedReasonWaiting},
		{"only queued waits", 1, map[string]int{dispoInQueue: 1}, strandedReasonWaiting},
		{"only scheduled waits", 1, map[string]int{dispoScheduled: 1}, strandedReasonWaiting},
		{"done plus waiting waits", 2, map[string]int{dispoClosed: 1, dispoDeferred: 1}, strandedReasonWaiting},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := convoyReason(tc.tracked, tc.evidence); got != tc.want {
				t.Fatalf("convoyReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatEvidence(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]int
		want string
	}{
		{"empty", map[string]int{}, ""},
		{"single", map[string]int{dispoDeferred: 1}, "1 deferred"},
		{"stable order", map[string]int{dispoBlocked: 2, dispoWorking: 1}, "1 working, 2 blocked"},
		{"closed hidden beside others", map[string]int{dispoClosed: 3, dispoBlocked: 1}, "1 blocked"},
		{"closed shown alone", map[string]int{dispoClosed: 3}, "3 closed"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatEvidence(tc.in); got != tc.want {
				t.Fatalf("formatEvidence() = %q, want %q", got, tc.want)
			}
		})
	}
}

// writeConvoyScanMockBd installs a `bd` stub returning one convoy tracking one
// issue with the given status, and points routes.jsonl at a rig so the bead is
// slingable. Returns the town root.
func writeConvoyScanMockBd(t *testing.T, convoyID, issueID, issueStatus string) string {
	t.Helper()

	binDir := t.TempDir()
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	routes := `{"prefix":"bd-","path":"beads/mayor/rig"}` + "\n" +
		`{"prefix":"gt-","path":"gastown/mayor/rig"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(routes), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	script := `#!/bin/sh
i=0
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) eval "pos$i=\"$arg\""; i=$((i+1)) ;;
  esac
done

case "$pos0" in
  list)
    echo '[{"id":"` + convoyID + `","title":"Work: deferred docs follow-up"}]'
    ;;
  sql)
    echo '[{"depends_on_id":"` + issueID + `"}]'
    ;;
  dep)
    echo '[{"id":"` + issueID + `","title":"Tracked issue","status":"` + issueStatus + `","issue_type":"task","assignee":"","dependency_type":"tracks"}]'
    ;;
  show)
    echo '[{"id":"` + issueID + `","title":"Tracked issue","status":"` + issueStatus + `","issue_type":"task","assignee":"","blocked_by":[],"blocked_by_count":0,"dependencies":[]}]'
    ;;
esac
exit 0
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return townRoot
}

// TestScanConvoys_DeferredConvoyIsWaitingNotStranded reproduces hq-cv-26syc:
// one convoy, one deferred tracked issue. It must not appear in the stranded
// list — no dog can feed it and no review can clear it — and it must still be
// reported as waiting so the reader can tell "nothing to do" from "not looked at".
func TestScanConvoys_DeferredConvoyIsWaitingNotStranded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy test on Windows")
	}

	townRoot := writeConvoyScanMockBd(t, "hq-cv-26syc", "bd-sdj", "deferred")

	stranded, waiting, err := scanConvoys(townRoot)
	if err != nil {
		t.Fatalf("scanConvoys() error: %v", err)
	}

	if len(stranded) != 0 {
		t.Fatalf("a convoy whose only tracked issue is deferred must not be stranded, got %+v", stranded)
	}
	if len(waiting) != 1 {
		t.Fatalf("expected 1 waiting convoy, got %d", len(waiting))
	}
	if waiting[0].Reason != strandedReasonWaiting {
		t.Errorf("waiting convoy Reason = %q, want %q", waiting[0].Reason, strandedReasonWaiting)
	}
	if got := formatEvidence(waiting[0].Evidence); got != "1 deferred" {
		t.Errorf("waiting convoy evidence = %q, want %q", got, "1 deferred")
	}

	// The JSON contract is the stranded array alone: the daemon and the deacon
	// both treat every entry in it as needing action.
	encoded, err := json.Marshal(stranded)
	if err != nil {
		t.Fatalf("json.Marshal(stranded): %v", err)
	}
	if strings.Contains(string(encoded), "hq-cv-26syc") {
		t.Errorf("waiting convoy leaked into --json output: %s", encoded)
	}
}

// TestScanConvoys_ReadyConvoyStillStranded is the control for the test above:
// the same fixture with an open issue must still be flagged and fed. Without
// it, "0 stranded" would be indistinguishable from a scan that flags nothing.
func TestScanConvoys_ReadyConvoyStillStranded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy test on Windows")
	}

	townRoot := writeConvoyScanMockBd(t, "hq-cv-ready", "bd-open1", "open")

	stranded, waiting, err := scanConvoys(townRoot)
	if err != nil {
		t.Fatalf("scanConvoys() error: %v", err)
	}

	if len(waiting) != 0 {
		t.Fatalf("expected no waiting convoys, got %+v", waiting)
	}
	if len(stranded) != 1 {
		t.Fatalf("expected 1 stranded convoy, got %d", len(stranded))
	}
	if stranded[0].Reason != strandedReasonFeedable {
		t.Errorf("Reason = %q, want %q", stranded[0].Reason, strandedReasonFeedable)
	}
	if stranded[0].ReadyCount != 1 || len(stranded[0].ReadyIssues) != 1 {
		t.Errorf("ReadyCount = %d, ReadyIssues = %v, want 1 / [bd-open1]", stranded[0].ReadyCount, stranded[0].ReadyIssues)
	}
}

// TestScanConvoys_CompletedConvoyReportsComplete keeps the auto-close path
// alive: a convoy whose tracked issues are all closed still needs an action
// (closing itself), so it stays on the stranded list with a reason that names
// that action instead of "needs agent review".
func TestScanConvoys_CompletedConvoyReportsComplete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping convoy test on Windows")
	}

	townRoot := writeConvoyScanMockBd(t, "hq-cv-done", "bd-done1", "closed")

	stranded, _, err := scanConvoys(townRoot)
	if err != nil {
		t.Fatalf("scanConvoys() error: %v", err)
	}

	if len(stranded) != 1 {
		t.Fatalf("expected 1 stranded convoy, got %d", len(stranded))
	}
	if stranded[0].Reason != strandedReasonComplete {
		t.Errorf("Reason = %q, want %q", stranded[0].Reason, strandedReasonComplete)
	}
	if stranded[0].TrackedCount != 1 {
		t.Errorf("TrackedCount = %d, want 1", stranded[0].TrackedCount)
	}
}

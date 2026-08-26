package web

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/activity"
	convoyops "github.com/steveyegge/gastown/internal/convoy"
)

// The convoy panel used to decide a convoy's verdict from the AGE of the last
// activity event: green under 5 minutes, yellow to 10, red past it. Every
// convoy doing work slower than ten minutes therefore turned STUCK and stayed
// stuck until it completed — and a polecat reading code or running a gate emits
// nothing for far longer than that. These tests hold the panel to execution
// state instead (gt-skzk.1).

// TestConvoyWorkStatus_LivePolecatSilentForThirtyMinutes is acceptance 1.
//
// The activity assertion is the control: it proves the fixture really is in the
// state the old code called stuck, so a pass here is the classifier ignoring
// age rather than the fixture never being stale.
func TestConvoyWorkStatus_LivePolecatSilentForThirtyMinutes(t *testing.T) {
	silent := time.Now().Add(-30 * time.Minute)
	if got := activity.Calculate(silent).ColorClass; got != activity.ColorRed {
		t.Fatalf("fixture is not stale: activity color = %q, want %q", got, activity.ColorRed)
	}

	assignee := "gastown/polecats/brahmin"
	sessionName, _ := convoyops.AssigneeSessionName(assignee)
	if sessionName == "" {
		t.Fatalf("AssigneeSessionName(%q) returned no session name", assignee)
	}

	f := &LiveConvoyFetcher{townRoot: t.TempDir()}
	tracked := []trackedIssueInfo{
		{ID: "gt-1", Status: "in_progress", Assignee: assignee, LastActivity: silent},
	}
	env := convoyops.Env{SessionAlive: func(n string) bool { return n == sessionName }}

	got, evidence := f.convoyWorkStatus(tracked, env)
	if got != convoyops.WorkStatusWorking {
		t.Fatalf("work status = %q, want %q (evidence: %v)", got, convoyops.WorkStatusWorking, evidence)
	}
	if got == convoyops.WorkStatusStuck {
		t.Fatal("a convoy with a live polecat must never render stuck")
	}
}

// TestConvoyWorkStatus_QueuedMRWithNoLivePolecat is acceptance 3.
func TestConvoyWorkStatus_QueuedMRWithNoLivePolecat(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}
	tracked := []trackedIssueInfo{
		{ID: "gt-2", Status: "hooked", Assignee: "gastown/polecats/brahmin"},
	}
	env := convoyops.Env{
		SessionAlive: func(string) bool { return false },
		QueuedMR:     func(id string) bool { return id == "gt-2" },
	}

	got, evidence := f.convoyWorkStatus(tracked, env)
	if got != convoyops.WorkStatusInQueue {
		t.Fatalf("work status = %q, want %q (evidence: %v)", got, convoyops.WorkStatusInQueue, evidence)
	}
}

// TestConvoyWorkStatus_CompleteAndEmpty pins the two counting verdicts, which
// are the ones an operator reads as "nothing to do here".
func TestConvoyWorkStatus_CompleteAndEmpty(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}
	env := convoyops.Env{}

	if got, _ := f.convoyWorkStatus(nil, env); got != convoyops.WorkStatusEmpty {
		t.Errorf("no tracked beads = %q, want %q", got, convoyops.WorkStatusEmpty)
	}

	closed := []trackedIssueInfo{{ID: "gt-3", Status: "closed"}, {ID: "gt-4", Status: "closed"}}
	if got, _ := f.convoyWorkStatus(closed, env); got != convoyops.WorkStatusComplete {
		t.Errorf("all closed = %q, want %q", got, convoyops.WorkStatusComplete)
	}
}

// TestConvoyClassifierEnv_SurvivesAnUnreadableTmux checks the panel degrades in
// the direction that shows work rather than hides it: with no tmux server, no
// session is alive, so an assigned bead reads as ready for attention instead of
// as somebody's live work.
func TestConvoyClassifierEnv_SurvivesAnUnreadableTmux(t *testing.T) {
	originalRun := fetcherRunCmd
	originalScheduled := fetcherScheduledBeads
	originalQueued := fetcherHasQueuedMR
	t.Cleanup(func() {
		fetcherRunCmd = originalRun
		fetcherScheduledBeads = originalScheduled
		fetcherHasQueuedMR = originalQueued
	})

	fetcherRunCmd = func(time.Duration, string, ...string) (*bytes.Buffer, error) {
		return nil, errors.New("no server running")
	}
	fetcherScheduledBeads = func(string) (map[string]bool, error) { return map[string]bool{}, nil }
	fetcherHasQueuedMR = func(string, string) bool { return false }

	f := &LiveConvoyFetcher{townRoot: t.TempDir()}
	env := f.convoyClassifierEnv()
	if env.SessionAlive("gt-gastown-brahmin") {
		t.Fatal("no tmux server must not report a live session")
	}

	tracked := []trackedIssueInfo{{ID: "gt-5", Status: "hooked", Assignee: "gastown/polecats/brahmin"}}
	got, evidence := f.convoyWorkStatus(tracked, env)
	if evidence[convoyops.DispoWorking] != 0 {
		t.Fatalf("unreadable tmux reported %d working beads (evidence: %v)", evidence[convoyops.DispoWorking], evidence)
	}
	if got == convoyops.WorkStatusWorking || got == convoyops.WorkStatusInQueue {
		t.Fatalf("work status = %q — an unreadable tmux must not manufacture a worker", got)
	}
}

// TestLiveSessionNames_ParsesTheListing keeps the one tmux call honest: a
// membership test against a mis-parsed listing reads as "nobody is working",
// which is exactly the false verdict this bead removed.
func TestLiveSessionNames_ParsesTheListing(t *testing.T) {
	original := fetcherRunCmd
	t.Cleanup(func() { fetcherRunCmd = original })

	fetcherRunCmd = func(time.Duration, string, ...string) (*bytes.Buffer, error) {
		return bytes.NewBufferString("gt-gastown-brahmin\ngt-gastown-witness\n"), nil
	}

	f := &LiveConvoyFetcher{townRoot: t.TempDir()}
	live := f.liveSessionNames()
	if !live["gt-gastown-brahmin"] || !live["gt-gastown-witness"] {
		t.Fatalf("liveSessionNames() = %v, want both sessions", live)
	}
	if live[""] {
		t.Error("blank line parsed as a session name")
	}
}

// TestTrackedIssueIDs_PrefersTheDepTable pins the lookup that made the panel
// blind. `bd dep list -t tracks` joins against the issues table, so it returns
// nothing for a convoy tracking work in another Dolt database — the panel read
// zero tracked beads for every live convoy in town. The dep table is the
// primary read now, and bd is only the fallback.
func TestTrackedIssueIDs_PrefersTheDepTable(t *testing.T) {
	originalIDs := fetcherTrackedIssueIDs
	originalRun := fetcherRunCmd
	t.Cleanup(func() {
		fetcherTrackedIssueIDs = originalIDs
		fetcherRunCmd = originalRun
	})

	fetcherTrackedIssueIDs = func(_, convoyID string) ([]string, error) {
		if convoyID != "hq-cv-1" {
			t.Errorf("dep table queried for %q, want hq-cv-1", convoyID)
		}
		return []string{"gt-9"}, nil
	}

	// A bd that fails loudly if it runs at all: the dep table answered, so the
	// join must not be consulted.
	dir := t.TempDir()
	f := &LiveConvoyFetcher{townRoot: dir, cmdTimeout: 5 * time.Second, bdBin: fakeBd(t, "", 1)}
	ids, err := f.trackedIssueIDs("hq-cv-1")
	if err != nil {
		t.Fatalf("trackedIssueIDs() error = %v", err)
	}
	if len(ids) != 1 || ids[0] != "gt-9" {
		t.Fatalf("trackedIssueIDs() = %v, want [gt-9]", ids)
	}
}

// TestTrackedIssueIDs_FallsBackToBd covers stores that are not Dolt-in-server
// mode, where there is no server to query.
func TestTrackedIssueIDs_FallsBackToBd(t *testing.T) {
	originalIDs := fetcherTrackedIssueIDs
	originalRun := fetcherRunCmd
	t.Cleanup(func() {
		fetcherTrackedIssueIDs = originalIDs
		fetcherRunCmd = originalRun
	})

	fetcherTrackedIssueIDs = func(string, string) ([]string, error) {
		return nil, errors.New("missing server metadata")
	}

	dir := t.TempDir()
	f := &LiveConvoyFetcher{townRoot: dir, cmdTimeout: 5 * time.Second, bdBin: fakeBd(t, `[{"id":"external:gt:gt-7"},{"id":"gt-8"}]`, 0)}
	ids, err := f.trackedIssueIDs("hq-cv-2")
	if err != nil {
		t.Fatalf("trackedIssueIDs() error = %v", err)
	}
	if len(ids) != 2 || ids[0] != "gt-7" || ids[1] != "gt-8" {
		t.Fatalf("trackedIssueIDs() = %v, want [gt-7 gt-8] with the external ref unwrapped", ids)
	}
}

// TestTrackedIssueIDs_NamesBothFailures keeps a two-sided outage legible: "the
// dep table was unreachable" and "bd also failed" send an operator to different
// places, and an error that names only the second sends them to the wrong one.
func TestTrackedIssueIDs_NamesBothFailures(t *testing.T) {
	originalIDs := fetcherTrackedIssueIDs
	originalRun := fetcherRunCmd
	t.Cleanup(func() {
		fetcherTrackedIssueIDs = originalIDs
		fetcherRunCmd = originalRun
	})

	fetcherTrackedIssueIDs = func(string, string) ([]string, error) {
		return nil, errors.New("dolt is down")
	}

	dir := t.TempDir()
	f := &LiveConvoyFetcher{townRoot: dir, cmdTimeout: 5 * time.Second, bdBin: fakeBd(t, "bd exploded", 1)}
	_, err := f.trackedIssueIDs("hq-cv-3")
	if err == nil {
		t.Fatal("trackedIssueIDs() with both reads failing returned no error")
	}
	if !strings.Contains(err.Error(), "dolt is down") {
		t.Errorf("error does not name the dep-table failure: %v", err)
	}
	if !strings.Contains(err.Error(), "hq-cv-3") {
		t.Errorf("error does not name the convoy: %v", err)
	}
}

// fakeBd writes a stand-in bd that prints out and exits with code. runBdCmd
// execs bd directly rather than through fetcherRunCmd, so this is the seam.
//
// A failing bd writes to STDERR: runBdCmd deliberately returns stdout even on a
// non-zero exit, so a fake that fails on stdout is not a failing bd.
func fakeBd(t *testing.T, out string, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bd")
	stream := ""
	if code != 0 {
		stream = " >&2"
	}
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s%s\nexit %d\n", shellQuote(out), stream, code)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake bd: %v", err)
	}
	return path
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

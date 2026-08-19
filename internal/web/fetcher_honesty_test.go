package web

// Six panels used to answer a failed query with (nil, nil): no rows, no error.
// The handler then logged nothing, the template saw an empty slice, and the
// panel rendered exactly as it would on a genuinely quiet town (gt-egq9).
//
// Every panel gets two tests, and the CONTROL is the load-bearing half: an
// assertion that a real failure errors passes just as well against code that
// errors unconditionally, and a caveat shown on every render is decoration
// rather than information. So each failure test is paired with a source that is
// genuinely empty and must stay silent.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// tmux exits non-zero both when it cannot be reached and when there is simply
// no server, so these are the two error shapes the panels must tell apart.
// They are the real messages, routed through runCmd's stderr capture.
var (
	errTmuxNoServer = errors.New("tmux: exit status 1: no server running on /tmp/tmux-1000/gtsock")
	errTmuxUnusable = errors.New("tmux timed out after 2s")
)

// stubTmuxRunner answers tmux invocations with the given output or error, and
// every other command with empty output.
func stubTmuxRunner(t *testing.T, out string, runErr error) {
	t.Helper()

	original := fetcherRunCmd
	t.Cleanup(func() { fetcherRunCmd = original })

	fetcherRunCmd = func(_ time.Duration, name string, _ ...string) (*bytes.Buffer, error) {
		if name != "tmux" {
			return bytes.NewBufferString(""), nil
		}
		if runErr != nil {
			return nil, runErr
		}
		return bytes.NewBufferString(out), nil
	}
}

// tmuxPanelFetcher builds a fetcher whose town has one registered rig, a bd
// that answers every list with an empty array, and no merge queue — so a test
// of the tmux path fails on tmux and nothing else.
func tmuxPanelFetcher(t *testing.T) *LiveConvoyFetcher {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, `{"version": 1, "rigs": {"gastown": {"git_url": "git@github.com:o/gastown.git"}}}`)

	bdPath := filepath.Join(t.TempDir(), "bd")
	if err := os.WriteFile(bdPath, []byte("#!/bin/sh\nprintf '[]'\n"), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	originalMQ := fetcherListMergeRequests
	t.Cleanup(func() { fetcherListMergeRequests = originalMQ })
	fetcherListMergeRequests = func(string, beads.ListOptions) ([]*beads.Issue, error) {
		return nil, nil
	}

	return &LiveConvoyFetcher{
		townRoot:       townRoot,
		cmdTimeout:     30 * time.Second,
		tmuxCmdTimeout: 2 * time.Second,
		bdBin:          bdPath,
	}
}

// --- Sessions ---------------------------------------------------------------

func TestFetchSessions_UnreachableTmuxIsAnErrorNotAnEmptyList(t *testing.T) {
	f := tmuxPanelFetcher(t)
	stubTmuxRunner(t, "", errTmuxUnusable)

	rows, err := f.FetchSessions()
	if err == nil {
		t.Fatal("a tmux the dashboard could not ask must return an error, not an empty session list")
	}
	if !strings.Contains(err.Error(), "listing tmux sessions") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the query failed", rows)
	}
}

func TestFetchSessions_AbsentTmuxServerIsAQuietTown(t *testing.T) {
	// Control: no tmux server really does mean no sessions. Without this, the
	// panel would carry an "unreadable" caveat on every town whose tmux is down.
	f := tmuxPanelFetcher(t)
	stubTmuxRunner(t, "", errTmuxNoServer)

	rows, err := f.FetchSessions()
	if err != nil {
		t.Fatalf("an absent tmux server is an empty town, not a failure: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

func TestFetchSessions_RunningTmuxWithNoGasTownSessionsIsQuiet(t *testing.T) {
	// The other control: tmux answers, and what it lists is genuinely nothing
	// this dashboard tracks.
	f := tmuxPanelFetcher(t)
	stubTmuxRunner(t, "", nil)

	rows, err := f.FetchSessions()
	if err != nil {
		t.Fatalf("a successful empty query must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

// --- Workers ----------------------------------------------------------------

func TestFetchWorkers_UnreachableTmuxIsAnErrorNotAnEmptyList(t *testing.T) {
	f := tmuxPanelFetcher(t)
	stubTmuxRunner(t, "", errTmuxUnusable)

	rows, err := f.FetchWorkers()
	if err == nil {
		t.Fatal("a tmux the dashboard could not ask must return an error, not an empty worker list")
	}
	if !strings.Contains(err.Error(), "listing worker sessions") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the query failed", rows)
	}
}

func TestFetchWorkers_AbsentTmuxServerIsAQuietTown(t *testing.T) {
	f := tmuxPanelFetcher(t)
	stubTmuxRunner(t, "", errTmuxNoServer)

	rows, err := f.FetchWorkers()
	if err != nil {
		t.Fatalf("an absent tmux server is a town with no workers, not a failure: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

func TestFetchWorkers_RunningTmuxWithNoWorkersIsQuiet(t *testing.T) {
	f := tmuxPanelFetcher(t)
	stubTmuxRunner(t, "", nil)

	rows, err := f.FetchWorkers()
	if err != nil {
		t.Fatalf("a successful empty query must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

// --- Mayor ------------------------------------------------------------------

// TestFetchMayor_UnreachableTmuxDoesNotClaimDetached is the sharpest case in
// this family: the panel used to answer a tmux failure with the specific,
// confident claim that the Mayor is not attached.
func TestFetchMayor_UnreachableTmuxDoesNotClaimDetached(t *testing.T) {
	f := tmuxPanelFetcher(t)
	stubTmuxRunner(t, "", errTmuxUnusable)

	status, err := f.FetchMayor()
	if err == nil {
		t.Fatal("a tmux the dashboard could not ask must return an error, not a detached Mayor")
	}
	if status != nil {
		t.Errorf("status = %+v, want nil: 'not attached' is a claim this query cannot support", status)
	}
	if !strings.Contains(err.Error(), "checking mayor session") {
		t.Errorf("error should say what failed, got: %v", err)
	}
}

func TestFetchMayor_AbsentTmuxServerIsDetached(t *testing.T) {
	// Control: with no tmux server there is no mayor session, so "Detached" is
	// the truth rather than a guess.
	f := tmuxPanelFetcher(t)
	stubTmuxRunner(t, "", errTmuxNoServer)

	status, err := f.FetchMayor()
	if err != nil {
		t.Fatalf("an absent tmux server is a detached Mayor, not a failure: %v", err)
	}
	if status == nil || status.IsAttached {
		t.Errorf("status = %+v, want a detached Mayor", status)
	}
}

func TestFetchMayor_RunningTmuxWithoutMayorSessionIsDetached(t *testing.T) {
	f := tmuxPanelFetcher(t)
	stubTmuxRunner(t, "gt-gastown-fury:1755000000\n", nil)

	status, err := f.FetchMayor()
	if err != nil {
		t.Fatalf("a successful query must not error: %v", err)
	}
	if status == nil || status.IsAttached {
		t.Errorf("status = %+v, want a detached Mayor", status)
	}
}

// --- Queues -----------------------------------------------------------------

func TestFetchQueues_BdFailureIsAnErrorNotAnEmptyList(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	// Output on stderr only: runBdCmd forgives a non-zero exit that still
	// produced stdout, so a hard failure must produce none.
	f := fakeBdFetcher(t, `#!/bin/sh
echo "dial tcp 127.0.0.1:3307: connect: connection refused" >&2
exit 1
`)

	rows, err := f.FetchQueues()
	if err == nil {
		t.Fatal("a failed bd query must return an error, not an empty queue list")
	}
	if !strings.Contains(err.Error(), "listing queues") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the query failed", rows)
	}
}

func TestFetchQueues_NoQueuesIsNotAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	f := fakeBdFetcher(t, `#!/bin/sh
echo "[]"
`)

	rows, err := f.FetchQueues()
	if err != nil {
		t.Fatalf("a successful empty query must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

// --- Activity ---------------------------------------------------------------

func TestFetchActivity_UnreadableEventLogIsAnError(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}

	// A directory where the log should be: present, and unreadable as a file.
	if err := os.Mkdir(filepath.Join(f.townRoot, ".events.jsonl"), 0o755); err != nil {
		t.Fatalf("creating unreadable event log: %v", err)
	}

	rows, err := f.FetchActivity()
	if err == nil {
		t.Fatal("an unreadable event log must return an error, not an empty timeline")
	}
	if !strings.Contains(err.Error(), "reading event log") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the log could not be read", rows)
	}
}

func TestFetchActivity_MissingEventLogIsNotAnError(t *testing.T) {
	// Control, and the reason this is not simply "any ReadFile error": a town
	// that has not logged anything yet has no file, and that is not a failure.
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}

	rows, err := f.FetchActivity()
	if err != nil {
		t.Fatalf("a town with no event log yet must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

func TestFetchActivity_EmptyEventLogIsNotAnError(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}
	if err := os.WriteFile(filepath.Join(f.townRoot, ".events.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("writing empty event log: %v", err)
	}

	rows, err := f.FetchActivity()
	if err != nil {
		t.Fatalf("an empty event log must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

// --- Convoys ----------------------------------------------------------------

// The failure paths are covered by TestFetchConvoysBreakerBacksOffAfterBdFailures
// (a backed-off query reports that it read nothing). This is its control.
func TestFetchConvoys_NoConvoysIsNotAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	f := fakeBdFetcher(t, `#!/bin/sh
echo "[]"
`)

	rows, err := f.FetchConvoys()
	if err != nil {
		t.Fatalf("a successful empty query must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

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

	// The rig's directory has to exist or the union that backs the Workers
	// panel reports it as a failed store — a real caveat, but not the one these
	// tests are about, and it would mask the controls that assert silence.
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown"), 0o755); err != nil {
		t.Fatalf("create rig dir: %v", err)
	}

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

	result, err := f.FetchWorkers()
	if err == nil {
		t.Fatal("a tmux the dashboard could not ask must return an error, not an empty worker list")
	}
	if !strings.Contains(err.Error(), "listing worker sessions") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if result.Rows != nil {
		t.Errorf("rows = %v, want nil when the query failed", result.Rows)
	}
}

func TestFetchWorkers_AbsentTmuxServerIsAQuietTown(t *testing.T) {
	f := tmuxPanelFetcher(t)
	stubTmuxRunner(t, "", errTmuxNoServer)

	result, err := f.FetchWorkers()
	if err != nil {
		t.Fatalf("an absent tmux server is a town with no workers, not a failure: %v", err)
	}
	if len(result.Rows) != 0 {
		t.Errorf("rows = %v, want empty", result.Rows)
	}
	if result.Partial() {
		t.Errorf("a town with no tmux server has no partial-store caveat to make, got %q", result.Warning())
	}
}

func TestFetchWorkers_RunningTmuxWithNoWorkersIsQuiet(t *testing.T) {
	f := tmuxPanelFetcher(t)
	stubTmuxRunner(t, "", nil)

	result, err := f.FetchWorkers()
	if err != nil {
		t.Fatalf("a successful empty query must not error: %v", err)
	}
	if len(result.Rows) != 0 {
		t.Errorf("rows = %v, want empty", result.Rows)
	}
	if result.Partial() {
		t.Errorf("every store answered, so there is no caveat to make, got %q", result.Warning())
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

// --- Health -----------------------------------------------------------------
//
// The health panel fails differently from the rest: its stat renders only when
// health is known, so a swallowed error did not show a wrong heartbeat — it
// removed the liveness indicator from the banner, and an absent warning light
// reads as a working one (gt-xw1t).

func TestFetchHealth_UnreadableHeartbeatIsAnErrorNotAMissingOne(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}

	// A directory where the heartbeat should be: present, and unreadable as a file.
	if err := os.MkdirAll(filepath.Join(f.townRoot, "deacon", "heartbeat.json"), 0o755); err != nil {
		t.Fatalf("creating unreadable heartbeat: %v", err)
	}

	row, err := f.FetchHealth()
	if err == nil {
		t.Fatal("an unreadable heartbeat must return an error, not the claim that there is none")
	}
	if !strings.Contains(err.Error(), "reading deacon heartbeat") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if row != nil {
		t.Errorf("row = %+v, want nil: 'no heartbeat' is a claim this read cannot support", row)
	}
}

func TestFetchHealth_MalformedHeartbeatIsAnErrorNotACycleOfZero(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}
	writeDeaconFile(t, f.townRoot, "heartbeat.json", "{not json")

	row, err := f.FetchHealth()
	if err == nil {
		t.Fatal("a heartbeat that could not be parsed must return an error, not a zeroed HealthRow")
	}
	if !strings.Contains(err.Error(), "parsing deacon heartbeat") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if row != nil {
		t.Errorf("row = %+v, want nil", row)
	}
}

func TestFetchHealth_AbsentHeartbeatIsARealNoHeartbeat(t *testing.T) {
	// Control: a Deacon that has never beaten leaves no file, and that is a fact
	// about the town rather than a failure to look. Without this, the panel would
	// carry an "unreadable" caveat on every town whose Deacon has not started.
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}

	row, err := f.FetchHealth()
	if err != nil {
		t.Fatalf("a town with no heartbeat yet must not error: %v", err)
	}
	if row == nil || row.DeaconHeartbeat != "no heartbeat" {
		t.Errorf("row = %+v, want the stated absence 'no heartbeat'", row)
	}
}

func TestFetchHealth_ReadableHeartbeatIsReported(t *testing.T) {
	// The other control: a heartbeat that parses must reach the banner intact.
	f := &LiveConvoyFetcher{
		townRoot:                t.TempDir(),
		heartbeatFreshThreshold: 5 * time.Minute,
	}
	beat := time.Now().Add(-1 * time.Minute).Format(time.RFC3339)
	writeDeaconFile(t, f.townRoot, "heartbeat.json",
		`{"timestamp":"`+beat+`","cycle":42,"healthy_agents":3,"unhealthy_agents":1}`)

	row, err := f.FetchHealth()
	if err != nil {
		t.Fatalf("a readable heartbeat must not error: %v", err)
	}
	if row.DeaconCycle != 42 || row.HealthyAgents != 3 || row.UnhealthyAgents != 1 {
		t.Errorf("row = %+v, want the heartbeat's own numbers", row)
	}
	if !row.HeartbeatFresh {
		t.Error("a heartbeat one minute old is fresh")
	}
}

func TestFetchHealth_UnreadablePauseStateIsAnErrorNotAnUnpausedTown(t *testing.T) {
	// "Not paused" is what a false IsPaused renders as, so a pause file the
	// dashboard could not read must not produce one.
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}
	pauseDir := filepath.Join(f.townRoot, ".runtime", "deacon", "paused.json")
	if err := os.MkdirAll(pauseDir, 0o755); err != nil {
		t.Fatalf("creating unreadable pause state: %v", err)
	}

	row, err := f.FetchHealth()
	if err == nil {
		t.Fatal("an unreadable pause file must return an error, not an unpaused town")
	}
	if !strings.Contains(err.Error(), "deacon pause state") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if row != nil {
		t.Errorf("row = %+v, want nil", row)
	}
}

func TestFetchHealth_AbsentPauseFileIsARunningTown(t *testing.T) {
	// Control: no pause file is the ordinary state of a town that is running.
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}

	row, err := f.FetchHealth()
	if err != nil {
		t.Fatalf("a town with no pause file must not error: %v", err)
	}
	if row.IsPaused {
		t.Error("no pause file means the town is not paused")
	}
}

// writeDeaconFile writes one of the Deacon's state files under the town root.
func writeDeaconFile(t *testing.T, townRoot, name, contents string) {
	t.Helper()

	deaconDir := filepath.Join(townRoot, "deacon")
	if err := os.MkdirAll(deaconDir, 0o755); err != nil {
		t.Fatalf("creating deacon dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deaconDir, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// --- Rigs -------------------------------------------------------------------
//
// The rig list is the town's roster, and it is also what the merge queue walks,
// so an unreadable rigs.json empties two panels at once.

func TestFetchRigs_UnreadableConfigIsAnErrorNotAnEmptyTown(t *testing.T) {
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}
	writeRigsConfig(t, f.townRoot, `{"version": 1, "rigs": {`)

	rows, err := f.FetchRigs()
	if err == nil {
		t.Fatal("an unreadable rigs config must return an error, not a town with no rigs")
	}
	if !strings.Contains(err.Error(), "loading rigs config") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the config could not be read", rows)
	}
}

func TestFetchRigs_NoRegisteredRigsIsNotAnError(t *testing.T) {
	// Control: `gt install` writes rigs.json before any rig is added, so an
	// empty roster is a real zero and must render as one.
	f := &LiveConvoyFetcher{townRoot: t.TempDir()}
	writeRigsConfig(t, f.townRoot, `{"version": 1, "rigs": {}}`)

	rows, err := f.FetchRigs()
	if err != nil {
		t.Fatalf("a town with no rigs registered must not error: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty", rows)
	}
}

// --- Mail -------------------------------------------------------------------

func TestFetchMail_BdFailureIsAnErrorNotAQuietTown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	f := fakeBdFetcher(t, `#!/bin/sh
echo "dial tcp 127.0.0.1:3307: connect: connection refused" >&2
exit 1
`)

	rows, err := f.FetchMail()
	if err == nil {
		t.Fatal("a failed bd query must return an error, not an empty mail list")
	}
	if !strings.Contains(err.Error(), "listing mail") {
		t.Errorf("error should say what failed, got: %v", err)
	}
	if rows != nil {
		t.Errorf("rows = %v, want nil when the query failed", rows)
	}
}

func TestFetchMail_NoMailIsNotAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	f := fakeBdFetcher(t, `#!/bin/sh
echo "[]"
`)

	rows, err := f.FetchMail()
	if err != nil {
		t.Fatalf("a successful empty query must not error: %v", err)
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

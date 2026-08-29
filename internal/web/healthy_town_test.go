package web

// Nothing in this package asserted that a HEALTHY town renders as healthy.
//
// Every dashboard defect found on 2026-08-25 was a FALSE alarm — panels crying
// stuck over working polecats, a Work count that could not fall — and a suite
// oriented around "does it show problems" cannot catch a surface that shows
// problems which are not there. Every existing test here drives one panel from
// one stubbed source; none of them assembles a whole town and asks what the
// operator would see.
//
// So this file builds one town in a known-good state, renders the whole
// dashboard from the real LiveConvoyFetcher, and asserts the page is quiet.
// Then it mutates ONE fact at a time and asserts that the indicator for that
// fact — and only that indicator — moves.
//
// The two halves are load-bearing in opposite directions and neither is worth
// much alone. The quiet assertion alone passes against a dashboard wired to
// report nothing; the mutations alone pass against a dashboard that reports
// everything. Together they say each signal fires when it should AND stays
// silent when it should not, which is the property the false alarms violated.
//
// The baseline also asserts that the fixture is NOT empty — convoys, workers,
// hooks, mail and a queued MR all present. A healthy-town test over an empty
// town is vacuous: it would stay green against panels that had stopped reading
// anything at all, which is the failure one door down from the one it is here
// to catch.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
)

// --- The fixture ------------------------------------------------------------

// fakeSession is one tmux session the fixture town is running.
type fakeSession struct {
	name     string
	activity time.Time
}

// townFixture describes a whole town: what each beads store answers, what tmux
// is running, what the merge queue holds, and which sources are broken.
//
// Mutations are edits to this struct before build, so a test names the one fact
// it changed rather than reaching into a running stub.
type townFixture struct {
	// stores maps store name (townStoreName, or a rig name) to that store's
	// canned bd answers, keyed by query. See fixtureBdScript for the keys.
	stores map[string]fakeBdStore

	// failStores names stores whose bd exits non-zero with no output — an
	// unreachable store.
	failStores []string

	// failQueries names, per store, the individual queries that fail while the
	// rest of that store still answers. A bad label or a schema mismatch fails
	// one panel; a whole-store failure cannot isolate that.
	failQueries map[string][]string

	// sessions is what tmux lists.
	sessions []fakeSession

	// issueDetails answers `bd show <ids> --json`, which the convoy panel uses
	// to resolve its tracked issues. Keyed by bead ID.
	issueDetails map[string]map[string]any

	// mergeRequests maps rig name to that rig's open MR beads.
	mergeRequests map[string][]*beads.Issue

	// heartbeatAge is how old the Deacon's last heartbeat is.
	heartbeatAge time.Duration

	// events are the lines of .events.jsonl.
	events []map[string]any
}

// The fixture town's shape, named once so the tests can refer to it.
const (
	fixtureRigGastown = "gastown"
	fixtureRigBeads   = "beads"

	// The work backlog the dashboard must report, counted by hand from the
	// fixtures below: town 2 open, gastown 4 open + 2 hooked, beads 3 open + 1
	// hooked. The convoy bead and the three mail beads in the town store are Gas
	// Town plumbing and are not work.
	fixtureWorkCount = 2 + 4 + 2 + 3 + 1

	// Hooked beads across all three stores.
	fixtureHookCount = 3

	// Worker sessions: three polecats and the refinery.
	fixtureWorkerCount = 4
)

// newHealthyTown builds a town in a known-good state: polecats working, an MR
// queued, a convoy mid-flight, no escalations, every store readable.
//
// Backlog beads are priority 3. That is a deliberate choice and not a neutral
// one: HasAlerts counts a P1 or P2 bead as something needing attention, so a
// town with ordinary high-priority work in its backlog can never render "all
// clear". Whether that is right belongs to the banner's own bead; here a P2
// would only mask the signals this file is trying to isolate.
func newHealthyTown() *townFixture {
	now := time.Now()

	return &townFixture{
		stores: map[string]fakeBdStore{
			townStoreName: {
				// One open list serves both the backlog and the convoy panel, as
				// it does against live bd: the same `bd list --status=open`
				// returns the convoy bead, the mail and the work together.
				"list--open": concatBeads(
					[]map[string]any{convoyBead("hq-cv-1", "ship the dashboard")},
					mailBeads("hq-msg", 3),
					workBeads("town", 2),
				),
				"list-gt:message-":        mailBeads("hq-msg", 3),
				"dep-hq-cv-1":             {{"id": "gt-w1"}, {"id": "gt-w2"}},
				"list-gt:escalation-open": {},
				"list-gt:queue-":          {},
			},
			fixtureRigGastown: {
				"list--open":   workBeads(fixtureRigGastown, 4),
				"list--hooked": hookedWork(fixtureRigGastown, []string{"furiosa", "nux"}, now.Add(-10*time.Minute)),
			},
			fixtureRigBeads: {
				"list--open":   workBeads(fixtureRigBeads, 3),
				"list--hooked": hookedWork(fixtureRigBeads, []string{"slit"}, now.Add(-10*time.Minute)),
			},
		},
		failQueries: map[string][]string{},
		sessions: []fakeSession{
			{name: "hq-mayor", activity: now.Add(-1 * time.Minute)},
			{name: "hq-deacon", activity: now.Add(-1 * time.Minute)},
			{name: "gt-witness", activity: now.Add(-1 * time.Minute)},
			{name: "gt-refinery", activity: now.Add(-2 * time.Minute)},
			{name: "gt-furiosa", activity: now.Add(-30 * time.Second)},
			{name: "gt-nux", activity: now.Add(-90 * time.Second)},
			{name: "bd-slit", activity: now.Add(-2 * time.Minute)},
		},
		issueDetails: map[string]map[string]any{
			"gt-w1": {"id": "gt-w1", "title": "first half", "status": "closed"},
			"gt-w2": {
				"id":         "gt-w2",
				"title":      "second half",
				"status":     "open",
				"assignee":   fixtureRigGastown + "/polecats/furiosa",
				"updated_at": now.Add(-2 * time.Minute).Format(time.RFC3339),
			},
		},
		mergeRequests: map[string][]*beads.Issue{
			fixtureRigGastown: {mrBead("gt-mr-1", "merge second half", map[string]string{
				"branch":       "polecat/furiosa",
				"target":       "main",
				"source_issue": "gt-w2",
				"worker":       "furiosa",
				"rig":          fixtureRigGastown,
			})},
		},
		heartbeatAge: time.Minute,
		events: []map[string]any{
			{
				"ts": now.Add(-8 * time.Minute).Format(time.RFC3339), "type": "sling", "actor": "mayor/",
				"payload": map[string]any{"bead": "gt-w2", "target": fixtureRigGastown + "/polecats/furiosa"},
			},
			{
				"ts": now.Add(-3 * time.Minute).Format(time.RFC3339), "type": "merged", "actor": fixtureRigGastown + "/refinery",
				"payload": map[string]any{"branch": "polecat/nux"},
			},
		},
	}
}

// build writes the fixture to disk, installs the command stubs, and returns a
// handler over a real LiveConvoyFetcher pointed at it.
//
// The fetcher is the production one on purpose. A mock fetcher would let the
// panels agree with a test's idea of them rather than with bd, and the defects
// this file exists to catch all live in the step between what bd answered and
// what the panel concluded from it.
func (tf *townFixture) build(t *testing.T) *ConvoyHandler {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-based command test")
	}

	townRoot := t.TempDir()
	writeRigsConfig(t, townRoot, fixtureRigsConfig)
	bdPath := tf.writeFixtureBd(t, townRoot)

	// A live process calls session.InitRegistry at startup. Several paths the
	// dashboard uses still read the package-level registry rather than the
	// fetcher's own — session.PrefixFor in the convoy's per-assignee activity
	// lookup, session.IsKnownSession in the Sessions panel — so leaving the
	// default empty would make those paths fail for a reason no town has.
	registry, err := session.BuildPrefixRegistryFromTown(townRoot)
	if err != nil {
		t.Fatalf("building prefix registry: %v", err)
	}
	previous := session.DefaultRegistry()
	session.SetDefaultRegistry(registry)
	t.Cleanup(func() { session.SetDefaultRegistry(previous) })

	tf.writeDeaconHeartbeat(t, townRoot)
	tf.writeEventLog(t, townRoot)
	tf.stubCommands(t)
	tf.stubMergeQueue(t)

	fetcher := &LiveConvoyFetcher{
		townRoot:                townRoot,
		townBeads:               filepath.Join(townRoot, ".beads"),
		registry:                registry,
		bdBin:                   bdPath,
		cmdTimeout:              30 * time.Second,
		ghCmdTimeout:            30 * time.Second,
		tmuxCmdTimeout:          30 * time.Second,
		staleThreshold:          5 * time.Minute,
		stuckThreshold:          30 * time.Minute,
		heartbeatFreshThreshold: 5 * time.Minute,
		mayorActiveThreshold:    5 * time.Minute,
	}

	handler, err := NewConvoyHandler(fetcher, 30*time.Second, "test-token")
	if err != nil {
		t.Fatalf("NewConvoyHandler() error = %v", err)
	}
	return handler
}

// collect renders the fixture town into the data the page is built from.
func (tf *townFixture) collect(t *testing.T) ConvoyData {
	t.Helper()
	return tf.build(t).collectDashboard(context.Background(), "")
}

// fixtureRigsConfig registers both rigs with the beads prefixes their tmux
// session names use. Two non-empty rig stores is the minimum that can catch a
// panel which reads only one: with a single rig, a town-root-only query passes
// the moment the town root is empty.
const fixtureRigsConfig = `{
  "version": 1,
  "rigs": {
    "gastown": {"git_url": "git@github.com:o/gastown.git", "beads": {"prefix": "gt"}},
    "beads": {"git_url": "git@github.com:o/beads.git", "beads": {"prefix": "bd"}}
  }
}`

// fixtureBdScript answers from a per-directory fixture file, so which store a
// query reaches is decided by the directory it runs in — the property a stubbed
// Go callback cannot check, and the one the union panels were getting wrong.
//
// The key names the whole query rather than just its status, because the panels
// ask the same store several different questions and a test that cannot tell
// them apart cannot break one of them on its own.
const fixtureBdScript = `#!/bin/sh
if [ -f ./.fail ]; then
  echo "dial tcp 127.0.0.1:3307: connect: connection refused" >&2
  exit 1
fi
if [ "$1" = "dep" ]; then
  key="dep-$3"
else
  label=""
  status=""
  for arg in "$@"; do
    case "$arg" in
      --label=*) label=${arg#--label=} ;;
      --status=*) status=${arg#--status=} ;;
    esac
  done
  key="list-$label-$status"
fi
if [ -f "./.failquery-$key" ]; then
  echo "unknown label" >&2
  exit 1
fi
if [ -f "./.fixture-$key.json" ]; then
  cat "./.fixture-$key.json"
else
  printf '[]'
fi
`

// writeFixtureBd lays out every store's directory and answers, and returns the
// path to the fake bd.
func (tf *townFixture) writeFixtureBd(t *testing.T, townRoot string) string {
	t.Helper()

	storeDir := func(name string) string {
		if name == townStoreName {
			return townRoot
		}
		return filepath.Join(townRoot, name)
	}

	for name, store := range tf.stores {
		dir := storeDir(name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating store dir %s: %v", name, err)
		}
		for key, rows := range store {
			encoded, err := json.Marshal(rows)
			if err != nil {
				t.Fatalf("encoding fixture %s/%s: %v", name, key, err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".fixture-"+key+".json"), encoded, 0o600); err != nil {
				t.Fatalf("writing fixture %s/%s: %v", name, key, err)
			}
		}
	}
	for _, name := range tf.failStores {
		dir := storeDir(name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating store dir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".fail"), nil, 0o600); err != nil {
			t.Fatalf("marking store %s unreachable: %v", name, err)
		}
	}
	for name, keys := range tf.failQueries {
		dir := storeDir(name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating store dir %s: %v", name, err)
		}
		for _, key := range keys {
			if err := os.WriteFile(filepath.Join(dir, ".failquery-"+key), nil, 0o600); err != nil {
				t.Fatalf("marking query %s/%s as failing: %v", name, key, err)
			}
		}
	}

	bdPath := filepath.Join(t.TempDir(), "bd")
	if err := os.WriteFile(bdPath, []byte(fixtureBdScript), 0o755); err != nil {
		t.Fatalf("writing fake bd: %v", err)
	}
	return bdPath
}

func (tf *townFixture) writeDeaconHeartbeat(t *testing.T, townRoot string) {
	t.Helper()
	beat := time.Now().Add(-tf.heartbeatAge).Format(time.RFC3339)
	writeDeaconFile(t, townRoot, "heartbeat.json", fmt.Sprintf(
		`{"timestamp":%q,"cycle":100,"healthy_agents":%d,"unhealthy_agents":0}`,
		beat, fixtureWorkerCount))
}

func (tf *townFixture) writeEventLog(t *testing.T, townRoot string) {
	t.Helper()
	var sb strings.Builder
	for _, event := range tf.events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encoding event: %v", err)
		}
		sb.Write(encoded)
		sb.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".events.jsonl"), []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("writing event log: %v", err)
	}
}

func (tf *townFixture) stubMergeQueue(t *testing.T) {
	t.Helper()
	original := fetcherListMergeRequests
	t.Cleanup(func() { fetcherListMergeRequests = original })
	fetcherListMergeRequests = func(rigPath string, _ beads.ListOptions) ([]*beads.Issue, error) {
		return tf.mergeRequests[filepath.Base(rigPath)], nil
	}
}

// stubCommands answers the two commands the fetcher runs through
// fetcherRunCmd: tmux, and the `bd show` the convoy panel resolves its tracked
// issues with. Everything routed through runBdCmd goes to the fake bd on disk,
// which answers per store directory.
func (tf *townFixture) stubCommands(t *testing.T) {
	t.Helper()

	originalRun := fetcherRunCmd
	t.Cleanup(func() { fetcherRunCmd = originalRun })
	fetcherRunCmd = func(_ time.Duration, name string, args ...string) (*bytes.Buffer, error) {
		switch name {
		case "tmux":
			return tf.runFakeTmux(args)
		case "bd":
			return tf.runFakeBdShow(args)
		}
		return bytes.NewBufferString(""), nil
	}

	// The Mayor's runtime label is read from the session's environment.
	// Answering with an error is what a session that never exported GT_AGENT
	// does, and it keeps this fixture off the host's agent config.
	originalEnv := fetcherGetSessionEnv
	t.Cleanup(func() { fetcherGetSessionEnv = originalEnv })
	fetcherGetSessionEnv = func(string, string) (string, error) {
		return "", fmt.Errorf("no such environment variable")
	}
}

// runFakeTmux answers the list-sessions and capture-pane calls the panels make.
//
// It renders the -F format string rather than matching on it, so a panel that
// switches between #{session_activity} and #{window_activity} keeps working —
// and it honours -f, because the convoy panel's per-assignee lookup is a
// filtered list-sessions, and a stub that ignored the filter would report every
// session's activity as every worker's.
func (tf *townFixture) runFakeTmux(args []string) (*bytes.Buffer, error) {
	var subcommand, format, filter string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-F":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case "-f":
			if i+1 < len(args) {
				filter = args[i+1]
				i++
			}
		case "-t":
			i++ // capture-pane's target; the hint text does not depend on it
		default:
			if subcommand == "" && !strings.HasPrefix(args[i], "-") {
				subcommand = args[i]
			}
		}
	}

	if subcommand == "capture-pane" {
		return bytes.NewBufferString("● Working on the assigned bead\n"), nil
	}

	// tmux's filter form for "just this session", as the convoy lookup builds it.
	wantName := ""
	if strings.HasPrefix(filter, "#{==:#{session_name},") {
		wantName = strings.TrimSuffix(strings.TrimPrefix(filter, "#{==:#{session_name},"), "}")
	}

	var lines []string
	for _, s := range tf.sessions {
		if wantName != "" && s.name != wantName {
			continue
		}
		stamp := fmt.Sprintf("%d", s.activity.Unix())
		line := format
		line = strings.ReplaceAll(line, "#{session_name}", s.name)
		line = strings.ReplaceAll(line, "#{session_activity}", stamp)
		line = strings.ReplaceAll(line, "#{window_activity}", stamp)
		lines = append(lines, line)
	}
	return bytes.NewBufferString(strings.Join(lines, "\n")), nil
}

// runFakeBdShow answers `bd show <id>... --json` from the fixture's details.
func (tf *townFixture) runFakeBdShow(args []string) (*bytes.Buffer, error) {
	found := []map[string]any{}
	for _, arg := range args {
		if detail, ok := tf.issueDetails[arg]; ok {
			found = append(found, detail)
		}
	}
	encoded, err := json.Marshal(found)
	if err != nil {
		return nil, err
	}
	return bytes.NewBuffer(encoded), nil
}

// --- Fixture beads ----------------------------------------------------------

func concatBeads(groups ...[]map[string]any) []map[string]any {
	var all []map[string]any
	for _, g := range groups {
		all = append(all, g...)
	}
	return all
}

// workBeads builds n ordinary backlog items. Priority 3 — see newHealthyTown.
func workBeads(store string, n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{
			"id":         fmt.Sprintf("%s-work-%d", store, i),
			"title":      fmt.Sprintf("work item %d in %s", i, store),
			"issue_type": "task",
			"status":     "open",
			"priority":   3,
			"created_at": time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		})
	}
	return out
}

// hookedWork builds one hooked bead per named polecat of a rig.
func hookedWork(rig string, polecats []string, updated time.Time) []map[string]any {
	out := make([]map[string]any, 0, len(polecats))
	for i, name := range polecats {
		out = append(out, map[string]any{
			"id":         fmt.Sprintf("%s-hook-%d", rig, i),
			"title":      fmt.Sprintf("hooked item for %s", name),
			"issue_type": "task",
			"status":     "hooked",
			"priority":   3,
			"assignee":   rig + "/polecats/" + name,
			"created_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
			"updated_at": updated.Format(time.RFC3339),
		})
	}
	return out
}

func mailBeads(prefix string, n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{
			"id":         fmt.Sprintf("%s-%d", prefix, i),
			"title":      fmt.Sprintf("message %d", i),
			"issue_type": "message",
			"status":     "open",
			"labels":     []string{"gt:message"},
			"created_by": "mayor/",
			"assignee":   fixtureRigGastown + "/polecats/furiosa",
			"created_at": time.Now().Add(-20 * time.Minute).Format(time.RFC3339),
		})
	}
	return out
}

func convoyBead(id, title string) map[string]any {
	return map[string]any{
		"id":         id,
		"title":      title,
		"issue_type": "convoy",
		"status":     "open",
		"labels":     []string{"gt:convoy"},
		"created_at": time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
	}
}

// escalationBead builds one open, unacked escalation. Severity low keeps it a
// single fact: a critical one would also be a P1 bead in the backlog, so the
// banner's high-priority alert would fire alongside the escalation alert and
// the test could not say which of the two it had proved.
func escalationBead(id, title string) map[string]any {
	return map[string]any{
		"id":         id,
		"title":      title,
		"issue_type": "task",
		"status":     "open",
		"priority":   3,
		"labels":     []string{"gt:escalation", "severity:low"},
		"created_by": fixtureRigGastown + "/witness",
		"created_at": time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
	}
}

// --- Indicators -------------------------------------------------------------

// indicatorsOf reduces the assembled page to the signals an operator reads: the
// counts, the alert flags, the caveats, and each row's status.
//
// The *Unavailable and *Warning fields are gathered BY FIELD NAME rather than
// listed, so a panel added later is covered by the healthy-town assertion on
// the day it lands rather than the day someone remembers this file. Same reason
// the summary is walked whole instead of field by field.
func indicatorsOf(t *testing.T, d ConvoyData) map[string]string {
	t.Helper()

	got := map[string]string{}

	data := reflect.ValueOf(d)
	for i := 0; i < data.NumField(); i++ {
		name := data.Type().Field(i).Name
		if !strings.HasSuffix(name, "Unavailable") && !strings.HasSuffix(name, "Warning") {
			continue
		}
		field := data.Field(i)
		if field.Kind() != reflect.String {
			t.Fatalf("ConvoyData.%s is %s, want string — indicatorsOf cannot read it", name, field.Kind())
		}
		got[name] = field.String()
	}

	if d.Summary == nil {
		t.Fatal("ConvoyData.Summary is nil — the banner is what tells an operator the town is fine")
	}
	summary := reflect.ValueOf(*d.Summary)
	for i := 0; i < summary.NumField(); i++ {
		got["Summary."+summary.Type().Field(i).Name] = fmt.Sprintf("%v", summary.Field(i).Interface())
	}

	got["IssueCount"] = fmt.Sprintf("%d", d.IssueCount)
	got["MergeQueueFailedRigs"] = strings.Join(d.MergeQueueFailedRigs, ",")
	got["rows.Convoys"] = fmt.Sprintf("%d", len(d.Convoys))
	got["rows.Workers"] = fmt.Sprintf("%d", len(d.Workers))
	got["rows.Hooks"] = fmt.Sprintf("%d", len(d.Hooks))
	got["rows.Issues"] = fmt.Sprintf("%d", len(d.Issues))
	got["rows.Mail"] = fmt.Sprintf("%d", len(d.Mail))
	got["rows.Escalations"] = fmt.Sprintf("%d", len(d.Escalations))
	got["rows.MergeQueue"] = fmt.Sprintf("%d", len(d.MergeQueue))
	got["rows.Rigs"] = fmt.Sprintf("%d", len(d.Rigs))
	got["rows.Sessions"] = fmt.Sprintf("%d", len(d.Sessions))
	got["rows.Activity"] = fmt.Sprintf("%d", len(d.Activity))

	// The statuses, not just the counts: the convoy panel's whole defect was a
	// row that stayed present and changed what it said about itself.
	got["status.Convoys"] = joinStatuses(d.Convoys, func(r ConvoyRow) string { return r.ID + "=" + r.WorkStatus })
	got["status.Workers"] = joinStatuses(d.Workers, func(r WorkerRow) string { return r.SessionID + "=" + r.WorkStatus })

	return got
}

func joinStatuses[T any](rows []T, render func(T) string) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, render(r))
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// changedIndicators names the indicators that differ between two renders.
func changedIndicators(before, after map[string]string) []string {
	var changed []string
	for key, was := range before {
		if now, ok := after[key]; !ok || now != was {
			changed = append(changed, key)
		}
	}
	for key := range after {
		if _, ok := before[key]; !ok {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

// assertOnlyTheseMoved is the discrimination check: mutate one fact, and the
// indicators that move must be exactly the ones named.
//
// The "only" half is what the suite lacked. An assertion that a signal fires
// passes equally well against a dashboard that fires it constantly, which is
// what every false alarm found on 2026-08-25 was doing.
func assertOnlyTheseMoved(t *testing.T, before, after map[string]string, want ...string) {
	t.Helper()

	got := changedIndicators(before, after)
	sort.Strings(want)
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if reflect.DeepEqual(got, want) {
		return
	}

	t.Errorf("indicators that moved = %v, want %v", got, want)
	for _, key := range got {
		t.Logf("  moved: %s: %q -> %q", key, before[key], after[key])
	}
	for _, key := range want {
		if before[key] == after[key] {
			t.Logf("  did NOT move: %s, still %q", key, before[key])
		}
	}
}

// --- The healthy town -------------------------------------------------------

// TestHealthyTownRendersHealthy is the assertion the suite did not have: a town
// with nothing wrong with it must produce a page that says nothing is wrong.
func TestHealthyTownRendersHealthy(t *testing.T) {
	data := newHealthyTown().collect(t)

	// Control first. Every assertion below is about the absence of an alarm, and
	// absence is also what a dashboard reading nothing at all produces. A fixture
	// that rendered no convoys, no workers and no queue would satisfy the whole
	// of the rest of this test while proving nothing.
	if len(data.Convoys) == 0 {
		t.Fatal("fixture rendered no convoys — a healthy-town assertion over an empty page is vacuous")
	}
	if len(data.Workers) != fixtureWorkerCount {
		t.Fatalf("workers = %d, want %d — the fixture's workers must reach the page", len(data.Workers), fixtureWorkerCount)
	}
	if len(data.Hooks) != fixtureHookCount {
		t.Fatalf("hooks = %d, want %d", len(data.Hooks), fixtureHookCount)
	}
	if len(data.MergeQueue) == 0 {
		t.Fatal("fixture rendered no merge queue — the town has an MR in flight")
	}
	if len(data.Mail) == 0 {
		t.Fatal("fixture rendered no mail")
	}

	// 1. No convoy renders "stuck". The convoy is mid-flight with a working
	//    polecat on it, which is the state the live dashboard painted red.
	for _, convoy := range data.Convoys {
		if convoy.WorkStatus == "stuck" {
			t.Errorf("convoy %s renders %q; its tracked work is assigned to a polecat whose session is seconds old",
				convoy.ID, convoy.WorkStatus)
		}
	}

	// 2. The Work count is the true count of non-internal open and hooked beads,
	//    not the size of a sample and not a number pinned by a cap.
	if data.IssueCount != fixtureWorkCount {
		t.Errorf("IssueCount = %d, want %d (the fixture's work beads; mail and the convoy bead excluded)",
			data.IssueCount, fixtureWorkCount)
	}
	if data.Summary.IssueCount != fixtureWorkCount {
		t.Errorf("Summary.IssueCount = %d, want %d — the banner and the panel must print the same number",
			data.Summary.IssueCount, fixtureWorkCount)
	}
	for _, issue := range data.Issues {
		if strings.HasPrefix(issue.ID, "hq-msg") || strings.HasPrefix(issue.ID, "hq-cv") {
			t.Errorf("issue %s is Gas Town plumbing and must not be counted as work", issue.ID)
		}
	}

	// 3. No *Unavailable flag is set, no panel carries a partial-read caveat, and
	//    no count is flagged as a floor. Read by field name so a panel added
	//    later is covered here the day it lands — which is not hypothetical:
	//    Summary.HooksPartial, IssuesPartial and MergeQueuePartial arrived with
	//    gt-skzk.2 after this file was written, and this loop covered them
	//    without being touched.
	for name, value := range indicatorsOf(t, data) {
		switch {
		case strings.HasSuffix(name, "Unavailable") && value != "" && value != "false":
			t.Errorf("%s = %q on a town whose every source answered", name, value)
		case strings.HasSuffix(name, "Warning") && value != "":
			t.Errorf("%s = %q on a town whose every store answered in full", name, value)
		case strings.HasSuffix(name, "Partial") && value != "false":
			t.Errorf("%s = %q; every count on this page is measured, not a floor", name, value)
		}
	}
	if len(data.MergeQueueFailedRigs) != 0 {
		t.Errorf("MergeQueueFailedRigs = %v, want none", data.MergeQueueFailedRigs)
	}

	// 4. The banner carries no alert, and none of the counts behind it is set.
	summary := data.Summary
	if summary.StuckPolecats != 0 {
		t.Errorf("StuckPolecats = %d, want 0 — every worker's session is minutes old", summary.StuckPolecats)
	}
	if summary.StaleHooks != 0 {
		t.Errorf("StaleHooks = %d, want 0 — every hook was updated ten minutes ago", summary.StaleHooks)
	}
	if summary.UnackedEscalations != 0 || summary.EscalationCount != 0 {
		t.Errorf("escalations = %d (%d unacked), want none", summary.EscalationCount, summary.UnackedEscalations)
	}
	if summary.DeadSessions != 0 {
		t.Errorf("DeadSessions = %d, want 0 — the event log records a sling and a merge", summary.DeadSessions)
	}
	if summary.HighPriorityIssues != 0 {
		t.Errorf("HighPriorityIssues = %d, want 0", summary.HighPriorityIssues)
	}
	if summary.HasAlerts {
		t.Errorf("HasAlerts = true on a healthy town; summary = %+v", *summary)
	}
}

// TestHealthyTownReportsPolecatsAsWorking is the positive half of the baseline.
// "No alarm" is also what a panel that quietly lost its rows produces, so the
// polecats must be visibly WORKING rather than merely not stuck.
//
// It also asks about the Refinery row, which used to be broken in exactly
// this way: ParseSessionNameWithRegistry leaves Name unset for a
// "<prefix>-refinery" session, and both refinery arms downstream used to key
// on the string workerName == "refinery" — so neither could fire, and the
// Refinery rendered idle with no status hint while it was merging (gt-ahwc).
// Measured against this fixture, with an MR sitting in the queue, the
// Refinery must render workStatus="working" and a non-empty StatusHint.
func TestHealthyTownReportsPolecatsAsWorking(t *testing.T) {
	data := newHealthyTown().collect(t)

	seen := 0
	refinerySeen := 0
	for _, worker := range data.Workers {
		if worker.AgentType == constants.RoleRefinery {
			refinerySeen++
			if worker.WorkStatus != "working" {
				t.Errorf("refinery %s renders %q, want \"working\"", worker.SessionID, worker.WorkStatus)
			}
			if worker.StatusHint == "" {
				t.Errorf("refinery %s carries no status hint while an MR is queued", worker.SessionID)
			}
			continue
		}
		if worker.AgentType != constants.RolePolecat {
			continue
		}
		seen++
		if worker.WorkStatus != "working" {
			t.Errorf("polecat %s renders %q, want \"working\"", worker.SessionID, worker.WorkStatus)
		}
		if worker.IssueID == "" {
			t.Errorf("polecat %s carries no issue; its hooked bead is in the %s store", worker.SessionID, worker.Rig)
		}
	}
	if seen != 3 {
		t.Errorf("polecat rows = %d, want 3 — the assertions above are per-row and vacuous over none", seen)
	}
	if refinerySeen != 1 {
		t.Errorf("refinery rows = %d, want 1 — the assertions above are per-row and vacuous over none", refinerySeen)
	}

	for _, convoy := range data.Convoys {
		if convoy.WorkStatus != "working" {
			t.Errorf("convoy %s renders %q, want \"working\"", convoy.ID, convoy.WorkStatus)
		}
		if convoy.Progress != "1/2" {
			t.Errorf("convoy %s progress = %q, want \"1/2\"", convoy.ID, convoy.Progress)
		}
	}
}

// --- One fact at a time -----------------------------------------------------

// TestOnePolecatSessionDiesMovesOnlyTheWorkerCount is the first discrimination
// case, and it pins the live town's error in the other direction: losing a
// worker must not touch the backlog. The polecat killed is in the rig the
// convoy does not track, so nothing but the worker roster has business moving.
func TestOnePolecatSessionDiesMovesOnlyTheWorkerCount(t *testing.T) {
	before := indicatorsOf(t, newHealthyTown().collect(t))

	town := newHealthyTown()
	town.killSession("bd-slit")
	after := indicatorsOf(t, town.collect(t))

	assertOnlyTheseMoved(t, before, after,
		"Summary.PolecatCount",
		"rows.Workers",
		"rows.Sessions",
		"status.Workers",
	)

	// The bead it was carrying is still hooked and still in the backlog. A dead
	// session is a fact about tmux, not about the work.
	if got, want := after["Summary.IssueCount"], fmt.Sprintf("%d", fixtureWorkCount); got != want {
		t.Errorf("Summary.IssueCount = %s after a session died, want %s", got, want)
	}
	if got, want := after["Summary.HookCount"], fmt.Sprintf("%d", fixtureHookCount); got != want {
		t.Errorf("Summary.HookCount = %s after a session died, want %s", got, want)
	}
}

// TestOnePolecatGoesQuietMovesOnlyTheStuckCount pins the signal the live
// dashboard fired constantly. It must fire — one polecat, silent past the stuck
// threshold, with a bead still on its hook — and it must move nothing else.
func TestOnePolecatGoesQuietMovesOnlyTheStuckCount(t *testing.T) {
	before := indicatorsOf(t, newHealthyTown().collect(t))

	town := newHealthyTown()
	town.setSessionActivity("gt-nux", time.Now().Add(-45*time.Minute))
	after := indicatorsOf(t, town.collect(t))

	assertOnlyTheseMoved(t, before, after,
		"Summary.StuckPolecats",
		"Summary.HasAlerts",
		"status.Workers",
	)

	if after["Summary.StuckPolecats"] != "1" {
		t.Errorf("StuckPolecats = %s, want 1", after["Summary.StuckPolecats"])
	}
	if after["Summary.HasAlerts"] != "true" {
		t.Error("HasAlerts = false with a polecat silent for 45 minutes")
	}
	// The other two polecats must stay working. A threshold that sweeps the whole
	// panel red is the defect, not the fix.
	if !strings.Contains(after["status.Workers"], "gt-furiosa=working") ||
		!strings.Contains(after["status.Workers"], "bd-slit=working") {
		t.Errorf("worker statuses = %q; only nux went quiet", after["status.Workers"])
	}
}

// TestOneStoreFailsMovesOnlyThatStoresRows covers a partial read. The rows that
// store held go missing and every panel that reads it says so — and the panels
// that do not read it stay exactly as they were.
func TestOneStoreFailsMovesOnlyThatStoresRows(t *testing.T) {
	before := indicatorsOf(t, newHealthyTown().collect(t))

	town := newHealthyTown()
	town.breakStore(fixtureRigBeads)
	after := indicatorsOf(t, town.collect(t))

	// The beads rig held 3 open and 1 hooked bead, and its polecat's hook is
	// what the Workers panel resolves an issue from — so slit loses its issue
	// and renders idle, which is why that panel carries a caveat of its own.
	//
	// HooksPartial, IssuesPartial and HasAlerts are here because gt-skzk.2
	// landed: the banner now says its counts are floors rather than printing a
	// short number in the same language as a measured one. Before that, this
	// expectation omitted all three and a store the dashboard could not read
	// left the banner reading "all clear".
	assertOnlyTheseMoved(t, before, after,
		"IssueCount",
		"IssuesWarning",
		"HooksWarning",
		"WorkersWarning",
		"Summary.IssueCount",
		"Summary.HookCount",
		"Summary.HooksPartial",
		"Summary.IssuesPartial",
		"Summary.HasAlerts",
		"rows.Issues",
		"rows.Hooks",
		"status.Workers",
	)

	for _, key := range []string{"IssuesWarning", "HooksWarning", "WorkersWarning"} {
		if !strings.Contains(after[key], fixtureRigBeads) {
			t.Errorf("%s = %q, want it to name the store that could not answer", key, after[key])
		}
	}
	// A floor, not a total — and it must not read as "no store answered".
	if after["IssuesUnavailable"] != "" {
		t.Errorf("IssuesUnavailable = %q; two stores answered, so the count is short, not absent", after["IssuesUnavailable"])
	}
	if got, want := after["Summary.IssueCount"], fmt.Sprintf("%d", fixtureWorkCount-4); got != want {
		t.Errorf("Summary.IssueCount = %s, want %s (the beads rig's 3 open and 1 hooked bead are missing)", got, want)
	}
}

// TestEveryStoreFailsIsNotAQuietTown is the far end of the same scale. With no
// store readable there is no count at all, and the banner must say so rather
// than print a calm zero.
func TestEveryStoreFailsIsNotAQuietTown(t *testing.T) {
	town := newHealthyTown()
	town.breakStore(townStoreName, fixtureRigGastown, fixtureRigBeads)
	data := town.collect(t)

	if data.IssuesUnavailable == "" {
		t.Error("IssuesUnavailable = \"\" with no readable store; the Work count is of nothing read")
	}
	if data.HooksUnavailable == "" {
		t.Error("HooksUnavailable = \"\" with no readable store")
	}
	if !data.Summary.IssuesUnavailable || !data.Summary.HooksUnavailable {
		t.Errorf("banner reports the union panels as readable; summary = %+v", *data.Summary)
	}
	if !data.Summary.HasAlerts {
		t.Error("HasAlerts = false on a town whose every beads store is unreachable")
	}
	if data.IssueCount != 0 {
		t.Errorf("IssueCount = %d, want 0 alongside the stated reason", data.IssueCount)
	}
}

// TestOneStorePastTheCapMovesOnlyTheBacklogFloor covers the third mutation: a
// store holding more beads than the safety cap allows. The count becomes a
// floor and the panel must name the store it is short by — while every other
// signal on the page stays where it was.
func TestOneStorePastTheCapMovesOnlyTheBacklogFloor(t *testing.T) {
	before := indicatorsOf(t, newHealthyTown().collect(t))

	town := newHealthyTown()
	town.stores[fixtureRigGastown]["list--open"] = workBeads(fixtureRigGastown, issuesPerStoreLimit+1)
	after := indicatorsOf(t, town.collect(t))

	// A capped count is a floor, and since gt-skzk.2 the banner says so rather
	// than printing it in the same language as a measured number — so it raises
	// IssuesPartial and an alert. HooksPartial does not move: the hooks union has
	// no cap, and a truncation in one panel must not caveat another.
	assertOnlyTheseMoved(t, before, after,
		"IssueCount",
		"IssuesWarning",
		"Summary.IssueCount",
		"Summary.IssuesPartial",
		"Summary.HasAlerts",
		"rows.Issues",
	)

	if !strings.Contains(after["IssuesWarning"], "truncated") ||
		!strings.Contains(after["IssuesWarning"], fixtureRigGastown) {
		t.Errorf("IssuesWarning = %q, want it to name %q as truncated", after["IssuesWarning"], fixtureRigGastown)
	}
	if after["IssuesUnavailable"] != "" {
		t.Errorf("IssuesUnavailable = %q; the store answered, it just had more to give", after["IssuesUnavailable"])
	}
	// The rendered page is a page and stays one. The count beside it is the
	// backlog's size and must not be the page's — that conflation is gt-eolg.
	if got, want := after["rows.Issues"], fmt.Sprintf("%d", issuesDisplayLimit); got != want {
		t.Errorf("rendered rows = %s, want the display limit %s", got, want)
	}
	if after["IssueCount"] == after["rows.Issues"] {
		t.Errorf("IssueCount = rows rendered = %s; the count must be the backlog, not the page", after["IssueCount"])
	}
}

// TestOneEscalationMovesOnlyTheEscalationSignal is the mutation the panels
// exist for. It must reach the banner — and it must not be mistaken for
// anything else on the page.
func TestOneEscalationMovesOnlyTheEscalationSignal(t *testing.T) {
	before := indicatorsOf(t, newHealthyTown().collect(t))

	town := newHealthyTown()
	town.raiseEscalation(escalationBead("hq-esc-1", "witness cannot reach the refinery"))
	after := indicatorsOf(t, town.collect(t))

	// The backlog moves with it, and that is correct rather than incidental: an
	// escalation IS an open bead, so `bd list --status=open` returns it and it is
	// not one of the plumbing types the Work panel hides. The fixture files it in
	// both queries for that reason — putting it in the escalation query alone
	// would be a town no bd could produce.
	assertOnlyTheseMoved(t, before, after,
		"Summary.EscalationCount",
		"Summary.UnackedEscalations",
		"Summary.HasAlerts",
		"Summary.IssueCount",
		"IssueCount",
		"rows.Escalations",
		"rows.Issues",
	)

	if after["Summary.UnackedEscalations"] != "1" {
		t.Errorf("UnackedEscalations = %s, want 1", after["Summary.UnackedEscalations"])
	}
	if after["Summary.HasAlerts"] != "true" {
		t.Error("HasAlerts = false with an open unacked escalation")
	}
	// Nobody went stuck, no store broke, no convoy stalled.
	if after["Summary.StuckPolecats"] != "0" || after["status.Convoys"] != before["status.Convoys"] {
		t.Errorf("an escalation moved unrelated signals: stuck=%s convoys=%q",
			after["Summary.StuckPolecats"], after["status.Convoys"])
	}
}

// TestEscalationQueryFailingIsNotZeroEscalations is the control for the case
// above, taken at the level the whole page is assembled: the two must not
// render the same. This is the swallowed-error family's worst member — the
// panel whose entire purpose is to surface problems, going quiet.
func TestEscalationQueryFailingIsNotZeroEscalations(t *testing.T) {
	before := indicatorsOf(t, newHealthyTown().collect(t))

	town := newHealthyTown()
	town.breakEscalationQuery()
	after := indicatorsOf(t, town.collect(t))

	if after["EscalationsUnavailable"] == "" {
		t.Error("EscalationsUnavailable = \"\" after the escalation query failed")
	}
	if after["Summary.EscalationsUnavailable"] != "true" {
		t.Error("the banner reports escalations as readable after the query failed")
	}
	if after["Summary.HasAlerts"] != "true" {
		t.Error("HasAlerts = false; a dashboard that cannot see escalations is not all clear")
	}
	// A panel that went dark is not a panel whose count is short. Unavailable and
	// Partial are degrees of the same failure and the banner renders them
	// differently, so the wrong one firing sends an operator to the wrong place.
	if after["Summary.IssuesPartial"] != "false" || after["Summary.HooksPartial"] != "false" {
		t.Errorf("a failed escalation query flagged the union counts as floors: issues=%s hooks=%s",
			after["Summary.IssuesPartial"], after["Summary.HooksPartial"])
	}
	// It is the escalation panel that went dark, not the town.
	if after["Summary.IssueCount"] != before["Summary.IssueCount"] {
		t.Errorf("Work count moved from %s to %s when only the escalation query failed",
			before["Summary.IssueCount"], after["Summary.IssueCount"])
	}
	if after["status.Workers"] != before["status.Workers"] {
		t.Errorf("worker statuses moved to %q when only the escalation query failed", after["status.Workers"])
	}
}

// TestDeaconHeartbeatGoesStaleMovesOnlyTheHeartbeat covers the banner's
// liveness indicator, which fails by hiding rather than by lying: the stat
// renders only when health is known, so an old heartbeat must stay known and
// simply go stale.
func TestDeaconHeartbeatGoesStaleMovesOnlyTheHeartbeat(t *testing.T) {
	before := indicatorsOf(t, newHealthyTown().collect(t))

	town := newHealthyTown()
	town.heartbeatAge = time.Hour
	data := town.collect(t)

	// The heartbeat is not one of the signals the summary carries, so nothing in
	// the reduced view is expected to move at all.
	assertOnlyTheseMoved(t, before, indicatorsOf(t, data))

	if data.Health == nil {
		t.Fatal("Health = nil for a heartbeat that is merely old")
	}
	if data.Health.HeartbeatFresh {
		t.Error("HeartbeatFresh = true for a heartbeat an hour old")
	}
	if data.HealthUnavailable != "" {
		t.Errorf("HealthUnavailable = %q; the heartbeat was read, it is just stale", data.HealthUnavailable)
	}
}

// --- Mutations --------------------------------------------------------------

func (tf *townFixture) killSession(name string) {
	kept := make([]fakeSession, 0, len(tf.sessions))
	for _, s := range tf.sessions {
		if s.name != name {
			kept = append(kept, s)
		}
	}
	tf.sessions = kept
}

func (tf *townFixture) setSessionActivity(name string, when time.Time) {
	for i := range tf.sessions {
		if tf.sessions[i].name == name {
			tf.sessions[i].activity = when
			return
		}
	}
}

// breakStore makes a store's bd exit non-zero. Its fixtures are dropped so a
// later reader cannot mistake them for answers the store still gives.
func (tf *townFixture) breakStore(names ...string) {
	for _, name := range names {
		delete(tf.stores, name)
		tf.failStores = append(tf.failStores, name)
	}
}

// raiseEscalation files one escalation, in both the queries live bd would
// return it from. See TestOneEscalationMovesOnlyTheEscalationSignal.
func (tf *townFixture) raiseEscalation(bead map[string]any) {
	town := tf.stores[townStoreName]
	town["list-gt:escalation-open"] = append(town["list-gt:escalation-open"], bead)
	town["list--open"] = append(town["list--open"], bead)
}

// breakEscalationQuery makes only the escalation list fail, leaving every other
// query the town store answers intact — which is what a bad label or a schema
// mismatch does, and what a whole-store failure cannot isolate.
func (tf *townFixture) breakEscalationQuery() {
	tf.failQueries[townStoreName] = append(tf.failQueries[townStoreName], "list-gt:escalation-open")
}

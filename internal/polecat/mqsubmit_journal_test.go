package polecat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/testguard"
)

func townLogContents(t *testing.T, townRoot string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(townRoot, "logs", "town.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("reading town log: %v", err)
	}
	return string(data)
}

func countLines(s, substr string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

func fixtureObservation(source string) MQSubmitObservation {
	return MQSubmitObservation{
		Rig:     "gastown",
		Polecat: "ace",
		Issue:   "gt-fixture",
		Branch:  "polecat/ace",
		Source:  source,
	}
}

func flaggedDisposition() WorkstateDisposition {
	return WorkstateDisposition{
		Verdict:       WorkstateVerdictNeedsMQSubmit,
		Reason:        "mq-refused-closed-source",
		NeedsRecovery: true,
		NeedsMQSubmit: true,
		MQStatus:      "refused_closed_source",
		Blockers:      []string{"mq_status=refused_closed_source (gt done made no MR: source issue was closed)"},
	}
}

func record(t *testing.T, townRoot string, obs MQSubmitObservation, d WorkstateDisposition) bool {
	t.Helper()
	logged, err := RecordNeedsMQSubmit(townRoot, obs, d)
	if err != nil {
		t.Fatalf("RecordNeedsMQSubmit() = %v, want nil", err)
	}
	return logged
}

// TestRecordNeedsMQSubmit_RisingEdgeLogsOnce is the whole point of the journal:
// the flag becomes a line, and the line survives the moment. Repeat observations
// must not repeat it — the witness reads these surfaces on a loop, and a line per
// read would bury the transition among its own restatements.
func TestRecordNeedsMQSubmit_RisingEdgeLogsOnce(t *testing.T) {
	townRoot := t.TempDir()

	if logged := record(t, townRoot, fixtureObservation("polecat-list"), flaggedDisposition()); !logged {
		t.Fatal("first flagged observation logged nothing, want a rising-edge line")
	}
	log := townLogContents(t, townRoot)
	if n := countLines(log, "[needs_mq_submit]"); n != 1 {
		t.Fatalf("rising-edge lines = %d, want 1\ntown log:\n%s", n, log)
	}
	for _, want := range []string{"gastown/polecats/ace", "reason=mq-refused-closed-source", "mq_status=refused_closed_source", "bead=gt-fixture", "branch=polecat/ace", "source=polecat-list"} {
		if !strings.Contains(log, want) {
			t.Errorf("rising-edge line missing %q\ntown log:\n%s", want, log)
		}
	}

	if logged := record(t, townRoot, fixtureObservation("polecat-list"), flaggedDisposition()); logged {
		t.Error("second flagged observation logged again, want the episode de-duplicated")
	}
	if n := countLines(townLogContents(t, townRoot), "[needs_mq_submit]"); n != 1 {
		t.Errorf("rising-edge lines after re-observation = %d, want 1", n)
	}
}

// TestRecordNeedsMQSubmit_OtherSurfaceDoesNotRelog covers the flapping case. The
// surfaces do not gather the same facts, so they reach the same live condition by
// different reasons — list can only see a recorded refusal, check-recovery runs
// the queue lookup. Keying de-duplication on the reason would make the two
// surfaces log at each other forever.
func TestRecordNeedsMQSubmit_OtherSurfaceDoesNotRelog(t *testing.T) {
	townRoot := t.TempDir()

	record(t, townRoot, fixtureObservation("polecat-list"), flaggedDisposition())
	fromRecovery := WorkstateDisposition{
		Verdict:       WorkstateVerdictNeedsMQSubmit,
		Reason:        "mq-not-submitted",
		NeedsRecovery: true,
		NeedsMQSubmit: true,
		MQStatus:      "not_submitted",
	}
	if logged := record(t, townRoot, fixtureObservation("check-recovery"), fromRecovery); logged {
		t.Error("a second surface logged the same open episode, want one line per episode")
	}
	if n := countLines(townLogContents(t, townRoot), "[needs_mq_submit]"); n != 1 {
		t.Errorf("rising-edge lines = %d, want 1", n)
	}
}

// TestRecordNeedsMQSubmit_UnprovenFalseLeavesEpisodeOpen is the guard against
// writing the same lie in the opposite direction. A surface reports
// needs_mq_submit=false when it never asked the merge-queue question at all —
// the inventory surface gathers no git state on purpose — and a "cleared" line
// written from that silence would say the branch reached the queue when nobody
// checked. The episode stays open until something proves otherwise.
func TestRecordNeedsMQSubmit_UnprovenFalseLeavesEpisodeOpen(t *testing.T) {
	townRoot := t.TempDir()
	record(t, townRoot, fixtureObservation("check-recovery"), flaggedDisposition())

	unproven := []WorkstateDisposition{
		{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true},
		{Verdict: WorkstateVerdictNeedsRecovery, Reason: "git-dirty", NeedsRecovery: true},
		{Verdict: WorkstateVerdictNeedsRecovery, Reason: "mq-lookup-failed", NeedsRecovery: true, MQStatus: "unknown"},
		{Verdict: WorkstateVerdictWorking, Reason: "session-busy"},
	}
	for _, d := range unproven {
		if logged := record(t, townRoot, fixtureObservation("polecat-list"), d); logged {
			t.Errorf("reason=%s mq_status=%q cleared the episode, want it left open", d.Reason, d.MQStatus)
		}
	}
	if n := countLines(townLogContents(t, townRoot), "needs_mq_submit_cleared"); n != 0 {
		t.Errorf("cleared lines = %d, want 0", n)
	}

	// The episode is still the same one: a later flagged observation must not
	// re-log a rising edge that never fell.
	if logged := record(t, townRoot, fixtureObservation("check-recovery"), flaggedDisposition()); logged {
		t.Error("re-flagging after an unproven false logged a second rising edge")
	}
}

// TestRecordNeedsMQSubmit_ProvenFalseClearsOnce closes the audit loop: without a
// falling edge the log says a polecat was stranded and never says it stopped
// being stranded, which is the same unanswerable question one step later.
func TestRecordNeedsMQSubmit_ProvenFalseClearsOnce(t *testing.T) {
	for _, mqStatus := range []string{"submitted", "not_required"} {
		t.Run(mqStatus, func(t *testing.T) {
			townRoot := t.TempDir()
			record(t, townRoot, fixtureObservation("check-recovery"), flaggedDisposition())

			resolved := WorkstateDisposition{
				Verdict:    WorkstateVerdictSafeToNuke,
				Reason:     "reusable",
				Reusable:   true,
				SafeToNuke: true,
				MQStatus:   mqStatus,
			}
			if logged := record(t, townRoot, fixtureObservation("check-recovery"), resolved); !logged {
				t.Fatal("proven resolution logged nothing, want a falling-edge line")
			}
			log := townLogContents(t, townRoot)
			if n := countLines(log, "[needs_mq_submit_cleared]"); n != 1 {
				t.Fatalf("cleared lines = %d, want 1\ntown log:\n%s", n, log)
			}
			for _, want := range []string{"mq_status=" + mqStatus, "was=mq-refused-closed-source", "flagged_for="} {
				if !strings.Contains(log, want) {
					t.Errorf("cleared line missing %q\ntown log:\n%s", want, log)
				}
			}

			if logged := record(t, townRoot, fixtureObservation("check-recovery"), resolved); logged {
				t.Error("second proven resolution logged again, want one line per episode")
			}

			// A new stranding after a resolved one is a new episode and gets its own line.
			if logged := record(t, townRoot, fixtureObservation("check-recovery"), flaggedDisposition()); !logged {
				t.Error("a fresh stranding after a cleared episode logged nothing")
			}
			if n := countLines(townLogContents(t, townRoot), "[needs_mq_submit]"); n != 2 {
				t.Errorf("rising-edge lines across two episodes = %d, want 2", n)
			}
		})
	}
}

// TestRecordNeedsMQSubmit_UnflaggedPolecatIsSilent keeps the log readable: the
// ordinary case is every polecat reading false forever, and that must cost
// nothing.
func TestRecordNeedsMQSubmit_UnflaggedPolecatIsSilent(t *testing.T) {
	townRoot := t.TempDir()
	resolved := WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke, Reason: "reusable", Reusable: true, SafeToNuke: true, MQStatus: "submitted"}

	if logged := record(t, townRoot, fixtureObservation("polecat-list"), resolved); logged {
		t.Error("a never-flagged polecat logged a falling edge")
	}
	if log := townLogContents(t, townRoot); log != "" {
		t.Errorf("town log for a never-flagged polecat:\n%s", log)
	}
	if _, err := os.Stat(MQSubmitStatePath(townRoot)); !os.IsNotExist(err) {
		t.Errorf("ledger stat = %v, want the file never created", err)
	}
}

// TestRecordNeedsMQSubmit_TracksPolecatsIndependently guards the ledger key: one
// stranded polecat must not suppress the line for the next one.
func TestRecordNeedsMQSubmit_TracksPolecatsIndependently(t *testing.T) {
	townRoot := t.TempDir()

	first := fixtureObservation("polecat-list")
	second := fixtureObservation("polecat-list")
	second.Polecat = "dag"
	sameNameOtherRig := fixtureObservation("polecat-list")
	sameNameOtherRig.Rig = "beads"

	for _, obs := range []MQSubmitObservation{first, second, sameNameOtherRig} {
		if logged := record(t, townRoot, obs, flaggedDisposition()); !logged {
			t.Errorf("%s/%s logged nothing, want its own rising-edge line", obs.Rig, obs.Polecat)
		}
	}
	log := townLogContents(t, townRoot)
	if n := countLines(log, "[needs_mq_submit]"); n != 3 {
		t.Fatalf("rising-edge lines = %d, want 3\ntown log:\n%s", n, log)
	}
	for _, want := range []string{"gastown/polecats/ace", "gastown/polecats/dag", "beads/polecats/ace"} {
		if !strings.Contains(log, want) {
			t.Errorf("town log missing %q\ntown log:\n%s", want, log)
		}
	}
}

// TestRecordNeedsMQSubmit_FiresOnTheRealRefusalPath is the regression test the
// journal exists to make possible. It drives the actual gt-46rk stranding
// signature — a pushed branch, gt done's recorded refusal, no MR — through
// DecideWorkstate and asserts a line lands. If the predicate ever silently stops
// firing, this fails; before the journal there was no observable difference
// between that regression and correct operation.
func TestRecordNeedsMQSubmit_FiresOnTheRealRefusalPath(t *testing.T) {
	townRoot := t.TempDir()

	d := DecideWorkstate(WorkstateInput{
		State:         StateDone,
		CleanupStatus: CleanupClean,
		Branch:        "polecat/ace",
		MRRefused:     true,
	})
	if !d.NeedsMQSubmit {
		t.Fatalf("DecideWorkstate() on the gt-46rk refusal signature = %+v, want needs_mq_submit", d)
	}

	obs := fixtureObservation("polecat-list")
	if logged := record(t, townRoot, obs, d); !logged {
		t.Fatal("the refusal signature logged nothing")
	}
	log := townLogContents(t, townRoot)
	if !strings.Contains(log, "needs_mq_submit") || !strings.Contains(log, "gastown/polecats/ace") {
		t.Fatalf("town log does not record the episode:\n%s", log)
	}

	// And it stays answerable: the flag is gone from a later read of the same
	// polecat, the log line is not.
	resolved := DecideWorkstate(WorkstateInput{
		State:              StateDone,
		CleanupStatus:      CleanupClean,
		Branch:             "polecat/ace",
		MRRefused:          true,
		MQCheckRequired:    true,
		HasSubmittableWork: false,
	})
	if resolved.NeedsMQSubmit {
		t.Fatalf("DecideWorkstate() after submission = %+v, want the flag cleared", resolved)
	}
	record(t, townRoot, obs, resolved)
	if n := countLines(townLogContents(t, townRoot), "[needs_mq_submit]"); n != 1 {
		t.Error("the rising-edge line did not survive the condition clearing")
	}
}

// TestRecordNeedsMQSubmit_CorruptLedgerRecovers keeps a damaged de-duplication
// file from silencing the record. The ledger is not the evidence, the log is.
func TestRecordNeedsMQSubmit_CorruptLedgerRecovers(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(MQSubmitStatePath(townRoot)), 0755); err != nil {
		t.Fatalf("creating ledger dir: %v", err)
	}
	if err := os.WriteFile(MQSubmitStatePath(townRoot), []byte("{not json"), 0600); err != nil {
		t.Fatalf("writing corrupt ledger: %v", err)
	}

	if logged := record(t, townRoot, fixtureObservation("polecat-list"), flaggedDisposition()); !logged {
		t.Fatal("a corrupt ledger suppressed the rising-edge line")
	}
}

// TestRecordNeedsMQSubmit_MissingIdentityIsANoop: a record keyed on nothing
// would collide across rigs, so the incomplete observation is dropped rather
// than guessed at.
func TestRecordNeedsMQSubmit_MissingIdentityIsANoop(t *testing.T) {
	townRoot := t.TempDir()
	for _, obs := range []MQSubmitObservation{
		{Polecat: "ace"},
		{Rig: "gastown"},
		{},
	} {
		if logged := record(t, townRoot, obs, flaggedDisposition()); logged {
			t.Errorf("observation %+v logged a line, want a no-op", obs)
		}
	}
	if log := townLogContents(t, townRoot); log != "" {
		t.Errorf("town log:\n%s", log)
	}
}

// TestRecordNeedsMQSubmit_RefusesLiveTownFromTestBinary mirrors the town log's
// own guard. The ledger needs one too: an entry written there SUPPRESSES the
// rising-edge line for a real polecat, so a stray test could silence the record
// rather than merely add to it.
func TestRecordNeedsMQSubmit_RefusesLiveTownFromTestBinary(t *testing.T) {
	live := t.TempDir()
	elsewhere := t.TempDir()
	t.Setenv("TMPDIR", elsewhere)
	if got := os.TempDir(); got != elsewhere {
		t.Skipf("os.TempDir() = %q, not the TMPDIR just set; cannot stage a live town root on this platform", got)
	}
	t.Setenv(testguard.AllowEnv, "")
	if err := os.Unsetenv(testguard.AllowEnv); err != nil {
		t.Fatalf("unset %s: %v", testguard.AllowEnv, err)
	}

	logged, err := RecordNeedsMQSubmit(live, fixtureObservation("polecat-list"), flaggedDisposition())
	if !errors.Is(err, testguard.ErrRefused) {
		t.Errorf("RecordNeedsMQSubmit() into a live town = %v, want ErrRefused", err)
	}
	if logged {
		t.Error("RecordNeedsMQSubmit() into a live town reported a line written")
	}
	if _, statErr := os.Stat(MQSubmitStatePath(live)); !os.IsNotExist(statErr) {
		t.Errorf("refused record wrote %s", MQSubmitStatePath(live))
	}
	if log := townLogContents(t, live); log != "" {
		t.Errorf("refused record wrote the live town log:\n%s", log)
	}

	// A town this test owns is still authorized by name, the same rule the other
	// routes use.
	t.Setenv(testguard.AllowEnv, live)
	if _, err := RecordNeedsMQSubmit(live, fixtureObservation("polecat-list"), flaggedDisposition()); err != nil {
		t.Errorf("RecordNeedsMQSubmit() into the authorized town = %v, want nil", err)
	}
}

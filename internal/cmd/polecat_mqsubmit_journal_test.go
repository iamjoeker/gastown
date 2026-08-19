package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/polecat"
)

func mqJournalTownLog(t *testing.T, townRoot string) string {
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

// TestRecordNeedsMQSubmitObservation_WritesTheEpisode covers the seam between the
// polecat surfaces and the journal. The journal's own tests prove the edges; this
// proves the command layer actually reaches it, which is the half that would fail
// silently — recording is deliberately non-fatal, so a broken call here looks
// exactly like a polecat that was never flagged (gt-7i07).
func TestRecordNeedsMQSubmitObservation_WritesTheEpisode(t *testing.T) {
	townRoot := t.TempDir()
	obs := polecat.MQSubmitObservation{
		Rig:     "gastown",
		Polecat: "ace",
		Issue:   "gt-fixture",
		Branch:  "polecat/ace",
		Source:  "polecat-list",
	}
	disposition := polecat.WorkstateDisposition{
		Verdict:       polecat.WorkstateVerdictNeedsMQSubmit,
		Reason:        "mq-refused-closed-source",
		NeedsRecovery: true,
		NeedsMQSubmit: true,
		MQStatus:      "refused_closed_source",
	}

	recordNeedsMQSubmitObservation(townRoot, obs, disposition)

	log := mqJournalTownLog(t, townRoot)
	if !strings.Contains(log, "[needs_mq_submit]") || !strings.Contains(log, "gastown/polecats/ace") {
		t.Fatalf("town log does not record the episode:\n%s", log)
	}
}

// TestRecordNeedsMQSubmitObservation_NoTownRootIsANoop: the surfaces resolve the
// town root best-effort and must keep reporting when it is unknown.
func TestRecordNeedsMQSubmitObservation_NoTownRootIsANoop(t *testing.T) {
	recordNeedsMQSubmitObservation("", polecat.MQSubmitObservation{Rig: "gastown", Polecat: "ace"}, polecat.WorkstateDisposition{NeedsMQSubmit: true})
}

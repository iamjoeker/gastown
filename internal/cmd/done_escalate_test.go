package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/testguard"
)

// stubRaiseEscalation swaps in a fake escalation raiser for the duration of a
// test and returns a pointer to the request it captured.
func stubRaiseEscalation(t *testing.T, outcome *escalationOutcome, err error) *escalationRequest {
	t.Helper()
	var captured escalationRequest
	prev := raiseEscalationFn
	raiseEscalationFn = func(req escalationRequest) (*escalationOutcome, error) {
		captured = req
		return outcome, err
	}
	t.Cleanup(func() { raiseEscalationFn = prev })
	return &captured
}

// captureNudges routes nudges to a log file instead of a live session and
// returns a reader for what was recorded.
func captureNudges(t *testing.T) func() string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "nudges.log")
	t.Setenv(testguard.LogEnv, logPath)
	return func() string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return ""
			}
			t.Fatalf("read nudge log: %v", err)
		}
		return string(data)
	}
}

// TestEscalateDoneRebaseFailureIsLoud covers gt-lj2n acceptance 3: a failed
// auto-rebase must exit non-zero AND reach somebody. Before this, gt done
// returned the error and told nobody, so a blocked polecat was indistinguishable
// from an idle one.
func TestEscalateDoneRebaseFailureIsLoud(t *testing.T) {
	readNudges := captureNudges(t)
	req := stubRaiseEscalation(t, &escalationOutcome{
		RecordID:      "hq-wisp-rec1",
		DurableBeadID: "hq-dur1",
		Actions:       []string{"bead", "mail:mayor"},
		Delivered:     true,
	}, nil)

	rebaseErr := errors.New("could not apply 004e45d5b")
	got := escalateDoneRebaseFailure(t.TempDir(), "beads", "beads/polecats/ace", "bd-4l6",
		"polecat/ace/bd-4l6", "upstream/main", 57, rebaseErr)

	if !errors.Is(got, rebaseErr) {
		t.Fatalf("returned error must still wrap the rebase failure so gt done exits non-zero, got %v", got)
	}
	if req.Severity != config.SeverityHigh {
		t.Fatalf("escalation severity = %q, want %q", req.Severity, config.SeverityHigh)
	}
	if req.RelatedBead != "bd-4l6" {
		t.Fatalf("escalation related bead = %q, want bd-4l6", req.RelatedBead)
	}
	if req.Source != "gt done" {
		t.Fatalf("escalation source = %q, want %q", req.Source, "gt done")
	}
	// The base is the thing that was wrong in gt-lj2n; an escalation that does
	// not name it cannot be triaged from the queue.
	if !strings.Contains(req.Description, "upstream/main") || !strings.Contains(req.Description, "polecat/ace/bd-4l6") {
		t.Fatalf("escalation description must name the branch and the base, got %q", req.Description)
	}
	if !strings.Contains(req.Reason, "could not apply 004e45d5b") {
		t.Fatalf("escalation reason must carry the rebase error, got %q", req.Reason)
	}

	nudges := readNudges()
	if !strings.Contains(nudges, "REBASE_BLOCKED") {
		t.Fatalf("Witness was not nudged about the blocked rebase, log: %q", nudges)
	}
	// A nudge carrying a newline submits the target's composer early, truncating
	// the message (gt-lj2n keeps the reason multi-line).
	if strings.Contains(strings.TrimSuffix(nudges, "\n"), "\n") {
		t.Fatalf("nudge message must be a single line, got %q", nudges)
	}
}

// TestEscalateDoneRebaseFailureStillFailsWhenEscalationFails guards the
// substitution error: escalation trouble must never soften the exit status.
func TestEscalateDoneRebaseFailureStillFailsWhenEscalationFails(t *testing.T) {
	readNudges := captureNudges(t)
	stubRaiseEscalation(t, nil, errors.New("bd unreachable"))

	rebaseErr := errors.New("could not apply 004e45d5b")
	got := escalateDoneRebaseFailure(t.TempDir(), "beads", "beads/polecats/ace", "bd-4l6",
		"polecat/ace/bd-4l6", "upstream/main", 57, rebaseErr)

	if !errors.Is(got, rebaseErr) {
		t.Fatalf("a failed escalation must not change the returned error, got %v", got)
	}
	if !strings.Contains(readNudges(), "REBASE_BLOCKED") {
		t.Fatal("Witness must still be nudged when the escalation could not be filed")
	}
}

// TestEscalateDoneRebaseFailureWithoutTownRootStillFails covers the path where
// no escalation is even attempted.
func TestEscalateDoneRebaseFailureWithoutTownRootStillFails(t *testing.T) {
	readNudges := captureNudges(t)
	raised := false
	prev := raiseEscalationFn
	raiseEscalationFn = func(escalationRequest) (*escalationOutcome, error) {
		raised = true
		return &escalationOutcome{Delivered: true}, nil
	}
	t.Cleanup(func() { raiseEscalationFn = prev })

	rebaseErr := errors.New("could not apply 004e45d5b")
	got := escalateDoneRebaseFailure("", "beads", "beads/polecats/ace", "bd-4l6",
		"polecat/ace/bd-4l6", "upstream/main", 57, rebaseErr)

	if !errors.Is(got, rebaseErr) {
		t.Fatalf("returned error = %v, want the rebase failure", got)
	}
	if raised {
		t.Fatal("no town root resolved, so no escalation should have been attempted")
	}
	// The nudge needs no town root and no bd, so it is the one signal that must
	// survive when everything else is unavailable.
	if !strings.Contains(readNudges(), "REBASE_BLOCKED") {
		t.Fatal("Witness must still be nudged when no town root could be resolved")
	}
}

// TestDoneRebaseEscalationFingerprintIsPerBlockage keeps reruns of gt done on
// one stuck branch from filing a fresh escalation each time, while still letting
// a different branch or a different base file its own.
func TestDoneRebaseEscalationFingerprintIsPerBlockage(t *testing.T) {
	a := doneRebaseEscalationFingerprint("polecat/ace/bd-4l6", "upstream/main")
	if a == "" {
		t.Fatal("fingerprint must not be empty, or every rerun files a duplicate escalation")
	}
	if a != doneRebaseEscalationFingerprint("polecat/ace/bd-4l6", "upstream/main") {
		t.Fatal("fingerprint must be stable across reruns of the same blockage")
	}
	if a == doneRebaseEscalationFingerprint("polecat/crater/gt-lj2n", "upstream/main") {
		t.Fatal("a different branch is a different blockage and must escalate separately")
	}
	if a == doneRebaseEscalationFingerprint("polecat/ace/bd-4l6", "origin/main") {
		t.Fatal("a different base is a different blockage and must escalate separately")
	}
}

package daemon

import (
	"strings"
	"testing"
	"time"
)

// These tests pin the logging half of gt-s1id. A convoy whose only ready issue
// cannot be slung is retried every scan, and the feeder used to write a
// byte-identical line to daemon.log every time — 120 lines an hour, forever,
// burying whatever was new. Two instances in one evening ran for 50 minutes and
// several Mayor checks respectively before a human noticed.

func TestNoteSlingFailure_FirstOccurrenceLogsVerbatim(t *testing.T) {
	m := &ConvoyManager{}
	now := time.Date(2026, 8, 26, 0, 23, 15, 0, time.UTC)

	line, log := m.noteSlingFailure("hq-cv1", "gt-wkcz", "bead gt-wkcz is already hooked", now)
	if !log {
		t.Fatal("first occurrence must be logged")
	}
	if line != "bead gt-wkcz is already hooked" {
		t.Fatalf("first occurrence must log the message unchanged, got %q", line)
	}
}

func TestNoteSlingFailure_UnchangedRepeatIsSuppressed(t *testing.T) {
	m := &ConvoyManager{}
	now := time.Date(2026, 8, 26, 0, 23, 15, 0, time.UTC)
	msg := "bead gt-wkcz is already hooked"

	m.noteSlingFailure("hq-cv1", "gt-wkcz", msg, now)

	// Nineteen more 30s scans — the whole first ten minutes — stay quiet.
	for i := 1; i < 20; i++ {
		at := now.Add(time.Duration(i) * 30 * time.Second)
		if line, log := m.noteSlingFailure("hq-cv1", "gt-wkcz", msg, at); log {
			t.Fatalf("retry %d at %s must be suppressed, logged %q", i, at.Sub(now), line)
		}
	}
}

func TestNoteSlingFailure_RelogsWithRepeatCountAfterInterval(t *testing.T) {
	m := &ConvoyManager{}
	now := time.Date(2026, 8, 26, 0, 23, 15, 0, time.UTC)
	msg := "bead gt-wkcz is already hooked"

	m.noteSlingFailure("hq-cv1", "gt-wkcz", msg, now)
	for i := 1; i < 20; i++ {
		m.noteSlingFailure("hq-cv1", "gt-wkcz", msg, now.Add(time.Duration(i)*30*time.Second))
	}

	line, log := m.noteSlingFailure("hq-cv1", "gt-wkcz", msg, now.Add(slingFailureRepeatInterval))
	if !log {
		t.Fatal("an unchanged failure must be re-logged once per interval, not silenced forever")
	}
	if !strings.Contains(line, msg) {
		t.Fatalf("the periodic line must still carry the failure, got %q", line)
	}
	// 20 retries after the one that was logged, and 10m of them: the counts are
	// what make a stuck convoy legible without one line per scan.
	if !strings.Contains(line, "20 retries") {
		t.Fatalf("expected the retry count in %q", line)
	}
	if !strings.Contains(line, "10m0s") {
		t.Fatalf("expected the elapsed span in %q", line)
	}
}

func TestNoteSlingFailure_ChangedMessageLogsImmediately(t *testing.T) {
	m := &ConvoyManager{}
	now := time.Date(2026, 8, 26, 0, 23, 15, 0, time.UTC)

	m.noteSlingFailure("hq-cv1", "gt-wkcz", "bead gt-wkcz is already hooked", now)

	// A different failure is news even one scan later — suppression keys on the
	// message, not on the issue, or a convoy that moved from "hooked" to
	// "no rig for prefix" would look unchanged for ten minutes.
	line, log := m.noteSlingFailure("hq-cv1", "gt-wkcz", "rig gastown is parked", now.Add(30*time.Second))
	if !log {
		t.Fatal("a changed failure must be logged immediately")
	}
	if line != "rig gastown is parked" {
		t.Fatalf("expected the new message verbatim, got %q", line)
	}
}

func TestNoteSlingFailure_StateIsPerIssue(t *testing.T) {
	m := &ConvoyManager{}
	now := time.Date(2026, 8, 26, 0, 23, 15, 0, time.UTC)
	msg := "bead is already hooked"

	m.noteSlingFailure("hq-cv1", "gt-wkcz", msg, now)

	// Same convoy, different issue: a first sighting, not a repeat.
	if _, log := m.noteSlingFailure("hq-cv1", "gt-z5h7", msg, now); !log {
		t.Fatal("a different issue with the same message must log")
	}
	// Same issue id, different convoy: also a first sighting.
	if _, log := m.noteSlingFailure("hq-cv2", "gt-wkcz", msg, now); !log {
		t.Fatal("a different convoy with the same message must log")
	}
}

func TestClearSlingFailure_MakesNextFailureNewsAgain(t *testing.T) {
	m := &ConvoyManager{}
	now := time.Date(2026, 8, 26, 0, 23, 15, 0, time.UTC)
	msg := "bead gt-wkcz is already hooked"

	m.noteSlingFailure("hq-cv1", "gt-wkcz", msg, now)
	if _, log := m.noteSlingFailure("hq-cv1", "gt-wkcz", msg, now.Add(30*time.Second)); log {
		t.Fatal("precondition: the repeat should be suppressed")
	}

	m.clearSlingFailure("hq-cv1", "gt-wkcz")

	if _, log := m.noteSlingFailure("hq-cv1", "gt-wkcz", msg, now.Add(time.Minute)); !log {
		t.Fatal("after a successful sling, a later failure must be reported as new")
	}
}

func TestPruneSlingFailures_DropsUntouchedEntries(t *testing.T) {
	m := &ConvoyManager{}
	now := time.Date(2026, 8, 26, 0, 23, 15, 0, time.UTC)

	m.noteSlingFailure("hq-cv1", "gt-stale", "gone quiet", now)
	m.noteSlingFailure("hq-cv1", "gt-fresh", "still failing", now)

	// gt-fresh keeps being attempted; gt-stale does not.
	later := now.Add(slingFailureStateTTL + time.Minute)
	m.noteSlingFailure("hq-cv1", "gt-fresh", "still failing", later)

	m.pruneSlingFailures(later)

	if len(m.slingFailures) != 1 {
		t.Fatalf("expected only the live entry to survive, have %d", len(m.slingFailures))
	}
	if _, ok := m.slingFailures["hq-cv1\x00gt-fresh"]; !ok {
		t.Fatal("the entry still being attempted was pruned")
	}
}

func TestNoteSlingFailure_EmptyMapIsUsable(t *testing.T) {
	// scan() prunes before anything has failed, and clearSlingFailure runs on
	// the success path before any failure exists. Neither may panic on the nil
	// map a fresh manager starts with.
	m := &ConvoyManager{}
	m.pruneSlingFailures(time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC))
	m.clearSlingFailure("hq-cv1", "gt-wkcz")
}

package dog

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/mail"
)

// fakeMailbox implements DispatchMailbox for testing.
type fakeMailbox struct {
	messages []*mail.Message
	archived []string
	listErr  error
	archErr  map[string]error
}

func (f *fakeMailbox) List() ([]*mail.Message, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.messages, nil
}

func (f *fakeMailbox) Archive(id string) error {
	if err, ok := f.archErr[id]; ok && err != nil {
		return err
	}
	f.archived = append(f.archived, id)
	return nil
}

func dispatchMsg(id string, sent time.Time) *mail.Message {
	return &mail.Message{
		ID:        id,
		From:      "deacon/",
		To:        "deacon/dogs/alpha",
		Subject:   DispatchSubjectPrefix + "rebuild-gt",
		Timestamp: sent,
	}
}

// =============================================================================
// IsDispatchMail
// =============================================================================

func TestIsDispatchMail_Classification(t *testing.T) {
	now := time.Now()
	addr := DogAddress("alpha")

	tests := []struct {
		name string
		msg  *mail.Message
		want bool
	}{
		{"deacon dispatch", dispatchMsg("m1", now), true},
		{
			"daemon dispatch",
			&mail.Message{ID: "m2", From: "daemon", To: addr, Subject: DispatchSubjectPrefix + "x"},
			true,
		},
		{
			"human message with similar subject is not a dispatch",
			&mail.Message{ID: "m3", From: "overseer", To: addr, Subject: DispatchSubjectPrefix + "x"},
			false,
		},
		{
			"deacon message that is not a dispatch",
			&mail.Message{ID: "m4", From: "deacon/", To: addr, Subject: "HELP: something"},
			false,
		},
		{
			"dispatch addressed to a different dog",
			&mail.Message{ID: "m5", From: "deacon/", To: DogAddress("bravo"), Subject: DispatchSubjectPrefix + "x"},
			false,
		},
		{"nil message", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDispatchMail(tt.msg, addr); got != tt.want {
				t.Errorf("IsDispatchMail = %v, want %v", got, tt.want)
			}
		})
	}
}

// =============================================================================
// ScanDispatchMail
// =============================================================================

func TestScanDispatchMail_CountsAndAges(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	mb := &fakeMailbox{messages: []*mail.Message{
		dispatchMsg("m1", now.Add(-10*time.Minute)),
		dispatchMsg("m2", now.Add(-62*time.Minute)), // oldest
		{ID: "other", From: "overseer", To: DogAddress("alpha"), Subject: "hello", Timestamp: now.Add(-5 * time.Hour)},
	}}

	scan, err := ScanDispatchMail(mb, DogAddress("alpha"), now)
	if err != nil {
		t.Fatalf("ScanDispatchMail: %v", err)
	}
	if scan.Open != 2 {
		t.Errorf("Open = %d, want 2", scan.Open)
	}
	if scan.OldestAge != 62*time.Minute {
		t.Errorf("OldestAge = %v, want 62m", scan.OldestAge)
	}
	if len(scan.IDs) != 2 {
		t.Errorf("IDs = %v, want 2 entries", scan.IDs)
	}
}

// A message whose timestamp is missing must still be counted. Reporting it as
// age zero would let an unbounded orphan read as brand new.
func TestScanDispatchMail_ZeroTimestampCountsButDoesNotAge(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	mb := &fakeMailbox{messages: []*mail.Message{
		dispatchMsg("m1", time.Time{}),
		dispatchMsg("m2", now.Add(-20*time.Minute)),
	}}

	scan, err := ScanDispatchMail(mb, DogAddress("alpha"), now)
	if err != nil {
		t.Fatalf("ScanDispatchMail: %v", err)
	}
	if scan.Open != 2 {
		t.Errorf("Open = %d, want 2", scan.Open)
	}
	if scan.OldestAge != 20*time.Minute {
		t.Errorf("OldestAge = %v, want 20m", scan.OldestAge)
	}
}

func TestScanDispatchMail_ListError(t *testing.T) {
	mb := &fakeMailbox{listErr: errors.New("dolt unreachable")}
	if _, err := ScanDispatchMail(mb, DogAddress("alpha"), time.Now()); err == nil {
		t.Fatal("expected error when mailbox list fails")
	}
}

func TestScanDispatchMail_NilMailbox(t *testing.T) {
	scan, err := ScanDispatchMail(nil, DogAddress("alpha"), time.Now())
	if err != nil || scan.Open != 0 {
		t.Fatalf("nil mailbox: scan=%+v err=%v, want empty scan and no error", scan, err)
	}
}

// =============================================================================
// ReclaimDispatchMail
// =============================================================================

func TestReclaimDispatchMail_ArchivesOnlyDispatches(t *testing.T) {
	now := time.Now()
	keep := &mail.Message{ID: "keep", From: "overseer", To: DogAddress("alpha"), Subject: "please look at this"}
	mb := &fakeMailbox{messages: []*mail.Message{
		dispatchMsg("m1", now),
		keep,
		dispatchMsg("m2", now),
	}}

	n, err := ReclaimDispatchMail(mb, DogAddress("alpha"))
	if err != nil {
		t.Fatalf("ReclaimDispatchMail: %v", err)
	}
	if n != 2 {
		t.Errorf("archived %d, want 2", n)
	}
	for _, id := range mb.archived {
		if id == "keep" {
			t.Error("archived a non-dispatch message")
		}
	}
}

// A single failing archive must not abort the rest: partial reclamation of an
// orphan backlog is strictly better than none.
func TestReclaimDispatchMail_PartialFailureContinues(t *testing.T) {
	now := time.Now()
	mb := &fakeMailbox{
		messages: []*mail.Message{
			dispatchMsg("m1", now),
			dispatchMsg("m2", now),
			dispatchMsg("m3", now),
		},
		archErr: map[string]error{"m2": errors.New("bead locked")},
	}

	n, err := ReclaimDispatchMail(mb, DogAddress("alpha"))
	if err == nil {
		t.Fatal("expected the archive failure to be reported")
	}
	if n != 2 {
		t.Errorf("archived %d, want 2 (m1 and m3)", n)
	}
}

// =============================================================================
// Alarm cooldown
// =============================================================================

func TestShouldAlarmDispatch_FiresOncePerCooldown(t *testing.T) {
	m, _ := testManager(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateIdle, LastActive: now, CreatedAt: now, UpdatedAt: now,
	})

	if !m.ShouldAlarmDispatch("alpha", 6*time.Hour, now) {
		t.Fatal("first alarm should fire")
	}
	if m.ShouldAlarmDispatch("alpha", 6*time.Hour, now.Add(1*time.Hour)) {
		t.Error("alarm within cooldown should be suppressed")
	}
	if !m.ShouldAlarmDispatch("alpha", 6*time.Hour, now.Add(7*time.Hour)) {
		t.Error("alarm after cooldown should fire again")
	}
}

func TestClearDispatchAlarm_ResetsCooldown(t *testing.T) {
	m, _ := testManager(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateIdle, LastActive: now, CreatedAt: now, UpdatedAt: now,
	})

	if !m.ShouldAlarmDispatch("alpha", 6*time.Hour, now) {
		t.Fatal("first alarm should fire")
	}
	m.ClearDispatchAlarm("alpha")
	if !m.ShouldAlarmDispatch("alpha", 6*time.Hour, now.Add(time.Minute)) {
		t.Error("a cleared alarm must let the next problem alarm immediately")
	}
}

// =============================================================================
// DeriveExecState
// =============================================================================

func TestDeriveExecState(t *testing.T) {
	tests := []struct {
		name       string
		pool       State
		session    bool
		dispatches int
		want       ExecState
	}{
		{"working with session", StateWorking, true, 0, ExecWorking},
		{"working with session and mail", StateWorking, true, 3, ExecWorking},
		{"working without session is stalled", StateWorking, false, 0, ExecStalled},
		{"working without session, holding mail, still stalled", StateWorking, false, 19, ExecStalled},
		{"idle with session is orphan", StateIdle, true, 0, ExecOrphan},
		{"idle with session and mail is still orphan", StateIdle, true, 19, ExecOrphan},
		{"idle holding undelivered dispatches is pending, not idle", StateIdle, false, 19, ExecPending},
		{"genuinely idle", StateIdle, false, 0, ExecIdle},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveExecState(tt.pool, tt.session, tt.dispatches)
			if got != tt.want {
				t.Errorf("DeriveExecState(%v, %v, %d) = %q, want %q",
					tt.pool, tt.session, tt.dispatches, got, tt.want)
			}
		})
	}
}

// The measured failure: three dogs reported idle while each held 19 open
// dispatches. Pool intent must never be able to report that as capacity.
func TestDeriveExecState_PendingIsNotIdle(t *testing.T) {
	got := DeriveExecState(StateIdle, false, 19)
	if got == ExecIdle {
		t.Fatal("a dog holding 19 undelivered dispatches must not report as idle")
	}
	if got.Healthy() {
		t.Errorf("%q must not be a healthy state", got)
	}
}

func TestExecState_Healthy(t *testing.T) {
	healthy := []ExecState{ExecIdle, ExecWorking}
	unhealthy := []ExecState{ExecStalled, ExecPending, ExecOrphan}

	for _, s := range healthy {
		if !s.Healthy() {
			t.Errorf("%q should be healthy", s)
		}
	}
	for _, s := range unhealthy {
		if s.Healthy() {
			t.Errorf("%q should not be healthy", s)
		}
	}
}

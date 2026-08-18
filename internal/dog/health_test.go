package dog

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
)

// mockSessionChecker implements sessionChecker for testing.
type mockSessionChecker struct {
	healthResults  map[string]tmux.ZombieStatus // session -> status
	sessionsAlive  map[string]bool              // session -> exists
	killedSessions []string
}

func newMockChecker() *mockSessionChecker {
	return &mockSessionChecker{
		healthResults: make(map[string]tmux.ZombieStatus),
		sessionsAlive: make(map[string]bool),
	}
}

func (m *mockSessionChecker) CheckSessionHealth(session string, _ time.Duration) tmux.ZombieStatus {
	if s, ok := m.healthResults[session]; ok {
		return s
	}
	return tmux.SessionDead
}

func (m *mockSessionChecker) HasSession(name string) (bool, error) {
	return m.sessionsAlive[name], nil
}

func (m *mockSessionChecker) KillSession(name string) error {
	m.killedSessions = append(m.killedSessions, name)
	return nil
}

// =============================================================================
// Healthy dogs
// =============================================================================

func TestHealth_IdleDog_NoSession(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateIdle, LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if r.NeedsAttention {
		t.Error("idle dog with no session should not need attention")
	}
	if r.SessionStatus != "none" {
		t.Errorf("session_status = %q, want 'none'", r.SessionStatus)
	}
	if r.WorkDuration != 0 {
		t.Errorf("work_duration = %v, want 0", r.WorkDuration)
	}
}

func TestHealth_WorkingDog_Healthy(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	workStart := now.Add(-10 * time.Minute)
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		WorkStartedAt: workStart, LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.SessionHealthy
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if r.NeedsAttention {
		t.Error("healthy working dog should not need attention")
	}
	if r.SessionStatus != "healthy" {
		t.Errorf("session_status = %q, want 'healthy'", r.SessionStatus)
	}
	if r.WorkDuration < 10*time.Minute {
		t.Errorf("work_duration = %v, want >= 10m", r.WorkDuration)
	}
}

// =============================================================================
// Zombies
// =============================================================================

func TestHealth_Zombie_SessionDead(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		WorkStartedAt: now.Add(-1 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.SessionDead
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if !r.NeedsAttention {
		t.Error("zombie (SessionDead) should need attention")
	}
	if r.AutoCleared {
		t.Error("should not auto-clear when autoClear=false")
	}
}

func TestHealth_Zombie_AgentDead(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		WorkStartedAt: now.Add(-1 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.AgentDead
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if !r.NeedsAttention {
		t.Error("zombie (AgentDead) should need attention")
	}
	if r.AutoCleared {
		t.Error("should not auto-clear when autoClear=false")
	}
}

// =============================================================================
// Hung
// =============================================================================

func TestHealth_Hung_ReportOnly(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		WorkStartedAt: now.Add(-2 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.AgentHung
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false) // autoClear=false: report only

	if !r.NeedsAttention {
		t.Error("hung dog should need attention")
	}
	if r.AutoCleared {
		t.Error("hung dog should NOT be auto-cleared when autoClear=false")
	}
	if r.SessionStatus != "agent-hung" {
		t.Errorf("session_status = %q, want 'agent-hung'", r.SessionStatus)
	}
}

func TestHealth_Hung_AutoCleared(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		WorkStartedAt: now.Add(-2 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.AgentHung
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, true) // autoClear=true: kill and reclaim

	if !r.NeedsAttention {
		t.Error("hung dog should need attention")
	}
	if !r.AutoCleared {
		t.Error("hung dog should be auto-cleared when autoClear=true")
	}
	if len(mc.killedSessions) != 1 || mc.killedSessions[0] != "hq-dog-alpha" {
		t.Errorf("killedSessions = %v, want [hq-dog-alpha]", mc.killedSessions)
	}

	// Verify state was cleared
	d2, _ := m.Get("alpha")
	if d2.State != StateIdle {
		t.Errorf("state = %q, want idle after auto-clear", d2.State)
	}
}

// =============================================================================
// Auto-clear zombies
// =============================================================================

func TestHealth_AutoClear_SessionDead(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		WorkStartedAt: now.Add(-1 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.SessionDead
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, true)

	if !r.AutoCleared {
		t.Error("zombie (SessionDead) should be auto-cleared")
	}

	// Verify state was actually cleared
	d2, _ := m.Get("alpha")
	if d2.State != StateIdle {
		t.Errorf("state = %q, want idle after auto-clear", d2.State)
	}
	if d2.Work != "" {
		t.Errorf("work = %q, want empty after auto-clear", d2.Work)
	}
}

func TestHealth_AutoClear_AgentDead(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		WorkStartedAt: now.Add(-1 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.AgentDead
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, true)

	if !r.AutoCleared {
		t.Error("zombie (AgentDead) should be auto-cleared")
	}

	// Verify session was killed
	if len(mc.killedSessions) != 1 || mc.killedSessions[0] != "hq-dog-alpha" {
		t.Errorf("killedSessions = %v, want [hq-dog-alpha]", mc.killedSessions)
	}

	// Verify state was cleared
	d2, _ := m.Get("alpha")
	if d2.State != StateIdle {
		t.Errorf("state = %q, want idle after auto-clear", d2.State)
	}
}

// =============================================================================
// Orphan sessions
// =============================================================================

func TestHealth_Orphan_IdleWithSession(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateIdle, LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.sessionsAlive["hq-dog-alpha"] = true
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if !r.NeedsAttention {
		t.Error("orphan session should need attention")
	}
	if r.SessionStatus != "orphan" {
		t.Errorf("session_status = %q, want 'orphan'", r.SessionStatus)
	}
}

func TestHealth_Orphan_AutoCleared(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateIdle, LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.sessionsAlive["hq-dog-alpha"] = true
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, true) // autoClear=true: kill orphan session

	if !r.NeedsAttention {
		t.Error("orphan session should need attention")
	}
	if !r.AutoCleared {
		t.Error("orphan session should be auto-cleared when autoClear=true")
	}
	if len(mc.killedSessions) != 1 || mc.killedSessions[0] != "hq-dog-alpha" {
		t.Errorf("killedSessions = %v, want [hq-dog-alpha]", mc.killedSessions)
	}
}

// =============================================================================
// WorkDuration computation
// =============================================================================

func TestHealth_WorkDuration_ZeroStartedAt(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	// Working dog with zero WorkStartedAt (legacy state file)
	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateWorking, Work: "task-1",
		LastActive: now, CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-alpha"] = tmux.SessionHealthy
	hc := NewHealthChecker(m, mc)

	d, _ := m.Get("alpha")
	r := hc.Check(d, 30*time.Minute, false)

	if r.WorkDuration != 0 {
		t.Errorf("work_duration = %v, want 0 for zero WorkStartedAt", r.WorkDuration)
	}
}

// =============================================================================
// CheckAll
// =============================================================================

func TestHealth_CheckAll_MultipleDogs(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()

	setupDogWithState(t, m, "alpha", &DogState{
		Name: "alpha", State: StateIdle, LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})
	setupDogWithState(t, m, "beta", &DogState{
		Name: "beta", State: StateWorking, Work: "task-1",
		WorkStartedAt: now.Add(-1 * time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})

	mc := newMockChecker()
	mc.healthResults["hq-dog-beta"] = tmux.SessionDead // zombie
	hc := NewHealthChecker(m, mc)

	results, err := hc.CheckAll(30*time.Minute, false)
	if err != nil {
		t.Fatalf("CheckAll() error = %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("CheckAll() returned %d results, want 2", len(results))
	}

	attention := NeedsAttentionCount(results)
	if attention != 1 {
		t.Errorf("NeedsAttentionCount = %d, want 1", attention)
	}
}

// =============================================================================
// NeedsAttentionCount
// =============================================================================

func TestNeedsAttentionCount(t *testing.T) {
	results := []DogHealthResult{
		{Name: "a", NeedsAttention: false},
		{Name: "b", NeedsAttention: true},
		{Name: "c", NeedsAttention: true},
		{Name: "d", NeedsAttention: false},
	}

	if got := NeedsAttentionCount(results); got != 2 {
		t.Errorf("NeedsAttentionCount = %d, want 2", got)
	}

	if got := NeedsAttentionCount(nil); got != 0 {
		t.Errorf("NeedsAttentionCount(nil) = %d, want 0", got)
	}
}

// =============================================================================
// Dispatch inspection (orphaned + stale dispatch mail)
// =============================================================================

// fakeInspector implements DispatchInspector for testing.
type fakeInspector struct {
	scans      map[string]DispatchScan
	scanErr    error
	reclaimed  map[string]int
	reclaimErr error
}

func newFakeInspector() *fakeInspector {
	return &fakeInspector{
		scans:     make(map[string]DispatchScan),
		reclaimed: make(map[string]int),
	}
}

func (f *fakeInspector) Scan(dogName string) (DispatchScan, error) {
	if f.scanErr != nil {
		return DispatchScan{}, f.scanErr
	}
	return f.scans[dogName], nil
}

func (f *fakeInspector) Reclaim(dogName string) (int, error) {
	if f.reclaimErr != nil {
		return 0, f.reclaimErr
	}
	n := f.scans[dogName].Open
	f.reclaimed[dogName] = n
	return n, nil
}

// setupIdleDog creates an idle dog and returns it.
func setupIdleDog(t *testing.T, m *Manager, name string) *Dog {
	t.Helper()
	now := time.Now()
	setupDogWithState(t, m, name, &DogState{
		Name: name, State: StateIdle, LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})
	d, err := m.Get(name)
	if err != nil {
		t.Fatalf("getting dog %s: %v", name, err)
	}
	return d
}

// The measured failure: a dog reported idle while holding 19 dispatches that
// no session will ever read.
func TestHealth_IdleDogHoldingDispatches_IsPendingAndNeedsAttention(t *testing.T) {
	m, _ := testManager(t)
	d := setupIdleDog(t, m, "charlie")

	insp := newFakeInspector()
	insp.scans["charlie"] = DispatchScan{Open: 19, OldestAge: 62 * time.Minute}

	hc := NewHealthChecker(m, newMockChecker()).WithDispatch(insp, 30*time.Minute, time.Hour)
	r := hc.Check(d, 30*time.Minute, false)

	if !r.NeedsAttention {
		t.Error("a dog holding 19 orphaned dispatches must need attention")
	}
	if r.ExecState != ExecPending {
		t.Errorf("exec_state = %q, want %q", r.ExecState, ExecPending)
	}
	if r.OpenDispatches != 19 {
		t.Errorf("open_dispatches = %d, want 19", r.OpenDispatches)
	}
	if r.DispatchesReclaimed != 0 {
		t.Errorf("reclaimed %d without auto-clear, want 0", r.DispatchesReclaimed)
	}
	if r.DispatchAlarm == "" {
		t.Error("an unreclaimed orphan backlog must raise an alarm")
	}
}

func TestHealth_AutoClear_ReclaimsOrphanedDispatches(t *testing.T) {
	m, _ := testManager(t)
	d := setupIdleDog(t, m, "charlie")

	insp := newFakeInspector()
	insp.scans["charlie"] = DispatchScan{Open: 19, OldestAge: 62 * time.Minute}

	hc := NewHealthChecker(m, newMockChecker()).WithDispatch(insp, 30*time.Minute, time.Hour)
	r := hc.Check(d, 30*time.Minute, true)

	if r.DispatchesReclaimed != 19 {
		t.Errorf("reclaimed = %d, want 19", r.DispatchesReclaimed)
	}
	if insp.reclaimed["charlie"] != 19 {
		t.Errorf("inspector reclaimed %d, want 19", insp.reclaimed["charlie"])
	}
	if r.DispatchAlarm != "" {
		t.Error("a reclaimed backlog should not also escalate")
	}
}

// A zombie's dispatches must be reclaimed too — session death has to fail the
// dispatch, not orphan it.
func TestHealth_DeadSessionZombie_ReclaimsDispatches(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "delta", &DogState{
		Name: "delta", State: StateWorking, Work: "plugin:rebuild-gt",
		WorkStartedAt: now.Add(-time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})
	d, _ := m.Get("delta")

	mc := newMockChecker()
	mc.healthResults["hq-dog-delta"] = tmux.SessionDead

	insp := newFakeInspector()
	insp.scans["delta"] = DispatchScan{Open: 3, OldestAge: 90 * time.Minute}

	hc := NewHealthChecker(m, mc).WithDispatch(insp, 30*time.Minute, time.Hour)
	r := hc.Check(d, 30*time.Minute, true)

	if r.ExecState != ExecStalled {
		t.Errorf("exec_state = %q, want %q", r.ExecState, ExecStalled)
	}
	if r.DispatchesReclaimed != 3 {
		t.Errorf("reclaimed = %d, want 3", r.DispatchesReclaimed)
	}
	// The session verdict must survive alongside the dispatch verdict.
	if !strings.Contains(r.Recommendation, "zombie") {
		t.Errorf("recommendation lost the session finding: %q", r.Recommendation)
	}
	if !strings.Contains(r.Recommendation, "dispatch") {
		t.Errorf("recommendation lost the dispatch finding: %q", r.Recommendation)
	}
}

// A live session holding a dispatch past the threshold alarms but is NOT
// reclaimed — it may still be mid-execution.
func TestHealth_StaleDispatchWithLiveSession_AlarmsWithoutReclaiming(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "delta", &DogState{
		Name: "delta", State: StateWorking, Work: "plugin:rebuild-gt",
		WorkStartedAt: now.Add(-time.Hour), LastActive: now,
		CreatedAt: now, UpdatedAt: now,
	})
	d, _ := m.Get("delta")

	mc := newMockChecker()
	mc.healthResults["hq-dog-delta"] = tmux.SessionHealthy

	insp := newFakeInspector()
	insp.scans["delta"] = DispatchScan{Open: 1, OldestAge: 58 * time.Minute}

	hc := NewHealthChecker(m, mc).WithDispatch(insp, 30*time.Minute, time.Hour)
	r := hc.Check(d, 30*time.Minute, true)

	if !r.NeedsAttention {
		t.Error("a dispatch open 58m past a 30m threshold must need attention")
	}
	if r.DispatchesReclaimed != 0 {
		t.Errorf("reclaimed %d from a live session, want 0", r.DispatchesReclaimed)
	}
	if r.DispatchAlarm == "" {
		t.Error("stale dispatch on a live session must alarm")
	}
	if r.ExecState != ExecWorking {
		t.Errorf("exec_state = %q, want %q", r.ExecState, ExecWorking)
	}
}

func TestHealth_FreshDispatchWithLiveSession_NoAlarm(t *testing.T) {
	m, _ := testManager(t)
	now := time.Now()
	setupDogWithState(t, m, "delta", &DogState{
		Name: "delta", State: StateWorking, Work: "plugin:rebuild-gt",
		WorkStartedAt: now, LastActive: now, CreatedAt: now, UpdatedAt: now,
	})
	d, _ := m.Get("delta")

	mc := newMockChecker()
	mc.healthResults["hq-dog-delta"] = tmux.SessionHealthy

	insp := newFakeInspector()
	insp.scans["delta"] = DispatchScan{Open: 1, OldestAge: 2 * time.Minute}

	hc := NewHealthChecker(m, mc).WithDispatch(insp, 30*time.Minute, time.Hour)
	r := hc.Check(d, 30*time.Minute, true)

	if r.NeedsAttention {
		t.Errorf("a 2-minute-old dispatch on a healthy session must not alarm: %q", r.Recommendation)
	}
	if r.DispatchAlarm != "" {
		t.Errorf("unexpected alarm: %q", r.DispatchAlarm)
	}
}

// Repeat health-check cycles must not re-escalate the same stranded dispatch.
func TestHealth_AlarmRespectsCooldownAcrossChecks(t *testing.T) {
	m, _ := testManager(t)
	d := setupIdleDog(t, m, "charlie")

	insp := newFakeInspector()
	insp.scans["charlie"] = DispatchScan{Open: 5, OldestAge: 2 * time.Hour}

	hc := NewHealthChecker(m, newMockChecker()).WithDispatch(insp, 30*time.Minute, 6*time.Hour)

	first := hc.Check(d, 30*time.Minute, false)
	if first.DispatchAlarm == "" {
		t.Fatal("first check should alarm")
	}
	second := hc.Check(d, 30*time.Minute, false)
	if second.DispatchAlarm != "" {
		t.Error("second check within cooldown must not re-escalate")
	}
	if !second.NeedsAttention {
		t.Error("cooldown suppresses the escalation, not the finding")
	}
}

// A mailbox we cannot read is the same blind spot that let orphans accumulate,
// so it must surface rather than pass silently.
func TestHealth_DispatchScanError_SurfacesAsAttention(t *testing.T) {
	m, _ := testManager(t)
	d := setupIdleDog(t, m, "charlie")

	insp := newFakeInspector()
	insp.scanErr = errors.New("dolt unreachable")

	hc := NewHealthChecker(m, newMockChecker()).WithDispatch(insp, 30*time.Minute, time.Hour)
	r := hc.Check(d, 30*time.Minute, false)

	if !r.NeedsAttention {
		t.Error("an unreadable dispatch mailbox must need attention")
	}
	if !strings.Contains(r.Recommendation, "dispatch scan failed") {
		t.Errorf("recommendation = %q, want it to name the scan failure", r.Recommendation)
	}
}

// Without an inspector the checker must behave exactly as before.
func TestHealth_NoInspector_SkipsDispatchChecks(t *testing.T) {
	m, _ := testManager(t)
	d := setupIdleDog(t, m, "charlie")

	hc := NewHealthChecker(m, newMockChecker())
	r := hc.Check(d, 30*time.Minute, false)

	if r.NeedsAttention {
		t.Error("idle dog with no session and no inspector must be healthy")
	}
	if r.OpenDispatches != 0 || r.DispatchAlarm != "" {
		t.Error("dispatch fields must stay empty without an inspector")
	}
	if r.ExecState != ExecIdle {
		t.Errorf("exec_state = %q, want %q", r.ExecState, ExecIdle)
	}
}

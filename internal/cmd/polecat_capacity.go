package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
)

const polecatAdmissionReservationTTL = 30 * time.Minute

var acquirePolecatAdmissionFn = acquirePolecatAdmission

// polecatCapacitySnapshot is only fully populated when admission control is on
// (max > 0). See MarshalJSON for how that is reflected on the wire; the field
// tags below are not what callers see.
type polecatCapacitySnapshot struct {
	Max int `json:"max"`
	// VerifiedReusableIdle counts idle polecats a git check cleared for reuse.
	// It is deliberately not called "reusable idle": nothing in ordinary
	// operation runs that check, so it is a floor on what is available, never
	// the total. See polecatCapacityUnverifiedNote (gt-rjhr).
	VerifiedReusableIdle int `json:"verified_reusable_idle"`
	Working              int `json:"working"`
	RecoveryBlocked      int `json:"recovery_blocked"`
	UnverifiedIdle       int `json:"unverified_idle"`
	PendingMR            int `json:"pending_mr"`
	Reservations         int `json:"reservations"`
	Free                 int `json:"free"`
	ActiveSessions       int `json:"active_sessions"`
	capacityUsed         int
	// unverifiedExample names one polecat counted in UnverifiedIdle, so the
	// note can hand the reader a command to run rather than a placeholder.
	unverifiedExample string
}

func (s polecatCapacitySnapshot) occupied() int {
	return s.capacityUsed + s.Reservations
}

// measuredPolecatCapacityJSON is the wire form of a snapshot taken while
// admission control is enabled (max > 0): every field was counted.
type measuredPolecatCapacityJSON struct {
	Max                  int    `json:"max"`
	Admission            string `json:"admission"`
	Measured             bool   `json:"measured"`
	Working              int    `json:"working"`
	RecoveryBlocked      int    `json:"recovery_blocked"`
	VerifiedReusableIdle int    `json:"verified_reusable_idle"`
	UnverifiedIdle       int    `json:"unverified_idle"`
	// UnverifiedIdleNote is absent exactly when unverified_idle is 0, which is
	// emitted unconditionally beside it — so its absence is readable, not a
	// silently dropped measurement.
	UnverifiedIdleNote string `json:"unverified_idle_note,omitempty"`
	PendingMR          int    `json:"pending_mr"`
	Reservations       int    `json:"reservations"`
	Free               int    `json:"free"`
	ActiveSessions     int    `json:"active_sessions"`
}

// unmeasuredPolecatCapacityJSON is the wire form of a snapshot taken while
// admission control is disabled (max <= 0). Only max and active_sessions are
// real; the per-disposition counters are omitted rather than emitted as zeros.
type unmeasuredPolecatCapacityJSON struct {
	Max            int    `json:"max"`
	Admission      string `json:"admission"`
	Measured       bool   `json:"measured"`
	ActiveSessions int    `json:"active_sessions"`
	Note           string `json:"note"`
}

// MarshalJSON emits the capacity block with an explicit measured/admission
// discriminator, and omits the per-disposition counters when they were never
// measured.
//
// polecatCapacitySnapshotForTownNoCleanup short-circuits at max <= 0 before it
// reads rigs.json, lists sessions, or applies any workstate disposition, so
// Working/RecoveryBlocked/VerifiedReusableIdle/PendingMR/Reservations/Free are struct
// zero values in that mode — not counts of zero. Marshaling them unconditionally
// told readers "free: 0" (capacity exhausted) when admission was in fact
// disabled and nothing was being gated, and "recovery_blocked: 0" while
// polecats needed recovery. The human-readable branch in `gt scheduler status`
// has always guarded on Max > 0; this keeps the JSON branch honest too. (gt-7yv)
func (s polecatCapacitySnapshot) MarshalJSON() ([]byte, error) {
	if s.Max <= 0 {
		return json.Marshal(unmeasuredPolecatCapacityJSON{
			Max:            s.Max,
			Admission:      "disabled",
			Measured:       false,
			ActiveSessions: s.ActiveSessions,
			// Spell out the max rather than writing a "less than" sign:
			// encoding/json HTML-escapes that into an unreadable <.
			Note: fmt.Sprintf("direct dispatch (scheduler.max_polecats=%d): admission control is disabled and per-disposition capacity counts are not measured", s.Max),
		})
	}
	return json.Marshal(measuredPolecatCapacityJSON{
		Max:                  s.Max,
		Admission:            "enabled",
		Measured:             true,
		Working:              s.Working,
		RecoveryBlocked:      s.RecoveryBlocked,
		VerifiedReusableIdle: s.VerifiedReusableIdle,
		UnverifiedIdle:       s.UnverifiedIdle,
		UnverifiedIdleNote:   polecatCapacityUnverifiedNote(s),
		PendingMR:            s.PendingMR,
		Reservations:         s.Reservations,
		Free:                 s.Free,
		ActiveSessions:       s.ActiveSessions,
	})
}

// polecatCapacityBreakdown renders the per-disposition counters. Four surfaces
// print this breakdown; rendering it in one place is what keeps the field name
// and its caveat from drifting apart between them (gt-rjhr).
func polecatCapacityBreakdown(s polecatCapacitySnapshot) string {
	return fmt.Sprintf(
		"working: %d, recovery_blocked: %d, reservations: %d, verified_reusable_idle: %d, unverified_idle: %d, pending_mr: %d",
		s.Working, s.RecoveryBlocked, s.Reservations, s.VerifiedReusableIdle, s.UnverifiedIdle, s.PendingMR)
}

// polecatCapacityUnverifiedNote states, wherever the breakdown is printed, what
// verified_reusable_idle does not count. It returns "" when there is nothing
// unverified to disclaim.
//
// Nothing promotes a polecat out of unverified_idle during ordinary operation:
// the inventory surface that feeds this snapshot runs no git and says so about
// itself, and `gt polecat check-recovery` is operator-invoked. The count is
// therefore pinned near zero by construction — it cannot rise on its own.
//
// Read bare, a structural zero reads as scarcity, and scarcity is pressure
// toward destructive reclamation: the figure was cited three times in one
// night as evidence that stuck polecats had to be nuked to reclaim slots, while
// 21 of 25 were in fact eligible for reuse. The load-bearing correction is that
// unverified idle polecats consume no capacity — `free` never counted them as
// occupied, so a low reusable count is not a full town. (gt-rjhr)
func polecatCapacityUnverifiedNote(s polecatCapacitySnapshot) string {
	if s.Max <= 0 || s.UnverifiedIdle <= 0 {
		return ""
	}
	noun := "polecats"
	if s.UnverifiedIdle == 1 {
		noun = "polecat"
	}
	example := s.unverifiedExample
	if example == "" {
		example = "<rig>/<name>"
	}
	return fmt.Sprintf(
		"%d idle %s were classified WITHOUT a git check and nothing verifies proactively, so verified_reusable_idle=%d is a floor, not the number available for reuse. None of them counts against free. For a measured verdict: gt polecat check-recovery %s",
		s.UnverifiedIdle, noun, s.VerifiedReusableIdle, example)
}

// printPolecatCapacityUnverifiedNote writes the note as its own dimmed line
// beneath a capacity breakdown, or nothing when there is none.
func printPolecatCapacityUnverifiedNote(w io.Writer, s polecatCapacitySnapshot) {
	note := polecatCapacityUnverifiedNote(s)
	if note == "" {
		return
	}
	fmt.Fprintf(w, "%s\n", style.Dim.Render("  "+note))
}

func (s *polecatCapacitySnapshot) addWorking() {
	s.Working++
	s.capacityUsed++
}

func (s *polecatCapacitySnapshot) addRecoveryBlocked(countsTowardCapacity bool) {
	s.RecoveryBlocked++
	if countsTowardCapacity {
		s.capacityUsed++
	}
}

func (s *polecatCapacitySnapshot) addVerifiedReusableIdle() {
	s.VerifiedReusableIdle++
}

// addUnverifiedIdle counts a polecat that nothing blocked and nothing checked.
//
// This snapshot is built from the bead-only inventory constructor, which runs no
// git and no merge-queue lookup, so these polecats were previously counted as
// reusable_idle on the strength of facts nobody gathered — and `gt sling` then
// refused them one by one at the measured gate (gt-49dp). They are counted
// separately rather than dropped: like verified_reusable_idle they hold no
// capacity, but a reader comparing this projection against dispatch reality
// needs to see that the pool's apparent depth is unconfirmed.
//
// This is the bucket almost every idle polecat lands in, because nothing
// proactively runs the check that would move one out of it — which is why the
// verified count beside it must never be read as "how many are available"
// (gt-rjhr).
func (s *polecatCapacitySnapshot) addUnverifiedIdle() {
	s.UnverifiedIdle++
}

func (s *polecatCapacitySnapshot) addPendingMR() {
	s.PendingMR++
}

type polecatAdmissionReservation struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	Rig       string    `json:"rig,omitempty"`
	Bead      string    `json:"bead,omitempty"`
	Operation string    `json:"operation"`
	CreatedAt time.Time `json:"created_at"`
}

type polecatAdmissionHandle struct {
	townRoot string
	id       string
	path     string
	disabled bool
}

func (h *polecatAdmissionHandle) Release() {
	if h == nil || h.disabled || h.path == "" {
		return
	}
	_ = os.Remove(h.path)
}

type polecatCapacityAdmissionError struct {
	Snapshot polecatCapacitySnapshot
	Rig      string
	Bead     string
	Reason   string
}

func (e *polecatCapacityAdmissionError) Error() string {
	if e == nil {
		return "polecat admission denied"
	}
	if e.Snapshot.Max <= 0 {
		return fmt.Sprintf("polecat admission denied: %s", e.Reason)
	}
	note := polecatCapacityUnverifiedNote(e.Snapshot)
	if note != "" {
		note = " Note: " + note + "."
	}
	return fmt.Sprintf(
		"polecat admission denied: %s (max: %d, occupied: %d, %s, free: %d). Resolve recovery-needed polecats or raise scheduler.max_polecats; inspect with `gt scheduler status --json` or `gt polecat list --all --json`.%s",
		e.Reason,
		e.Snapshot.Max,
		e.Snapshot.occupied(),
		polecatCapacityBreakdown(e.Snapshot),
		e.Snapshot.Free,
		note,
	)
}

func acquirePolecatAdmission(townRoot, rigName, beadID, operation string) (*polecatAdmissionHandle, polecatCapacitySnapshot, error) {
	max, err := configuredSchedulerMaxPolecats(townRoot)
	if err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}
	if max <= 0 {
		return &polecatAdmissionHandle{disabled: true}, polecatCapacitySnapshot{Max: max, ActiveSessions: countActivePolecats()}, nil
	}

	lock, err := acquirePolecatAdmissionLock(townRoot)
	if err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}
	defer func() { _ = lock.Unlock() }()

	if err := cleanupStalePolecatAdmissionReservations(townRoot, time.Now()); err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}

	snapshot, err := polecatCapacitySnapshotForTownNoCleanup(townRoot)
	if err != nil {
		return nil, polecatCapacitySnapshot{}, err
	}
	if snapshot.Free <= 0 {
		return nil, snapshot, &polecatCapacityAdmissionError{
			Snapshot: snapshot,
			Rig:      rigName,
			Bead:     beadID,
			Reason:   "configured scheduler.max_polecats capacity is full",
		}
	}

	reservation, path, err := writePolecatAdmissionReservation(townRoot, rigName, beadID, operation)
	if err != nil {
		return nil, snapshot, err
	}
	snapshot.Reservations++
	snapshot.Free--
	return &polecatAdmissionHandle{townRoot: townRoot, id: reservation.ID, path: path}, snapshot, nil
}

func configuredSchedulerMaxPolecats(townRoot string) (int, error) {
	settings, err := config.LoadOrCreateTownSettings(config.TownSettingsPath(townRoot))
	if err != nil {
		return 0, fmt.Errorf("loading town settings for polecat admission: %w", err)
	}
	schedulerCfg := settings.Scheduler
	if schedulerCfg == nil {
		schedulerCfg = capacity.DefaultSchedulerConfig()
	}
	return schedulerCfg.GetMaxPolecats(), nil
}

func polecatCapacitySnapshotForTown(townRoot string) (polecatCapacitySnapshot, error) {
	max, err := configuredSchedulerMaxPolecats(townRoot)
	if err != nil {
		return polecatCapacitySnapshot{}, err
	}
	if max > 0 {
		if err := cleanupStalePolecatAdmissionReservationsWithLock(townRoot, time.Now()); err != nil {
			return polecatCapacitySnapshot{}, err
		}
	}
	return polecatCapacitySnapshotForTownNoCleanup(townRoot)
}

func polecatCapacitySnapshotForTownNoCleanup(townRoot string) (polecatCapacitySnapshot, error) {
	max, err := configuredSchedulerMaxPolecats(townRoot)
	if err != nil {
		return polecatCapacitySnapshot{}, err
	}
	snapshot := polecatCapacitySnapshot{Max: max, ActiveSessions: countActivePolecats()}
	if max <= 0 {
		return snapshot, nil
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return snapshot, fmt.Errorf("loading rigs config for polecat capacity: %w", err)
	}

	tmuxClient := tmux.NewTmux()
	sessionNames, err := tmuxClient.ListSessions()
	if err != nil {
		return snapshot, fmt.Errorf("listing tmux sessions for polecat capacity: %w", err)
	}
	sessions := newPolecatSessionSet(sessionNames)
	for rigName := range rigsConfig.Rigs {
		rigPath := filepath.Join(townRoot, rigName)
		if _, err := os.Stat(rigPath); err != nil {
			if !os.IsNotExist(err) {
				return snapshot, fmt.Errorf("stat rig path for %s capacity: %w", rigName, err)
			}
			continue
		}
		polecatNames, err := listPolecatDirectoryNames(rigPath)
		if err != nil {
			return snapshot, fmt.Errorf("listing polecat dirs for %s capacity: %w", rigName, err)
		}
		if len(polecatNames) == 0 {
			continue
		}

		rigBeads := beads.New(rigPath)
		agents, err := rigBeads.ListAgentBeads()
		if err != nil {
			return snapshot, fmt.Errorf("listing agent beads for %s capacity: %w", rigName, err)
		}
		activeWork, err := listActivePolecatWorkByName(rigBeads, rigName)
		if err != nil {
			return snapshot, fmt.Errorf("listing active polecat work for %s capacity: %w", rigName, err)
		}
		// Capacity counts a polecat holding an open MR as pending-MR rather than
		// reusable, so it needs the same branch-level queue view the inventory
		// surface uses (gt-46rk). A failed index degrades to the previous
		// bead-only counting instead of failing the whole snapshot.
		mrIndex, mrIndexErr := newPolecatBranchMRIndex(rigBeads)
		if mrIndexErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to index merge requests for %s capacity: %v\n", rigName, mrIndexErr)
			mrIndex = nil
		}
		prefix := beads.GetPrefixForRig(townRoot, rigName)
		for _, name := range polecatNames {
			agentID := beads.PolecatBeadIDWithPrefix(prefix, rigName, name)
			issue := agents[agentID]
			fields := parsePolecatAgentFields(issue)
			applyAgentFieldsToCapacitySnapshot(&snapshot, rigName, name, fields, activeWork[name], sessions, mrIndex)
		}
	}

	reservations, err := readPolecatAdmissionReservations(townRoot)
	if err != nil {
		return snapshot, err
	}
	snapshot.Reservations = len(reservations)
	if max > 0 {
		snapshot.Free = max - snapshot.occupied()
		if snapshot.Free < 0 {
			snapshot.Free = 0
		}
	}
	return snapshot, nil
}

func listPolecatDirectoryNames(rigPath string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(rigPath, "polecats"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func applyAgentFieldsToCapacitySnapshot(snapshot *polecatCapacitySnapshot, rigName, polecatName string, fields *beads.AgentFields, activeWork *beads.Issue, sessions polecatSessionSet, mrIndex *polecatBranchMRIndex) {
	item := buildPolecatInventoryItem(rigName, polecatName, fields, activeWork, sessions, mrIndex)
	unverifiedBefore := snapshot.UnverifiedIdle
	applyWorkstateDispositionToCapacitySnapshot(snapshot, item.State, item.Disposition)
	if snapshot.UnverifiedIdle > unverifiedBefore && snapshot.unverifiedExample == "" {
		snapshot.unverifiedExample = rigName + "/" + polecatName
	}
}

func applyWorkstateDispositionToCapacitySnapshot(snapshot *polecatCapacitySnapshot, state polecat.State, disposition polecat.WorkstateDisposition) {
	if disposition.ReuseStatus == "idle-pr-open" {
		snapshot.addPendingMR()
		return
	}
	if disposition.Reusable {
		snapshot.addVerifiedReusableIdle()
		return
	}
	if disposition.Verdict == polecat.WorkstateVerdictUnverified {
		snapshot.addUnverifiedIdle()
		return
	}
	if disposition.NeedsRecovery {
		snapshot.addRecoveryBlocked(disposition.CountsTowardCapacity)
		return
	}
	if state == polecat.StateWorking || disposition.Verdict == polecat.WorkstateVerdictWorking {
		snapshot.addWorking()
		return
	}
	if disposition.CountsTowardCapacity {
		snapshot.addRecoveryBlocked(true)
	}
}

func acquirePolecatAdmissionLock(townRoot string) (*flock.Flock, error) {
	lockDir := filepath.Join(townRoot, ".runtime", "locks")
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return nil, fmt.Errorf("creating polecat admission lock dir: %w", err)
	}
	lock := flock.New(filepath.Join(lockDir, "polecat-admission.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("acquiring polecat admission lock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("polecat admission is busy; retry shortly")
	}
	return lock, nil
}

func polecatAdmissionDir(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "polecat-admission")
}

func writePolecatAdmissionReservation(townRoot, rigName, beadID, operation string) (polecatAdmissionReservation, string, error) {
	dir := polecatAdmissionDir(townRoot)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return polecatAdmissionReservation{}, "", fmt.Errorf("creating polecat admission dir: %w", err)
	}
	now := time.Now().UTC()
	id := fmt.Sprintf("%d-%d", os.Getpid(), now.UnixNano())
	reservation := polecatAdmissionReservation{
		ID:        id,
		PID:       os.Getpid(),
		Rig:       rigName,
		Bead:      beadID,
		Operation: operation,
		CreatedAt: now,
	}
	path := filepath.Join(dir, id+".json")
	tmpPath := path + ".tmp"
	data, err := json.MarshalIndent(reservation, "", "  ")
	if err != nil {
		return polecatAdmissionReservation{}, "", err
	}
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return polecatAdmissionReservation{}, "", fmt.Errorf("writing polecat admission reservation: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return polecatAdmissionReservation{}, "", fmt.Errorf("publishing polecat admission reservation: %w", err)
	}
	return reservation, path, nil
}

func readPolecatAdmissionReservations(townRoot string) ([]polecatAdmissionReservation, error) {
	dir := polecatAdmissionDir(townRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading polecat admission reservations: %w", err)
	}
	reservations := make([]polecatAdmissionReservation, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			_ = os.Remove(path)
			continue
		}
		var reservation polecatAdmissionReservation
		if err := json.Unmarshal(data, &reservation); err != nil {
			_ = os.Remove(path)
			continue
		}
		if reservation.ID == "" || reservation.PID <= 0 || reservation.CreatedAt.IsZero() || reservation.ID+".json" != entry.Name() {
			_ = os.Remove(path)
			continue
		}
		reservations = append(reservations, reservation)
	}
	return reservations, nil
}

func cleanupStalePolecatAdmissionReservations(townRoot string, now time.Time) error {
	dir := polecatAdmissionDir(townRoot)
	reservations, err := readPolecatAdmissionReservations(townRoot)
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if reservation.PID <= 0 {
			continue
		}
		age := now.Sub(reservation.CreatedAt)
		if processAlive(reservation.PID) {
			continue
		}
		if age < polecatAdmissionReservationTTL {
			continue
		}
		_ = os.Remove(filepath.Join(dir, reservation.ID+".json"))
	}
	return nil
}

func cleanupStalePolecatAdmissionReservationsWithLock(townRoot string, now time.Time) error {
	lock, err := acquirePolecatAdmissionLock(townRoot)
	if err != nil {
		if strings.Contains(err.Error(), "admission is busy") {
			return nil
		}
		return err
	}
	defer func() { _ = lock.Unlock() }()
	return cleanupStalePolecatAdmissionReservations(townRoot, now)
}

package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

// `gt dolt migrate` moves database directories on disk after a point-in-time
// check that no Dolt server is running. Nothing holds the server down for the
// rest of the run, so an auto-restarting supervisor can put a live server back
// on .dolt-data while the moves are still happening (gt-2xsa). These tests pin
// the guard that bounds that window: it must re-check at every boundary, stop
// the run, and hand the operator the on-disk state without overstating what the
// check proved.

func supervisedUnit() *doltserver.Supervisor {
	return &doltserver.Supervisor{
		Kind:     "systemd",
		Unit:     "gt-dolt.service",
		UserUnit: true,
		Restart:  "always",
	}
}

func fakeMigrations(names ...string) []doltserver.Migration {
	var out []doltserver.Migration
	for _, n := range names {
		out = append(out, doltserver.Migration{
			RigName:    n,
			SourcePath: "/home/op/gt/" + n + "/.beads/dolt/" + n,
			TargetPath: "/home/op/gt/.dolt-data/" + n,
		})
	}
	return out
}

// guardOverMigrations runs the production loop with a probe that reports the
// server as running from the runningFrom'th check onwards (0 = the very first
// check, before anything moves). Returns the databases actually moved and the
// error the loop produced.
func guardOverMigrations(t *testing.T, names []string, runningFrom int, sup *doltserver.Supervisor) ([]string, error) {
	t.Helper()

	checks := 0
	guard := &migrationGuard{
		isRunning: func() (bool, int) {
			running := checks >= runningFrom
			checks++
			if running {
				return true, 4242
			}
			return false, 0
		},
		detect:  func(int) *doltserver.Supervisor { return sup },
		dataDir: "/home/op/gt/.dolt-data",
	}

	var moved []string
	err := runGuardedMigrations(guard, fakeMigrations(names...), func(m doltserver.Migration) error {
		moved = append(moved, m.RigName)
		return nil
	})
	return moved, err
}

// The control for every abort case below: with the server staying down, the
// guard must move everything and return nil. Without this, a guard that
// refused unconditionally would pass all the abort tests.
func TestMigrationGuardMigratesEverythingWhenServerStaysDown(t *testing.T) {
	const neverRunning = 1 << 30

	moved, err := guardOverMigrations(t, []string{"hq", "gastown", "beads"}, neverRunning, nil)
	if err != nil {
		t.Fatalf("no server appeared, so migration must complete: %v", err)
	}
	if got := strings.Join(moved, ","); got != "hq,gastown,beads" {
		t.Errorf("all databases must be migrated in order, moved %q", got)
	}
}

// The window this bead is about: the precheck passes inside the supervisor's
// restart window and the server is back before the first directory moves.
func TestMigrationGuardStopsBeforeMovingAnything(t *testing.T) {
	moved, err := guardOverMigrations(t, []string{"hq", "gastown"}, 0, supervisedUnit())
	if err == nil {
		t.Fatal("a server on the data directory must stop the run")
	}
	if len(moved) != 0 {
		t.Errorf("nothing may be moved under a live server, moved %v", moved)
	}
	if !strings.Contains(err.Error(), "Moved:     none") {
		t.Errorf("message must report that nothing moved, got:\n%s", err)
	}
	if !strings.Contains(err.Error(), "Not moved: hq, gastown") {
		t.Errorf("message must name what is still in the old layout, got:\n%s", err)
	}
}

// A server that appears part-way through must stop the run at the next
// boundary, not after the whole town has been moved under it.
func TestMigrationGuardStopsAtTheNextBoundary(t *testing.T) {
	// Checks run: [0] before hq, [1] before gastown, [2] before beads.
	moved, err := guardOverMigrations(t, []string{"hq", "gastown", "beads"}, 2, supervisedUnit())
	if err == nil {
		t.Fatal("a server appearing mid-run must stop the run")
	}
	if got := strings.Join(moved, ","); got != "hq,gastown" {
		t.Errorf("run must stop at the boundary after gastown, moved %q", got)
	}
	if !strings.Contains(err.Error(), "Moved:     hq, gastown") {
		t.Errorf("message must report the databases already in .dolt-data, got:\n%s", err)
	}
	if !strings.Contains(err.Error(), "Not moved: beads") {
		t.Errorf("message must report what is still in the old layout, got:\n%s", err)
	}
}

// The final check exists so a server that appears during the LAST move is still
// caught. Without it the caller goes on to rewrite metadata and auto-start a
// server, and the run reports a clean migration over a possibly corrupt one.
func TestMigrationGuardChecksAfterTheLastMove(t *testing.T) {
	// Checks run: [0] before hq, [1] after hq (the trailing check).
	moved, err := guardOverMigrations(t, []string{"hq"}, 1, supervisedUnit())
	if err == nil {
		t.Fatal("a server appearing during the last move must be caught by the trailing check")
	}
	if got := strings.Join(moved, ","); got != "hq" {
		t.Errorf("the last move itself still happens, moved %q", got)
	}
	if !strings.Contains(err.Error(), "Not moved: none") {
		t.Errorf("message must say nothing is left to move, got:\n%s", err)
	}
}

// The guard checks BETWEEN moves. It cannot prove the server was down for the
// whole of a move, so it must not imply the abort caught everything: the most
// recently moved database is the one that could have moved out from under a
// live server, and the operator has to be told which one that is.
func TestMigrationGuardDoesNotOverstateWhatItProved(t *testing.T) {
	_, err := guardOverMigrations(t, []string{"hq", "gastown", "beads"}, 2, supervisedUnit())
	if err == nil {
		t.Fatal("expected an abort")
	}
	out := err.Error()

	if !strings.Contains(out, "does not prove the server was down") {
		t.Errorf("message must state the limit of a between-moves check, got:\n%s", out)
	}
	if !strings.Contains(out, "Verify gastown first") {
		t.Errorf("message must name the last-moved database as the one at risk, got:\n%s", out)
	}
	if !strings.Contains(out, "corrupt") {
		t.Errorf("message must state what is at stake, got:\n%s", out)
	}
}

// Nothing moved means nothing is at risk. Naming an at-risk database here would
// send the operator to inspect a database this run never touched.
func TestMigrationGuardNamesNoAtRiskDatabaseWhenNothingMoved(t *testing.T) {
	_, err := guardOverMigrations(t, []string{"hq"}, 0, supervisedUnit())
	if err == nil {
		t.Fatal("expected an abort")
	}
	// Not a bare "Verify " check: step 2 of the recovery is "Verify it is down",
	// which must stay. What must be absent is the at-risk-database paragraph.
	if strings.Contains(err.Error(), "does not prove the server was down") {
		t.Errorf("nothing moved, so nothing is at risk to verify, got:\n%s", err)
	}
	if strings.Contains(err.Error(), "Verify hq first") {
		t.Errorf("hq was never moved, so it must not be named as at risk, got:\n%s", err)
	}
	if !strings.Contains(err.Error(), "Verify it is down") {
		t.Errorf("the recovery's own verification step must survive, got:\n%s", err)
	}
}

// Same defect gt-xvwu and gt-4ruo fixed in the other two remedies: under a unit
// with Restart=always, `gt dolt stop` signals the process and the supervisor
// replaces it seconds later, so it is never the step that gets the server down.
func TestServerAppearedRemedyUnderSupervisor(t *testing.T) {
	out := serverAppearedDuringMigrationRemedy(
		supervisedUnit(), "/home/op/gt/.dolt-data", []string{"hq"}, []string{"gastown"})

	if !strings.Contains(out, "systemctl --user stop gt-dolt.service") {
		t.Errorf("remedy must stop the unit, got:\n%s", out)
	}
	if strings.Contains(out, "Stop the server:   gt dolt stop") {
		t.Errorf("remedy must not tell a supervised operator to `gt dolt stop`, got:\n%s", out)
	}
	if !strings.Contains(out, "Restart=always") {
		t.Errorf("remedy must name the policy that makes `gt dolt stop` ineffective, got:\n%s", out)
	}

	stopIdx := strings.Index(out, "systemctl --user stop")
	verifyIdx := strings.Index(out, "gt dolt status")
	rerunIdx := strings.Index(out, "gt dolt migrate")
	if !(stopIdx < verifyIdx && verifyIdx < rerunIdx) {
		t.Errorf("steps must run stop -> verify -> re-run, got:\n%s", out)
	}
}

// `gt dolt migrate` starts a server itself when it finishes. That process used
// to belong to no unit, so this remedy told the operator to stop it and start
// the unit by hand. doltserver.Start now routes through the unit itself
// (gt-cru5), which makes that instruction wrong in the worse direction: it
// sends an operator to start a unit that is already running, and its failure
// reads as a broken recovery.
func TestServerAppearedRemedySaysTheAutoStartIsSupervised(t *testing.T) {
	out := serverAppearedDuringMigrationRemedy(
		supervisedUnit(), "/home/op/gt/.dolt-data", []string{"hq"}, []string{"gastown"})

	if !strings.Contains(out, "starts a server again when it finishes") {
		t.Errorf("remedy must account for the auto-start that follows a successful re-run, got:\n%s", out)
	}
	if !strings.Contains(out, "gt-dolt.service, so it comes back supervised") {
		t.Errorf("remedy must say the auto-start goes through the unit, got:\n%s", out)
	}
	if strings.Contains(out, "systemctl --user start gt-dolt.service") {
		t.Errorf("no hand-off step is needed any more — telling the operator to start a running unit is a failing command:\n%s", out)
	}
	// The stop step is still the operator's job and must survive this change.
	if !strings.Contains(out, "systemctl --user stop gt-dolt.service") {
		t.Errorf("remedy must still stop the unit, got:\n%s", out)
	}
}

// The refusal that gt-2xsa could not implement: with the server down,
// DetectSupervisor has no PID to read a cgroup from, so migration could only
// bound the restart window rather than refuse. The unit remembered in
// dolt-state.json closes that gap (gt-cru5) — but only if the message tells the
// operator which state was read and how to confirm the fix.
func TestUnitNotStoppedRemedy(t *testing.T) {
	out := unitNotStoppedRemedy(supervisedUnit(), "activating")

	if !strings.Contains(out, "ActiveState=activating") {
		t.Errorf("remedy must report the state it actually read, got:\n%s", out)
	}
	if !strings.Contains(out, "systemctl --user stop gt-dolt.service") {
		t.Errorf("remedy must name the unit's stop command, got:\n%s", out)
	}
	if !strings.Contains(out, "must print: inactive") {
		t.Errorf("remedy must give a confirmation step with a pass condition, got:\n%s", out)
	}
	if !strings.Contains(out, "systemctl --user show -p ActiveState --value gt-dolt.service") {
		t.Errorf("confirmation must be a command the operator can run, got:\n%s", out)
	}
}

// An unreadable ActiveState is why the refusal fires at all in that case, so
// the message must not invent a state systemd never reported.
func TestUnitNotStoppedRemedyWithNoAnswerFromSystemd(t *testing.T) {
	out := unitNotStoppedRemedy(supervisedUnit(), "")

	if strings.Contains(out, "ActiveState=") {
		t.Errorf("no state was read, so none may be quoted, got:\n%s", out)
	}
	if !strings.Contains(out, "gave no ActiveState") {
		t.Errorf("remedy must say systemd did not answer, got:\n%s", out)
	}
}

func TestServerAppearedRemedyWithoutSupervisor(t *testing.T) {
	out := serverAppearedDuringMigrationRemedy(
		nil, "/home/op/gt/.dolt-data", []string{"hq"}, []string{"gastown"})

	if !strings.Contains(out, "gt dolt stop") {
		t.Errorf("unsupervised remedy should use gt's own stop, got:\n%s", out)
	}
	if strings.Contains(out, "systemctl") {
		t.Errorf("no supervisor detected, so no systemctl advice:\n%s", out)
	}
	// Detection returning nil means "unknown", not "definitely unsupervised",
	// so the verification step has to survive here too.
	if !strings.Contains(out, "gt dolt status") {
		t.Errorf("remedy must keep the verification step even with no supervisor, got:\n%s", out)
	}
}

// Re-running is the whole recovery, so the claim that it only moves what is
// left has to hold: FindMigratableDatabases skips any rig whose target already
// contains .dolt.
func TestServerAppearedRemedySaysMigrationIsResumable(t *testing.T) {
	out := serverAppearedDuringMigrationRemedy(
		nil, "/home/op/gt/.dolt-data", []string{"hq"}, []string{"gastown"})

	if !strings.Contains(out, "resumable") {
		t.Errorf("remedy must say the re-run is safe to repeat, got:\n%s", out)
	}
	if !strings.Contains(out, "/home/op/gt/.dolt-data") {
		t.Errorf("remedy must name the data directory it skips against, got:\n%s", out)
	}
}

// recordMoved is called once per successful move; it must not run past the end
// of the list, or order[:completed] would panic on the trailing check.
func TestMigrationGuardRecordMovedStopsAtTheEnd(t *testing.T) {
	guard := &migrationGuard{order: []string{"hq"}}
	guard.recordMoved()
	guard.recordMoved()

	if guard.completed != 1 {
		t.Fatalf("completed must not exceed the number of databases, got %d", guard.completed)
	}
}

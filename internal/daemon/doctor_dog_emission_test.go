package daemon

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeBdForDoctor writes a stub `bd` that answers the calls pourDogMolecule and
// the step closes make, and records every invocation. It never reaches Dolt.
//
// The recording is the point: these tests are about WHETHER a molecule is
// poured, and the only honest evidence of that is the argv the daemon handed to
// bd. Asserting on log lines alone would pass for a daemon that logged
// "suppressed" and poured anyway.
func fakeBdForDoctor(t *testing.T) (bdPath, callLog string) {
	t.Helper()
	dir := t.TempDir()
	callLog = filepath.Join(dir, "calls.log")
	bdPath = filepath.Join(dir, "bd")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + callLog + `"
case "$1" in
  mol) printf 'Spawned wisp: hq-wisp-test01 - mol-dog-doctor\n' ;;
  show) printf '{"hq-wisp-test01":[{"id":"hq-wisp-step1","title":"Probe Dolt server connectivity","status":"closed"}],"schema_version":1}\n' ;;
  *) : ;;
esac
exit 0
`
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	return bdPath, callLog
}

// doctorTestDaemon builds a daemon whose Dolt health check last reported the
// given warnings.
func doctorTestDaemon(t *testing.T, warnings []string) (*Daemon, *bytes.Buffer, string) {
	t.Helper()
	bdPath, callLog := fakeBdForDoctor(t)
	var logBuf bytes.Buffer
	d := &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		bdPath: bdPath,
		logger: log.New(&logBuf, "", 0),
		patrolConfig: &DaemonPatrolConfig{
			Patrols: &PatrolsConfig{DoctorDog: &DoctorDogConfig{Enabled: true}},
		},
		doltServer: &DoltServerManager{
			config:       &DoltServerConfig{Port: 3399},
			lastWarnings: warnings,
		},
	}
	return d, &logBuf, callLog
}

func bdCalls(t *testing.T, callLog string) string {
	t.Helper()
	data, err := os.ReadFile(callLog)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read bd call log: %v", err)
	}
	return string(data)
}

// TestRunDoctorDogPoursNothingWhenHealthy is the gt-bnpw regression.
//
// The old runDoctorDog poured a mol-dog-doctor molecule on every tick regardless
// of health — four wisps every five minutes, ~1150/day, for a formula nothing
// slings and which it closed one second later. A clean check must now cost zero
// beads.
func TestRunDoctorDogPoursNothingWhenHealthy(t *testing.T) {
	d, logBuf, callLog := doctorTestDaemon(t, nil)

	d.runDoctorDog()

	if calls := bdCalls(t, callLog); calls != "" {
		t.Fatalf("healthy cycle must not invoke bd at all, got calls:\n%s", calls)
	}
	if !d.lastDoctorMolTime.IsZero() {
		t.Error("healthy cycle must not consume the molecule cooldown")
	}
	if got := logBuf.String(); !strings.Contains(got, "no warnings") {
		t.Errorf("expected the clean cycle to still report the check ran, got %q", got)
	}
}

// TestRunDoctorDogPoursOnWarnings: the molecule still exists for the case it was
// meant for. Suppressing it always would trade a bead flood for a blind spot.
func TestRunDoctorDogPoursOnWarnings(t *testing.T) {
	d, logBuf, callLog := doctorTestDaemon(t, []string{"connection count 900 is 90% of max 1000"})

	d.runDoctorDog()

	calls := bdCalls(t, callLog)
	if !strings.Contains(calls, "mol wisp mol-dog-doctor") {
		t.Fatalf("expected a mol-dog-doctor pour on an anomaly, got calls:\n%s", calls)
	}
	if d.lastDoctorMolTime.IsZero() {
		t.Error("a pour must consume the cooldown shared with ensureDoltServerRunning")
	}
	if got := logBuf.String(); !strings.Contains(got, "90% of max") {
		t.Errorf("expected the warning text in the log, got %q", got)
	}
}

// TestRunDoctorDogRespectsCooldown: ensureDoltServerRunning pours the same
// molecule from the heartbeat. Both emitters share lastDoctorMolTime so a single
// anomaly cannot mint two molecules inside one cooldown window.
func TestRunDoctorDogRespectsCooldown(t *testing.T) {
	d, logBuf, callLog := doctorTestDaemon(t, []string{"server is in READ-ONLY mode"})
	d.lastDoctorMolTime = time.Now().Add(-doctorMolCooldown / 2)

	d.runDoctorDog()

	if calls := bdCalls(t, callLog); calls != "" {
		t.Fatalf("cooldown must suppress the pour, got calls:\n%s", calls)
	}
	got := logBuf.String()
	if !strings.Contains(got, "cooldown") {
		t.Errorf("a suppressed anomaly must still be reported, got %q", got)
	}
	if !strings.Contains(got, "READ-ONLY") {
		t.Errorf("expected the suppressed warning text in the log, got %q", got)
	}
}

// TestRunDoctorDogInactivePatrol keeps the disable switch meaningful.
func TestRunDoctorDogInactivePatrol(t *testing.T) {
	d, _, callLog := doctorTestDaemon(t, []string{"server is in READ-ONLY mode"})
	d.disabledPatrols = map[string]bool{"doctor_dog": true}

	d.runDoctorDog()

	if calls := bdCalls(t, callLog); calls != "" {
		t.Fatalf("disabled patrol must not invoke bd, got calls:\n%s", calls)
	}
}

// TestLastDoltWarningsNilServer: a town with no managed Dolt server has no Dolt
// health to report, and must not crash trying.
func TestLastDoltWarningsNilServer(t *testing.T) {
	d := &Daemon{}
	if got := d.lastDoltWarnings(); got != nil {
		t.Errorf("expected nil warnings with no dolt server, got %v", got)
	}
}

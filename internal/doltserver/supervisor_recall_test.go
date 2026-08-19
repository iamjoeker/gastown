package doltserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// DetectSupervisor answers "what owns this process" from /proc/<pid>/cgroup, so
// it goes blind exactly when gt is about to START a server: no process, no
// cgroup, no unit name. These tests pin the memory that fills that gap — the
// unit recorded in dolt-state.json while a server was running, and the rules
// for when that memory may be trusted. (gt-cru5)

// fakeSystemd answers unit properties from a table, and records what was asked
// so a test can prove a lookup was skipped rather than merely unhelpful.
type fakeSystemd struct {
	properties map[string]string
	asked      []string
}

func (f *fakeSystemd) read(unit string, userUnit bool, property string) string {
	f.asked = append(f.asked, property)
	return f.properties[property]
}

func loadedUnitState() *State {
	return &State{
		Supervisor: &SupervisorRecord{
			Kind:       "systemd",
			Unit:       "gt-dolt.service",
			UserUnit:   true,
			ObservedAt: time.Now().Add(-2 * time.Hour),
		},
	}
}

func TestRecallSupervisorReturnsTheRememberedUnit(t *testing.T) {
	systemd := &fakeSystemd{properties: map[string]string{
		"LoadState":              "loaded",
		"Restart":                "on-failure",
		"NRestarts":              "2",
		"StartLimitIntervalUSec": "0",
		"StartLimitBurst":        "5",
	}}

	sup := recallSupervisor(loadedUnitState(), systemd.read)
	if sup == nil {
		t.Fatal("a loaded unit that was remembered must be recalled")
	}
	if sup.Unit != "gt-dolt.service" || !sup.UserUnit || sup.Kind != "systemd" {
		t.Errorf("identity not recalled from the record: %+v", sup)
	}
	if got := sup.StartCommand(); got != "systemctl --user start gt-dolt.service" {
		t.Errorf("StartCommand() = %q — the recalled unit is what Start routes through", got)
	}
}

// The record stores identity only. This town's unit had its Restart policy
// changed under it on 2026-08-18 (gt-09e4), and a policy served from disk would
// have gone on claiming Restart=always for as long as the file survived —
// exactly the "supervisor will bring it back" advice that is backwards under
// on-failure.
func TestRecallSupervisorReadsVolatilePropertiesLive(t *testing.T) {
	systemd := &fakeSystemd{properties: map[string]string{
		"LoadState":              "loaded",
		"Restart":                "on-failure",
		"NRestarts":              "7",
		"StartLimitIntervalUSec": "0",
		"StartLimitBurst":        "5",
	}}

	sup := recallSupervisor(loadedUnitState(), systemd.read)
	if sup.Restart != "on-failure" {
		t.Errorf("Restart = %q, want the live policy", sup.Restart)
	}
	if sup.AutoRestarts() {
		t.Error("Restart=on-failure does not revive a clean stop — recall must not report otherwise")
	}
	if sup.NRestarts != 7 {
		t.Errorf("NRestarts = %d, want the live count 7", sup.NRestarts)
	}
	if !sup.RateLimitDisabled {
		t.Error("StartLimitIntervalUSec=0 must be read live too")
	}
}

// Every non-"loaded" state means starting the unit would fail, so recall must
// hand back nil and let gt start the process itself. Unlike the rest of the
// supervisor code, an absent answer here fails toward gt's own start rather
// than toward keeping the detection: there is no systemd to start anything
// with.
func TestRecallSupervisorRejectsUnusableUnits(t *testing.T) {
	tests := []struct {
		name      string
		loadState string
	}{
		{"unit was removed", "not-found"},
		{"operator masked the unit", "masked"},
		{"systemd cannot use the unit", "error"},
		{"unit has a bad setting", "bad-setting"},
		{"no systemctl, no bus", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			systemd := &fakeSystemd{properties: map[string]string{
				"LoadState": tt.loadState,
				"Restart":   "always",
			}}
			if sup := recallSupervisor(loadedUnitState(), systemd.read); sup != nil {
				t.Errorf("recallSupervisor() = %+v, want nil for LoadState=%q", sup, tt.loadState)
			}
			if len(systemd.asked) != 1 || systemd.asked[0] != "LoadState" {
				t.Errorf("an unusable unit must be dropped before its other properties are read, asked %v", systemd.asked)
			}
		})
	}
}

func TestRecallSupervisorWithNothingRemembered(t *testing.T) {
	systemd := &fakeSystemd{properties: map[string]string{"LoadState": "loaded"}}

	for _, tt := range []struct {
		name  string
		state *State
	}{
		{"no state at all", nil},
		{"state without a supervisor", &State{Running: true, PID: 42}},
		{"record with an empty unit", &State{Supervisor: &SupervisorRecord{Kind: "systemd"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if sup := recallSupervisor(tt.state, systemd.read); sup != nil {
				t.Errorf("recallSupervisor() = %+v, want nil", sup)
			}
		})
	}
}

func TestUnitIsLoaded(t *testing.T) {
	tests := []struct {
		property string
		want     bool
	}{
		{"loaded", true},
		{"loaded\n", true}, // systemctl --value still emits a trailing newline
		{"not-found", false},
		{"masked", false},
		{"error", false},
		{"bad-setting", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.property, func(t *testing.T) {
			if got := unitIsLoaded(tt.property); got != tt.want {
				t.Errorf("unitIsLoaded(%q) = %v, want %v", tt.property, got, tt.want)
			}
		})
	}
}

// The caller of ConfirmedStopped is about to move database directories. Only a
// unit that will not put a server back on them by itself may pass, and an
// unreadable state is not that — this is the one place where unknown must fail
// closed.
func TestUnitConfirmedStopped(t *testing.T) {
	tests := []struct {
		activeState string
		want        bool
	}{
		{"inactive", true},
		{"inactive\n", true},
		{"failed", true}, // a failed unit stays down until something starts it
		{"active", false},
		{"activating", false}, // the pause between a crash and an auto-restart
		{"deactivating", false},
		{"reloading", false},
		{"", false}, // systemd gave no answer: not permission to move files
	}
	for _, tt := range tests {
		t.Run(tt.activeState, func(t *testing.T) {
			if got := unitConfirmedStopped(tt.activeState); got != tt.want {
				t.Errorf("unitConfirmedStopped(%q) = %v, want %v", tt.activeState, got, tt.want)
			}
		})
	}
}

// A supervisor with no unit is not a reason to block a migration: there is no
// unit that could revive the server.
func TestConfirmedStoppedWithoutAUnit(t *testing.T) {
	for _, sup := range []*Supervisor{nil, {Kind: "systemd"}} {
		if _, stopped := sup.ConfirmedStopped(); !stopped {
			t.Errorf("ConfirmedStopped() on %+v must not block — there is no unit to revive anything", sup)
		}
	}
}

func TestConfirmStoppedCommand(t *testing.T) {
	tests := []struct {
		name string
		sup  *Supervisor
		want string
	}{
		{"nil supervisor falls back to gt", nil, "gt dolt status"},
		{
			"user unit",
			&Supervisor{Kind: "systemd", Unit: "gt-dolt.service", UserUnit: true},
			"systemctl --user show -p ActiveState --value gt-dolt.service",
		},
		{
			"system unit",
			&Supervisor{Kind: "systemd", Unit: "dolt.service"},
			"systemctl show -p ActiveState --value dolt.service",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sup.ConfirmStoppedCommand(); got != tt.want {
				t.Errorf("ConfirmStoppedCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRememberSupervisorRoundTrip(t *testing.T) {
	townRoot := t.TempDir()

	// State the town already had: remembering must not cost the rest of it.
	before := &State{Running: true, PID: 3580495, Port: 3307, DataDir: "/gt/.dolt-data", Databases: []string{"hq"}}
	if err := SaveState(townRoot, before); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	sup := &Supervisor{Kind: "systemd", Unit: "gt-dolt.service", UserUnit: true, Restart: "on-failure"}
	if err := RememberSupervisor(townRoot, sup); err != nil {
		t.Fatalf("RememberSupervisor: %v", err)
	}

	after, err := LoadState(townRoot)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if after.Supervisor == nil {
		t.Fatal("the supervisor was not remembered")
	}
	if after.Supervisor.Unit != "gt-dolt.service" || !after.Supervisor.UserUnit {
		t.Errorf("remembered identity is wrong: %+v", after.Supervisor)
	}
	if after.Supervisor.ObservedAt.IsZero() {
		t.Error("ObservedAt must record when the unit was seen")
	}
	if after.PID != before.PID || after.Port != before.Port || len(after.Databases) != 1 {
		t.Errorf("remembering the supervisor clobbered the rest of the state: %+v", after)
	}

	// The volatile properties must not be persisted: a Restart policy served
	// from disk outlives the policy itself.
	raw, err := os.ReadFile(filepath.Join(townRoot, "daemon", "dolt-state.json"))
	if err != nil {
		t.Fatalf("reading state file: %v", err)
	}
	var onDisk map[string]json.RawMessage
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("state file is not JSON: %v", err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(onDisk["supervisor"], &record); err != nil {
		t.Fatalf("supervisor record is not JSON: %v", err)
	}
	for _, volatile := range []string{"restart", "nrestarts", "n_restarts", "rate_limit_disabled"} {
		if _, found := record[volatile]; found {
			t.Errorf("%q must be re-read from systemd, not persisted", volatile)
		}
	}
}

// Detection returns nil for "unsupervised" AND for every flavour of "cannot
// tell". Forgetting a real unit because one probe came back empty would put
// gt back to spawning servers behind systemd's back — the whole defect.
func TestRememberSupervisorDoesNotForgetOnANilDetection(t *testing.T) {
	townRoot := t.TempDir()

	if err := RememberSupervisor(townRoot, &Supervisor{Kind: "systemd", Unit: "gt-dolt.service", UserUnit: true}); err != nil {
		t.Fatalf("RememberSupervisor: %v", err)
	}
	for _, sup := range []*Supervisor{nil, {Kind: "systemd"}} {
		if err := RememberSupervisor(townRoot, sup); err != nil {
			t.Fatalf("RememberSupervisor(%+v): %v", sup, err)
		}
	}

	after, err := LoadState(townRoot)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if after.Supervisor == nil || after.Supervisor.Unit != "gt-dolt.service" {
		t.Errorf("an undetected supervisor erased the remembered unit: %+v", after.Supervisor)
	}
}

// `gt dolt status` runs constantly. Re-writing dolt-state.json on every probe
// would churn the file for information it already holds.
func TestRememberSupervisorSkipsTheWriteWhenNothingChanged(t *testing.T) {
	townRoot := t.TempDir()
	sup := &Supervisor{Kind: "systemd", Unit: "gt-dolt.service", UserUnit: true}

	if err := RememberSupervisor(townRoot, sup); err != nil {
		t.Fatalf("RememberSupervisor: %v", err)
	}
	stateFile := StateFile(townRoot)
	first, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Backdate the file so a rewrite is unambiguous on filesystems with coarse
	// timestamps.
	backdated := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stateFile, backdated, backdated); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if err := RememberSupervisor(townRoot, sup); err != nil {
		t.Fatalf("RememberSupervisor (second): %v", err)
	}
	second, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !second.ModTime().Equal(backdated) {
		t.Errorf("re-observing the same unit rewrote the state file (mtime %v → %v, first write %v)",
			backdated, second.ModTime(), first.ModTime())
	}

	// A different unit IS news and must be written.
	if err := RememberSupervisor(townRoot, &Supervisor{Kind: "systemd", Unit: "dolt.service"}); err != nil {
		t.Fatalf("RememberSupervisor (changed): %v", err)
	}
	after, err := LoadState(townRoot)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if after.Supervisor.Unit != "dolt.service" || after.Supervisor.UserUnit {
		t.Errorf("a changed unit must overwrite the record: %+v", after.Supervisor)
	}
}

func TestSupervisorRecord(t *testing.T) {
	if got := (*Supervisor)(nil).Record(); got != nil {
		t.Errorf("Record() on nil = %+v, want nil", got)
	}
	if got := (&Supervisor{Kind: "systemd"}).Record(); got != nil {
		t.Errorf("Record() with no unit = %+v, want nil — there is nothing to start later", got)
	}
	got := (&Supervisor{Kind: "systemd", Unit: "gt-dolt.service", UserUnit: true, Restart: "always"}).Record()
	if got == nil || got.Unit != "gt-dolt.service" || !got.UserUnit || got.Kind != "systemd" {
		t.Fatalf("Record() = %+v", got)
	}
}

// The escape hatch has to work with a supervisor remembered — that is the only
// state in which it does anything.
func TestDirectStartEnvVarBypassesTheSupervisor(t *testing.T) {
	townRoot := t.TempDir()
	if err := RememberSupervisor(townRoot, &Supervisor{Kind: "systemd", Unit: "gt-dolt.service", UserUnit: true}); err != nil {
		t.Fatalf("RememberSupervisor: %v", err)
	}

	t.Setenv(DirectStartEnvVar, "1")
	if sup := supervisorForStart(townRoot); sup != nil {
		t.Errorf("supervisorForStart() = %+v with %s=1, want nil", sup, DirectStartEnvVar)
	}
}

func TestIsTruthyEnv(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "on", " 1 "} {
		if !isTruthyEnv(value) {
			t.Errorf("isTruthyEnv(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "off", "maybe"} {
		if isTruthyEnv(value) {
			t.Errorf("isTruthyEnv(%q) = true, want false", value)
		}
	}
}

// Nothing to start means nothing to exec: StartUnit must refuse rather than run
// a systemctl command with an empty unit name.
func TestStartUnitRefusesWithoutAUnit(t *testing.T) {
	for _, sup := range []*Supervisor{nil, {Kind: "systemd"}} {
		if _, err := sup.StartUnit(); err == nil {
			t.Errorf("StartUnit() on %+v = nil error, want a refusal", sup)
		}
	}
}

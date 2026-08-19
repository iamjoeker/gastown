package doltserver

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestParseSystemdUnit(t *testing.T) {
	tests := []struct {
		name     string
		cgroup   string
		wantUnit string
		wantUser bool
	}{
		{
			// The live gt-dolt.service on a cgroup v2 host (gt-xvwu).
			name:     "cgroup v2 user unit",
			cgroup:   "0::/user.slice/user-1000.slice/user@1000.service/app.slice/gt-dolt.service\n",
			wantUnit: "gt-dolt.service",
			wantUser: true,
		},
		{
			name:     "cgroup v2 system unit",
			cgroup:   "0::/system.slice/gt-dolt.service\n",
			wantUnit: "gt-dolt.service",
			wantUser: false,
		},
		{
			// A server started by hand from a shell: nothing restarts it.
			name:     "tmux scope is not a supervisor",
			cgroup:   "0::/user.slice/user-1000.slice/user@1000.service/tmux-spawn-219312f4.scope\n",
			wantUnit: "",
		},
		{
			// Directly in the per-user service manager's own cgroup — that is
			// the manager, not a unit supervising this process.
			name:     "user manager service is not a unit",
			cgroup:   "0::/user.slice/user-1000.slice/user@1000.service\n",
			wantUnit: "",
		},
		{
			name: "cgroup v1 picks the systemd hierarchy",
			cgroup: "11:devices:/system.slice\n" +
				"3:cpu,cpuacct:/system.slice\n" +
				"1:name=systemd:/system.slice/gt-dolt.service\n",
			wantUnit: "gt-dolt.service",
			wantUser: false,
		},
		{
			name:     "empty file",
			cgroup:   "",
			wantUnit: "",
		},
		{
			name:     "malformed lines are skipped",
			cgroup:   "garbage\n0::\n",
			wantUnit: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unit, userUnit := parseSystemdUnit(tt.cgroup)
			if unit != tt.wantUnit {
				t.Errorf("unit = %q, want %q", unit, tt.wantUnit)
			}
			if unit != "" && userUnit != tt.wantUser {
				t.Errorf("userUnit = %v, want %v", userUnit, tt.wantUser)
			}
		})
	}
}

// Stop sends SIGTERM, which systemd scores as a CLEAN exit (SIGTERM is one of
// the four signals excluded from "terminated by a signal" in systemd.service(5),
// and Dolt handles it and exits 0 besides — the unit logs Result=success). Only
// policies that restart after SUCCESS revive the server.
//
// The failure-triggered policies are the ones that matter to get right:
// Restart=on-failure is the leading candidate for this town's unit, and claiming
// it revives the server would tell an operator their stop failed when it worked.
func TestSupervisorAutoRestarts(t *testing.T) {
	tests := []struct {
		name string
		sup  *Supervisor
		want bool
	}{
		{"nil supervisor", nil, false},
		{"Restart=always", &Supervisor{Unit: "gt-dolt.service", Restart: "always"}, true},
		{"Restart=on-success", &Supervisor{Unit: "gt-dolt.service", Restart: "on-success"}, true},
		{"Restart=on-failure does not revive a clean SIGTERM exit", &Supervisor{Unit: "gt-dolt.service", Restart: "on-failure"}, false},
		{"Restart=on-abnormal does not revive a clean SIGTERM exit", &Supervisor{Unit: "gt-dolt.service", Restart: "on-abnormal"}, false},
		{"Restart=on-abort does not revive a handled signal", &Supervisor{Unit: "gt-dolt.service", Restart: "on-abort"}, false},
		{"Restart=on-watchdog does not revive a clean exit", &Supervisor{Unit: "gt-dolt.service", Restart: "on-watchdog"}, false},
		{"Restart=no", &Supervisor{Unit: "gt-dolt.service", Restart: "no"}, false},
		{"unknown policy", &Supervisor{Unit: "gt-dolt.service"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sup.AutoRestarts(); got != tt.want {
				t.Errorf("AutoRestarts() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSupervisorCommands(t *testing.T) {
	tests := []struct {
		name      string
		sup       *Supervisor
		wantStop  string
		wantStart string
		wantDesc  string
	}{
		{
			name:      "no supervisor falls back to gt",
			sup:       nil,
			wantStop:  "gt dolt stop",
			wantStart: "gt dolt start",
			wantDesc:  "",
		},
		{
			name:      "user unit",
			sup:       &Supervisor{Kind: "systemd", Unit: "gt-dolt.service", UserUnit: true, Restart: "always"},
			wantStop:  "systemctl --user stop gt-dolt.service",
			wantStart: "systemctl --user start gt-dolt.service",
			wantDesc:  "systemd unit gt-dolt.service (Restart=always)",
		},
		{
			name:      "system unit",
			sup:       &Supervisor{Kind: "systemd", Unit: "dolt.service"},
			wantStop:  "systemctl stop dolt.service",
			wantStart: "systemctl start dolt.service",
			wantDesc:  "systemd unit dolt.service",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sup.StopCommand(); got != tt.wantStop {
				t.Errorf("StopCommand() = %q, want %q", got, tt.wantStop)
			}
			if got := tt.sup.StartCommand(); got != tt.wantStart {
				t.Errorf("StartCommand() = %q, want %q", got, tt.wantStart)
			}
			if got := tt.sup.Describe(); got != tt.wantDesc {
				t.Errorf("Describe() = %q, want %q", got, tt.wantDesc)
			}
		})
	}
}

// NRestarts is a lower bound that must never be inflated: reporting restarts that
// did not happen would send an operator hunting a crash that never occurred, and
// "unknown" (older systemd has no such property, and systemctlProperty returns ""
// on any failure) must not read as zero either.
func TestParseNRestarts(t *testing.T) {
	tests := []struct {
		property string
		want     int
	}{
		{"2", 2},          // the live unit during the gt-qiok incident
		{"0", 0},          // never restarted
		{"2\n", 2},        // systemctl --value still emits a trailing newline
		{"", -1},          // no systemctl, no bus, or property absent
		{"n/a", -1},       // unparseable
		{"-1", -1},        // nonsense from the property, not a count
		{"[not set]", -1}, // systemd's own placeholder
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.property), func(t *testing.T) {
			if got := parseNRestarts(tt.property); got != tt.want {
				t.Errorf("parseNRestarts(%q) = %d, want %d", tt.property, got, tt.want)
			}
		})
	}
}

// Zero on EITHER key disables systemd's start-limit check, and an unreadable
// property means "unknown" — which must stay silent rather than accuse a unit of
// having crash-loop detection off when it does not.
func TestParseRateLimitDisabled(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		burst    string
		want     bool
	}{
		{"the town's unit: interval zeroed", "0", "5", true},
		{"burst zeroed instead", "10s", "0", true},
		{"both zeroed", "0", "0", true},
		{"systemd defaults are armed", "10s", "5", false},
		{"a long interval is still armed", "1min 30s", "5", false},
		{"infinity is the opposite of zero, not a form of it", "infinity", "5", false},
		{"unreadable interval is unknown, not disabled", "", "5", false},
		{"unreadable burst is unknown, not disabled", "10s", "", false},
		{"nothing readable at all stays silent", "", "", false},
		{"trailing newline from systemctl --value", "0\n", "5", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRateLimitDisabled(tt.interval, tt.burst); got != tt.want {
				t.Errorf("parseRateLimitDisabled(%q, %q) = %v, want %v", tt.interval, tt.burst, got, tt.want)
			}
		})
	}
}

// The notice is the whole point of gt-qiok: a silent restart leaves gt dolt status
// describing a healthy server on a fresh PID, with nothing tying the new PID to
// the old one's death.
func TestSupervisorRestartNotice(t *testing.T) {
	unit := func(restarts int, rateLimitDisabled bool) *Supervisor {
		return &Supervisor{
			Kind: "systemd", Unit: "gt-dolt.service", UserUnit: true, Restart: "always",
			NRestarts: restarts, RateLimitDisabled: rateLimitDisabled,
		}
	}

	t.Run("nil supervisor says nothing", func(t *testing.T) {
		if got := (*Supervisor)(nil).RestartNotice(); got != nil {
			t.Errorf("RestartNotice() = %v, want nil", got)
		}
	})

	t.Run("healthy unit that has never restarted says nothing", func(t *testing.T) {
		if got := unit(0, false).RestartNotice(); len(got) != 0 {
			t.Errorf("RestartNotice() = %v, want none", got)
		}
	})

	// The 2026-08-18 incident: two exits, systemd back up 5s later each time, and
	// no rate limit to ever mark the unit failed.
	t.Run("restarts and no rate limit are two separate notices", func(t *testing.T) {
		got := unit(2, true).RestartNotice()
		if len(got) != 2 {
			t.Fatalf("RestartNotice() = %d notices, want 2: %v", len(got), got)
		}
		if !strings.Contains(got[0], "Restarted 2 time(s)") {
			t.Errorf("first notice does not report the count: %q", got[0])
		}
		if !strings.Contains(got[0], "journalctl --user -u gt-dolt.service") {
			t.Errorf("first notice does not hand over the journal command: %q", got[0])
		}
		if !strings.Contains(got[1], "StartLimitIntervalSec=0") {
			t.Errorf("second notice does not name the disabled key: %q", got[1])
		}
	})

	// Worth reporting on its own: it answers "would I be told if it kept dying?",
	// which is false regardless of whether it has died yet.
	t.Run("no rate limit is reported even with zero restarts", func(t *testing.T) {
		got := unit(0, true).RestartNotice()
		if len(got) != 1 || !strings.Contains(got[0], "Crash-loop detection is OFF") {
			t.Errorf("RestartNotice() = %v, want the crash-loop notice alone", got)
		}
	})

	// -1 is "systemd gave no answer". Reporting "restarted -1 times" — or treating
	// unknown as evidence of a restart — is worse than staying quiet.
	t.Run("unknown restart count is not a restart", func(t *testing.T) {
		got := unit(-1, false).RestartNotice()
		if len(got) != 0 {
			t.Errorf("RestartNotice() = %v, want none for an unknown count", got)
		}
	})
}

func TestSupervisorJournalCommand(t *testing.T) {
	tests := []struct {
		name string
		sup  *Supervisor
		want string
	}{
		{"nil supervisor", nil, ""},
		{"no unit", &Supervisor{Kind: "systemd"}, ""},
		{
			"user unit",
			&Supervisor{Kind: "systemd", Unit: "gt-dolt.service", UserUnit: true},
			"journalctl --user -u gt-dolt.service -n 100",
		},
		{
			"system unit",
			&Supervisor{Kind: "systemd", Unit: "dolt.service"},
			"journalctl -u dolt.service -n 100",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sup.JournalCommand(); got != tt.want {
				t.Errorf("JournalCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectSupervisorRejectsBadPID(t *testing.T) {
	if sup := DetectSupervisor(0); sup != nil {
		t.Errorf("DetectSupervisor(0) = %+v, want nil", sup)
	}
	if sup := DetectSupervisor(-1); sup != nil {
		t.Errorf("DetectSupervisor(-1) = %+v, want nil", sup)
	}
}

// unitOwns is what separates "this unit supervises the server" from "the server
// merely inherited a unit's cgroup from whoever started it". A Dolt process
// spawned by gt while gt ran under, say, gt-snapshot.service sits in that unit's
// cgroup without being its main process; reporting it as supervised sends the
// operator to stop an unrelated unit.
func TestOwnsPID(t *testing.T) {
	tests := []struct {
		name     string
		property string
		pid      int
		want     bool
	}{
		{"unit's main process is the server", "3580495", 3580495, true},
		{"unit names a different main process", "1416", 3580495, false},
		{"no systemctl answer leaves cgroup evidence standing", "", 3580495, true},
		{"unit not running reports MainPID 0", "0", 3580495, true},
		{"unparseable property is not a contradiction", "n/a", 3580495, true},
		{"trailing newline from systemctl --value", "3580495\n", 3580495, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ownsPID(tt.property, tt.pid); got != tt.want {
				t.Errorf("ownsPID(%q, %d) = %v, want %v", tt.property, tt.pid, got, tt.want)
			}
		})
	}
}

// The live half of the same rule. Skips on machines where the test process is
// not inside a service cgroup, so it can only ever add confidence, never remove
// it — TestOwnsPID above is the load-bearing check.
func TestUnitOwnsAgainstLiveSystemd(t *testing.T) {
	// A unit that does not exist yields no MainPID. That is a missing answer,
	// not a contradicting one, so detection must be kept rather than discarded.
	if !unitOwns("gt-does-not-exist-9f3a.service", true, 1234) {
		t.Error("unitOwns should keep detection when systemd gives no MainPID")
	}

	// Self-check against the live system: this test process is never the main
	// process of any unit, so any unit systemd does name must be rejected. This
	// is the case the check exists for, and it fails if the comparison is
	// dropped.
	self := os.Getpid()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", self))
	if err != nil {
		t.Skip("no /proc cgroup for this process")
	}
	unit, userUnit := parseSystemdUnit(string(data))
	if unit == "" {
		t.Skip("test process is not inside a systemd service cgroup")
	}
	if mainPID := systemctlProperty(unit, userUnit, "MainPID"); mainPID == "" || mainPID == "0" {
		t.Skipf("systemd reports no MainPID for %s", unit)
	}
	if unitOwns(unit, userUnit, self) {
		t.Errorf("unitOwns(%s, pid=%d) = true, but the test process is not that unit's main process", unit, self)
	}
	if sup := DetectSupervisor(self); sup != nil {
		t.Errorf("DetectSupervisor(self) = %+v, want nil — cgroup membership alone is not supervision", sup)
	}
}

package doltserver

import "testing"

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

func TestSupervisorAutoRestarts(t *testing.T) {
	tests := []struct {
		name string
		sup  *Supervisor
		want bool
	}{
		{"nil supervisor", nil, false},
		{"Restart=always", &Supervisor{Unit: "gt-dolt.service", Restart: "always"}, true},
		{"Restart=on-failure", &Supervisor{Unit: "gt-dolt.service", Restart: "on-failure"}, true},
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

func TestDetectSupervisorRejectsBadPID(t *testing.T) {
	if sup := DetectSupervisor(0); sup != nil {
		t.Errorf("DetectSupervisor(0) = %+v, want nil", sup)
	}
	if sup := DetectSupervisor(-1); sup != nil {
		t.Errorf("DetectSupervisor(-1) = %+v, want nil", sup)
	}
}

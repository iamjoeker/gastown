package doltserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"
	"time"
)

// Supervisor describes a process supervisor that owns the running Dolt server.
//
// This matters because gt's own stop path (doltserver.Stop) signals the process.
// When a supervisor owns that process, signalling it is not the same as stopping
// the server: systemd's Restart=always brings a new server up on the same data
// directory seconds later. Any advice of the form "stop the server, then touch
// the files" is wrong — and dangerous — unless it stops the supervisor. (gt-xvwu)
//
// A nil *Supervisor means "no supervisor detected", which includes the case
// where detection is not implemented for the platform. Callers must treat that
// as "unknown", not as "definitely unsupervised" — always keep an explicit
// verification step in operator instructions.
type Supervisor struct {
	Kind     string // "systemd"; empty when nothing was detected
	Unit     string // unit name, e.g. "gt-dolt.service"
	UserUnit bool   // true for a `systemctl --user` unit, false for a system unit
	Restart  string // the unit's Restart= policy ("always", "no", ...); "" if unknown
}

// AutoRestarts reports whether the supervisor brings the server back up on its
// own after the process exits. When true, `gt dolt stop` does not keep the
// server down.
func (s *Supervisor) AutoRestarts() bool {
	if s == nil {
		return false
	}
	switch s.Restart {
	case "", "no":
		return false
	default:
		return true
	}
}

// StopCommand returns the command that actually stops the server: the
// supervisor's stop when one owns the process, gt's own stop otherwise.
func (s *Supervisor) StopCommand() string {
	if s == nil || s.Unit == "" {
		return "gt dolt stop"
	}
	return fmt.Sprintf("systemctl %sstop %s", s.systemctlScope(), s.Unit)
}

// StartCommand returns the command that brings the server back up after a
// StopCommand. Starting the process directly while the unit is stopped would
// leave the supervisor believing the service is down.
func (s *Supervisor) StartCommand() string {
	if s == nil || s.Unit == "" {
		return "gt dolt start"
	}
	return fmt.Sprintf("systemctl %sstart %s", s.systemctlScope(), s.Unit)
}

// Describe renders the supervisor for operator-facing messages, e.g.
// "systemd unit gt-dolt.service (Restart=always)".
func (s *Supervisor) Describe() string {
	if s == nil || s.Unit == "" {
		return ""
	}
	desc := fmt.Sprintf("%s unit %s", s.Kind, s.Unit)
	if s.Restart != "" {
		desc += fmt.Sprintf(" (Restart=%s)", s.Restart)
	}
	return desc
}

func (s *Supervisor) systemctlScope() string {
	if s.UserUnit {
		return "--user "
	}
	return ""
}

// DetectSupervisor reports which supervisor, if any, owns the given process.
//
// Detection is by cgroup membership rather than by looking for a known unit
// name: it answers "what owns THIS process", which is the question that
// matters, and it stays correct when the unit is named something other than
// gt-dolt.service or when the running server was started outside its unit.
// Returns nil when nothing is detected.
func DetectSupervisor(pid int) *Supervisor {
	if runtime.GOOS != "linux" || pid <= 0 {
		return nil
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return nil
	}
	unit, userUnit := parseSystemdUnit(string(data))
	if unit == "" {
		return nil
	}
	return &Supervisor{
		Kind:     "systemd",
		Unit:     unit,
		UserUnit: userUnit,
		Restart:  systemctlProperty(unit, userUnit, "Restart"),
	}
}

// parseSystemdUnit extracts the owning systemd unit from the contents of
// /proc/<pid>/cgroup. Returns ("", false) when the process is not in a unit.
//
// cgroup v2 writes a single "0::<path>" line; v1 writes one line per
// controller, of which the "name=systemd" one carries the unit path. Both are
// handled by scanning every line for a path whose last segment is a unit.
func parseSystemdUnit(cgroupFile string) (unit string, userUnit bool) {
	for _, line := range strings.Split(cgroupFile, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		cgPath := parts[2]
		leaf := path.Base(cgPath)
		if !strings.HasSuffix(leaf, ".service") {
			// A .scope (tmux, a login session, a manually started process) is
			// not a supervisor: nothing restarts its members.
			continue
		}
		// user@<uid>.service is the per-user service manager itself. A process
		// sitting directly in it belongs to no unit of its own.
		if strings.HasPrefix(leaf, "user@") {
			continue
		}
		return leaf, strings.Contains(cgPath, "/user@")
	}
	return "", false
}

// systemctlProperty reads one property of a unit. Returns "" on any failure —
// callers degrade to less specific advice rather than to wrong advice.
func systemctlProperty(unit string, userUnit bool, property string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	args := []string{}
	if userUnit {
		args = append(args, "--user")
	}
	args = append(args, "show", "-p", property, "--value", unit)

	out, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

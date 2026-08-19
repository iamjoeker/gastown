package doltserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strconv"
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
// own after gt stops the server. When true, `gt dolt stop` does not keep the
// server down.
//
// The answer depends on the signal gt sends, not just on the policy. Stop sends
// SIGTERM, and systemd counts termination by SIGTERM as a CLEAN exit — it is one
// of the four signals (SIGHUP, SIGINT, SIGTERM, SIGPIPE) excluded from the
// "terminated by a signal" clause in systemd.service(5). Dolt also handles the
// signal itself and exits 0, which is why the unit logs Result=success on every
// gt-issued stop. Both routes agree: the exit is a success.
//
// So only the policies that restart after a SUCCESSFUL exit revive the server.
// "on-failure", "on-abnormal", "on-abort" and "on-watchdog" all leave it down —
// a distinction that matters because Restart=on-failure is the leading candidate
// for this town's unit (gt-09e4), and reporting "the supervisor will restart it"
// under that policy would be exactly backwards.
//
// Caveat: if the server ignores SIGTERM for 5s, Stop escalates to SIGKILL, which
// IS an unclean exit and does revive a failure-triggered unit. That path is the
// exception, not the case this predicts.
func (s *Supervisor) AutoRestarts() bool {
	if s == nil {
		return false
	}
	return s.Restart == "always" || s.Restart == "on-success"
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
	if !unitOwns(unit, userUnit, pid) {
		return nil
	}
	return &Supervisor{
		Kind:     "systemd",
		Unit:     unit,
		UserUnit: userUnit,
		Restart:  systemctlProperty(unit, userUnit, "Restart"),
	}
}

// unitOwns reports whether pid is the unit's MAIN process, i.e. the process the
// unit's Restart= policy actually applies to.
//
// Cgroup membership alone is too weak a signal. Every child inherits its
// parent's cgroup, so a Dolt server that gt started itself sits in the cgroup of
// whatever unit gt was running under — a timer-driven unit like
// gt-snapshot.service, or the town daemon. Without this check, such a server is
// reported as supervised, and gt tells the operator to stop an unrelated unit to
// bring Dolt down. That advice does nothing to Dolt and stops something else.
//
// Only a positively different MainPID disproves ownership. If systemctl cannot
// be reached or gives no answer, we cannot disprove it and keep the detection —
// failing back to cgroup membership rather than to silence.
func unitOwns(unit string, userUnit bool, pid int) bool {
	return ownsPID(systemctlProperty(unit, userUnit, "MainPID"), pid)
}

// ownsPID decides ownership from a unit's raw MainPID property. Split out from
// unitOwns so the rule is testable without a live systemd.
//
// "" (no systemctl, no bus, unknown unit) and "0" (unit not running) are both
// absent answers, not contradicting ones — they leave the cgroup evidence
// standing. Only a parsed, non-zero MainPID naming a different process disproves
// ownership.
func ownsPID(mainPIDProperty string, pid int) bool {
	parsed, err := strconv.Atoi(strings.TrimSpace(mainPIDProperty))
	if err != nil || parsed == 0 {
		return true
	}
	return parsed == pid
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

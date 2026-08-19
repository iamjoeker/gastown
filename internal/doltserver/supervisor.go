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

	// NRestarts is systemd's count of automatic restarts of this unit, i.e. how
	// many times the supervisor replaced a dead server behind gt's back. -1 means
	// systemd gave no answer.
	//
	// The counter resets whenever the unit is started or restarted by hand, so it
	// counts restarts within the current manual start, not since boot. That makes
	// it a lower bound on how often the server has died — never an overstatement.
	NRestarts int

	// RateLimitDisabled reports that the unit's start-limit burst check is known to
	// be off. systemd then restarts forever and NEVER marks the unit failed, so no
	// status surface — not `systemctl status`, not `journalctl -p err`, not
	// `gt dolt status` — reports a problem no matter how often the server dies.
	// That is the observability defect in gt-qiok: the restart policy hides its own
	// restarts and a crash becomes a mystery.
	//
	// Stated as "known to be disabled" rather than "armed" so that the zero value,
	// and any unreadable property, stay silent. Claiming crash-loop detection is
	// off when it is on would send an operator to edit a unit that needs no edit.
	RateLimitDisabled bool
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

// ConfirmStoppedCommand returns the command that PROVES the unit is down, for
// instructions that must not end at "stop it" — a stop command that failed and
// a stop command that worked look the same in a terminal.
func (s *Supervisor) ConfirmStoppedCommand() string {
	if s == nil || s.Unit == "" {
		return "gt dolt status"
	}
	return fmt.Sprintf("systemctl %sshow -p ActiveState --value %s", s.systemctlScope(), s.Unit)
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
		NRestarts: parseNRestarts(
			systemctlProperty(unit, userUnit, "NRestarts")),
		RateLimitDisabled: parseRateLimitDisabled(
			systemctlProperty(unit, userUnit, "StartLimitIntervalUSec"),
			systemctlProperty(unit, userUnit, "StartLimitBurst")),
	}
}

// RestartNotice renders the operator-facing notices about restarts this unit has
// performed and about whether a crash loop would ever surface. Each element is
// one notice; a notice may span several lines, separated by "\n". Returns nil
// when there is nothing worth saying.
//
// The two facts are separate notices on purpose. A restart count answers "did the
// server die?" for the incident in front of the operator; the missing rate limit
// answers "would I be told if it kept dying?", which stays true and worth fixing
// even when the count is zero.
func (s *Supervisor) RestartNotice() []string {
	if s == nil || s.Unit == "" {
		return nil
	}
	var notices []string
	if s.NRestarts > 0 {
		// Names the unit but not its Restart= policy: callers print Describe()
		// alongside, and repeating the policy here pushes the count — the fact that
		// matters — off the end of the line.
		notices = append(notices, fmt.Sprintf(
			"Restarted %d time(s) by %s unit %s since it was last started by hand.\n"+
				"A restart replaces the PID silently, so the PID and uptime above\n"+
				"describe the REPLACEMENT and the server reads as healthy. If you did\n"+
				"not restart it, the previous process died.\n"+
				"What happened: %s",
			s.NRestarts, s.Kind, s.Unit, s.JournalCommand()))
	}
	if s.RateLimitDisabled {
		notices = append(notices,
			"Crash-loop detection is OFF for this unit (StartLimitIntervalSec=0 or\n"+
				"StartLimitBurst=0). systemd restarts forever and never marks the unit\n"+
				"failed, so no status surface reports a problem however often Dolt dies.")
	}
	return notices
}

// JournalCommand returns the command that shows what the unit did, including the
// exits that a silent restart leaves no other trace of.
func (s *Supervisor) JournalCommand() string {
	if s == nil || s.Unit == "" {
		return ""
	}
	return fmt.Sprintf("journalctl %s-u %s -n 100", s.systemctlScope(), s.Unit)
}

// parseNRestarts reads systemd's NRestarts property. Anything unparseable — the
// property is absent on older systemd, and "" is what systemctlProperty returns
// on any failure — becomes -1 ("unknown"), which callers must not report as zero.
func parseNRestarts(property string) int {
	n, err := strconv.Atoi(strings.TrimSpace(property))
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// parseRateLimitDisabled decides whether the start-limit burst check is known to
// be off, from the unit's StartLimitIntervalUSec and StartLimitBurst properties.
// Setting EITHER to zero disables rate limiting outright (systemd.unit(5)), so
// reading only the interval — the key the town's unit actually sets — would miss
// the other half.
func parseRateLimitDisabled(intervalProperty, burstProperty string) bool {
	return isZeroProperty(intervalProperty) || isZeroProperty(burstProperty)
}

// isZeroProperty reports whether a systemd property is an explicit zero. USec
// properties render as a plain "0" when disabled and as a duration ("10s",
// "1min 30s") otherwise, and "infinity" is the opposite of zero, not a form of
// it. An empty or unrecognised value is unknown, never zero.
func isZeroProperty(property string) bool {
	switch strings.TrimSpace(property) {
	case "0", "0s", "0us":
		return true
	}
	return false
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

// SupervisorRecord is the persisted identity of the supervisor that owned this
// town's Dolt server the last time gt saw one running. It lives in
// daemon/dolt-state.json.
//
// It exists because DetectSupervisor answers "what owns THIS process" from
// /proc/<pid>/cgroup, so the answer disappears exactly when it is most needed:
// with the server stopped there is no PID, no cgroup, and no unit name. Every
// remedy gt prints tells the operator to stop the unit before touching the
// filesystem, and the commands that then bring a server back up — `gt dolt
// start`, and the auto-start at the end of `gt dolt migrate` — had no way to
// find out that a supervisor was ever involved. (gt-cru5)
//
// Only identity is stored. Restart=, NRestarts and the start-limit properties
// are volatile — this town's unit had its Restart policy changed under it on
// 2026-08-18 (gt-09e4) — so they are re-read from systemd on every recall
// rather than served stale from disk.
type SupervisorRecord struct {
	Kind       string    `json:"kind"`
	Unit       string    `json:"unit"`
	UserUnit   bool      `json:"user_unit"`
	ObservedAt time.Time `json:"observed_at"`
}

// Record renders the persistable identity of a detected supervisor. Returns nil
// when there is no unit to remember.
func (s *Supervisor) Record() *SupervisorRecord {
	if s == nil || s.Unit == "" {
		return nil
	}
	return &SupervisorRecord{
		Kind:       s.Kind,
		Unit:       s.Unit,
		UserUnit:   s.UserUnit,
		ObservedAt: time.Now(),
	}
}

// propertyReader reads one systemd unit property. Injected so the recall path
// is testable without a live systemd.
type propertyReader func(unit string, userUnit bool, property string) string

// ObserveSupervisor detects the supervisor owning pid and remembers it for this
// town, returning what it detected.
//
// Call it wherever gt already holds the PID of a *running* server — that is the
// only moment the unit is discoverable, and the remembered answer is what later
// commands consult once the process is gone. Remembering is best-effort: a
// failed write costs the memory, never the caller's operation.
func ObserveSupervisor(townRoot string, pid int) *Supervisor {
	sup := DetectSupervisor(pid)
	if sup != nil {
		_ = RememberSupervisor(townRoot, sup)
	}
	return sup
}

// RememberSupervisor persists sup as the owner of this town's Dolt server.
//
// A nil sup, or one with no unit, does NOT erase an existing record. Detection
// returns nil for "unsupervised" and for every flavour of "cannot tell" —
// unsupported platform, unreadable /proc, no systemctl — and treating those
// alike would let one unlucky probe forget a real unit. Records are discarded
// on recall instead, where the unit's continued existence can be checked
// directly.
func RememberSupervisor(townRoot string, sup *Supervisor) error {
	record := sup.Record()
	if record == nil {
		return nil
	}
	state, err := LoadState(townRoot)
	if err != nil {
		return err
	}
	if state == nil {
		state = &State{}
	}
	if !supervisorRecordChanged(state.Supervisor, record) {
		// Steady state. Rewriting dolt-state.json on every status probe would
		// churn the file for no new information.
		return nil
	}
	state.Supervisor = record
	return SaveState(townRoot, state)
}

// supervisorRecordChanged compares the identity fields only: ObservedAt differs
// on every call and is not a reason to rewrite the file.
func supervisorRecordChanged(old, current *SupervisorRecord) bool {
	if old == nil {
		return current != nil
	}
	return old.Unit != current.Unit || old.UserUnit != current.UserUnit || old.Kind != current.Kind
}

// RecallSupervisor returns the supervisor remembered for this town's Dolt
// server, with its volatile properties re-read from systemd. Returns nil when
// nothing is remembered or the unit no longer exists.
//
// Unlike DetectSupervisor this answers with the server DOWN, which is the whole
// point: it is what lets `gt dolt start` hand the start to the unit instead of
// spawning a server the unit believes is not there.
func RecallSupervisor(townRoot string) *Supervisor {
	state, err := LoadState(townRoot)
	if err != nil {
		return nil
	}
	return recallSupervisor(state, systemctlProperty)
}

func recallSupervisor(state *State, read propertyReader) *Supervisor {
	if state == nil || state.Supervisor == nil || state.Supervisor.Unit == "" {
		return nil
	}
	record := state.Supervisor
	if !unitIsLoaded(read(record.Unit, record.UserUnit, "LoadState")) {
		return nil
	}
	return &Supervisor{
		Kind:      record.Kind,
		Unit:      record.Unit,
		UserUnit:  record.UserUnit,
		Restart:   read(record.Unit, record.UserUnit, "Restart"),
		NRestarts: parseNRestarts(read(record.Unit, record.UserUnit, "NRestarts")),
		RateLimitDisabled: parseRateLimitDisabled(
			read(record.Unit, record.UserUnit, "StartLimitIntervalUSec"),
			read(record.Unit, record.UserUnit, "StartLimitBurst")),
	}
}

// unitIsLoaded reports whether systemd still has this unit. Only an explicit
// "loaded" counts.
//
// The asymmetry with the rest of this file is deliberate. Elsewhere an absent
// answer keeps a detection standing, because the cost of forgetting a
// supervisor is bad advice. Here the recalled unit is about to be STARTED, and
// every non-loaded state means that would fail: "not-found" (the unit was
// removed), "masked" (the operator deliberately took it out of service),
// "error"/"bad-setting" (systemd cannot use it), "" (no systemctl, no bus —
// nothing to start it with). Falling back to gt's own start keeps the server
// coming up in all of them.
func unitIsLoaded(loadState string) bool {
	return strings.TrimSpace(loadState) == "loaded"
}

// DirectStartEnvVar forces Start to spawn the Dolt process itself even when a
// supervisor is remembered.
//
// The escape hatch exists because routing through the unit inherits the unit's
// configuration — its data directory, its port, its ExecStart. An operator
// recovering from a unit that is wrong, or bringing up a second town on the
// same host, needs a way to say "start the server I configured, not the one the
// unit describes" without editing systemd first.
const DirectStartEnvVar = "GT_DOLT_DIRECT_START"

// supervisorForStart returns the supervisor Start should hand the launch to, or
// nil to start the process directly.
func supervisorForStart(townRoot string) *Supervisor {
	if isTruthyEnv(os.Getenv(DirectStartEnvVar)) {
		return nil
	}
	return RecallSupervisor(townRoot)
}

func isTruthyEnv(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// StartUnit asks the supervisor to start its unit, returning the unit's main
// PID (0 when systemd gives no answer).
func (s *Supervisor) StartUnit() (int, error) {
	if s == nil || s.Unit == "" {
		return 0, fmt.Errorf("no supervisor unit to start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	args := []string{}
	if s.UserUnit {
		args = append(args, "--user")
	}
	args = append(args, "start", s.Unit)

	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return 0, fmt.Errorf("%s: %w: %s", s.StartCommand(), err, detail)
		}
		return 0, fmt.Errorf("%s: %w", s.StartCommand(), err)
	}

	pid, convErr := strconv.Atoi(strings.TrimSpace(
		systemctlProperty(s.Unit, s.UserUnit, "MainPID")))
	if convErr != nil || pid < 0 {
		return 0, nil
	}
	return pid, nil
}

// ConfirmedStopped reports whether the unit is positively known to be down,
// alongside the ActiveState it read (for the operator-facing message).
//
// "Down" here means "will not put a server back on the data directory by
// itself". systemd's Restart= policy applies to a unit that is running, so an
// inactive or failed unit stays inactive until something starts it; every other
// state — active, activating (which includes the pause between a crash and an
// auto-restart), deactivating, reloading — can produce a live server moments
// from now.
//
// An unreadable state is NOT stopped. This is the one place in this file where
// unknown must fail closed: the caller is about to move database directories,
// and "I could not tell" is not permission to do that.
func (s *Supervisor) ConfirmedStopped() (string, bool) {
	if s == nil || s.Unit == "" {
		return "", true
	}
	activeState := systemctlProperty(s.Unit, s.UserUnit, "ActiveState")
	return activeState, unitConfirmedStopped(activeState)
}

func unitConfirmedStopped(activeState string) bool {
	switch strings.TrimSpace(activeState) {
	case "inactive", "failed":
		return true
	}
	return false
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

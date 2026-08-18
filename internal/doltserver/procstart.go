package doltserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// linuxUserHZ is the clock-tick rate the kernel uses for the time fields in
// /proc/<pid>/stat. Unlike the kernel's internal HZ, USER_HZ is a fixed part
// of the Linux ABI and is 100 everywhere.
const linuxUserHZ = 100

// ProcessStartTime returns the wall-clock time at which the process with the
// given PID started, according to the operating system.
//
// This is the authoritative answer to "when did this server come up". Gas
// Town's own dolt-state.json records when *gt* last launched Dolt, so it goes
// stale the moment anything else restarts the server, silently overstating
// uptime (gt-pdd). The PID is already resolved from the live process, so the
// start time can come from the same place.
//
// Returns ok=false when the start time cannot be determined: unsupported
// platform, dead process, or unreadable /proc.
func ProcessStartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	switch runtime.GOOS {
	case "windows":
		return time.Time{}, false
	case "linux":
		// /proc is exact and needs no subprocess; ps is the fallback.
		if started, ok := procStatStartTime(pid); ok {
			return started, true
		}
	}
	return psStartTime(pid)
}

// ResolveStartedAt returns the best available start time for a running server:
// the OS-reported start time of the live process, falling back to gt's own
// recorded value. The second return value reports whether the answer came from
// the live process (and is therefore restart-proof).
func ResolveStartedAt(pid int, recorded time.Time) (time.Time, bool) {
	if started, ok := ProcessStartTime(pid); ok {
		return started, true
	}
	return recorded, false
}

func procStatStartTime(pid int) (time.Time, bool) {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return time.Time{}, false
	}
	ticks, ok := parseProcStatStartTicks(string(stat))
	if !ok {
		return time.Time{}, false
	}
	bootStat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	bootTime, ok := parseProcBootTime(string(bootStat))
	if !ok {
		return time.Time{}, false
	}
	sinceBoot := time.Duration(float64(ticks) / linuxUserHZ * float64(time.Second))
	return bootTime.Add(sinceBoot), true
}

// parseProcStatStartTicks extracts field 22 (starttime, in clock ticks since
// boot) from a /proc/<pid>/stat line. Field 2 (comm) is parenthesized and may
// itself contain spaces and parentheses, so parsing resumes after its final
// ')' — from there fields[0] is field 3, making field 22 fields[19].
func parseProcStatStartTicks(stat string) (uint64, bool) {
	commEnd := strings.LastIndex(stat, ")")
	if commEnd < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[commEnd+1:])
	if len(fields) < 20 {
		return 0, false
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return ticks, true
}

// parseProcBootTime extracts the btime line (boot time, in Unix seconds) from
// /proc/stat contents.
func parseProcBootTime(procStat string) (time.Time, bool) {
	for _, line := range strings.Split(procStat, "\n") {
		rest, found := strings.CutPrefix(line, "btime ")
		if !found {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(secs, 0), true
	}
	return time.Time{}, false
}

// psStartTime asks ps when the process started. Used on macOS, and on Linux
// when /proc is unreadable. LC_ALL=C pins the layout of lstart.
func psStartTime(pid int) (time.Time, bool) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	setProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}, false
	}
	return parsePsLstart(string(out))
}

// parsePsLstart parses the "Mon Aug 17 20:49:25 2026" form that ps -o lstart=
// prints under LC_ALL=C. The day of month is space-padded, so the fields are
// re-joined on single spaces before parsing.
func parsePsLstart(out string) (time.Time, bool) {
	normalized := strings.Join(strings.Fields(out), " ")
	if normalized == "" {
		return time.Time{}, false
	}
	started, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", normalized, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return started, true
}

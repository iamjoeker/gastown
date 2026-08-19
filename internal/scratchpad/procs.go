package scratchpad

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LiveProcesses returns the running agent processes that could own a
// scratchpad, with the start time and project attribution the classifier needs.
//
// The match is deliberately generous. Over-reporting a process only widens the
// set of scratchpads treated as possibly live, which costs disk space;
// under-reporting one deletes a live agent's working files, which costs the
// agent its work. Every ambiguity here resolves toward reporting the process.
func LiveProcesses(now time.Time) ([]Process, error) {
	// -o with trailing "=" suppresses headers; args must come last because it
	// is the only field that can contain spaces.
	out, err := exec.Command("ps", "-eo", "pid=,etime=,args=").Output()
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}

	var procs []Process
	self := os.Getpid()
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid == self {
			continue
		}
		elapsed, err := parseEtime(fields[1])
		if err != nil {
			// A process whose age cannot be parsed gets the most protective
			// possible start time: now. It then shadows every scratchpad,
			// because none can be shown to predate it.
			elapsed = 0
		}
		args := fields[2:]
		if !isAgentProcess(args) {
			continue
		}
		p := Process{
			PID:   pid,
			Start: now.Add(-elapsed),
			Cwd:   processCwd(pid),
		}
		p.Slugs = candidateSlugs(pid, p.Cwd)
		procs = append(procs, p)
	}
	return procs, nil
}

// isAgentProcess reports whether a command line is a Claude Code process.
//
// The executable is matched rather than the whole command line: shell wrappers
// spawned by an agent mention "claude" in their arguments constantly (snapshot
// paths, mail bodies), and counting those as agents would attribute liveness to
// whatever a command happened to quote.
func isAgentProcess(args []string) bool {
	if len(args) == 0 {
		return false
	}
	exe := strings.TrimSuffix(filepath.Base(args[0]), ".exe")
	if exe == "claude" {
		return true
	}
	// Claude Code also runs as a node process against a JS entrypoint.
	if exe == "node" {
		for _, a := range args[1:] {
			if strings.Contains(a, "claude") {
				return true
			}
		}
	}
	return false
}

// processCwd returns the working directory of a process, or "" when it cannot
// be read. "" is not "no directory" — it is "unknown", and the classifier
// treats such a process as capable of owning any project's sessions.
//
// On hardened kernels (kernel.yama.ptrace_scope >= 1) readlink of another
// process's cwd fails even for the same user, so lsof — which may be setuid or
// hold CAP_SYS_PTRACE — is the fallback.
func processCwd(pid int) string {
	pidStr := strconv.Itoa(pid)
	if target, err := os.Readlink(filepath.Join("/proc", pidStr, "cwd")); err == nil {
		// Linux appends " (deleted)" once the directory is removed.
		return strings.TrimSuffix(target, " (deleted)")
	}
	// -a ANDs -p and -d; without it lsof ORs them and returns every process.
	out, err := exec.Command("lsof", "-a", "-p", pidStr, "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return line[1:]
		}
	}
	return ""
}

// envSlugKeys are environment variables that name a directory a session could
// have been started from. A session's project slug is fixed at startup from the
// process's working directory, and an agent that changes directory mid-session
// would otherwise look like it owns a different project than its scratchpad.
var envSlugKeys = []string{"PWD", "GT_POLECAT_PATH"}

// candidateSlugs returns every project slug a process could own sessions under.
// An empty result means the process could not be attributed at all.
func candidateSlugs(pid int, cwd string) []string {
	var slugs []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		s := Slugify(dir)
		for _, existing := range slugs {
			if existing == s {
				return
			}
		}
		slugs = append(slugs, s)
	}

	add(cwd)
	for k, v := range processEnv(pid) {
		for _, key := range envSlugKeys {
			if k == key {
				add(v)
			}
		}
	}
	return slugs
}

// processEnv reads a process's environment, best-effort. An unreadable
// environment yields nothing; the caller still has the working directory, and
// if that is also unknown the process becomes a wildcard.
func processEnv(pid int) map[string]string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil
	}
	env := make(map[string]string)
	for _, entry := range strings.Split(string(data), "\x00") {
		k, v, ok := strings.Cut(entry, "=")
		if ok && k != "" {
			env[k] = v
		}
	}
	return env
}

// parseEtime parses the ps elapsed-time format, [[DD-]HH:]MM:SS.
func parseEtime(etime string) (time.Duration, error) {
	var days int
	if d, rest, ok := strings.Cut(etime, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, fmt.Errorf("parsing days in %q: %w", etime, err)
		}
		days = n
		etime = rest
	}

	parts := strings.Split(etime, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("unrecognized elapsed time %q", etime)
	}
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return 0, fmt.Errorf("parsing elapsed time %q: %w", etime, err)
		}
		nums[i] = n
	}

	var hours, minutes, seconds int
	if len(parts) == 3 {
		hours, minutes, seconds = nums[0], nums[1], nums[2]
	} else {
		minutes, seconds = nums[0], nums[1]
	}
	return time.Duration(days)*24*time.Hour +
		time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds)*time.Second, nil
}

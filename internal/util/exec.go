package util

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// FirstLine returns the first non-empty line from s, trimmed of whitespace.
// Used to extract the meaningful error message from subprocess stderr, which
// often includes multi-line cobra usage text after the actual error.
func FirstLine(s string) string {
	for _, line := range strings.SplitN(s, "\n", -1) {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(s)
}

// ExecWithOutput runs a command in the specified directory and returns stdout.
// If the command fails, stderr content is included in the error message.
func ExecWithOutput(workDir, cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...) //nolint:gosec // G204: callers validate args
	c.Dir = workDir
	SetDetachedProcessGroup(c) // suppress console window flash on Windows

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		return "", subprocessError(err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

// ExecRun runs a command in the specified directory.
// If the command fails, stderr content is included in the error message.
func ExecRun(workDir, cmd string, args ...string) error {
	c := exec.Command(cmd, args...) //nolint:gosec // G204: callers validate args
	c.Dir = workDir
	SetDetachedProcessGroup(c) // suppress console window flash on Windows

	var stderr bytes.Buffer
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		return subprocessError(err, stderr.String())
	}

	return nil
}

// subprocessError turns a failed command into the most informative error
// available: the subprocess's own stderr when it managed to write any, and
// otherwise a description of how it died.
//
// A killed process writes nothing — SIGKILL gives it no chance — so an empty
// stderr is exactly the case where the wait status is all the information
// there is. Reporting it as Go's bare "signal: killed" loses both the exit
// code an operator would recognize and the fact that an unrequested SIGKILL is
// usually the OOM killer.
func subprocessError(err error, stderr string) error {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	if info := ClassifyExitError(err, nil); info.Signaled {
		return fmt.Errorf("%s", info.Describe())
	}
	return err
}

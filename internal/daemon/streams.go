package daemon

import (
	"bytes"
	"os/exec"
	"strings"
)

// runSplitOutput runs cmd with stdout and stderr captured into separate buffers
// and returns both, trimmed of trailing whitespace.
//
// The daemon shells out to `gt` subcommands that write machine-readable results
// to stdout and advisory warnings to stderr. exec.CombinedOutput interleaves the
// two, which makes an unrelated warning look like part of a failure: a scheduler
// dispatch that failed on a missing beads table was read as a directory
// permissions problem for a full day, because the permissions warning landed on
// stderr immediately after the real error on stdout (gt-q134).
//
// Callers that log command output on failure should use this instead of
// CombinedOutput, then render the streams with formatSplitOutput.
func runSplitOutput(cmd *exec.Cmd) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return strings.TrimRight(outBuf.String(), " \t\r\n"), strings.TrimRight(errBuf.String(), " \t\r\n"), err
}

// formatSplitOutput renders captured streams as a single labelled log fragment,
// suitable for appending to a log message. Each stream is named so a reader can
// tell the failure from the noise, and empty streams are omitted entirely rather
// than logged as empty quotes.
//
// The result is empty when both streams are empty, so callers can append it
// unconditionally without producing a dangling separator.
func formatSplitOutput(stdout, stderr string) string {
	var parts []string
	if stdout != "" {
		parts = append(parts, "stdout: "+stdout)
	}
	if stderr != "" {
		parts = append(parts, "stderr: "+stderr)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " | ") + ")"
}

package daemon

import (
	"os/exec"
	"strings"
	"testing"
)

// shellCmd builds a command that runs a small sh snippet, skipping the test if
// no POSIX shell is available (Windows CI).
func shellCmd(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	return exec.Command(sh, "-c", script)
}

func TestRunSplitOutputKeepsStreamsApart(t *testing.T) {
	// The gt-q134 shape: the real failure is reported on stdout while an
	// unrelated advisory warning goes to stderr. Merged capture made the
	// warning look like part of the failure.
	cmd := shellCmd(t, `echo '{"error": "table not found: leases"}'; echo 'Warning: .beads has permissions 0755' >&2; exit 1`)

	stdout, stderr, err := runSplitOutput(cmd)
	if err == nil {
		t.Fatal("expected non-nil error from failing command")
	}
	if stdout != `{"error": "table not found: leases"}` {
		t.Errorf("stdout = %q, want the JSON error alone", stdout)
	}
	if stderr != "Warning: .beads has permissions 0755" {
		t.Errorf("stderr = %q, want the warning alone", stderr)
	}
	if strings.Contains(stdout, "Warning") {
		t.Errorf("stderr leaked into stdout: %q", stdout)
	}
}

func TestRunSplitOutputSuccess(t *testing.T) {
	cmd := shellCmd(t, `echo dispatched`)

	stdout, stderr, err := runSplitOutput(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout != "dispatched" {
		t.Errorf("stdout = %q, want %q", stdout, "dispatched")
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestRunSplitOutputTrimsTrailingNewlines(t *testing.T) {
	cmd := shellCmd(t, `printf 'out\n\n'; printf 'err\n' >&2`)

	stdout, stderr, err := runSplitOutput(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout != "out" {
		t.Errorf("stdout = %q, want %q", stdout, "out")
	}
	if stderr != "err" {
		t.Errorf("stderr = %q, want %q", stderr, "err")
	}
}

func TestFormatSplitOutput(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		stderr string
		want   string
	}{
		{name: "both empty", want: ""},
		{name: "stdout only", stdout: "planning dispatch failed", want: " (stdout: planning dispatch failed)"},
		{name: "stderr only", stderr: "Warning: permissions 0755", want: " (stderr: Warning: permissions 0755)"},
		{
			name:   "both streams labelled",
			stdout: "table not found: leases",
			stderr: "Warning: permissions 0755",
			want:   " (stdout: table not found: leases | stderr: Warning: permissions 0755)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatSplitOutput(tt.stdout, tt.stderr); got != tt.want {
				t.Errorf("formatSplitOutput(%q, %q) = %q, want %q", tt.stdout, tt.stderr, got, tt.want)
			}
		})
	}
}

package cmd

import (
	"os/exec"
	"strings"
	"testing"
)

// withBuildVars sets the link-time provenance vars for the duration of a test
// and restores them afterwards.
func withBuildVars(t *testing.T, commit, branch string) {
	t.Helper()
	origCommit, origBranch := Commit, Branch
	Commit, Branch = commit, branch
	t.Cleanup(func() { Commit, Branch = origCommit, origBranch })
}

// runVersion executes the version command with exactly the named flags set and
// returns stdout.
//
// The flag vars are package-level, so runVersion clears ALL of them before
// applying the requested ones and restores the originals afterwards. Setting a
// flag at the call site instead would make the restore a no-op (it would
// capture the already-modified value) and leak into every later test — which is
// how an earlier draft of this file silently disabled its own display
// assertions.
func runVersion(t *testing.T, flags ...string) string {
	t.Helper()
	origVerbose, origShort, origCommitOnly := versionVerbose, versionShort, versionCommitOnly
	t.Cleanup(func() {
		versionVerbose, versionShort, versionCommitOnly = origVerbose, origShort, origCommitOnly
	})

	versionVerbose, versionShort, versionCommitOnly = false, false, false
	for _, f := range flags {
		switch f {
		case "verbose":
			versionVerbose = true
		case "short":
			versionShort = true
		case "commit":
			versionCommitOnly = true
		default:
			t.Fatalf("runVersion: unknown flag %q", f)
		}
	}

	// The command prints with fmt.Printf, so capture the real stdout rather
	// than cobra's writer.
	return captureStdout(t, func() {
		versionCmd.Run(versionCmd, nil)
	})
}

// TestVersionBranchIsNotReadFromWorkingDirectory is the functional check for
// gt-5mvj: `gt version` reported the branch of whatever repository the process
// happened to be standing in, so the same binary described itself differently
// from each directory and none of the answers described the build.
//
// The test creates a real git repo on a distinctive branch, chdirs into it, and
// requires that neither the branch name nor its HEAD sha reaches the output.
func TestVersionBranchIsNotReadFromWorkingDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}

	repo := t.TempDir()
	branch := "not-the-build-branch"
	gitInit(t, repo, branch)
	head := gitHead(t, repo)

	withBuildVars(t, "", "")
	t.Chdir(repo)

	// Positive control: the ambient repo really is on that branch with that
	// HEAD, so a probe reading the cwd COULD find them. Without this, a clean
	// result below would be indistinguishable from a broken fixture.
	if got := gitBranch(t, repo); got != branch {
		t.Fatalf("fixture repo is on branch %q, want %q — the test cannot detect the defect", got, branch)
	}

	out := runVersion(t)

	if strings.Contains(out, branch) {
		t.Errorf("gt version leaked the working directory's branch %q into its output:\n%s", branch, out)
	}
	if strings.Contains(out, head[:12]) {
		t.Errorf("gt version leaked the working directory's HEAD %s into its output:\n%s", head[:12], out)
	}
	if !strings.Contains(out, "unknown") {
		t.Errorf("unstamped build should report its commit as unknown, got:\n%s", out)
	}
	// The "branch@sha" form asserts an identification of this binary. An
	// unstamped build has none, so it must not be printed in that shape.
	if strings.Contains(out, "@") {
		t.Errorf("unstamped build printed a branch@sha identification:\n%s", out)
	}
}

// TestVersionUsesStampedProvenance verifies the other half: when `make build`
// stamps the gastown sha and branch, those are exactly what is reported.
func TestVersionUsesStampedProvenance(t *testing.T) {
	withBuildVars(t, "0123456789abcdef0123456789abcdef01234567", "carry/operational")

	out := runVersion(t)

	if !strings.Contains(out, "carry/operational@0123456789ab") {
		t.Errorf("stamped build should report branch@sha, got:\n%s", out)
	}
	if strings.Contains(out, "unknown") {
		t.Errorf("stamped build should not report unknown provenance, got:\n%s", out)
	}
}

// TestVersionCommitFlagOnlyReportsStampedCommits guards the machine-readable
// instrument the Makefile's forward-only gate depends on. It must never emit a
// sha that was not stamped at link time: a plausible sha resolves in some real
// repo, so an ancestry test against it succeeds and answers confidently wrong.
func TestVersionCommitFlagOnlyReportsStampedCommits(t *testing.T) {
	t.Run("stamped", func(t *testing.T) {
		withBuildVars(t, "0123456789abcdef0123456789abcdef01234567", "main")
		out := strings.TrimSpace(runVersion(t, "commit"))
		if out != "0123456789abcdef0123456789abcdef01234567" {
			t.Errorf("--commit = %q, want the full stamped sha", out)
		}
	})

	t.Run("unstamped", func(t *testing.T) {
		withBuildVars(t, "", "")
		out := strings.TrimSpace(runVersion(t, "commit"))
		if out != "unknown" {
			t.Errorf("--commit = %q, want \"unknown\" for an unstamped build", out)
		}
	})
}

// TestVersionStampedCommitDoesNotBorrowAmbientBranch covers the display path
// where the old cwd fallback actually reached the user: a binary stamped with a
// commit but no branch (a release or CI build off a detached HEAD) printed
// "<caller's branch>@<sha>", which reads as "this build came from that branch"
// and did not.
//
// This is the case the unstamped test above cannot reach — with no commit there
// is no "branch@sha" line to contaminate — so both are needed.
func TestVersionStampedCommitDoesNotBorrowAmbientBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}

	repo := t.TempDir()
	ambient := "callers-branch"
	gitInit(t, repo, ambient)

	withBuildVars(t, "0123456789abcdef0123456789abcdef01234567", "")
	t.Chdir(repo)

	if got := gitBranch(t, repo); got != ambient {
		t.Fatalf("fixture repo is on branch %q, want %q — the test cannot detect the defect", got, ambient)
	}

	out := runVersion(t)

	if strings.Contains(out, ambient) {
		t.Errorf("gt version labelled the build with the caller's branch %q:\n%s", ambient, out)
	}
	if !strings.Contains(out, "0123456789ab") {
		t.Errorf("stamped commit should still be reported, got:\n%s", out)
	}
}

// TestResolveBranchDoesNotShellOutToGit is the narrow unit-level statement of
// the same rule, so a reintroduced fallback fails here even if the printed
// format changes.
func TestResolveBranchDoesNotShellOutToGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}

	repo := t.TempDir()
	gitInit(t, repo, "ambient-branch")
	withBuildVars(t, "", "")
	t.Chdir(repo)

	if got := resolveBranch(); got == "ambient-branch" {
		t.Errorf("resolveBranch() read the working directory's git branch")
	}
}

func gitInit(t *testing.T, dir, branch string) {
	t.Helper()
	runGitForVersionTest(t, dir, "init", "--initial-branch="+branch)
	runGitForVersionTest(t, dir, "config", "user.email", "test@example.com")
	runGitForVersionTest(t, dir, "config", "user.name", "Test")
	runGitForVersionTest(t, dir, "commit", "--allow-empty", "-m", "seed")
}

func gitHead(t *testing.T, dir string) string {
	t.Helper()
	return runGitForVersionTest(t, dir, "rev-parse", "HEAD")
}

func gitBranch(t *testing.T, dir string) string {
	t.Helper()
	return runGitForVersionTest(t, dir, "symbolic-ref", "--short", "HEAD")
}

func runGitForVersionTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Keep the ambient user's git config out of the fixture (hooks, templates,
	// signing) — and never discard stderr on a probe.
	cmd.Env = append(cmd.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// newMRRoutingTown builds a minimal town whose "gastown" rig routes to
// <town>/gastown/mayor/rig, and returns (townRoot, rigRoot, polecatWorkDir).
// The rig root is a real git repo so bd's role routing has something to key off.
// The polecat work dir mirrors a real polecat worktree: it lives under the town
// but is NOT the rig repo, so a create must be routed to the rig.
func newMRRoutingTown(t *testing.T) (townRoot, rigRoot, polecatDir string) {
	t.Helper()

	townRoot = t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	rigRoot = filepath.Join(townRoot, "gastown", "mayor", "rig")
	rigBeads := filepath.Join(rigRoot, ".beads")
	polecatDir = filepath.Join(townRoot, "gastown", "polecats", "dust", "gastown")

	for _, dir := range []string{townBeads, rigBeads, polecatDir, filepath.Join(townRoot, "mayor")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	// FindTownRoot keys off mayor/town.json.
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townBeads, "routes.jsonl"),
		[]byte(`{"prefix":"hq-","path":"."}`+"\n"+`{"prefix":"gt-","path":"gastown/mayor/rig"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), []byte(`{"dolt_database":"gastown"}`), 0644); err != nil {
		t.Fatal(err)
	}

	gitInit(t, rigRoot)
	ResetEnsuredDirs()
	return townRoot, rigRoot, polecatDir
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

// setGitRole writes beads.role into dir's own config file.
func setGitRole(t *testing.T, dir, role string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "config", "--local", beadsRoleKey, role).CombinedOutput(); err != nil {
		t.Fatalf("set %s=%s: %v: %s", beadsRoleKey, role, err, out)
	}
}

// storedGitRole reads beads.role as it is written on disk, ignoring any
// GIT_CONFIG_* override, so a test can prove gt never rewrote the repo.
func storedGitRole(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "config", "--local", "--get", beadsRoleKey)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// installFakeBD installs a POSIX-shell `bd` on PATH that logs its argv, logs the
// beads.role git resolves for the rig under the environment it was handed, and
// returns a canned wisp for `create`. Resolving through the real git binary is
// the point: it is how bd itself decides which store to route to, so the log
// records the routing decision bd would actually make.
func installFakeBD(t *testing.T, rigRoot string) (logPath string) {
	t.Helper()

	binDir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "bd.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
printf 'resolved-role=%s\n' "$(git -C "$FAKE_RIG_ROOT" config --get beads.role)" >> "$BD_LOG"
if [ "$1" = "create" ]; then
  printf '%s\n' '{"id":"gt-wisp-abc","title":"Merge: gt-2ta","status":"open","priority":1,"ephemeral":true,"labels":["gt:merge-request"],"created_at":"2026-08-02T00:00:00Z","updated_at":"2026-08-02T00:00:00Z"}'
  exit 0
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	t.Setenv("FAKE_RIG_ROOT", rigRoot)
	return logPath
}

// TestRigCreateOverridesContributorRouting is the gt-2ta regression test.
//
// An MR wisp created from a polecat cwd must land in the owning rig's database.
// With the rig repo marked beads.role=contributor, bd silently diverts the write
// into the contributor planning store: bd exits 0 with a real ID, but the wisp
// never reaches the project's wisps table, so `gt mq list` and the refinery are
// blind to it and the polecat's work is stranded.
func TestRigCreateOverridesContributorRouting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell fake bd")
	}

	_, rigRoot, polecatDir := newMRRoutingTown(t)
	logPath := installFakeBD(t, rigRoot)

	// The broken state observed in production.
	setGitRole(t, rigRoot, "contributor")

	issue, err := NewIsolated(polecatDir).Create(CreateOptions{
		Title:       "Merge: gt-2ta",
		Labels:      []string{"gt:merge-request"},
		Priority:    1,
		Description: "branch: polecat/dust/gt-2ta",
		Ephemeral:   true,
		Rig:         "gastown",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if issue.ID != "gt-wisp-abc" {
		t.Fatalf("created issue ID = %q, want gt-wisp-abc", issue.ID)
	}

	log := string(mustReadFile(t, logPath))
	if !strings.Contains(log, "resolved-role="+beadsRoleMaintainer) {
		t.Fatalf("bd would still resolve contributor routing, so the MR bead lands in a personal store and is stranded:\n%s", log)
	}
	// hq-1uf2: --repo alongside BEADS_DIR opens the same database twice and
	// deadlocks. The role fix must not reintroduce it.
	if strings.Contains(log, "--repo") {
		t.Fatalf("rig create must not pass --repo (hq-1uf2 deadlock):\n%s", log)
	}
	if !strings.Contains(log, "--ephemeral") || !strings.Contains(log, "--labels=gt:merge-request") {
		t.Fatalf("MR create lost its wisp/label flags:\n%s", log)
	}
}

// TestCreateNeverRewritesRepoConfig is the governance guarantee. Which store a
// repo's own `bd list` reads is the repo owner's decision; gt corrects routing
// only for the command it issues, and must leave the repo's config on disk
// exactly as it found it.
func TestCreateNeverRewritesRepoConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell fake bd")
	}

	for _, tc := range []struct{ name, seed, want string }{
		{"contributor is preserved on disk", "contributor", "contributor"},
		{"unset stays unset", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, rigRoot, polecatDir := newMRRoutingTown(t)
			installFakeBD(t, rigRoot)
			if tc.seed != "" {
				setGitRole(t, rigRoot, tc.seed)
			}

			if _, err := NewIsolated(polecatDir).Create(CreateOptions{
				Title: "Merge: gt-2ta", Priority: 1, Ephemeral: true, Rig: "gastown",
			}); err != nil {
				t.Fatalf("Create: %v", err)
			}

			if got := storedGitRole(t, rigRoot); got != tc.want {
				t.Fatalf("stored %s = %q, want %q — gt must not rewrite repo config", beadsRoleKey, got, tc.want)
			}
		})
	}
}

// TestSilentMRCreateFailureIsDiagnosable is the gt-h7p regression test, run
// through the real MR create path a polecat's `gt done` takes.
//
// A bd that exits non-zero without writing to either stream produced exactly
// "bd create --json --title=...: exit status 1". That message is what a stalled
// polecat, its witness, and the Mayor all had to diagnose from — and it is
// indistinguishable from gt having thrown bd's stderr away, so the report it
// generated (gt-h7p) chased a discarded-stderr bug that was not there.
func TestSilentMRCreateFailureIsDiagnosable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell fake bd")
	}

	_, _, polecatDir := newMRRoutingTown(t)
	binDir := t.TempDir()
	// bd fails saying nothing at all — the case gt-k3h's stdout fallback cannot
	// reach, because there is no output to fall back to.
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := NewIsolated(polecatDir).Create(CreateOptions{
		Title:       "Merge: gt-h7p",
		Labels:      []string{"gt:merge-request"},
		Priority:    1,
		Description: "branch: polecat/settler/gt-h7p",
		Ephemeral:   true,
		Rig:         "gastown",
	})
	if err == nil {
		t.Fatal("Create succeeded against a bd that exits 1")
	}

	msg := err.Error()
	if !strings.Contains(msg, "bd wrote nothing to stdout or stderr") {
		t.Errorf("MR create failure does not say bd was silent: %v", err)
	}
	// The rig database, not the polecat worktree: naming it is what lets an
	// operator see why the same command by hand — from a different cwd, against
	// a different store — succeeds.
	if !strings.Contains(msg, filepath.Join("gastown", "mayor", "rig", ".beads")) {
		t.Errorf("MR create failure does not name the target database: %v", err)
	}
	if !strings.Contains(msg, "cwd="+polecatDir) {
		t.Errorf("MR create failure does not name the working directory: %v", err)
	}
}

// TestWithProjectBeadsRoleThroughGit proves the override is one git actually
// honors, rather than only asserting the strings we appended.
func TestWithProjectBeadsRoleThroughGit(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	setGitRole(t, dir, "contributor")

	cmd := exec.Command("git", "-C", dir, "config", "--get", beadsRoleKey)
	cmd.Env = withProjectBeadsRole(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --get: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != beadsRoleMaintainer {
		t.Fatalf("git resolved %s = %q, want %q", beadsRoleKey, got, beadsRoleMaintainer)
	}
	if got := storedGitRole(t, dir); got != "contributor" {
		t.Fatalf("stored %s = %q, want contributor — the override must not touch the config file", beadsRoleKey, got)
	}
}

func TestWithProjectBeadsRole(t *testing.T) {
	// envIndex returns the GIT_CONFIG_ index the role override was written at,
	// along with the final count.
	roleIndexAndCount := func(t *testing.T, env []string) (string, string) {
		t.Helper()
		count := envValue(env, "GIT_CONFIG_COUNT")
		for i := 0; ; i++ {
			key := envValue(env, "GIT_CONFIG_KEY_"+strconv.Itoa(i))
			if key == "" {
				t.Fatalf("role override not found in env: %v", env)
			}
			if key == beadsRoleKey {
				return strconv.Itoa(i), count
			}
		}
	}

	t.Run("appends at 0 when no overrides exist", func(t *testing.T) {
		env := withProjectBeadsRole([]string{"PATH=/usr/bin"})
		idx, count := roleIndexAndCount(t, env)
		if idx != "0" || count != "1" {
			t.Fatalf("index=%s count=%s, want 0 and 1", idx, count)
		}
		if got := envValue(env, "GIT_CONFIG_VALUE_0"); got != beadsRoleMaintainer {
			t.Fatalf("GIT_CONFIG_VALUE_0 = %q, want %q", got, beadsRoleMaintainer)
		}
	})

	t.Run("preserves overrides the caller already set", func(t *testing.T) {
		env := withProjectBeadsRole([]string{
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=user.name",
			"GIT_CONFIG_VALUE_0=someone",
		})
		idx, count := roleIndexAndCount(t, env)
		if idx != "1" || count != "2" {
			t.Fatalf("index=%s count=%s, want 1 and 2", idx, count)
		}
		if got := envValue(env, "GIT_CONFIG_KEY_0"); got != "user.name" {
			t.Fatalf("clobbered caller override: GIT_CONFIG_KEY_0 = %q", got)
		}
		if n := countEnvKeyTest(env, "GIT_CONFIG_COUNT"); n != 1 {
			t.Fatalf("GIT_CONFIG_COUNT appears %d times, want exactly 1", n)
		}
	})

	// A malformed count makes git reject every override, including ours, so it
	// must be replaced rather than appended to.
	t.Run("replaces a malformed count", func(t *testing.T) {
		for _, bad := range []string{"not-a-number", "-1", ""} {
			env := withProjectBeadsRole([]string{"GIT_CONFIG_COUNT=" + bad})
			idx, count := roleIndexAndCount(t, env)
			if idx != "0" || count != "1" {
				t.Fatalf("count=%q: index=%s count=%s, want 0 and 1", bad, idx, count)
			}
			if n := countEnvKeyTest(env, "GIT_CONFIG_COUNT"); n != 1 {
				t.Fatalf("count=%q: GIT_CONFIG_COUNT appears %d times, want exactly 1", bad, n)
			}
		}
	})
}

func countEnvKeyTest(env []string, key string) int {
	n := 0
	for _, e := range env {
		if k, _, ok := strings.Cut(e, "="); ok && k == key {
			n++
		}
	}
	return n
}

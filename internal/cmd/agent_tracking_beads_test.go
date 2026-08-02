package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// agentStateDirFixture builds a town containing a rig whose .beads redirects to
// a separate rig database, installs a fake bd that only resolves agentBead in
// beadWithBeadsDir, and chdirs into the rig work dir. It returns the town beads
// dir, the rig beads dir, and the fake bd invocation log path.
func agentStateDirFixture(t *testing.T, beadHomeIsTown bool) (townBeads, rigBeads, logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	tmp := t.TempDir()
	townRoot := filepath.Join(tmp, "gt")
	townBeads = filepath.Join(townRoot, ".beads")
	rigWorkDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	rigRedirect := filepath.Join(rigWorkDir, ".beads")
	rigBeads = filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	mayorDir := filepath.Join(townRoot, "mayor")

	for _, dir := range []string{townBeads, rigRedirect, rigBeads, mayorDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	// FindTownRoot identifies the town by mayor/town.json.
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigRedirect, "redirect"), []byte("../../mayor/rig/.beads"), 0o644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}

	beadHome := rigBeads
	if beadHomeIsTown {
		beadHome = townBeads
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath = filepath.Join(tmp, "bd.log")

	// The fake bd resolves the agent bead in exactly one database, mirroring a
	// town where agent identity beads live only in the town db.
	bdScript := fmt.Sprintf(`#!/bin/sh
printf 'cmd=%%s BEADS_DIR=%%s\n' "$1" "${BEADS_DIR-}" >> "$BD_LOG"
case "$1" in
  show)
    if [ "${BEADS_DIR-}" = "%s" ]; then
      printf '[{"labels":["gt:agent","idle:2"]}]\n'
    else
      printf 'issue not found: %%s\n' "$2" >&2
      exit 1
    fi
    ;;
  update)
    ;;
  list)
    for arg in "$@"; do
      if [ "$arg" = "--include-infra" ]; then
        if [ "${BEADS_DIR-}" = "%s" ]; then
          printf '[{"id":"gt-gastown-witness","status":"open"}]\n'
        else
          printf '[]\n'
        fi
        exit 0
      fi
    done
    printf '[]\n'
    ;;
  *)
    printf 'unexpected bd command: %%s\n' "$1" >&2
    exit 1
    ;;
esac
`, beadHome, beadHome)

	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	t.Setenv("BEADS_DIR", townBeads)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(rigWorkDir); err != nil {
		t.Fatalf("chdir rig work dir: %v", err)
	}

	return townBeads, rigBeads, logPath
}

// A town-scoped agent bead must resolve to the town database. Pinning state ops
// to the rig db drops every idle/heartbeat read and write (gt-5n3).
func TestResolveAgentStateBeadsDirFallsBackToTownWhenBeadOnlyInTown(t *testing.T) {
	townBeads, rigBeads, _ := agentStateDirFixture(t, true)

	got, err := resolveAgentStateBeadsDir("gt-gastown-witness")
	if err != nil {
		t.Fatalf("resolveAgentStateBeadsDir() error = %v", err)
	}
	if got != townBeads {
		t.Fatalf("resolveAgentStateBeadsDir() = %q, want town beads %q (rig beads %q)", got, townBeads, rigBeads)
	}
}

// A rig-local agent bead still wins, so per-rig agent tracking is preserved and
// the town db is never probed.
func TestResolveAgentStateBeadsDirPrefersRigWhenBeadIsRigLocal(t *testing.T) {
	townBeads, rigBeads, logPath := agentStateDirFixture(t, false)

	got, err := resolveAgentStateBeadsDir("gt-gastown-witness")
	if err != nil {
		t.Fatalf("resolveAgentStateBeadsDir() error = %v", err)
	}
	if got != rigBeads {
		t.Fatalf("resolveAgentStateBeadsDir() = %q, want rig beads %q", got, rigBeads)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	if strings.Contains(string(data), "BEADS_DIR="+townBeads) {
		t.Fatalf("town db was probed even though the bead is rig-local:\n%s", data)
	}
}

// An empty agent bead means there is nothing to track, so no probe should run.
func TestResolveAgentStateBeadsDirSkipsProbeForEmptyBead(t *testing.T) {
	_, rigBeads, logPath := agentStateDirFixture(t, true)

	got, err := resolveAgentStateBeadsDir("")
	if err != nil {
		t.Fatalf("resolveAgentStateBeadsDir() error = %v", err)
	}
	if got != rigBeads {
		t.Fatalf("resolveAgentStateBeadsDir(\"\") = %q, want rig beads %q", got, rigBeads)
	}

	// A missing log file is the expected result: bd was never invoked at all.
	data, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read fake bd log: %v", err)
	}
	if strings.TrimSpace(string(data)) != "" {
		t.Fatalf("bd was invoked for an empty agent bead:\n%s", data)
	}
}

// State reads and writes must land in the town db when the agent bead lives
// there — the read/write half of gt-5n3.
func TestRunAgentStateWritesToTownDBForTownScopedBead(t *testing.T) {
	townBeads, rigBeads, logPath := agentStateDirFixture(t, true)

	oldSet, oldIncr, oldDel, oldJSON := agentStateSet, agentStateIncr, agentStateDel, agentStateJSON
	t.Cleanup(func() {
		agentStateSet, agentStateIncr, agentStateDel, agentStateJSON = oldSet, oldIncr, oldDel, oldJSON
	})
	agentStateSet = nil
	agentStateIncr = "idle"
	agentStateDel = nil
	agentStateJSON = false

	if err := runAgentState(nil, []string{"gt-gastown-witness"}); err != nil {
		t.Fatalf("runAgentState() error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	log := string(data)
	if !strings.Contains(log, "cmd=update BEADS_DIR="+townBeads) {
		t.Fatalf("idle increment did not write to the town db %q:\n%s", townBeads, log)
	}
	if strings.Contains(log, "cmd=update BEADS_DIR="+rigBeads) {
		t.Fatalf("idle increment wrote to the rig db %q, where the bead does not exist:\n%s", rigBeads, log)
	}
}

func TestResolveAgentTrackingBeadsDirPrefersCwdRigRedirectOverBeadsDir(t *testing.T) {
	tmp := t.TempDir()
	townRoot := filepath.Join(tmp, "gt")
	townBeads := filepath.Join(townRoot, ".beads")
	rigWorkDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	rigRedirect := filepath.Join(rigWorkDir, ".beads")
	rigBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")

	for _, dir := range []string{townBeads, rigRedirect, rigBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(rigRedirect, "redirect"), []byte("../../mayor/rig/.beads"), 0o644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}
	t.Setenv("BEADS_DIR", townBeads)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(rigWorkDir); err != nil {
		t.Fatalf("chdir rig work dir: %v", err)
	}

	gotWorkDir, err := findCwdBeadsWorkDir()
	if err != nil {
		t.Fatalf("findCwdBeadsWorkDir() error = %v", err)
	}
	if gotWorkDir != rigWorkDir {
		t.Fatalf("findCwdBeadsWorkDir() = %q, want %q", gotWorkDir, rigWorkDir)
	}

	gotBeadsDir, err := resolveAgentTrackingBeadsDir()
	if err != nil {
		t.Fatalf("resolveAgentTrackingBeadsDir() error = %v", err)
	}
	if gotBeadsDir != rigBeads {
		t.Fatalf("resolveAgentTrackingBeadsDir() = %q, want %q", gotBeadsDir, rigBeads)
	}

	gotLocalWorkDir, err := findLocalBeadsDir()
	if err != nil {
		t.Fatalf("findLocalBeadsDir() error = %v", err)
	}
	if gotLocalWorkDir != townRoot {
		t.Fatalf("findLocalBeadsDir() = %q, want env parent %q", gotLocalWorkDir, townRoot)
	}
}

func TestRunAgentStateUsesCwdRigBeadsDirWhenBeadsDirPointsTown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	tmp := t.TempDir()
	townRoot := filepath.Join(tmp, "gt")
	townBeads := filepath.Join(townRoot, ".beads")
	rigWorkDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	rigRedirect := filepath.Join(rigWorkDir, ".beads")
	rigBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")

	for _, dir := range []string{townBeads, rigRedirect, rigBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(rigRedirect, "redirect"), []byte("../../mayor/rig/.beads"), 0o644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}
	metadata := []byte(`{"dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`)
	if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), metadata, 0o644); err != nil {
		t.Fatalf("write rig metadata: %v", err)
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath := filepath.Join(tmp, "bd.log")
	bdScript := `#!/bin/sh
printf 'cmd=%s BEADS_DIR=%s DB=%s READONLY=%s AUTO=%s\n' "$1" "${BEADS_DIR-}" "${BEADS_DOLT_SERVER_DATABASE-}" "${BD_READONLY-}" "${BD_DOLT_AUTO_COMMIT-}" >> "$BD_LOG"
case "$1" in
  show)
    printf '[{"labels":["gt:agent","idle:2"]}]\n'
    ;;
  update)
    ;;
  *)
    printf 'unexpected bd command: %s\n' "$1" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	t.Setenv("BEADS_DIR", townBeads)
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "town")
	t.Setenv("BD_READONLY", "true")
	t.Setenv("BD_DOLT_AUTO_COMMIT", "off")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(rigWorkDir); err != nil {
		t.Fatalf("chdir rig work dir: %v", err)
	}

	oldSet := agentStateSet
	oldIncr := agentStateIncr
	oldDel := agentStateDel
	oldJSON := agentStateJSON
	t.Cleanup(func() {
		agentStateSet = oldSet
		agentStateIncr = oldIncr
		agentStateDel = oldDel
		agentStateJSON = oldJSON
	})
	agentStateSet = []string{"idle=0"}
	agentStateIncr = ""
	agentStateDel = nil
	agentStateJSON = false

	if err := runAgentState(nil, []string{"gt-gastown-refinery"}); err != nil {
		t.Fatalf("runAgentState() error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	log := strings.TrimSpace(string(data))
	if log == "" {
		t.Fatal("fake bd was not invoked")
	}

	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, "BEADS_DIR="+rigBeads) {
			t.Fatalf("bd call was not pinned to rig beads %q: %s\nfull log:\n%s", rigBeads, line, log)
		}
		if strings.Contains(line, "BEADS_DIR="+townBeads) {
			t.Fatalf("bd call used inherited town BEADS_DIR %q: %s\nfull log:\n%s", townBeads, line, log)
		}
		if !strings.Contains(line, "DB=rigdb") || strings.Contains(line, "DB=town") {
			t.Fatalf("bd call was not pinned to rig database: %s\nfull log:\n%s", line, log)
		}
		if strings.Contains(line, "cmd=show") {
			if !strings.Contains(line, "READONLY=true") || !strings.Contains(line, "AUTO=off") {
				t.Fatalf("bd read was not read-only pinned: %s\nfull log:\n%s", line, log)
			}
		}
		if strings.Contains(line, "cmd=update") {
			if !strings.Contains(line, "READONLY= ") || !strings.Contains(line, "AUTO=on") {
				t.Fatalf("bd mutation was not mutation pinned: %s\nfull log:\n%s", line, log)
			}
		}
	}
}

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gt-pzx: `pour` was parsed and never read, so both sling paths materialized
// every formula's steps as child wisps. Only pour = true formulas should get
// step rows; everything else is instantiated root-only, the way patrol wisps
// already were, and read inline from the formula by gt prime.

// newPourStubTown builds a town whose bd stub records the formula
// instantiation commands sling issues, and answers both of them.
func newPourStubTown(t *testing.T) (townRoot, logPath string) {
	t.Helper()

	townRoot = t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, ".beads", "routes.jsonl"), []byte(`{"prefix":"gt-","path":"."}`), 0644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	logPath = filepath.Join(townRoot, "bd.log")

	bdScript := `#!/bin/sh
echo "CMD:$*" >> "${BD_LOG}"
cmd="$1"; shift || true
case "$cmd" in
  cook) exit 0;;
  mol)
    sub="$1"; shift || true
    case "$sub" in
      wisp) echo '{"new_epic_id":"gt-wisp-pour","id_mapping":{"mol-polecat-work":"gt-wisp-pour"}}';;
      bond) echo '{"result_id":"gt-bead","id_mapping":{"mol-polecat-work":"gt-wisp-pour"}}';;
    esac;;
esac
exit 0
`
	bdScriptWindows := `@echo off
setlocal enableextensions
echo CMD:%*>>"%BD_LOG%"
set "cmd=%1"
set "sub=%2"
if "%cmd%"=="cook" exit /b 0
if "%cmd%"=="mol" (
  if "%sub%"=="wisp" (
    echo {^"new_epic_id^":^"gt-wisp-pour^",^"id_mapping^":{^"mol-polecat-work^":^"gt-wisp-pour^"}}
    exit /b 0
  )
  if "%sub%"=="bond" (
    echo {^"result_id^":^"gt-bead^",^"id_mapping^":{^"mol-polecat-work^":^"gt-wisp-pour^"}}
    exit /b 0
  )
)
exit /b 0
`
	_ = writeBDStub(t, binDir, bdScript, bdScriptWindows)

	t.Setenv("BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return townRoot, logPath
}

// TestInstantiateFormulaOnBeadHonorsPour is the gt-pzx regression for the
// formula-on-bead path: mol-polecat-work declares no pour and must not reach
// `bd mol bond`, which spawns every step; the same formula with pour = true on
// disk must.
func TestInstantiateFormulaOnBeadHonorsPour(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pours       bool
		wantCmd     string
		unwantedCmd string
	}{
		{
			name:        "formula declares no pour",
			pours:       false,
			wantCmd:     "mol wisp create mol-polecat-work --root-only --json",
			unwantedCmd: "mol bond",
		},
		{
			name:        "formula declares pour = true",
			pours:       true,
			wantCmd:     "mol bond mol-polecat-work gt-bead --json --ephemeral",
			unwantedCmd: "mol wisp",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			townRoot, logPath := newPourStubTown(t)
			if tc.pours {
				installPouringFormula(t, townRoot, "mol-polecat-work")
			}

			result, err := InstantiateFormulaOnBead(context.Background(), "mol-polecat-work", "gt-bead", "Some work", "", townRoot, false, nil)
			if err != nil {
				t.Fatalf("InstantiateFormulaOnBead: %v", err)
			}
			if result.WispRootID != "gt-wisp-pour" {
				t.Fatalf("WispRootID = %q, want gt-wisp-pour", result.WispRootID)
			}

			logBytes, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			logContent := string(logBytes)
			if !strings.Contains(logContent, tc.wantCmd) {
				t.Errorf("missing %q in bd log:\n%s", tc.wantCmd, logContent)
			}
			if strings.Contains(logContent, tc.unwantedCmd) {
				t.Errorf("unexpected %q in bd log:\n%s", tc.unwantedCmd, logContent)
			}
		})
	}
}

// TestSpawnFormulaRootOnlyLinksWispToBead verifies the root-only path replaces
// bond's attach with the identical dependency edge: `bd dep add <blocked>
// <blocker>` writes issue_id=<wisp>, depends_on_issue_id=<bead>, type=blocks,
// which is the row bond leaves behind.
func TestSpawnFormulaRootOnlyLinksWispToBead(t *testing.T) {
	townRoot, logPath := newPourStubTown(t)

	rootID, err := spawnFormulaRootOnly("mol-polecat-work", "mol-polecat-work", "gt-bead", townRoot, townRoot, []string{"feature=Some work", "issue=gt-bead"})
	if err != nil {
		t.Fatalf("spawnFormulaRootOnly: %v", err)
	}
	if rootID != "gt-wisp-pour" {
		t.Fatalf("rootID = %q, want gt-wisp-pour", rootID)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(logBytes), "dep add gt-wisp-pour gt-bead") {
		t.Errorf("root-only spawn did not link the wisp to its bead:\n%s", string(logBytes))
	}
}

// TestFormulaWispArgsHonorsPour covers the standalone formula sling path, the
// one that turned `gt sling mol-deacon-patrol deacon` into 26 step wisps per
// invocation while autoSpawnPatrol created the same formula root-only.
func TestFormulaWispArgsHonorsPour(t *testing.T) {
	townRoot := t.TempDir()

	args := formulaWispArgs("mol-deacon-patrol", townRoot, "", []string{"cycle=1"})
	if !containsArg(args, "--root-only") {
		t.Errorf("mol-deacon-patrol declares no pour, want --root-only, got %v", args)
	}
	if !containsArg(args, "cycle=1") || !containsArg(args, "--json") {
		t.Errorf("vars and --json must survive the root-only flag, got %v", args)
	}

	installPouringFormula(t, townRoot, "mol-deacon-patrol")
	args = formulaWispArgs("mol-deacon-patrol", townRoot, "", nil)
	if containsArg(args, "--root-only") {
		t.Errorf("pour = true formula must materialize its steps, got %v", args)
	}
}

// TestFormulaPoursStepsFallsBackToPouring pins the safe default: a formula that
// resolves nowhere keeps the pre-gt-pzx behavior rather than being silently
// stripped of its steps.
func TestFormulaPoursStepsFallsBackToPouring(t *testing.T) {
	if !formulaPoursSteps("mol-no-such-formula-anywhere", t.TempDir(), "") {
		t.Error("unresolvable formula should fall back to pouring")
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

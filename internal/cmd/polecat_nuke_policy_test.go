package cmd

import (
	"strings"
	"testing"
)

// The restart-first policy (gt-dsgp) says the witness NEVER nukes polecats and
// that nuking happens only via explicit `gt polecat nuke` from a human or Mayor.
// Before gt-y20 that rule was prose in a formula preamble while the tooling
// pointed the other way; duly_noted/obsidian was destroyed as a result (dn-v29).
// These tests pin the rule into the binary.

func TestCheckRestartFirstNukePolicy(t *testing.T) {
	tests := []struct {
		name     string
		gtRole   string
		bdActor  string
		override bool
		refused  bool
	}{
		{
			name:    "witness identity is refused",
			gtRole:  "gastown/witness",
			refused: true,
		},
		{
			name:    "bare witness role is refused",
			gtRole:  "witness",
			refused: true,
		},
		{
			name:    "witness identity from BD_ACTOR alone is refused",
			bdActor: "gastown/witness",
			refused: true,
		},
		{
			name:     "witness with explicit override proceeds",
			gtRole:   "gastown/witness",
			override: true,
			refused:  false,
		},
		{
			name:    "mayor identity is allowed",
			gtRole:  "mayor",
			bdActor: "mayor",
			refused: false,
		},
		{
			name:    "human (no agent identity) is allowed",
			refused: false,
		},
		{
			name:    "deacon identity is allowed",
			gtRole:  "deacon",
			refused: false,
		},
		{
			name:    "refinery identity is allowed",
			gtRole:  "gastown/refinery",
			refused: false,
		},
		{
			// GT_ROLE is authoritative: a witness shell that spawned polecats
			// can carry stale GT_POLECAT/BD_ACTOR values, and the witness must
			// still be caught.
			name:    "GT_ROLE witness wins over a polecat BD_ACTOR",
			gtRole:  "gastown/witness",
			bdActor: "gastown/polecats/toast",
			refused: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvGTRole, tt.gtRole)
			t.Setenv("BD_ACTOR", tt.bdActor)

			err := checkRestartFirstNukePolicy("gt polecat nuke", tt.override)

			if !tt.refused {
				if err != nil {
					t.Fatalf("expected nuke to be permitted, got refusal: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("expected witness identity to be refused, got nil")
			}
			msg := err.Error()
			// The refusal has to name the policy and point at the alternative,
			// or it just reads as an arbitrary failure to route around.
			for _, want := range []string{"witness", "restart-first", "gt session restart"} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal message missing %q:\n%s", want, msg)
				}
			}
			if strings.Contains(msg, "--force") {
				t.Errorf("refusal must not suggest --force as the way through:\n%s", msg)
			}
		})
	}
}

// The refusal must name the command that was actually refused, so
// `gt polecat stale --cleanup` doesn't send the reader to the wrong override.
func TestCheckRestartFirstNukePolicyNamesCommand(t *testing.T) {
	t.Setenv(EnvGTRole, "gastown/witness")
	t.Setenv("BD_ACTOR", "")

	err := checkRestartFirstNukePolicy("gt polecat stale --cleanup", false)
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "gt polecat stale --cleanup") {
		t.Errorf("refusal should name the invoking command:\n%s", err)
	}
	if !strings.Contains(err.Error(), "--"+restartFirstOverrideFlag) {
		t.Errorf("refusal should name the override flag:\n%s", err)
	}
}

// End-to-end through the command entry point: the guard must fire before
// anything else runs, including --dry-run, and must not fire for the Mayor.
func TestRunPolecatNukeEnforcesRestartFirstPolicy(t *testing.T) {
	// Nonexistent rig: if the policy guard does NOT fire, target resolution
	// fails instead, which is exactly how we tell the two paths apart.
	const bogus = "no-such-rig-gt-y20/no-such-polecat"

	t.Run("witness is refused before any work happens", func(t *testing.T) {
		for _, dryRun := range []bool{false, true} {
			t.Setenv(EnvGTRole, "gastown/witness")
			t.Setenv("BD_ACTOR", "")
			restore := swapNukeFlags(t, dryRun, false)
			defer restore()

			err := runPolecatNuke(polecatNukeCmd, []string{bogus})
			if err == nil {
				t.Fatalf("dry-run=%v: expected refusal", dryRun)
			}
			if !strings.Contains(err.Error(), "may not nuke polecats") {
				t.Errorf("dry-run=%v: expected policy refusal, got: %v", dryRun, err)
			}
		}
	})

	t.Run("mayor reaches the normal path", func(t *testing.T) {
		t.Setenv(EnvGTRole, "mayor")
		t.Setenv("BD_ACTOR", "mayor")
		restore := swapNukeFlags(t, false, false)
		defer restore()

		err := runPolecatNuke(polecatNukeCmd, []string{bogus})
		// The Mayor gets past the policy gate and fails on the bogus rig, which
		// is the point: the gate is identity-scoped, not a blanket block.
		if err != nil && strings.Contains(err.Error(), "may not nuke polecats") {
			t.Fatalf("mayor identity was refused by the restart-first policy: %v", err)
		}
	})

	t.Run("witness override reaches the normal path", func(t *testing.T) {
		t.Setenv(EnvGTRole, "gastown/witness")
		t.Setenv("BD_ACTOR", "")
		restore := swapNukeFlags(t, false, true)
		defer restore()

		err := runPolecatNuke(polecatNukeCmd, []string{bogus})
		if err != nil && strings.Contains(err.Error(), "may not nuke polecats") {
			t.Fatalf("--%s did not let the call through: %v", restartFirstOverrideFlag, err)
		}
	})
}

// swapNukeFlags sets the package-level nuke flags and returns a restore func.
// These are cobra-bound globals, so tests have to put them back.
func swapNukeFlags(t *testing.T, dryRun, override bool) func() {
	t.Helper()
	prevDryRun, prevOverride := polecatNukeDryRun, polecatNukeOverrideRestartFirst
	polecatNukeDryRun, polecatNukeOverrideRestartFirst = dryRun, override
	return func() {
		polecatNukeDryRun, polecatNukeOverrideRestartFirst = prevDryRun, prevOverride
	}
}

func TestNukeCallerIdentity(t *testing.T) {
	tests := []struct {
		name      string
		gtRole    string
		bdActor   string
		wantRole  Role
		wantActor string
	}{
		{"GT_ROLE preferred", "gastown/witness", "mayor", RoleWitness, "gastown/witness"},
		{"BD_ACTOR fallback", "", "gastown/witness", RoleWitness, "gastown/witness"},
		{"whitespace-only GT_ROLE falls through", "   ", "mayor", RoleMayor, "mayor"},
		{"no identity", "", "", "", ""},
		{"polecat identity", "gastown/polecats/shiny", "", RolePolecat, "gastown/polecats/shiny"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvGTRole, tt.gtRole)
			t.Setenv("BD_ACTOR", tt.bdActor)

			role, actor := nukeCallerIdentity()
			if role != tt.wantRole {
				t.Errorf("role = %q, want %q", role, tt.wantRole)
			}
			if actor != tt.wantActor {
				t.Errorf("actor = %q, want %q", actor, tt.wantActor)
			}
		})
	}
}

// Every verdict must resolve to an action the witness is actually permitted to
// take. If a new verdict ever falls through to the default, it must land on
// restart — never on nuking.
func TestWitnessActionFor(t *testing.T) {
	tests := map[string]string{
		"SAFE_TO_NUKE":    "restart",
		"NEEDS_RECOVERY":  "escalate",
		"NEEDS_MQ_SUBMIT": "escalate",
		"PENDING_MR":      "leave-alone",
		"":                "restart",
		"SOME_NEW_STATE":  "restart",
	}

	for verdict, want := range tests {
		if got := witnessActionFor(verdict); got != want {
			t.Errorf("witnessActionFor(%q) = %q, want %q", verdict, got, want)
		}
		if strings.Contains(witnessActionFor(verdict), "nuke") {
			t.Errorf("witnessActionFor(%q) offered nuking to a witness", verdict)
		}
	}
}

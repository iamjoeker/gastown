package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/deps"
)

func writeFakeClaudeVersion(t *testing.T, version string) {
	t.Helper()
	fakeDir := t.TempDir()
	writeFakeClaude(t, fakeDir,
		fmt.Sprintf("#!/bin/sh\necho '%s (Claude Code)'\n", version),
		fmt.Sprintf("@echo off\r\necho %s (Claude Code)\r\n", version),
	)
	t.Setenv("PATH", fakeDir)
}

func writeClaudeJSON(t *testing.T, home string, state claudeOnboardingState) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeOnboardingVersionCheck_Metadata(t *testing.T) {
	check := NewClaudeOnboardingVersionCheck()

	if check.Name() != "claude-onboarding-version" {
		t.Errorf("Name() = %q, want %q", check.Name(), "claude-onboarding-version")
	}
	if check.Category() != CategoryInfrastructure {
		t.Errorf("Category() = %q, want %q", check.Category(), CategoryInfrastructure)
	}
	if check.CanFix() {
		t.Error("CanFix() should return false (detection only)")
	}
}

func TestClaudeOnboardingVersionCheck_Matches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFakeClaudeVersion(t, deps.RecommendedClaudeCodeVersion)
	writeClaudeJSON(t, home, claudeOnboardingState{
		LastOnboardingVersion: deps.RecommendedClaudeCodeVersion,
		LastReleaseNotesSeen:  deps.RecommendedClaudeCodeVersion,
	})

	check := NewClaudeOnboardingVersionCheck()
	result := check.Run(&CheckContext{TownRoot: t.TempDir()})

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when versions match, got %v: %s", result.Status, result.Message)
	}
}

func TestClaudeOnboardingVersionCheck_Diverged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFakeClaudeVersion(t, deps.RecommendedClaudeCodeVersion)
	writeClaudeJSON(t, home, claudeOnboardingState{
		LastOnboardingVersion: "1.0.62",
		LastReleaseNotesSeen:  "1.0.62",
	})

	check := NewClaudeOnboardingVersionCheck()
	result := check.Run(&CheckContext{TownRoot: t.TempDir()})

	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning when onboarding state is stale, got %v: %s", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "1.0.62") {
		t.Errorf("expected stale version in message, got %q", result.Message)
	}
	if result.FixHint == "" {
		t.Error("expected a fix hint")
	}
}

func TestClaudeOnboardingVersionCheck_NoClaudeJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFakeClaudeVersion(t, deps.RecommendedClaudeCodeVersion)
	// No ~/.claude.json written.

	check := NewClaudeOnboardingVersionCheck()
	result := check.Run(&CheckContext{TownRoot: t.TempDir()})

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when ~/.claude.json is absent, got %v: %s", result.Status, result.Message)
	}
}

func TestClaudeOnboardingVersionCheck_ClaudeNotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)
	writeClaudeJSON(t, home, claudeOnboardingState{LastOnboardingVersion: "1.0.62"})

	check := NewClaudeOnboardingVersionCheck()
	result := check.Run(&CheckContext{TownRoot: t.TempDir()})

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK when claude is not installed, got %v: %s", result.Status, result.Message)
	}
}

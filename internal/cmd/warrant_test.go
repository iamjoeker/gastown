package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

func setupWarrantTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

// =============================================================================
// Warrant Tests
// =============================================================================

// TestWarrantFile_NewWarrant verifies that filing a new warrant creates the file.
func TestWarrantFile_NewWarrant(t *testing.T) {
	tmpDir := t.TempDir()
	warrantDir := filepath.Join(tmpDir, "warrants")

	// Create warrant manually (simulating the function)
	if err := os.MkdirAll(warrantDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	target := "gastown/polecats/alpha"
	reason := "Zombie detected: no session, idle >10m"

	warrant := Warrant{
		ID:       "warrant-test-123",
		Target:   target,
		Reason:   reason,
		FiledBy:  "test-agent",
		FiledAt:  time.Now(),
		Executed: false,
	}

	data, err := json.MarshalIndent(warrant, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}

	warrantPath := filepath.Join(warrantDir, "gastown_polecats_alpha.warrant.json")
	if err := os.WriteFile(warrantPath, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Verify file exists and can be read back
	readData, err := os.ReadFile(warrantPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var readWarrant Warrant
	if err := json.Unmarshal(readData, &readWarrant); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if readWarrant.Target != target {
		t.Errorf("Target = %q, want %q", readWarrant.Target, target)
	}
	if readWarrant.Reason != reason {
		t.Errorf("Reason = %q, want %q", readWarrant.Reason, reason)
	}
	if readWarrant.Executed {
		t.Error("Executed = true, want false")
	}
}

// TestWarrantFile_DuplicateWarrant verifies that filing a duplicate warrant
// is handled gracefully (doesn't overwrite).
func TestWarrantFile_DuplicateWarrant(t *testing.T) {
	tmpDir := t.TempDir()
	warrantDir := filepath.Join(tmpDir, "warrants")

	if err := os.MkdirAll(warrantDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	target := "gastown/polecats/alpha"
	originalReason := "First reason"

	// Create first warrant
	warrant := Warrant{
		ID:       "warrant-first",
		Target:   target,
		Reason:   originalReason,
		FiledBy:  "test-agent",
		FiledAt:  time.Now(),
		Executed: false,
	}

	warrantPath := filepath.Join(warrantDir, "gastown_polecats_alpha.warrant.json")
	data, _ := json.MarshalIndent(warrant, "", "  ")
	if err := os.WriteFile(warrantPath, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Try to detect duplicate (simulating the check in runWarrantFile)
	if _, err := os.Stat(warrantPath); err == nil {
		// File exists - read it
		existingData, _ := os.ReadFile(warrantPath)
		var existing Warrant
		if json.Unmarshal(existingData, &existing) == nil && !existing.Executed {
			// Duplicate detected - this is the expected behavior
			if existing.Reason != originalReason {
				t.Errorf("Existing warrant reason = %q, want %q", existing.Reason, originalReason)
			}
			return // Test passes - duplicate was detected
		}
	}

	t.Error("Expected duplicate warrant to be detected")
}

// TestWarrantExecute_MarksExecuted verifies that executing a warrant marks it as executed.
func TestWarrantExecute_MarksExecuted(t *testing.T) {
	tmpDir := t.TempDir()
	warrantDir := filepath.Join(tmpDir, "warrants")

	if err := os.MkdirAll(warrantDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	target := "gastown/polecats/alpha"

	// Create pending warrant
	warrant := Warrant{
		ID:       "warrant-pending",
		Target:   target,
		Reason:   "Test execution",
		FiledBy:  "test-agent",
		FiledAt:  time.Now(),
		Executed: false,
	}

	warrantPath := filepath.Join(warrantDir, "gastown_polecats_alpha.warrant.json")
	data, _ := json.MarshalIndent(warrant, "", "  ")
	if err := os.WriteFile(warrantPath, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Simulate execution (mark as executed)
	now := time.Now()
	warrant.Executed = true
	warrant.ExecutedAt = &now

	data, _ = json.MarshalIndent(warrant, "", "  ")
	if err := os.WriteFile(warrantPath, data, 0644); err != nil {
		t.Fatalf("WriteFile() after execution error = %v", err)
	}

	// Verify warrant is marked as executed
	readData, _ := os.ReadFile(warrantPath)
	var readWarrant Warrant
	if err := json.Unmarshal(readData, &readWarrant); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if !readWarrant.Executed {
		t.Error("Executed = false, want true")
	}
	if readWarrant.ExecutedAt == nil {
		t.Error("ExecutedAt = nil, want non-nil")
	}
}

func TestTargetToSessionName(t *testing.T) {
	setupWarrantTestRegistry(t)
	tests := []struct {
		target  string
		wantErr bool
		want    string
	}{
		{"gastown/polecats/alpha", false, "gt-alpha"},
		{"beads/polecats/charlie", false, "bd-charlie"},
		{"deacon/dogs", true, ""},
		{"deacon/dogs/alpha", false, "hq-dog-alpha"},
		{"gastown/crew/bob", false, "gt-crew-bob"},
		{"gastown/witness", false, "gt-witness"},
		{"gastown/refinery", false, "gt-refinery"},
		{"beads/witness", false, "bd-witness"},
		{"beads/refinery", false, "bd-refinery"},
		// Unrecognised shapes must error, never resolve to a fabricated name.
		{"unknownrig/something/else", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			got, err := targetToSessionName(tt.target)
			if (err != nil) != tt.wantErr {
				t.Errorf("targetToSessionName(%q) error = %v, wantErr %v", tt.target, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("targetToSessionName(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

// TestTargetToSessionName_RejectsUnresolvableTargets is the regression test for
// the defect chain: targetToSessionName used to fabricate a plausible session
// name for any unrecognised target and return a nil error. Because tmux never
// has a session by a fabricated name, the caller read "not found", reported it
// as "already dead", and marked the warrant executed — while the real agent
// kept running. Every one of these inputs must produce an error.
func TestTargetToSessionName_RejectsUnresolvableTargets(t *testing.T) {
	setupWarrantTestRegistry(t)

	targets := []string{
		"gt-abc123",                 // bead id — the reported case
		"hq-6h21",                   // bead id, town prefix
		"institute",                 // bare agent name, no path
		"gastown/polecats",          // truncated: no polecat name
		"gastown/polecats/a/b",      // over-long path
		"gastown/mayor",             // unsupported role
		"deacon",                    // town singleton, no shape for it
		"gastown/polecats/",         // empty name component
		"/polecats/alpha",           // empty rig component
		"gastown//alpha",            // empty middle component
		"",                          // empty target
		"unknownrig/something/else", // three parts, unknown middle
	}

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			got, err := targetToSessionName(target)
			if err == nil {
				t.Errorf("targetToSessionName(%q) = %q with nil error; want an error — a fabricated name makes an unexecuted warrant look executed", target, got)
			}
			if got != "" {
				t.Errorf("targetToSessionName(%q) returned session name %q alongside error; want empty string", target, got)
			}
		})
	}
}

// TestExecuteOneWarrant_UnresolvableTargetStaysPending verifies the composed
// behaviour that the defect report describes: a warrant whose target cannot be
// resolved must NOT be marked executed. Before the fix it was, so the warrant
// disappeared from the pending list having terminated nothing.
//
// No tmux session is involved — resolution fails before any session lookup.
func TestExecuteOneWarrant_UnresolvableTargetStaysPending(t *testing.T) {
	setupWarrantTestRegistry(t)

	warrantDir := t.TempDir()
	w := Warrant{
		ID:       "warrant-bead-id-target",
		Target:   "gt-abc123", // bead id, not an agent path
		Reason:   "Zombie: no session, idle >10m",
		FiledBy:  "test",
		FiledAt:  time.Now().Add(-5 * time.Minute),
		Executed: false,
	}
	warrantPath := filepath.Join(warrantDir, "gt-abc123.warrant.json")
	data, _ := json.MarshalIndent(w, "", "  ")
	if err := os.WriteFile(warrantPath, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// A real Tmux is passed deliberately: if the resolution guard were ever
	// removed, the fabricated name would look up cleanly as "not found" and the
	// assertions below would catch the warrant being marked executed.
	err := executeOneWarrant(&w, warrantPath, tmux.NewTmux())
	if err == nil {
		t.Fatal("executeOneWarrant() error = nil, want an error for an unresolvable target")
	}
	if w.Executed {
		t.Error("Executed = true after a failed resolution, want false — the warrant must stay pending")
	}
	if w.ExecutedAt != nil {
		t.Error("ExecutedAt is set after a failed resolution, want nil")
	}
	if w.Outcome != "" {
		t.Errorf("Outcome = %q after a failed resolution, want empty", w.Outcome)
	}

	// The on-disk warrant must be untouched, so the next triage cycle sees it.
	onDisk := Warrant{}
	readData, readErr := os.ReadFile(warrantPath)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if err := json.Unmarshal(readData, &onDisk); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if onDisk.Executed {
		t.Error("on-disk Executed = true, want false — a warrant that resolved to nothing was recorded as carried out")
	}
}

// TestWarrantOutcomeLabel verifies that each outcome renders distinctly, and
// that a warrant with no recorded outcome does not read as a termination.
func TestWarrantOutcomeLabel(t *testing.T) {
	if got := warrantOutcomeLabel(WarrantTerminated); !strings.Contains(got, "terminated") {
		t.Errorf("warrantOutcomeLabel(terminated) = %q, want it to mention termination", got)
	}
	absent := warrantOutcomeLabel(WarrantTargetAbsent)
	if !strings.Contains(absent, "nothing terminated") {
		t.Errorf("warrantOutcomeLabel(target-absent) = %q, want it to say nothing was terminated", absent)
	}
	legacy := warrantOutcomeLabel("")
	if strings.Contains(legacy, "session terminated") {
		t.Errorf("warrantOutcomeLabel(\"\") = %q, want it not to claim a termination", legacy)
	}
}

// TestWarrantFilePath verifies warrant file path generation.
func TestWarrantFilePath(t *testing.T) {
	tests := []struct {
		dir    string
		target string
		want   string
	}{
		{
			dir:    filepath.Join("/tmp", "warrants"),
			target: "gastown/polecats/alpha",
			want:   filepath.Join("/tmp", "warrants", "gastown_polecats_alpha.warrant.json"),
		},
		{
			dir:    filepath.Join("/home", "user", "gt", "warrants"),
			target: "deacon/dogs/bravo",
			want:   filepath.Join("/home", "user", "gt", "warrants", "deacon_dogs_bravo.warrant.json"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			got := warrantFilePath(tt.dir, tt.target)
			if got != tt.want {
				t.Errorf("warrantFilePath(%q, %q) = %q, want %q", tt.dir, tt.target, got, tt.want)
			}
		})
	}
}

package formula

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// knownFormula is an embedded formula every gt build ships; the drift tests
// need a name that exists in tier 3 so a disk copy can shadow it.
const knownFormula = "mol-polecat-work"

func knownFormulaFile() string { return knownFormula + ".formula.toml" }

// writeTownFormula writes a tier-2 copy with the given content and returns its path.
func writeTownFormula(t *testing.T, townRoot, filename, content string) string {
	t.Helper()
	dir := filepath.Join(townRoot, ".beads", "formulas")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// writeInstalledRecord writes .installed.json for the town tier.
func writeInstalledRecord(t *testing.T, townRoot string, entries map[string]string) {
	t.Helper()
	dir := filepath.Join(townRoot, ".beads", "formulas")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	data, err := json.Marshal(InstalledRecord{Formulas: entries})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".installed.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func embeddedHashOf(t *testing.T, filename string) string {
	t.Helper()
	content, err := GetEmbeddedFormulaContent(filename)
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent(%s): %v", filename, err)
	}
	return computeHash(content)
}

// TestResolveFormula_EmbeddedTier verifies the no-disk-copy case reports the
// embedded tier and no drift.
func TestResolveFormula_EmbeddedTier(t *testing.T) {
	r, err := ResolveFormula(knownFormula, t.TempDir(), "")
	if err != nil {
		t.Fatalf("ResolveFormula: %v", err)
	}
	if r.Tier != TierEmbedded {
		t.Errorf("Tier = %q, want %q", r.Tier, TierEmbedded)
	}
	if r.Path != "" {
		t.Errorf("Path = %q, want empty for the embedded tier", r.Path)
	}
	if r.Drift != DriftNone {
		t.Errorf("Drift = %q, want none", r.Drift)
	}
	if r.ShadowsNewerDefault() {
		t.Error("the embedded copy cannot shadow itself")
	}
	if r.DriftNotice() != "" {
		t.Errorf("DriftNotice = %q, want empty", r.DriftNotice())
	}
}

// TestResolveFormula_DriftClassification walks every relation a disk copy can
// have to the shipped default. The pinned case is the one gt-0wm7 is about:
// locally edited AND the default moved afterwards, so gt skips it forever.
func TestResolveFormula_DriftClassification(t *testing.T) {
	filename := knownFormulaFile()
	embeddedHash := embeddedHashOf(t, filename)
	embeddedContent, err := GetEmbeddedFormulaContent(filename)
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent: %v", err)
	}
	localContent := string(embeddedContent) + "\n# local edit\n"
	localHash := computeHash([]byte(localContent))

	tests := []struct {
		name        string
		content     string
		installed   map[string]string
		wantDrift   DriftKind
		wantShadows bool
		wantAutoFix bool
	}{
		{
			name:      "identical to embedded is not drift",
			content:   string(embeddedContent),
			installed: map[string]string{filename: embeddedHash},
			wantDrift: DriftNone,
		},
		{
			name:        "unmodified older install is outdated",
			content:     localContent,
			installed:   map[string]string{filename: localHash},
			wantDrift:   DriftOutdated,
			wantShadows: true,
			wantAutoFix: true,
		},
		{
			name:        "no install record is untracked",
			content:     localContent,
			installed:   map[string]string{},
			wantDrift:   DriftUntracked,
			wantShadows: true,
			wantAutoFix: true,
		},
		{
			name:      "local edit with an unmoved default is customized, not drift",
			content:   localContent,
			installed: map[string]string{filename: embeddedHash},
			wantDrift: DriftCustomized,
		},
		{
			name:        "local edit plus a moved default is pinned",
			content:     localContent,
			installed:   map[string]string{filename: "0000000000000000000000000000000000000000000000000000000000000000"},
			wantDrift:   DriftPinned,
			wantShadows: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()
			path := writeTownFormula(t, townRoot, filename, tc.content)
			writeInstalledRecord(t, townRoot, tc.installed)

			r, err := ResolveFormula(knownFormula, townRoot, "")
			if err != nil {
				t.Fatalf("ResolveFormula: %v", err)
			}
			if r.Tier != TierTown {
				t.Errorf("Tier = %q, want %q", r.Tier, TierTown)
			}
			if r.Path != path {
				t.Errorf("Path = %q, want %q", r.Path, path)
			}
			if r.Drift != tc.wantDrift {
				t.Errorf("Drift = %q, want %q", r.Drift, tc.wantDrift)
			}
			if got := r.ShadowsNewerDefault(); got != tc.wantShadows {
				t.Errorf("ShadowsNewerDefault() = %v, want %v", got, tc.wantShadows)
			}
			if got := r.AutoFixable(); got != tc.wantAutoFix {
				t.Errorf("AutoFixable() = %v, want %v", got, tc.wantAutoFix)
			}
			notice := r.DriftNotice()
			if tc.wantShadows && notice == "" {
				t.Error("a shadowing copy must produce a drift notice")
			}
			if !tc.wantShadows && notice != "" {
				t.Errorf("DriftNotice = %q, want empty", notice)
			}
			if tc.wantDrift == DriftPinned && !strings.Contains(notice, "gt formula drift "+knownFormula) {
				t.Errorf("pinned notice must point at the reconcile command, got:\n%s", notice)
			}
		})
	}
}

// TestResolveFormula_RigTierWins verifies tier 1 beats tier 2, and that a rig
// copy is never reported as auto-fixable — UpdateFormulas only manages the town.
func TestResolveFormula_RigTierWins(t *testing.T) {
	townRoot := t.TempDir()
	filename := knownFormulaFile()

	writeTownFormula(t, townRoot, filename, "# town copy\n")

	rigDir := filepath.Join(townRoot, "somerig", ".beads", "formulas")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rigPath := filepath.Join(rigDir, filename)
	if err := os.WriteFile(rigPath, []byte("# rig copy\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r, err := ResolveFormula(knownFormula, townRoot, "somerig")
	if err != nil {
		t.Fatalf("ResolveFormula: %v", err)
	}
	if r.Tier != TierRig {
		t.Errorf("Tier = %q, want %q", r.Tier, TierRig)
	}
	if string(r.Content) != "# rig copy\n" {
		t.Errorf("Content = %q, want the rig copy", r.Content)
	}
	if !r.ShadowsNewerDefault() {
		t.Error("a rig copy that differs from the shipped default shadows it")
	}
	if r.AutoFixable() {
		t.Error("nothing auto-updates a rig-tier copy")
	}
	if notice := r.DriftNotice(); !strings.Contains(notice, "Nothing updates a rig-tier copy") {
		t.Errorf("rig notice must not promise an auto-fix, got:\n%s", notice)
	}
}

// TestResolveFormula_LocalOnlyFormulaIsNotDrift verifies a formula this build
// ships no default for is left alone rather than reported as drifted.
func TestResolveFormula_LocalOnlyFormulaIsNotDrift(t *testing.T) {
	townRoot := t.TempDir()
	writeTownFormula(t, townRoot, "totally-local.formula.toml", "# local only\n")

	r, err := ResolveFormula("totally-local", townRoot, "")
	if err != nil {
		t.Fatalf("ResolveFormula: %v", err)
	}
	if r.EmbeddedHash != "" {
		t.Error("no embedded default should exist for a local-only formula")
	}
	if r.Drift != DriftNone {
		t.Errorf("Drift = %q, want none", r.Drift)
	}
	if r.ShadowsNewerDefault() {
		t.Error("a local-only formula shadows nothing")
	}
}

// TestResolveFormulaContent_AgreesWithResolveFormula guards the wrapper: the
// two entry points must never disagree about which copy executes.
func TestResolveFormulaContent_AgreesWithResolveFormula(t *testing.T) {
	townRoot := t.TempDir()
	filename := knownFormulaFile()
	writeTownFormula(t, townRoot, filename, "# town copy\n")

	content, err := ResolveFormulaContent(knownFormula, townRoot, "")
	if err != nil {
		t.Fatalf("ResolveFormulaContent: %v", err)
	}
	r, err := ResolveFormula(knownFormula, townRoot, "")
	if err != nil {
		t.Fatalf("ResolveFormula: %v", err)
	}
	if string(content) != string(r.Content) {
		t.Errorf("ResolveFormulaContent = %q, ResolveFormula.Content = %q", content, r.Content)
	}

	// And with a suffixed name, which callers do pass.
	suffixed, err := ResolveFormulaContent(filename, townRoot, "")
	if err != nil {
		t.Fatalf("ResolveFormulaContent(suffixed): %v", err)
	}
	if string(suffixed) != string(content) {
		t.Errorf("suffixed name resolved differently: %q vs %q", suffixed, content)
	}
}

// TestExecutingDrift lists only the formulas that shadow a newer default.
func TestExecutingDrift(t *testing.T) {
	townRoot := t.TempDir()
	filename := knownFormulaFile()

	// One pinned formula...
	writeTownFormula(t, townRoot, filename, "# edited\n")
	// ...and one deliberate customization with an unmoved default, which must
	// not be listed.
	otherFile := "mol-boot-triage.formula.toml"
	otherEmbedded := embeddedHashOf(t, otherFile)
	writeTownFormula(t, townRoot, otherFile, "# customized\n")

	writeInstalledRecord(t, townRoot, map[string]string{
		filename:  "0000000000000000000000000000000000000000000000000000000000000000",
		otherFile: otherEmbedded,
	})

	drifted, err := ExecutingDrift(townRoot, "")
	if err != nil {
		t.Fatalf("ExecutingDrift: %v", err)
	}
	if len(drifted) != 1 {
		names := make([]string, 0, len(drifted))
		for _, d := range drifted {
			names = append(names, d.Name+"="+string(d.Drift))
		}
		t.Fatalf("ExecutingDrift returned %v, want exactly the pinned formula", names)
	}
	if drifted[0].Name != knownFormula {
		t.Errorf("drifted[0].Name = %q, want %q", drifted[0].Name, knownFormula)
	}
	if drifted[0].Drift != DriftPinned {
		t.Errorf("drifted[0].Drift = %q, want %q", drifted[0].Drift, DriftPinned)
	}
}

// TestMarkReconciled clears a pinned formula without touching its contents —
// the missing half of "reconcile by hand" (gt-0wm7 remedy step 2).
func TestMarkReconciled(t *testing.T) {
	townRoot := t.TempDir()
	filename := knownFormulaFile()
	const local = "# reconciled by hand\n"
	path := writeTownFormula(t, townRoot, filename, local)
	writeInstalledRecord(t, townRoot, map[string]string{
		filename: "0000000000000000000000000000000000000000000000000000000000000000",
	})

	before, err := ResolveFormula(knownFormula, townRoot, "")
	if err != nil {
		t.Fatalf("ResolveFormula: %v", err)
	}
	if before.Drift != DriftPinned {
		t.Fatalf("precondition: Drift = %q, want pinned", before.Drift)
	}

	if err := MarkReconciled(before); err != nil {
		t.Fatalf("MarkReconciled: %v", err)
	}

	after, err := ResolveFormula(knownFormula, townRoot, "")
	if err != nil {
		t.Fatalf("ResolveFormula after: %v", err)
	}
	if after.Drift != DriftCustomized {
		t.Errorf("Drift = %q, want customized after reconciling", after.Drift)
	}
	if after.ShadowsNewerDefault() {
		t.Error("a reconciled formula must stop reporting as drifted")
	}
	got, err := os.ReadFile(path) //nolint:gosec // G304: test temp path
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != local {
		t.Errorf("MarkReconciled changed the file contents: %q", got)
	}
}

// TestAcceptEmbedded replaces the disk copy and clears the drift.
func TestAcceptEmbedded(t *testing.T) {
	townRoot := t.TempDir()
	filename := knownFormulaFile()
	path := writeTownFormula(t, townRoot, filename, "# stale local edit\n")
	writeInstalledRecord(t, townRoot, map[string]string{
		filename: "0000000000000000000000000000000000000000000000000000000000000000",
	})

	before, err := ResolveFormula(knownFormula, townRoot, "")
	if err != nil {
		t.Fatalf("ResolveFormula: %v", err)
	}
	if err := AcceptEmbedded(before); err != nil {
		t.Fatalf("AcceptEmbedded: %v", err)
	}

	after, err := ResolveFormula(knownFormula, townRoot, "")
	if err != nil {
		t.Fatalf("ResolveFormula after: %v", err)
	}
	if after.Drift != DriftNone {
		t.Errorf("Drift = %q, want none after accepting the embedded default", after.Drift)
	}
	embedded, err := GetEmbeddedFormulaContent(filename)
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent: %v", err)
	}
	got, err := os.ReadFile(path) //nolint:gosec // G304: test temp path
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(embedded) {
		t.Error("AcceptEmbedded did not write the embedded default to disk")
	}
}

// TestAcceptEmbedded_RejectsEmbeddedTier guards against writing to an empty path.
func TestAcceptEmbedded_RejectsEmbeddedTier(t *testing.T) {
	r, err := ResolveFormula(knownFormula, t.TempDir(), "")
	if err != nil {
		t.Fatalf("ResolveFormula: %v", err)
	}
	if err := AcceptEmbedded(r); err == nil {
		t.Error("AcceptEmbedded on an embedded-tier resolution must fail")
	}
	if err := MarkReconciled(r); err == nil {
		t.Error("MarkReconciled on an embedded-tier resolution must fail")
	}
}

// TestDriftNotice_NamesAnActionThatChangesSomething guards the circular
// remediation defect from gt-nrzk: the pinned notice used to end with
// "Reconcile: gt formula drift <name>" — the very command that printed it. An
// operator who followed the advice got the same warning back, and since
// "gt doctor --fix cannot repair this one" appears two lines above, the message
// named no action at all.
//
// The assertion that matters is not "the notice mentions gt formula drift" —
// the old broken text passed that. It is that the notice names at least one
// flag that CHANGES state, and that stripping the bare invocation still leaves
// runnable instructions behind.
func TestDriftNotice_NamesAnActionThatChangesSomething(t *testing.T) {
	filename := knownFormulaFile()

	for _, tc := range []struct {
		name      string
		installed map[string]string
		wantDrift DriftKind
	}{
		{
			name:      "pinned",
			installed: map[string]string{filename: strings.Repeat("0", 64)},
			wantDrift: DriftPinned,
		},
		{
			// A town-tier untracked copy IS auto-fixable, so force the
			// non-auto-fixable arm with a rig-tier copy instead.
			name:      "untracked at a tier nothing updates",
			installed: nil,
			wantDrift: DriftUntracked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			townRoot := t.TempDir()
			var path string
			if tc.wantDrift == DriftUntracked {
				dir := filepath.Join(townRoot, "somerig", ".beads", "formulas")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatalf("MkdirAll: %v", err)
				}
				path = filepath.Join(dir, filename)
				if err := os.WriteFile(path, []byte("# local edit\n"), 0644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			} else {
				path = writeTownFormula(t, townRoot, filename, "# local edit\n")
				writeInstalledRecord(t, townRoot, tc.installed)
			}

			rig := ""
			if tc.wantDrift == DriftUntracked {
				rig = "somerig"
			}
			r, err := ResolveFormula(knownFormula, townRoot, rig)
			if err != nil {
				t.Fatalf("ResolveFormula: %v", err)
			}
			if r.Drift != tc.wantDrift {
				t.Fatalf("Drift = %q, want %q (fixture no longer produces the case under test)", r.Drift, tc.wantDrift)
			}
			if r.AutoFixable() {
				t.Fatalf("fixture is auto-fixable; this test covers the arm where no command repairs it")
			}

			notice := r.DriftNotice()

			// Each of these does something the bare command does not.
			for _, flag := range []string{"--embedded", "--mark-reconciled", "--accept-embedded"} {
				if !strings.Contains(notice, flag) {
					t.Errorf("notice must name %s; got:\n%s", flag, notice)
				}
			}

			// The disk path has to appear or step 2's diff is unrunnable.
			if !strings.Contains(notice, r.Path) {
				t.Errorf("notice must name the disk copy %q so the diff is runnable; got:\n%s", r.Path, notice)
			}

			// The circularity check itself: delete every bare "gt formula
			// drift <name>" that carries no flag, and an actionable notice
			// still has instructions left. The pre-fix text did not.
			bare := "gt formula drift " + knownFormula
			var actionable []string
			for _, line := range strings.Split(notice, "\n") {
				if !strings.Contains(line, bare) {
					continue
				}
				if strings.Contains(line, bare+" --") {
					actionable = append(actionable, line)
				}
			}
			if len(actionable) == 0 {
				t.Errorf("every 'gt formula drift %s' in the notice is the bare command that printed it — circular advice; got:\n%s", knownFormula, notice)
			}
		})
	}
}

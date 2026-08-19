package formula

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDeclaredWispTypeEmbedded reads the values through the real resolver, so a
// formula losing its [vars.wisp_type] block fails here. The declarations existed
// long before anything read them (gt-fqd5); nothing else notices if they go.
func TestDeclaredWispTypeEmbedded(t *testing.T) {
	cases := map[string]string{
		"mol-witness-patrol":     "patrol",
		"mol-deacon-patrol":      "patrol",
		"mol-refinery-patrol":    "patrol",
		"mol-pr-feedback-patrol": "patrol",
		"mol-session-gc":         "gc_report",
		// Work formulas declare nothing: bd's vocabulary has seven TTL buckets
		// and none of them means "a polecat implementing a bead". Unclassified
		// is the correct answer, not a gap to fill in.
		"mol-polecat-work": "",
	}
	for name, want := range cases {
		if got := DeclaredWispType(name, "", ""); got != want {
			t.Errorf("DeclaredWispType(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDeclaredWispTypeMissingFormula(t *testing.T) {
	if got := DeclaredWispType("mol-does-not-exist", "", ""); got != "" {
		t.Errorf("DeclaredWispType on a missing formula = %q, want \"\"", got)
	}
}

// TestDeclaredWispTypeTownOverride pins the tier precedence. This is the case
// that decides real behaviour: every town provisions formulas to
// .beads/formulas/, and those disk copies shadow the embedded ones. A change to
// an embedded formula's wisp_type does NOT reach a town that already has a copy.
func TestDeclaredWispTypeTownOverride(t *testing.T) {
	townRoot := t.TempDir()
	dir := filepath.Join(townRoot, ".beads", "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `formula = 'mol-witness-patrol'
version = 1

[vars.wisp_type]
default = "gc_report"
`
	path := filepath.Join(dir, "mol-witness-patrol.formula.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := DeclaredWispType("mol-witness-patrol", townRoot, ""); got != "gc_report" {
		t.Errorf("town-tier copy = %q, want %q (embedded copy shadowed the disk one)", got, "gc_report")
	}

	// And a disk copy that declares nothing yields nothing, rather than falling
	// back to the embedded declaration — which is exactly why the dog spawner
	// carries its own default instead of trusting resolution.
	silent := "formula = 'mol-witness-patrol'\nversion = 1\n"
	if err := os.WriteFile(path, []byte(silent), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DeclaredWispType("mol-witness-patrol", townRoot, ""); got != "" {
		t.Errorf("silent town-tier copy = %q, want \"\"", got)
	}
}

func TestDeclaredWispTypeRigBeatsTown(t *testing.T) {
	townRoot := t.TempDir()
	rig := "gastown"
	for tier, value := range map[string]string{
		filepath.Join(townRoot, ".beads", "formulas"):      "gc_report",
		filepath.Join(townRoot, rig, ".beads", "formulas"): "patrol",
	} {
		if err := os.MkdirAll(tier, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "formula = 'mol-witness-patrol'\nversion = 1\n\n[vars.wisp_type]\ndefault = \"" + value + "\"\n"
		if err := os.WriteFile(filepath.Join(tier, "mol-witness-patrol.formula.toml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if got := DeclaredWispType("mol-witness-patrol", townRoot, rig); got != "patrol" {
		t.Errorf("rig tier = %q, want %q", got, "patrol")
	}
}

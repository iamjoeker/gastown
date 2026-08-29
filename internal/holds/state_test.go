package holds

import (
	"testing"
)

func TestLoad_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()

	reg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load with missing file: %v", err)
	}
	if len(reg.Holds) != 0 {
		t.Errorf("expected no holds, got %d", len(reg.Holds))
	}
}

func TestAddSaveLoad_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()

	reg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	h := reg.Add("session restart", "cost > $50", "cost review pending", "mayor approves", "mayor")
	if h.ID != "hold-1" {
		t.Errorf("expected ID hold-1, got %s", h.ID)
	}

	if err := Save(tmpDir, reg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Holds) != 1 {
		t.Fatalf("expected 1 hold, got %d", len(loaded.Holds))
	}
	got := loaded.Holds[0]
	if got.Scope != "session restart" || got.Reason != "cost review pending" || got.SetBy != "mayor" {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if got.Released {
		t.Error("expected new hold to not be released")
	}
}

func TestActive_ExcludesReleased(t *testing.T) {
	reg := &Registry{}
	reg.Add("scope-a", "", "reason-a", "", "mayor")
	reg.Add("scope-b", "", "reason-b", "", "mayor")

	if err := reg.Release("hold-1", "mayor", "resolved"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	active := reg.Active()
	if len(active) != 1 {
		t.Fatalf("expected 1 active hold, got %d", len(active))
	}
	if active[0].ID != "hold-2" {
		t.Errorf("expected hold-2 active, got %s", active[0].ID)
	}

	all := reg.Holds
	if len(all) != 2 {
		t.Fatalf("expected 2 total holds, got %d", len(all))
	}
}

func TestRelease_UnknownID(t *testing.T) {
	reg := &Registry{}
	if err := reg.Release("hold-999", "mayor", "n/a"); err == nil {
		t.Error("expected error releasing unknown hold")
	}
}

func TestRelease_AlreadyReleased(t *testing.T) {
	reg := &Registry{}
	reg.Add("scope-a", "", "reason-a", "", "mayor")
	if err := reg.Release("hold-1", "mayor", "first"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := reg.Release("hold-1", "mayor", "second"); err == nil {
		t.Error("expected error releasing already-released hold")
	}
}

func TestFind(t *testing.T) {
	reg := &Registry{}
	reg.Add("scope-a", "", "reason-a", "", "mayor")

	if _, ok := reg.Find("hold-1"); !ok {
		t.Error("expected to find hold-1")
	}
	if _, ok := reg.Find("hold-nope"); ok {
		t.Error("expected not to find hold-nope")
	}
}

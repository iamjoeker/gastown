package scratchpad

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeSession builds a scratchpad directory the way Claude Code lays one out
// and backdates every mtime in it.
func makeSession(t *testing.T, root, slug, id string, age time.Duration, contents map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, slug, id)
	if err := os.MkdirAll(filepath.Join(dir, "scratchpad"), 0o700); err != nil {
		t.Fatalf("creating session directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o700); err != nil {
		t.Fatalf("creating tasks directory: %v", err)
	}
	for name, body := range contents {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	when := time.Now().Add(-age)
	// Deepest first, so that touching a file does not bump its parent back to
	// the present after the parent has been set.
	var paths []string
	_ = filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err == nil {
			paths = append(paths, p)
		}
		return nil
	})
	for i := len(paths) - 1; i >= 0; i-- {
		if err := os.Chtimes(paths[i], when, when); err != nil {
			t.Fatalf("backdating %s: %v", paths[i], err)
		}
	}
	return dir
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	slug := "-home-u-src-rig"
	sessionID := "360031bc-da3a-42ab-af49-8f5027879679"
	makeSession(t, root, slug, sessionID, 3*time.Hour, map[string]string{
		"scratchpad/notes.md": "0123456789",
	})
	// A directory under a project that is not a session id: not ours, and the
	// sweep must never see it.
	if err := os.MkdirAll(filepath.Join(root, slug, "not-a-session"), 0o700); err != nil {
		t.Fatalf("creating decoy: %v", err)
	}
	// A file agents wrote directly into the root, outside the convention.
	if err := os.WriteFile(filepath.Join(root, "build.out"), []byte("stray"), 0o600); err != nil {
		t.Fatalf("writing stray file: %v", err)
	}

	res, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("found %d sessions, want 1 (a non-UUID directory must not be treated as a session)", len(res.Sessions))
	}
	s := res.Sessions[0]
	if s.ID != sessionID || s.ProjectSlug != slug {
		t.Errorf("session = %s/%s, want %s/%s", s.ProjectSlug, s.ID, slug, sessionID)
	}
	if s.Bytes != 10 {
		t.Errorf("bytes = %d, want 10", s.Bytes)
	}
	if idle := time.Since(s.LastWrite); idle < 2*time.Hour {
		t.Errorf("last write %v ago, want roughly 3h — the whole subtree should be measured", idle)
	}
	if res.StrayFiles != 1 || res.StrayBytes != 5 {
		t.Errorf("stray = %d files / %d bytes, want 1 / 5", res.StrayFiles, res.StrayBytes)
	}
}

func TestScanMeasuresTheDeepestWrite(t *testing.T) {
	root := t.TempDir()
	dir := makeSession(t, root, "-p", "360031bc-da3a-42ab-af49-8f5027879679", 6*time.Hour, map[string]string{
		"scratchpad/old.txt": "old",
	})
	// A live agent writing one file deep in the tree makes the whole session
	// active, even though the session directory's own mtime stayed old.
	fresh := filepath.Join(dir, "scratchpad", "fresh.txt")
	if err := os.WriteFile(fresh, []byte("now"), 0o600); err != nil {
		t.Fatalf("writing fresh file: %v", err)
	}

	res, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if idle := time.Since(res.Sessions[0].LastWrite); idle > time.Minute {
		t.Fatalf("last write %v ago, want ~0 — a deep fresh write must surface", idle)
	}
}

func TestScanMissingRoot(t *testing.T) {
	if _, err := Scan(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("Scan of a missing root returned no error; the caller cannot tell an empty box from a wrong path")
	}
}

func TestScanTranscripts(t *testing.T) {
	home := t.TempDir()
	id := "360031bc-da3a-42ab-af49-8f5027879679"
	other := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	older := filepath.Join(home, ".claude", "projects", "-p")
	newer := filepath.Join(home, ".claude-accounts", "claude", "projects", "-p")
	for _, d := range []string{older, newer} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}
	writeAged := func(dir, name string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatalf("backdating %s: %v", p, err)
		}
	}
	// The same session recorded under two accounts: the newest write wins,
	// because either one is evidence the session is still talking.
	writeAged(older, id+".jsonl", 10*time.Hour)
	writeAged(newer, id+".jsonl", 1*time.Hour)
	writeAged(newer, other+".jsonl", 5*time.Hour)
	writeAged(newer, "notes.md", 0)

	got := ScanTranscripts(TranscriptRoots(home))
	if len(got) != 2 {
		t.Fatalf("found %d transcripts, want 2 (a non-transcript file must be ignored)", len(got))
	}
	if age := time.Since(got[id]); age > 2*time.Hour {
		t.Errorf("%s recorded as %v old, want the newest of the two accounts (~1h)", id, age)
	}
}

func TestExecuteRemovesAndRevalidates(t *testing.T) {
	root := t.TempDir()
	policy := DefaultPolicy()

	deadDir := makeSession(t, root, "-p", "360031bc-da3a-42ab-af49-8f5027879679", 24*time.Hour, map[string]string{
		"scratchpad/notes.md": "dead",
	})
	revivedDir := makeSession(t, root, "-p", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", 24*time.Hour, map[string]string{
		"scratchpad/notes.md": "revived",
	})

	res, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	decisions := make([]Decision, 0, len(res.Sessions))
	for _, s := range res.Sessions {
		decisions = append(decisions, Decision{Session: s, Verdict: VerdictSweep})
	}

	// The agent owning the second scratchpad comes back to life between
	// classification and deletion.
	if err := os.WriteFile(filepath.Join(revivedDir, "scratchpad", "live.txt"), []byte("back"), 0o600); err != nil {
		t.Fatalf("reviving session: %v", err)
	}

	removals := Execute(decisions, policy, time.Now())
	if len(removals) != 2 {
		t.Fatalf("got %d removals, want 2", len(removals))
	}
	byPath := map[string]Removal{}
	for _, r := range removals {
		byPath[r.Session.Path] = r
	}
	if !byPath[deadDir].Removed {
		t.Errorf("dead scratchpad not removed: %s", byPath[deadDir].Reason)
	}
	if _, err := os.Stat(deadDir); !os.IsNotExist(err) {
		t.Errorf("dead scratchpad still on disk: %v", err)
	}
	if byPath[revivedDir].Removed {
		t.Error("removed a scratchpad that was written to between classification and deletion")
	}
	if _, err := os.Stat(revivedDir); err != nil {
		t.Errorf("revived scratchpad was destroyed: %v", err)
	}
}

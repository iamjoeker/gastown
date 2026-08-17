package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRigRepoPaths covers the source of gt-a7a: `gt rig list --json` published
// no filesystem paths at all, so three deacon plugins read a "repo_path" key
// that never existed and skipped every rig with exit 0.
func TestRigRepoPaths(t *testing.T) {
	// makeClone creates a directory that looks like a git working tree.
	makeClone := func(t *testing.T, path string, gitAsFile bool) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		dotGit := filepath.Join(path, ".git")
		if gitAsFile {
			// A worktree or submodule checkout has .git as a file.
			if err := os.WriteFile(dotGit, []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
				t.Fatalf("write %s: %v", dotGit, err)
			}
			return
		}
		if err := os.MkdirAll(dotGit, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dotGit, err)
		}
	}

	t.Run("both clones present", func(t *testing.T) {
		rigPath := t.TempDir()
		makeClone(t, filepath.Join(rigPath, "mayor", "rig"), false)
		makeClone(t, filepath.Join(rigPath, "refinery", "rig"), false)

		mayor, refinery, repos := rigRepoPaths(rigPath)

		wantMayor := filepath.Join(rigPath, "mayor", "rig")
		wantRefinery := filepath.Join(rigPath, "refinery", "rig")
		if mayor != wantMayor {
			t.Errorf("mayorRepo = %q, want %q", mayor, wantMayor)
		}
		if refinery != wantRefinery {
			t.Errorf("refineryRepo = %q, want %q", refinery, wantRefinery)
		}
		if len(repos) != 2 || repos[0] != wantMayor || repos[1] != wantRefinery {
			t.Errorf("repos = %v, want [%q %q]", repos, wantMayor, wantRefinery)
		}
	})

	t.Run("worktree-style .git file counts as a clone", func(t *testing.T) {
		rigPath := t.TempDir()
		makeClone(t, filepath.Join(rigPath, "refinery", "rig"), true)

		mayor, refinery, repos := rigRepoPaths(rigPath)

		if mayor != "" {
			t.Errorf("mayorRepo = %q, want empty", mayor)
		}
		if refinery == "" {
			t.Error("refineryRepo is empty, want the worktree path")
		}
		if len(repos) != 1 {
			t.Errorf("repos = %v, want exactly the refinery clone", repos)
		}
	})

	t.Run("directory without .git is not reported", func(t *testing.T) {
		rigPath := t.TempDir()
		// Present but not a clone — callers must be able to trust every
		// returned path, so this must be omitted rather than reported.
		if err := os.MkdirAll(filepath.Join(rigPath, "mayor", "rig"), 0o755); err != nil {
			t.Fatal(err)
		}

		mayor, refinery, repos := rigRepoPaths(rigPath)

		if mayor != "" || refinery != "" {
			t.Errorf("got mayor=%q refinery=%q, want both empty", mayor, refinery)
		}
		if len(repos) != 0 {
			t.Errorf("repos = %v, want empty", repos)
		}
	})

	t.Run("empty rig path", func(t *testing.T) {
		mayor, refinery, repos := rigRepoPaths("")

		if mayor != "" || refinery != "" {
			t.Errorf("got mayor=%q refinery=%q, want both empty", mayor, refinery)
		}
		if repos == nil {
			t.Error("repos is nil; it must be non-nil so JSON consumers see [] not null")
		}
	})

	t.Run("no clones yields non-nil empty slice", func(t *testing.T) {
		// A nil slice marshals to JSON null, which reintroduces exactly the
		// ambiguity gt-a7a was about: absent key versus empty work list.
		_, _, repos := rigRepoPaths(t.TempDir())

		if repos == nil {
			t.Fatal("repos is nil; it must be non-nil so JSON consumers see [] not null")
		}
		if len(repos) != 0 {
			t.Errorf("repos = %v, want empty", repos)
		}
	})
}

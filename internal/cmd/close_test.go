package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractBeadIDs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "single bead ID",
			args: []string{"gt-abc"},
			want: []string{"gt-abc"},
		},
		{
			name: "multiple bead IDs",
			args: []string{"gt-abc", "gt-def"},
			want: []string{"gt-abc", "gt-def"},
		},
		{
			name: "bead ID with boolean flags",
			args: []string{"--force", "gt-abc", "--suggest-next"},
			want: []string{"gt-abc"},
		},
		{
			name: "bead ID with short boolean flag",
			args: []string{"-f", "gt-abc"},
			want: []string{"gt-abc"},
		},
		{
			name: "bead ID with reason flag (separate value)",
			args: []string{"gt-abc", "--reason", "Done"},
			want: []string{"gt-abc"},
		},
		{
			name: "bead ID with reason flag (= form)",
			args: []string{"gt-abc", "--reason=Done"},
			want: []string{"gt-abc"},
		},
		{
			name: "bead ID with short reason flag",
			args: []string{"-r", "Done", "gt-abc"},
			want: []string{"gt-abc"},
		},
		{
			name: "bead ID with comment alias",
			args: []string{"--comment", "Finished", "gt-abc"},
			want: []string{"gt-abc"},
		},
		{
			name: "bead ID with session flag",
			args: []string{"gt-abc", "--session", "sess-123"},
			want: []string{"gt-abc"},
		},
		{
			name: "bead ID with db flag",
			args: []string{"--db", "/path/to/db", "gt-abc"},
			want: []string{"gt-abc"},
		},
		{
			name: "bead ID with -C directory flag",
			args: []string{"-C", "/home/user/gt", "hq-abc"},
			want: []string{"hq-abc"},
		},
		{
			name: "bead ID with --directory flag",
			args: []string{"--directory", "/home/user/gt", "hq-abc"},
			want: []string{"hq-abc"},
		},
		{
			name: "no bead IDs (flags only)",
			args: []string{"--force", "--reason", "cleanup"},
			want: nil,
		},
		{
			name: "empty args",
			args: []string{},
			want: nil,
		},
		{
			name: "multiple IDs with mixed flags",
			args: []string{"--force", "gt-abc", "--reason", "Done", "hq-cv-xyz", "-v"},
			want: []string{"gt-abc", "hq-cv-xyz"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBeadIDs(tt.args)
			if len(got) != len(tt.want) {
				t.Fatalf("extractBeadIDs(%v) = %v, want %v", tt.args, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("extractBeadIDs(%v)[%d] = %q, want %q", tt.args, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractCascadeFlag(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantCascade bool
		wantArgs    []string
	}{
		{
			name:        "no cascade flag",
			args:        []string{"gt-abc", "--force"},
			wantCascade: false,
			wantArgs:    []string{"gt-abc", "--force"},
		},
		{
			name:        "cascade flag present",
			args:        []string{"gt-abc", "--cascade"},
			wantCascade: true,
			wantArgs:    []string{"gt-abc"},
		},
		{
			name:        "cascade flag with other flags",
			args:        []string{"--cascade", "gt-abc", "--reason", "Done"},
			wantCascade: true,
			wantArgs:    []string{"gt-abc", "--reason", "Done"},
		},
		{
			name:        "empty args",
			args:        []string{},
			wantCascade: false,
			wantArgs:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCascade, gotArgs := extractCascadeFlag(tt.args)
			if gotCascade != tt.wantCascade {
				t.Errorf("extractCascadeFlag(%v) cascade = %v, want %v", tt.args, gotCascade, tt.wantCascade)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Fatalf("extractCascadeFlag(%v) args = %v, want %v", tt.args, gotArgs, tt.wantArgs)
			}
			for i := range gotArgs {
				if gotArgs[i] != tt.wantArgs[i] {
					t.Errorf("extractCascadeFlag(%v) args[%d] = %q, want %q", tt.args, i, gotArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}

// bd's -C only sets BEADS_DIR; it never chdirs, so a -C forwarded to bd close
// would select the store while the role stayed with gt's working directory.
// gt close pulls -C out of argv and runs bd there instead (gt-d37).
func TestExtractChangeDir(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantDir  string
		wantArgs []string
		wantErr  bool
	}{
		{
			name:     "no change dir flag",
			args:     []string{"gt-abc", "--reason", "Done"},
			wantDir:  "",
			wantArgs: []string{"gt-abc", "--reason", "Done"},
		},
		{
			name:     "short flag with separate value",
			args:     []string{"-C", "/home/user/gt", "hq-abc"},
			wantDir:  "/home/user/gt",
			wantArgs: []string{"hq-abc"},
		},
		{
			name:     "long flag with separate value",
			args:     []string{"--directory", "/home/user/gt", "hq-abc"},
			wantDir:  "/home/user/gt",
			wantArgs: []string{"hq-abc"},
		},
		{
			name:     "short flag with = form",
			args:     []string{"-C=/home/user/gt", "hq-abc"},
			wantDir:  "/home/user/gt",
			wantArgs: []string{"hq-abc"},
		},
		{
			name:     "long flag with = form",
			args:     []string{"--directory=/home/user/gt", "hq-abc"},
			wantDir:  "/home/user/gt",
			wantArgs: []string{"hq-abc"},
		},
		{
			name:     "shorthand with attached value",
			args:     []string{"-C/home/user/gt", "hq-abc"},
			wantDir:  "/home/user/gt",
			wantArgs: []string{"hq-abc"},
		},
		{
			name:     "flag after the bead ID",
			args:     []string{"hq-abc", "--reason", "Done", "-C", "/home/user/gt"},
			wantDir:  "/home/user/gt",
			wantArgs: []string{"hq-abc", "--reason", "Done"},
		},
		{
			name:     "last occurrence wins",
			args:     []string{"-C", "/first", "hq-abc", "-C", "/second"},
			wantDir:  "/second",
			wantArgs: []string{"hq-abc"},
		},
		{
			name:     "flag-like reason value is not a change dir",
			args:     []string{"hq-abc", "--reason", "-C/tmp"},
			wantDir:  "",
			wantArgs: []string{"hq-abc", "--reason", "-C/tmp"},
		},
		{
			name:    "missing value",
			args:    []string{"hq-abc", "-C"},
			wantErr: true,
		},
		{
			name:    "empty value",
			args:    []string{"-C", "", "hq-abc"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDir, gotArgs, err := extractChangeDir(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("extractChangeDir(%v) = (%q, %v, nil), want error", tt.args, gotDir, gotArgs)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractChangeDir(%v) returned %v", tt.args, err)
			}
			if gotDir != tt.wantDir {
				t.Errorf("extractChangeDir(%v) dir = %q, want %q", tt.args, gotDir, tt.wantDir)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Fatalf("extractChangeDir(%v) args = %v, want %v", tt.args, gotArgs, tt.wantArgs)
			}
			for i := range gotArgs {
				if gotArgs[i] != tt.wantArgs[i] {
					t.Errorf("extractChangeDir(%v) args[%d] = %q, want %q", tt.args, i, gotArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}

// A -C that cannot be honoured must fail loudly rather than fall back to prefix
// routing — a target selector that looks honoured and is not is the whole of
// gt-d37.
func TestResolveCloseChangeDir(t *testing.T) {
	dir := t.TempDir()

	got, err := resolveCloseChangeDir(dir)
	if err != nil {
		t.Fatalf("resolveCloseChangeDir(%q) returned %v", dir, err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolveCloseChangeDir(%q) = %q, want an absolute path", dir, got)
	}

	if _, err := resolveCloseChangeDir(filepath.Join(dir, "nope")); err == nil {
		t.Error("resolveCloseChangeDir() on a missing directory = nil error, want error")
	}

	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", file, err)
	}
	if _, err := resolveCloseChangeDir(file); err == nil {
		t.Error("resolveCloseChangeDir() on a regular file = nil error, want error")
	}
}

// An explicit -C overrides prefix routing; without one, the first bead's prefix
// still chooses the directory.
func TestCloseBeadDir_ChangeDirWins(t *testing.T) {
	if got := closeBeadDir("/explicit/dir", []string{"gt-abc"}); got != "/explicit/dir" {
		t.Errorf("closeBeadDir() = %q, want /explicit/dir", got)
	}
	if got := closeBeadDir("/explicit/dir", nil); got != "/explicit/dir" {
		t.Errorf("closeBeadDir() with no bead IDs = %q, want /explicit/dir", got)
	}
	if got := closeBeadDir("", nil); got != "" {
		t.Errorf("closeBeadDir() with no inputs = %q, want empty", got)
	}
}

func TestChildBeadUnmarshal(t *testing.T) {
	jsonData := `[{"id":"gt-abc","status":"open"},{"id":"gt-def","status":"closed"}]`
	var children []childBead
	if err := json.Unmarshal([]byte(jsonData), &children); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("got %d children, want 2", len(children))
	}
	if children[0].ID != "gt-abc" || children[0].Status != "open" {
		t.Errorf("child[0] = %+v, want {ID:gt-abc Status:open}", children[0])
	}
	if children[1].ID != "gt-def" || children[1].Status != "closed" {
		t.Errorf("child[1] = %+v, want {ID:gt-def Status:closed}", children[1])
	}
}

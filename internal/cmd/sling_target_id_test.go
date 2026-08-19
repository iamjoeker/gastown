package cmd

import (
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/session"
)

// resolveTargetAgentID must answer for an agent whose session is gone. That is
// the whole point of splitting it out of resolveTargetAgent: `gt unsling` needs
// only the identity, and requiring a live pane made it fail with "getting pane
// for gt-<name>: exit status 1" on exactly the dead agents check-recovery
// escalates for recovery (gt-dh3d).
//
// The session names below are deliberately ones no tmux server has.
func TestResolveTargetAgentIDNeedsNoSession(t *testing.T) {
	// Out of any real town: the shorthand form probes the cwd's town for a crew
	// member of that name before falling through to a polecat, and this test is
	// about resolution, not about whichever town the suite happens to run in.
	t.Chdir(t.TempDir())

	prev := session.DefaultRegistry()
	t.Cleanup(func() { session.SetDefaultRegistry(prev) })
	registry := session.NewPrefixRegistry()
	registry.Register("gt", "gastown")
	session.SetDefaultRegistry(registry)

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{name: "explicit polecat path", target: "gastown/polecats/synth", want: "gastown/polecats/synth"},
		{name: "shorthand polecat path", target: "gastown/synth", want: "gastown/polecats/synth"},
		{name: "witness role", target: "gastown/witness", want: "gastown/witness"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTargetAgentID(tt.target)
			if err != nil {
				t.Fatalf("resolveTargetAgentID(%q) = %v, want no error for a dead agent", tt.target, err)
			}
			if got != tt.want {
				t.Fatalf("resolveTargetAgentID(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

// The unparseable-target error must survive the split: dropping the pane lookup
// must not turn a bad target into a silently accepted one.
func TestResolveTargetAgentIDRejectsUnparseableTarget(t *testing.T) {
	if got, err := resolveTargetAgentID("gastown/a/b/c/d"); err == nil {
		t.Fatalf("resolveTargetAgentID() = %q, want an error", got)
	} else if !strings.Contains(err.Error(), "cannot parse path") {
		t.Fatalf("error = %v, want a parse failure", err)
	}
}

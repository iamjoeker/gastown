package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestEveryParentCommandIsRunnable walks the whole command tree and fails on any
// parent command (one with children) that has no Run/RunE of its own.
//
// Cobra answers an unknown subcommand of such a parent by printing the parent's
// help to STDOUT and exiting 0. So `gt dog whatevr` succeeds, and a script doing
// TOWN_ROOT=$(gt dog whatevr) captures help text as data and never notices —
// which is exactly how three plugins ended up with help text in TOWN_ROOT
// (gt-cr2). It is the "exits 0, prints an affirmative message, changes no state"
// class (gt-dr6t): the command's own output is the surface that lies, so reading
// it is not verification.
//
// requireSubcommand is the fix; this test is the guard, so a parent added
// without it fails here instead of in a caller's script months later.
//
// The root command is exempt: bare `gt` printing help is conventional, and cobra
// already rejects `gt <unknown>` with a non-zero exit at the root level.
func TestEveryParentCommandIsRunnable(t *testing.T) {
	var offenders []string

	walkCommandTree(rootCmd, func(cmd *cobra.Command) {
		if cmd == rootCmd || !cmd.HasSubCommands() || cmd.Runnable() {
			return
		}
		offenders = append(offenders, cmd.CommandPath())
	})

	if len(offenders) > 0 {
		t.Errorf("parent commands with no Run/RunE — for these, an unknown subcommand prints help and exits 0:\n\t%s\n\nFix: add `RunE: requireSubcommand` to each.",
			strings.Join(offenders, "\n\t"))
	}
}

// TestRequireSubcommandRejects pins the behaviour the guard above depends on:
// requireSubcommand must return an error both for a missing subcommand and for
// an unknown one. Without this, requireSubcommand could be softened to return
// nil and every parent using it would silently rejoin the exit-0 class while
// TestEveryParentCommandIsRunnable still passed.
func TestRequireSubcommandRejects(t *testing.T) {
	parent := &cobra.Command{Use: "parent", RunE: requireSubcommand}
	parent.AddCommand(&cobra.Command{Use: "child", RunE: func(*cobra.Command, []string) error { return nil }})

	t.Run("no subcommand", func(t *testing.T) {
		err := requireSubcommand(parent, nil)
		if err == nil {
			t.Fatal("requireSubcommand(parent, nil) = nil, want error")
		}
		if !strings.Contains(err.Error(), "requires a subcommand") {
			t.Errorf("error = %q, want it to mention that a subcommand is required", err)
		}
	})

	t.Run("unknown subcommand", func(t *testing.T) {
		err := requireSubcommand(parent, []string{"chidl"})
		if err == nil {
			t.Fatal(`requireSubcommand(parent, ["chidl"]) = nil, want error`)
		}
		if !strings.Contains(err.Error(), `unknown command "chidl"`) {
			t.Errorf("error = %q, want it to name the unknown command", err)
		}
		if !strings.Contains(err.Error(), "child") {
			t.Errorf("error = %q, want a did-you-mean suggestion for the near-miss", err)
		}
	})
}

// TestParentCommandsRejectUnknownSubcommand exercises the real command tree the
// way a caller does: dispatch `<parent> zzznotacmd` through cobra and require a
// non-nil error. This catches the failure by behaviour rather than by shape —
// a parent that is Runnable but whose Run prints help and returns nil passes
// TestEveryParentCommandIsRunnable and fails here.
//
// Only parents wired to requireSubcommand are dispatched: parents with real
// default behaviour would execute it, and this test must not perform side
// effects.
func TestParentCommandsRejectUnknownSubcommand(t *testing.T) {
	checked := 0
	walkCommandTree(rootCmd, func(cmd *cobra.Command) {
		// Compare by function identity, before calling anything: a parent with
		// genuine default behaviour must not be executed by this test.
		if !cmd.HasSubCommands() || !usesRequireSubcommand(cmd) {
			return
		}
		checked++
		if err := cmd.RunE(cmd, []string{"zzznotacmd"}); err == nil {
			t.Errorf("%s with an unknown subcommand returned nil error (would exit 0 with help)", cmd.CommandPath())
		}
	})

	if checked == 0 {
		t.Fatal("no parent commands use requireSubcommand — the identity check is broken, so this test proves nothing")
	}
}

// usesRequireSubcommand reports whether cmd's RunE is the shared
// requireSubcommand helper. Comparison is by code pointer, so it neither
// executes the command nor depends on its error text.
func usesRequireSubcommand(cmd *cobra.Command) bool {
	if cmd.RunE == nil {
		return false
	}
	return funcPC(cmd.RunE) == funcPC(requireSubcommand)
}

func funcPC(fn func(*cobra.Command, []string) error) uintptr {
	return reflect.ValueOf(fn).Pointer()
}

func walkCommandTree(cmd *cobra.Command, fn func(*cobra.Command)) {
	fn(cmd)
	for _, c := range cmd.Commands() {
		walkCommandTree(c, fn)
	}
}

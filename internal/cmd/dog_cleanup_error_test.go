package cmd

import (
	"os"
	"strings"
	"testing"
)

// TestClosePluginMailsReturnsErrorOutsideWorkspace pins the contract that
// gt-u58w violated: closePluginMails must never fail silently.
//
// The original signature was `func closePluginMails(dogName string)` — no
// return value — with two silent `return` paths and output only on success. A
// dog ran `gt dog done`, archived ZERO dispatch mail, printed NOTHING, exited 0,
// and 230+ dispatches accumulated across the pack at ~1 per 3 minutes with no
// signal to any operator. The function's own doc comment recorded the history it
// was written to fix: "Plugin dispatch mails ... accumulate because gt dog done
// never closed them."
//
// This test covers the one path reachable without a live town: the
// not-in-a-workspace skip. That path previously returned bare, making "skipped"
// indistinguishable from "cleaned up successfully". The archive-refusal path
// (the live actor-guard failure) is covered at the layer below by
// TestReclaimDispatchMail_PartialFailureContinues.
//
// The stronger guarantee is structural and compiler-enforced: closePluginMails
// now returns an error, so a caller CANNOT discard it without writing code that
// visibly ignores a value.
func TestClosePluginMailsReturnsErrorOutsideWorkspace(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	// A temp dir is not a Gas Town workspace, so FindFromCwd must fail.
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	gotClosed, gotErr := closePluginMails("alpha")
	if gotClosed != 0 {
		t.Errorf("closePluginMails archived %d mails outside a workspace; want 0", gotClosed)
	}
	if gotErr == nil {
		t.Fatal("closePluginMails returned nil outside a Gas Town workspace. " +
			"That is the gt-u58w defect: the caller cannot distinguish 'cleaned up' " +
			"from 'did nothing', which is how dispatch mail accumulated unbounded.")
	}
	if !strings.Contains(gotErr.Error(), "alpha") {
		t.Errorf("error %q does not name the dog; an operator cannot tell which dog failed", gotErr)
	}
}

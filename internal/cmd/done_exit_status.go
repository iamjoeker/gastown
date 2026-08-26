package cmd

import (
	"fmt"
	"strings"
)

// doneOutcome records what gt done failed to do, so the exit status can say so.
type doneOutcome struct {
	// PushFailed: commits exist locally and are not on the remote.
	PushFailed bool
	// MRFailed: the branch is pushed but no merge request reached the queue.
	MRFailed bool
	// MRRefused: gt done declined to create a merge request (gt-7qm).
	MRRefused bool
	// LedgerNoteErr: a --skip-verify annotation was lost, so the close it was
	// the only audit trail for was withheld (gt-290c).
	LedgerNoteErr error
	// Reasons is the accumulated doneErrors slice, already reported to the
	// witness. Carried here so the exit status can name a cause too.
	Reasons []string
}

// doneExitError converts a gt done outcome into the error that decides the
// process exit status, or nil when nothing failed.
//
// Every failure below already printed a warning, set a flag on the agent bead
// and nudged the witness — and then fell through to `return nil`, so the
// process exited 0 (gt-7k3q). Two consecutive failing runs exiting 0 is what
// let a caller checking exit status read a push that never landed, and an MR
// that was never created, as a clean completion. The witness sees the reason
// and a shell sees success, which is the worst of both: the failure is
// reported to the one reader that cannot act on the exit code and hidden from
// the one that can.
//
// Deliberately narrow. A benign no-MR completion (report-only work, a code
// bead superseded by a sibling's landed commit, a push rejected only because
// the content was already on the remote) did what it set out to do and exits 0.
func doneExitError(o doneOutcome) error {
	var failures []string
	if o.PushFailed {
		failures = append(failures, "the branch was not pushed")
	}
	if o.MRFailed {
		failures = append(failures, "no merge request reached the queue")
	}
	if o.MRRefused {
		failures = append(failures, "merge request creation was refused")
	}
	if o.LedgerNoteErr != nil {
		failures = append(failures, "the ledger annotation was not recorded, so the close was withheld")
	}
	if len(failures) == 0 {
		return nil
	}
	msg := "gt done did not complete: " + strings.Join(failures, "; ")
	if reasons := summarizeDoneErrors(o.Reasons); reasons != "" {
		msg += "\nreasons: " + reasons
	}
	return fmt.Errorf("%s\nThe witness has been notified and this work is not landed — do NOT close the bead by hand", msg)
}

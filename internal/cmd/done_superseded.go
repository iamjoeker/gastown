package cmd

// A code bead whose work a sibling already landed had no exit from `gt done`
// at all (gt-7k3q). Every path available to the polecat refused it:
//
//	gt done                -> "cannot close no-MR code bead in fork/upstream
//	                          mode: <branch> has no commits ahead of <base>"
//	gt done --skip-verify  -> refused; --skip-verify is non-code only
//	gt done --status DEFERRED -> accepted, and false: the work is finished, so
//	                          the bead reopens and is dispatched again
//
// Facing no correct action, the polecat closed the bead by hand. That is the
// reasonable move from the position it was left in, and it orphaned a P0 merge
// request: an open MR carrying the real fix, hanging off a closed source issue,
// while every listing surface read the work as finished.
//
// The missing exit is narrow and provable. Zero commits ahead of the target
// says this branch adds nothing. It does not say the WORK is done — the two
// readings are "a sibling landed it" and "the polecat wrote nothing" — and
// closing the bead is only correct under the first. So the close is gated on
// evidence from git rather than on the polecat's say-so: a commit reachable
// from the target whose message names this bead.
//
// That evidence is deliberately weaker than proof, and the code says so. A
// commit can name a bead without implementing it, and a shallow clone can miss
// one that did. Both failure modes are handled the same way — the verdict
// carries what was and was not established, the close reason records the
// commit so a witness can audit it, and anything short of a clean hit refuses.

import (
	"fmt"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
)

// supersededCommitScanDepth bounds how many subject matches are read. A bead ID
// is unique, so one hit settles the question; a few more are read so a revert
// at the head of the list does not hide the landing commit beneath it.
const supersededCommitScanDepth = 10

// supersededProbe is the slice of the git client this check needs.
//
// CommitsWithSubjectToken, not a whole-message search: the bead ID appears in
// the SUBJECT of the commit that does the work and of the merge that lands it,
// while body mentions are cross-references — follow-ups filed, related beads —
// and were measured wrong nine times in ten as attribution.
type supersededProbe interface {
	CommitsWithSubjectToken(ref, token string, limit int) ([]git.CommitRef, error)
}

// supersededRequest describes the completion being assessed. The caller has
// already established that the branch is zero commits ahead of BaseRef.
type supersededRequest struct {
	IssueID string
	BaseRef string
}

// supersededVerdict is what the probe could establish about work the polecat
// believes a sibling already landed.
type supersededVerdict struct {
	// Landed is true only when a commit reachable from the target names this
	// bead. It is the sole condition under which the bead may be closed here.
	Landed bool
	// Commit is the evidence behind Landed: the newest commit on the target
	// whose subject names the bead. Recorded on the bead so the close is
	// auditable against git rather than trusted.
	Commit git.CommitRef
	// Reason states why landing could not be established. Always set when
	// Landed is false, and phrased for a polecat that has to act on it.
	Reason string
}

// assessSupersededWork reports whether a zero-commits-ahead completion may be
// closed as superseded by work already on the target.
//
// It refuses on every uncertainty. An unreadable ref, a shallow history, a
// commit that only mentions the bead in a revert — each returns Landed=false
// with the reason, because the cost of a wrong "landed" is a bead closed over
// work that is not in the target, which is precisely the end state gt-7k3q
// exists to prevent. A wrong refusal costs one escalation.
func assessSupersededWork(g supersededProbe, req supersededRequest) supersededVerdict {
	issueID := strings.TrimSpace(req.IssueID)
	baseRef := strings.TrimSpace(req.BaseRef)

	switch {
	case g == nil:
		return supersededVerdict{Reason: "no git client available to check whether the work is already on the target"}
	case issueID == "":
		return supersededVerdict{Reason: "no source issue to look for on the target"}
	case baseRef == "":
		return supersededVerdict{Reason: "no target ref to check the work against"}
	}

	commits, err := g.CommitsWithSubjectToken(baseRef, issueID, supersededCommitScanDepth)
	if err != nil {
		return supersededVerdict{Reason: fmt.Sprintf("could not search %s for commits naming %s: %v", baseRef, issueID, err)}
	}

	reverts := 0
	for _, c := range commits {
		// A commit the probe could not identify is not evidence of anything.
		if strings.TrimSpace(c.SHA) == "" {
			continue
		}
		// A revert names the bead exactly as the landing commit does, and means
		// the opposite. Text ABOUT the work satisfies a search FOR it, so the
		// asserting form has to be separated from the negating one — and the
		// separation has to be reported, or a refusal that found a revert reads
		// identically to one that found nothing.
		if isRevertSubject(c.Subject) {
			reverts++
			continue
		}
		return supersededVerdict{Landed: true, Commit: c}
	}

	if reverts > 0 {
		return supersededVerdict{Reason: fmt.Sprintf(
			"every commit on %s naming %s is a revert (%d of them) — the work is not on the target",
			baseRef, issueID, reverts)}
	}
	return supersededVerdict{Reason: fmt.Sprintf(
		"no commit reachable from %s has %s in its subject, so there is no evidence the work landed "+
			"(a polecat clone is shallow, so a commit older than its graft point is invisible here)",
		baseRef, issueID)}
}

// isRevertSubject reports whether a commit subject is a revert. Both the
// git-generated form and the conventional-commits form are recognised.
func isRevertSubject(subject string) bool {
	s := strings.ToLower(strings.TrimSpace(subject))
	return strings.HasPrefix(s, "revert ") ||
		strings.HasPrefix(s, "revert:") ||
		strings.HasPrefix(s, "revert(")
}

// supersededCloseReason renders the bead close reason for a superseded
// completion. The landing commit is recorded so the close can be audited
// against git rather than trusted.
func supersededCloseReason(req supersededRequest, v supersededVerdict) string {
	return fmt.Sprintf(
		"Superseded: equivalent work is already on %s; this branch adds nothing\n"+
			"superseded: true\ntarget_branch: %s\ncommit_sha: %s\nlanded_as: %s",
		req.BaseRef, req.BaseRef, v.Commit.SHA, v.Commit.Subject)
}

// supersededRefusalHint is appended to the refusals a polecat hits when its
// branch is zero commits ahead. Without it the error names the state and not
// the way out, which is how gt-7k3q's polecat concluded the tool had no
// correct action and closed the bead by hand.
func supersededRefusalHint(v supersededVerdict) string {
	return fmt.Sprintf("Checked whether a sibling already landed this work: %s\n"+
		"Do NOT close the bead by hand — that orphans any merge request still carrying the work.\n"+
		"If the work IS on the target, land it under a commit naming the bead and re-run gt done.\n"+
		"If you are blocked: gt done --status ESCALATED\n"+
		"If the work is genuinely unfinished: gt done --status DEFERRED (leaves the bead open for re-dispatch)",
		v.Reason)
}

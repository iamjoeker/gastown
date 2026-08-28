package refinery

import (
	"fmt"
	"strings"
)

// preVerifyGit is the subset of *git.Git the pre-verification fast path needs.
type preVerifyGit interface {
	Rev(ref string) (string, error)
	MergeBase(a, b string) (string, error)
}

// preVerifyDecision is the fast path's answer together with the line to print
// for it, so the reason a batch did or did not skip gates is in the operator's
// log next to the decision it explains.
type preVerifyDecision struct {
	SkipGates bool
	Log       string
}

// decidePreVerifiedFastPath answers whether the refinery may merge an MR
// without re-running gates.
//
// The MR's `pre_verified_base` is the polecat's account of the commit it gated
// against. gt-eygw caught one that was wrong — it named 3431f90f7 while the
// branch's real parent was 3116d331a — and the old rule compared that field
// against the target head and skipped gates on a match. A self-report is a fine
// thing to require and a bad thing to decide on: the field can be wrong for
// reasons no amount of care at this end prevents, an older gt on the polecat
// side being the ordinary one, and every way it goes wrong here spends its
// error in the unsafe direction, merging work no gate ever saw.
//
// So the claim is still required, and git decides. The branch's merge-base with
// the target is where the branch is actually built; when that is the target's
// current head, the gates the polecat ran were run against exactly what this
// merge will land on. Anything else — a stale branch, an unresolvable ref, a
// recorded base that git contradicts — runs gates, which costs minutes and is
// the direction an error is survivable in.
func decidePreVerifiedFastPath(g preVerifyGit, mr *MRInfo) preVerifyDecision {
	if mr == nil || !mr.PreVerified || strings.TrimSpace(mr.PreVerifiedBase) == "" {
		return preVerifyDecision{}
	}
	claimed := strings.TrimSpace(mr.PreVerifiedBase)
	target := strings.TrimSpace(mr.Target)
	if target == "" {
		return preVerifyDecision{Log: fmt.Sprintf("[Engineer] Pre-verification claims base %s but the MR names no target — running gates normally\n", shortSHA(claimed))}
	}
	if g == nil {
		return preVerifyDecision{Log: "[Engineer] Pre-verification unverifiable: no git client — running gates normally\n"}
	}
	targetRef := "origin/" + target

	head, err := g.Rev(targetRef)
	if err != nil {
		return preVerifyDecision{Log: fmt.Sprintf("[Engineer] Pre-verification unverifiable: could not resolve %s: %v — running gates normally\n", targetRef, err)}
	}
	head = strings.TrimSpace(head)

	submitted := strings.TrimSpace(mr.CommitSHA)
	if submitted == "" {
		submitted = strings.TrimSpace(mr.Branch)
	}
	if submitted == "" {
		return preVerifyDecision{Log: "[Engineer] Pre-verification unverifiable: MR names neither a commit nor a branch — running gates normally\n"}
	}

	base, err := g.MergeBase(targetRef, submitted)
	if err != nil {
		return preVerifyDecision{Log: fmt.Sprintf("[Engineer] Pre-verification unverifiable: merge-base of %s and %s: %v — running gates normally\n", targetRef, shortSHA(submitted), err)}
	}
	base = strings.TrimSpace(base)

	// Reported whichever way the decision goes: a claim git contradicts is a
	// defect in whatever wrote it, and it stays invisible if only the runs it
	// happens to change get a line.
	var mismatch string
	if base != claimed {
		mismatch = fmt.Sprintf("[Engineer] Note: MR %s claims pre-verified base %s but %s is built on %s — trusting git\n",
			mr.ID, shortSHA(claimed), shortSHA(submitted), shortSHA(base))
	}

	if base == head {
		return preVerifyDecision{
			SkipGates: true,
			Log: mismatch + fmt.Sprintf("[Engineer] Pre-verification valid — %s is built on current %s (%s), skipping gates (fast-path)\n",
				shortSHA(submitted), targetRef, shortSHA(head)),
		}
	}
	return preVerifyDecision{
		Log: mismatch + fmt.Sprintf("[Engineer] Pre-verification stale — %s is built on %s but %s is at %s, running gates normally\n",
			shortSHA(submitted), shortSHA(base), targetRef, shortSHA(head)),
	}
}

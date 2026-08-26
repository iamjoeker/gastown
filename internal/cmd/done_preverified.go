package cmd

import (
	"fmt"
	"strings"
)

// preVerifiedBaseGit is the subset of *git.Git that resolvePreVerifiedBase
// needs. Defined as an interface so the recording rule can be exercised without
// standing up a repo for every case.
type preVerifiedBaseGit interface {
	Rev(ref string) (string, error)
	MergeBase(a, b string) (string, error)
}

// preVerifiedBase is what `gt done --pre-verified` knows about the submission
// at the moment it writes the MR bead: the commit the branch is actually built
// on, and where the target stood as it wrote.
type preVerifiedBase struct {
	// Base is the branch's merge-base with the target — the commit the gates
	// the polecat ran were run against.
	Base string
	// TargetTip is the target's head when gt done ran.
	TargetTip string
}

// OnTargetTip reports whether the branch really is built on the target's
// current head, i.e. whether the pre-verification is still good for this base.
func (p preVerifiedBase) OnTargetTip() bool {
	return p.Base != "" && p.Base == p.TargetTip
}

// resolvePreVerifiedBase measures the base a --pre-verified submission was
// gated on, rather than asserting it.
//
// gt-eygw: this used to record the target's current tip with the comment "the
// polecat rebased onto this SHA before running gates". Nothing had checked
// that, and on the path that matters gt done had already measured the opposite
// — the contamination preflight a few hundred lines earlier finds the branch
// behind the target and then deliberately SKIPS the auto-rebase precisely
// because --pre-verified is set, since rebasing would invalidate the gate
// results the flag attests to. That skip is the right call. Stamping the tip it
// declined to rebase onto is not: it turns a known-stale branch into a bead
// that reads as freshly rebased.
//
// The refinery then compares that field against the target head to decide
// whether it may skip gates entirely. The two agree whenever the target has not
// moved in the seconds between gt done and the refinery's read — which is
// exactly the case where the recorded value carries no information, because it
// was copied from the thing it is being compared against. So the fast path
// fired on the copy, not on the branch, and unverified work merged ungated.
//
// The merge-base is the same value whenever the polecat genuinely did rebase,
// so the fast path is unchanged for submissions that earned it, and it is the
// older commit when it did not.
func resolvePreVerifiedBase(g preVerifiedBaseGit, baseRef, submitted string) (preVerifiedBase, error) {
	baseRef = strings.TrimSpace(baseRef)
	if baseRef == "" {
		return preVerifiedBase{}, fmt.Errorf("no target base ref to measure against")
	}
	submitted = strings.TrimSpace(submitted)
	if submitted == "" {
		submitted = "HEAD"
	}

	tip, err := g.Rev(baseRef)
	if err != nil {
		return preVerifiedBase{}, fmt.Errorf("resolve %s: %w", baseRef, err)
	}
	base, err := g.MergeBase(baseRef, submitted)
	if err != nil {
		return preVerifiedBase{}, fmt.Errorf("merge-base of %s and %s: %w", baseRef, submitted, err)
	}
	return preVerifiedBase{
		Base:      strings.TrimSpace(base),
		TargetTip: strings.TrimSpace(tip),
	}, nil
}

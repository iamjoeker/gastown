package polecat

import (
	"fmt"
	"strings"
)

// DetachedHeadName is the literal branch name git reports for a detached HEAD
// (`git rev-parse --abbrev-ref HEAD`). Git refuses to create refs/heads/HEAD, so
// this value is never a real branch — it always means "HEAD is detached".
const DetachedHeadName = "HEAD"

// BranchLookup is the git surface needed to name a polecat's working branch.
type BranchLookup interface {
	CurrentBranch() (string, error)
	BranchesPointingAt(rev string) ([]string, error)
}

// BranchResolution describes how a polecat's working branch was determined.
//
// Branch is empty exactly when the branch could not be named. That is a
// different condition from "the push failed", and callers must keep it
// separate: a push result was never measured, so none may be reported (gt-e45).
type BranchResolution struct {
	Branch    string // Resolved branch name; empty when unresolvable
	Detached  bool   // HEAD was detached
	Recovered bool   // Detached, but a branch pointing at HEAD named the work
	Reason    string // Why the branch could not be named (set iff Branch == "")
}

// Unresolvable reports whether the working branch could not be named.
func (r BranchResolution) Unresolvable() bool { return r.Branch == "" }

// ResolveWorkingBranch names the branch a polecat's commits live on, by a method
// that cannot return the literal "HEAD".
//
// Polecat worktrees are routinely detached, and `git rev-parse --abbrev-ref HEAD`
// reports "HEAD" for a detached worktree. Feeding that name into a push refspec,
// or into a remote branch check, produces a check that can never pass — no remote
// carries refs/heads/HEAD — so the resulting alarm cannot distinguish a genuinely
// failed push from a branch nobody could name (gt-e45).
//
// When HEAD is detached at a commit a local branch still points at, that branch
// is the work and is returned with Recovered set. When nothing names the commit,
// the resolution is unresolvable and Reason explains why; callers must escalate
// that as its own condition rather than reporting a push outcome.
func ResolveWorkingBranch(g BranchLookup, polecatName, defaultBranch string) (BranchResolution, error) {
	branch, err := g.CurrentBranch()
	if err != nil {
		return BranchResolution{}, err
	}
	if branch != DetachedHeadName {
		return BranchResolution{Branch: branch}, nil
	}

	res := BranchResolution{Detached: true}
	candidates, err := g.BranchesPointingAt("HEAD")
	if err != nil {
		res.Reason = fmt.Sprintf("HEAD is detached and its candidate branches could not be listed: %v", err)
		return res, nil
	}

	picked, reason := pickWorkingBranch(filterBranchCandidates(candidates, defaultBranch), polecatName)
	if picked == "" {
		res.Reason = reason
		return res, nil
	}
	res.Branch = picked
	res.Recovered = true
	return res, nil
}

// filterBranchCandidates drops branches that can never be a polecat's work
// branch: the rig's default branch and master (submitting either is refused
// anyway), and the unusable "HEAD" name itself.
func filterBranchCandidates(candidates []string, defaultBranch string) []string {
	var kept []string
	for _, c := range candidates {
		switch c {
		case "", DetachedHeadName, "master", "main":
			continue
		}
		if defaultBranch != "" && c == defaultBranch {
			continue
		}
		kept = append(kept, c)
	}
	return kept
}

// pickWorkingBranch chooses the branch that names the detached work, preferring
// this polecat's own branches. Ambiguity is refused rather than guessed: pushing
// the wrong branch name attributes work to the wrong bead.
func pickWorkingBranch(candidates []string, polecatName string) (branch, reason string) {
	if len(candidates) == 0 {
		return "", "HEAD is detached and no local branch points at it"
	}

	if polecatName != "" {
		owned := filterOwnedBranches(candidates, polecatName)
		if len(owned) == 1 {
			return owned[0], ""
		}
		if len(owned) > 1 {
			return "", fmt.Sprintf("HEAD is detached and %d branches for polecat %s point at it: %s",
				len(owned), polecatName, strings.Join(owned, ", "))
		}
	}

	if owned := filterPolecatBranches(candidates); len(owned) == 1 {
		return owned[0], ""
	} else if len(owned) > 1 {
		return "", fmt.Sprintf("HEAD is detached and %d polecat branches point at it: %s",
			len(owned), strings.Join(owned, ", "))
	}

	if len(candidates) == 1 {
		return candidates[0], ""
	}
	return "", fmt.Sprintf("HEAD is detached and %d branches point at it: %s",
		len(candidates), strings.Join(candidates, ", "))
}

func filterOwnedBranches(candidates []string, polecatName string) []string {
	var owned []string
	for _, c := range candidates {
		if meta, ok := ParseBranchName(c); ok && meta.Polecat == polecatName {
			owned = append(owned, c)
		}
	}
	return owned
}

func filterPolecatBranches(candidates []string) []string {
	var branches []string
	for _, c := range candidates {
		if _, ok := ParseBranchName(c); ok {
			branches = append(branches, c)
		}
	}
	return branches
}

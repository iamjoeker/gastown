package witness

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Answering "is this bead's work already on the trunk?" from state that OUTLIVES
// the polecat (gt-e7dd).
//
// resetAbandonedBead opens with a guard: if the dead polecat's work is already
// on the default branch, close the bead rather than re-dispatching it (#2036).
// The guard was implemented as verifyCommitOnMain, which resolves the polecat
// WORKTREE and runs git there. Both callers that reach resetAbandonedBead do so
// only AFTER proving that directory is gone — DetectOrphanedBeads and
// DetectOrphanedMolecules each require os.Stat on the polecat directory to
// report IsNotExist, and the latter re-checks it. So on the orphan paths the
// guard's git lookup could not succeed: no worktree, no HEAD, error, guard
// skipped, bead reset. It fired only from HandleMerged, where the directory
// still exists. Every surface read as though #2036 was protected on all paths;
// it was protected on one.
//
// What that costs became larger with gt-429i: gt done no longer closes the
// source bead when it queues an MR, the refinery closes it on merge. The
// companion "open MR in the queue" guard covers the ordinary submit→merge
// window. It does not cover "merge landed, post-merge never ran (gt-3jx0), MR
// wisp then reaped": no open MR, no worktree, so the bead was reset and sent to
// a second polecat to redo work already in main.
//
// The fix is to ask the question of state that survives the nuke: the rig's own
// clone, and the branch the polecat PUSHED. Both are still there — `gt done`
// pushes before it nukes, and the refinery owns remote branch cleanup, not the
// polecat.
//
// Two rules constrain the answer, and both are load-bearing.
//
// FIRST, the branch must be attributable to THIS bead. Generated polecat
// branches encode the issue (polecat/<name>/<issue>+<suffix>); the legacy
// polecat/<name>-<suffix> form does not. A branch that names no bead proves
// nothing about this one, so it is skipped rather than credited — crediting it
// would close a bead because some other work of the same polecat's had landed.
//
// SECOND, ancestry proves containment one way only. A branch that is not an
// ancestor of the trunk may still have landed by squash or cherry-pick, which
// is how the refinery lands most work. Containment is therefore decided by
// git.PushRemoteRefTargetStatusAny — ancestry, then a no-op merge tree, then
// patch identity — against every ref that is honestly the trunk on this rig.
//
// The bias when anything cannot be measured is to report NOT landed, which
// leaves the pre-existing reset behaviour in place. Closing a bead is the
// destructive direction here: a wrongly reset bead costs a duplicate polecat and
// is loud, a wrongly closed one silently drops the work.

// maxLandedCandidateBranches bounds how many branches for one bead are compared
// against the trunk. Each comparison is a fetch, and a bead re-dispatched many
// times accumulates a branch per attempt. Truncation is recorded in the
// evidence rather than applied silently.
const maxLandedCandidateBranches = 8

// LandedEvidence is what a landed-check found, including how it found it.
//
// Landed false means one of two very different things, and Reason is what
// separates them: "compared, and the work is not there" versus "could not
// compare". Only the first is ever grounds for closing a bead, and callers that
// get an error alongside must treat the answer as the second.
type LandedEvidence struct {
	// Landed is true only when some branch's content was positively found in a
	// comparison target.
	Landed bool

	// Branch is the pushed branch that carried the work, when one was found.
	Branch string
	// CommitSHA is that branch's tip as the remote listed it.
	CommitSHA string
	// Polecat is the polecat encoded in Branch, which is not necessarily the
	// polecat that died holding the bead: a re-dispatched bead can be landed by
	// an earlier attempt.
	Polecat string

	// ContainedIn names which target held the content. "Landed" is not a fact
	// about a repository until you know which trunk it landed on.
	ContainedIn string
	// Evidence names how containment was decided: "ancestor",
	// "merge_tree_noop", "cherry", or "worktree-ancestor" for the fast path.
	Evidence string

	// Reason explains the verdict in one sentence, for a log line a human reads
	// after the fact.
	Reason string
}

// evidenceLabelFor renders an evidence token for a human.
func evidenceLabelFor(evidence string) string {
	if strings.TrimSpace(evidence) == "worktree-ancestor" {
		return "the polecat's worktree HEAD is an ancestor of the default branch"
	}
	return evidenceLabel(evidence)
}

// CloseReason is the reason recorded on the bead when the guard fires. It names
// the branch and the trunk it landed on: a bare "work already on main" cannot be
// checked afterwards by anyone wondering whether the guard was right.
func (e LandedEvidence) CloseReason() string {
	var b strings.Builder
	b.WriteString("Work already landed — verified by witness")
	if e.Branch != "" {
		b.WriteString(" from pushed branch ")
		b.WriteString(e.Branch)
	} else if e.Polecat != "" {
		b.WriteString(" from polecat ")
		b.WriteString(e.Polecat)
	}
	if e.ContainedIn != "" {
		b.WriteString(", content is in ")
		b.WriteString(e.ContainedIn)
	}
	if e.Evidence != "" {
		b.WriteString(" (")
		b.WriteString(evidenceLabelFor(e.Evidence))
		b.WriteString(")")
	}
	return b.String()
}

// landedCheckGit is the slice of git the durable landed-check needs. It is an
// interface so the decision logic can be tested without a remote.
type landedCheckGit interface {
	BranchSweepRefResolver

	// ListPushRemoteRefsWithHashes must read the PUSH url: split fetch/push
	// remotes are configured on these rigs, and listing the fetch side misses
	// the branch entirely, which reads as "the polecat never pushed".
	ListPushRemoteRefsWithHashes(remote, prefix string) ([]git.RemoteRef, error)
	// PushRemoteRefTargetStatusAny fetches the exact listed hash before
	// deciding, so a remote-only tip is classified against what is actually on
	// the remote rather than a stale tracking ref.
	PushRemoteRefTargetStatusAny(remote string, ref git.RemoteRef, targets []string) (git.BranchPreservationStatus, error)
	// Fetch refreshes a comparison remote. A stale trunk understates landing,
	// which is exactly the failure this check exists to end.
	Fetch(remote string) error
}

// verifyWorkLandedFromDurableState is a package-level var so tests can override
// it without a rig on disk.
var verifyWorkLandedFromDurableState = _verifyWorkLandedFromDurableState

// _verifyWorkLandedFromDurableState answers the landed question for one bead
// using only state that survives a polecat nuke.
//
// townRoot is resolved from workDir, the rig's durable clone from townRoot, and
// the branch from the bead ID encoded in pushed branch names. The polecat's
// worktree is never touched — by the time this runs it is usually gone, and
// depending on it is the whole defect.
func _verifyWorkLandedFromDurableState(workDir, rigName, polecatName, hookBead string) (LandedEvidence, error) {
	hookBead = strings.TrimSpace(hookBead)
	if hookBead == "" {
		return LandedEvidence{Reason: "no work bead, so no branch can be attributed to it"}, nil
	}

	townRoot, err := workspace.Find(workDir)
	if err != nil || townRoot == "" {
		return LandedEvidence{}, fmt.Errorf("no town root above %s, so the rig's durable clone cannot be located: %v", workDir, err)
	}

	repoGit, err := rigDurableRepo(filepath.Join(townRoot, rigName))
	if err != nil {
		return LandedEvidence{}, err
	}

	return landedFromPushedBranches(repoGit, "origin", polecatName, hookBead)
}

// rigDurableRepo opens the rig's own repository — the bare mirror when there is
// one, else the mayor's worktree. This is the clone that outlives every polecat,
// which is the entire reason the check is done here and not in the sandbox.
func rigDurableRepo(rigPath string) (*git.Git, error) {
	bare := filepath.Join(rigPath, ".repo.git")
	if info, err := os.Stat(bare); err == nil && info.IsDir() {
		return git.NewGitWithDir(bare, ""), nil
	}
	worktree := filepath.Join(rigPath, "mayor", "rig")
	if info, err := os.Stat(worktree); err == nil && info.IsDir() {
		return git.NewGit(worktree), nil
	}
	return nil, fmt.Errorf("no durable repository for rig at %s: neither %s nor %s exists", rigPath, bare, worktree)
}

// beadBranchCandidate is one pushed branch that names the bead in question.
type beadBranchCandidate struct {
	ref     git.RemoteRef
	branch  string
	polecat string
	// own is true when the branch belongs to the polecat that died holding the
	// bead, which is the likeliest carrier and so is compared first.
	own bool
}

// landedFromPushedBranches decides the landed question from the remote.
//
// The return contract is the delicate part. A nil error with Landed false means
// the comparison ran and found nothing; a non-nil error means the comparison did
// not happen, and the caller must not read that as "not landed" even though the
// two share a boolean.
func landedFromPushedBranches(g landedCheckGit, remote, polecatName, hookBead string) (LandedEvidence, error) {
	if remote = strings.TrimSpace(remote); remote == "" {
		remote = "origin"
	}

	refs, err := g.ListPushRemoteRefsWithHashes(remote, defaultPolecatRefPrefix)
	if err != nil {
		return LandedEvidence{}, fmt.Errorf("listing %s on the push url of %s: %w", defaultPolecatRefPrefix, remote, err)
	}

	candidates := candidateBranchesForBead(refs, polecatName, hookBead)
	if len(candidates) == 0 {
		// Nothing on the remote claims this bead. That is a measured "no", and
		// the bead should be recovered exactly as before — a polecat that died
		// before pushing has left nothing to land.
		return LandedEvidence{
			Reason: fmt.Sprintf("no branch on %s names bead %s, so nothing was pushed for it to land", remote, hookBead),
		}, nil
	}

	// Targets are resolved only once a candidate exists: the resolution costs
	// several git calls and answers nothing when there is no branch to compare.
	targets := ResolveComparisonTargets(g, remote, "")
	if len(targets) == 0 {
		return LandedEvidence{}, fmt.Errorf("no comparison ref exists on %s: landed and unlanded are indistinguishable without one", remote)
	}

	truncated := 0
	if len(candidates) > maxLandedCandidateBranches {
		truncated = len(candidates) - maxLandedCandidateBranches
		candidates = candidates[:maxLandedCandidateBranches]
	}

	// Refresh the trunks before comparing. A rig clone that has not fetched
	// since the merge reports landed work as unlanded, which is the false
	// negative this whole check exists to remove.
	var staleTargets []string
	for _, targetRemote := range TargetRemotes(targets, remote) {
		if fetchErr := g.Fetch(targetRemote); fetchErr != nil {
			staleTargets = append(staleTargets, fmt.Sprintf("%s (%v)", targetRemote, fetchErr))
		}
	}

	var compareErrs []string
	for _, candidate := range candidates {
		status, statusErr := g.PushRemoteRefTargetStatusAny(remote, candidate.ref, targets)
		if statusErr != nil {
			compareErrs = append(compareErrs, fmt.Sprintf("%s: %v", candidate.branch, statusErr))
			continue
		}
		if !status.Preserved {
			continue
		}
		containedIn := strings.TrimSpace(status.ComparisonBase)
		if containedIn == "" {
			containedIn = targets[0]
		}
		return LandedEvidence{
			Landed:      true,
			Branch:      candidate.branch,
			CommitSHA:   candidate.ref.Hash,
			Polecat:     candidate.polecat,
			ContainedIn: containedIn,
			Evidence:    status.Evidence,
			Reason: fmt.Sprintf("branch %s is contained in %s (%s)",
				candidate.branch, containedIn, evidenceLabelFor(status.Evidence)),
		}, nil
	}

	// Every candidate failed to compare: nothing was measured, so this must not
	// come back as a clean "not landed".
	if len(compareErrs) == len(candidates) {
		return LandedEvidence{}, fmt.Errorf("could not compare any branch for %s against %s: %s",
			hookBead, strings.Join(targets, " or "), strings.Join(compareErrs, "; "))
	}

	return LandedEvidence{
		Reason: notLandedReason(hookBead, targets, candidates, compareErrs, staleTargets, truncated),
	}, nil
}

// notLandedReason states what was compared and what was not, so a reader can
// tell a thorough "no" from a partial one.
func notLandedReason(hookBead string, targets []string, candidates []beadBranchCandidate, compareErrs, staleTargets []string, truncated int) string {
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, candidate.branch)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "no branch for %s is contained in %s (checked %s)",
		hookBead, strings.Join(targets, " or "), strings.Join(names, ", "))
	if truncated > 0 {
		fmt.Fprintf(&b, "; %d further branch(es) for this bead were NOT checked", truncated)
	}
	if len(compareErrs) > 0 {
		fmt.Fprintf(&b, "; %d branch(es) could not be compared: %s", len(compareErrs), strings.Join(compareErrs, "; "))
	}
	if len(staleTargets) > 0 {
		fmt.Fprintf(&b, "; could not refresh %s, so the comparison ref may be stale", strings.Join(staleTargets, ", "))
	}
	return b.String()
}

// candidateBranchesForBead picks the pushed branches that name this bead,
// this polecat's own first.
//
// A branch whose name encodes no issue is skipped entirely. The legacy
// polecat/<name>-<suffix> form is exactly that case, and treating it as this
// bead's work would close a bead on the strength of unrelated work by the same
// polecat.
//
// Branches from OTHER polecats that name this bead are kept, because a
// re-dispatched bead's work can have been landed by an earlier attempt — that is
// the same "already done, do not send another polecat" the guard exists for.
func candidateBranchesForBead(refs []git.RemoteRef, polecatName, hookBead string) []beadBranchCandidate {
	var candidates []beadBranchCandidate
	for _, ref := range refs {
		if !strings.HasPrefix(ref.Name, "refs/heads/") || strings.TrimSpace(ref.Hash) == "" {
			continue
		}
		branch := strings.TrimPrefix(ref.Name, "refs/heads/")
		meta, ok := polecat.ParseBranchName(branch)
		if !ok || meta.Issue == "" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(meta.Issue), hookBead) {
			continue
		}
		candidates = append(candidates, beadBranchCandidate{
			ref:     ref,
			branch:  branch,
			polecat: meta.Polecat,
			own:     meta.Polecat == polecatName,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].own != candidates[j].own {
			return candidates[i].own
		}
		return candidates[i].branch < candidates[j].branch
	})
	return candidates
}

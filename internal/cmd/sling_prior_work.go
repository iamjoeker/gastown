package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
)

// Prior-work detection for dispatch (gt-79li).
//
// A bead can return to a dispatchable state while work for it already exists:
// a polecat pushes a branch and submits an MR, then the bead is reopened or
// reset to open by orphan recovery, or its MR is rejected. The branch and the
// commit survive all of that — only the bead state regresses. Dispatching such
// a bead without saying so makes the next polecat redo work that is already
// committed.
//
// Two things go wrong in that window, and this file addresses both:
//
//  1. An OPEN MR means the work is queued and about to merge. Dispatching then
//     is duplicate work against a branch that is landing, so it is refused
//     unless --force.
//  2. A CLOSED MR (rejected, superseded, conflicted) means the work exists but
//     has no queued path to land. Dispatch is legitimate here, but the new
//     polecat must be told which branch to build on.

// Where a priorAttempt was observed. The distinction matters: only a live MR
// bead proves the work is QUEUED, which is the case that blocks dispatch. A
// bare branch proves work exists but has no path to land, which only warrants
// telling the next polecat about it.
const (
	priorSourceMR     = "mr"
	priorSourceBranch = "branch"
)

// priorAttempt is the useful part of a merge-request bead — or, when no MR
// bead survives, of a pushed branch — for an issue about to be dispatched again.
type priorAttempt struct {
	Source      string // priorSourceMR or priorSourceBranch
	MRID        string
	MRStatus    string // "open" or "closed" (MR source only)
	Branch      string
	CommitSHA   string
	CloseReason string // merged, rejected, conflict, superseded (closed MRs only)
}

// Open reports whether this attempt is still queued in the merge queue.
// A branch-sourced attempt is never open: nothing is holding a slot for it.
func (p *priorAttempt) Open() bool {
	return p != nil && p.Source == priorSourceMR && !strings.EqualFold(p.MRStatus, "closed")
}

// Merged reports whether this attempt already landed. A merged MR is not
// prior work to build on — it is work that is already in the target branch.
func (p *priorAttempt) Merged() bool {
	return p != nil && strings.EqualFold(p.CloseReason, "merged")
}

// selectPriorAttempt picks the MR bead that best represents work already done
// for an issue. Open MRs win over closed ones because a queued branch is the
// stronger signal; within each class the most recent (last) entry wins, which
// matches how gt mq submit appends and supersedes. MRs without a branch carry
// no recoverable work and are skipped.
func selectPriorAttempt(mrs []*beads.Issue) *priorAttempt {
	var best *priorAttempt
	for _, mr := range mrs {
		if mr == nil {
			continue
		}
		fields := beads.ParseMRFields(mr)
		if fields == nil || fields.Branch == "" {
			continue
		}
		candidate := &priorAttempt{
			Source:      priorSourceMR,
			MRID:        mr.ID,
			MRStatus:    mr.Status,
			Branch:      fields.Branch,
			CommitSHA:   fields.CommitSHA,
			CloseReason: fields.CloseReason,
		}
		// An open MR always beats a closed one; otherwise later wins.
		if best != nil && best.Open() && !candidate.Open() {
			continue
		}
		best = candidate
	}
	return best
}

// priorAttemptVars renders the formula vars a re-dispatched polecat needs in
// order to build on prior work instead of restarting. Returns nil when there is
// nothing worth saying — including for merged work, where pointing the polecat
// at the old branch would be actively wrong.
func priorAttemptVars(p *priorAttempt) []string {
	if p == nil || p.Branch == "" || p.Merged() {
		return nil
	}
	vars := []string{
		"prior_branch=" + p.Branch,
		"prior_status=" + priorAttemptStatusLabel(p),
	}
	if p.CommitSHA != "" {
		vars = append(vars, "prior_commit="+p.CommitSHA)
	}
	if p.CloseReason != "" {
		vars = append(vars, "prior_failure="+p.CloseReason)
	}
	return vars
}

// priorAttemptStatusLabel describes the state in the terms a polecat cares
// about: is the branch still queued, or is it stranded?
func priorAttemptStatusLabel(p *priorAttempt) string {
	if p.Open() {
		return "queued"
	}
	if p.Source == priorSourceBranch {
		return "unqueued-branch"
	}
	if p.CloseReason != "" {
		return "closed:" + p.CloseReason
	}
	return "closed"
}

// priorWorkBeads returns a Beads handle rooted at the database where the bead's
// merge requests actually live.
//
// This routing is load-bearing (gt-79li). MR beads are wisps in the RIG
// database, but dispatch callers hold the TOWN beads dir, and
// ListMergeRequests runs raw SQL with no prefix routing. Handing it the town
// dir queries hq, where no rig MR has ever existed — measured 2026-08-18:
// 0 gt:merge-request wisps from <townRoot>/.beads against 36 from the gastown
// rig dir, with hq's 27684 wisps / 23375 label rows as the control proving the
// query itself works.
func priorWorkBeads(townRoot, beadID string) *beads.Beads {
	beadsDir := beads.ResolveBeadsDirForID(filepath.Join(townRoot, ".beads"), beadID)
	if beadsDir == "" {
		return nil
	}
	return beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
}

// findPriorAttempt looks up prior work for a bead across open AND closed MRs,
// falling back to the remote branches when no MR bead survives.
// Errors are swallowed: prior-work context is an advisory enrichment, and a
// beads or network hiccup must not block dispatch.
func findPriorAttempt(townRoot, beadID string) *priorAttempt {
	if bd := priorWorkBeads(townRoot, beadID); bd != nil {
		if mrs, err := bd.FindMRsForIssue(beadID, true); err == nil {
			if prior := selectPriorAttempt(mrs); prior != nil {
				return prior
			}
		}
	}
	return findPriorBranch(townRoot, beadID)
}

// findPriorBranch looks for a pushed polecat branch carrying this bead's work.
//
// MR beads do not outlive the work they describe: the wisp reaper deletes them,
// so "no MR" is not evidence that nothing was done. The branch on the remote is
// the durable artifact — gt-wlco's polecat/foundation/gt-wlco+msz8v9e1 survived
// its bead being reopened AND its MR being rejected — so it is the last line of
// defence against dispatching a second polecat onto finished work.
//
// A surviving branch also means the work has NOT landed, so no ancestry check is
// needed: the refinery deletes polecat/* from origin on a successful merge
// (engineer.go, "work branches that should never persist after merge"). That is
// also why this listing stays small — 2 refs on gastown at the time of writing.
func findPriorBranch(townRoot, beadID string) *priorAttempt {
	refs, ok := polecatRemoteRefs(townRoot, beadID)
	if !ok {
		return nil
	}
	return selectPriorBranch(refs, beadID)
}

// selectPriorBranch picks the branch among remote polecat refs that encodes
// this bead's ID. Later matches win, mirroring selectPriorAttempt: when a bead
// has been worked more than once, the most recent push is the one to build on.
func selectPriorBranch(refs []git.RemoteRef, beadID string) *priorAttempt {
	var best *priorAttempt
	for _, ref := range refs {
		branch := strings.TrimPrefix(ref.Name, "refs/heads/")
		meta, parsed := polecat.ParseBranchName(branch)
		if !parsed || meta.Issue != beadID {
			continue
		}
		best = &priorAttempt{
			Source:    priorSourceBranch,
			Branch:    branch,
			CommitSHA: ref.Hash,
		}
	}
	return best
}

// polecatRemoteRefsCache memoises the ls-remote listing per rig. Dispatch runs
// this lookup for every bead, including the common case of a bead nobody has
// ever worked, and one network round trip per bead would tax batch sling for no
// benefit. The TTL keeps a long-lived daemon from going stale.
var (
	polecatRemoteRefsMu    sync.Mutex
	polecatRemoteRefsCache = map[string]polecatRefsEntry{}
)

const polecatRemoteRefsTTL = 60 * time.Second

type polecatRefsEntry struct {
	refs []git.RemoteRef
	at   time.Time
	ok   bool
}

// polecatRemoteRefs lists refs/heads/polecat/* on the push target of the rig
// repo the bead routes to. The push target, not the fetch URL: with a
// fork-backed remote those differ, and the branch was written to the push side.
func polecatRemoteRefs(townRoot, beadID string) ([]git.RemoteRef, bool) {
	rigPath := beads.GetRigPathForPrefix(townRoot, beads.ExtractPrefix(beadID))
	if rigPath == "" {
		return nil, false
	}

	polecatRemoteRefsMu.Lock()
	defer polecatRemoteRefsMu.Unlock()
	if entry, cached := polecatRemoteRefsCache[rigPath]; cached && time.Since(entry.at) < polecatRemoteRefsTTL {
		return entry.refs, entry.ok
	}

	refs, err := git.NewGit(rigPath).ListPushRemoteRefsWithHashes("origin", "refs/heads/polecat/")
	entry := polecatRefsEntry{refs: refs, at: time.Now(), ok: err == nil}
	polecatRemoteRefsCache[rigPath] = entry
	return entry.refs, entry.ok
}

// checkPriorWorkGuard refuses to dispatch a bead whose work is already queued
// in the merge queue, and reports (without blocking) prior work that is
// stranded. Returns a non-nil error only for the refusal case.
//
// force mirrors --force: the caller has decided the queued MR is not the work
// they mean, so let them through.
func checkPriorWorkGuard(townRoot, beadID string, force bool) (*priorAttempt, error) {
	prior := findPriorAttempt(townRoot, beadID)
	if prior == nil {
		return nil, nil
	}
	if prior.Open() && !force {
		return prior, fmt.Errorf("%s", duplicateDispatchMessage(beadID, prior))
	}
	return prior, nil
}

// duplicateDispatchMessage explains the refusal in terms of what exists and
// what to do about it, rather than just naming a rule.
func duplicateDispatchMessage(beadID string, p *priorAttempt) string {
	var b strings.Builder
	fmt.Fprintf(&b, "refusing to sling %s: work for this bead is already in the merge queue\n", beadID)
	fmt.Fprintf(&b, "  merge request: %s (open)\n", p.MRID)
	fmt.Fprintf(&b, "  branch:        %s\n", p.Branch)
	if p.CommitSHA != "" {
		fmt.Fprintf(&b, "  commit:        %s\n", p.CommitSHA)
	}
	b.WriteString("\nDispatching now means a second polecat redoes committed work.\n")
	fmt.Fprintf(&b, "Check it with: gt mq status %s\n", p.MRID)
	b.WriteString("If that MR is genuinely dead, close it first, or re-sling with --force.")
	return b.String()
}

// describePriorAttempt is the one-line dispatch-time notice for prior work that
// does not block. It names the MR state so the reader can tell a stranded
// branch from a queued one.
func describePriorAttempt(p *priorAttempt) string {
	if p == nil || p.Branch == "" {
		return ""
	}
	if p.Merged() {
		return fmt.Sprintf("Prior attempt %s already MERGED as %s — verify this bead still needs work", p.MRID, p.Branch)
	}
	if p.Source == priorSourceBranch {
		return fmt.Sprintf("Prior work already pushed to %s (%s), no merge request — context injected for polecat", p.Branch, shortSHA(p.CommitSHA))
	}
	return fmt.Sprintf("Prior attempt %s (%s) on %s — context injected for polecat", p.MRID, priorAttemptStatusLabel(p), p.Branch)
}

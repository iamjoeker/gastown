package witness

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
)

// The backward half of stranded-work detection (gt-by1e).
//
// internal/refinery/stranded_reject.go closes the FORWARD hole: when the
// engineer's automatic rejection leaves a closed source issue under an unmerged
// branch, it files a report. That stops NEW strandings from being silent. It
// does nothing about the ones already made, and it only sees the one route it
// sits on. Six strandings in a single session on 2026-08-19 arrived by six
// different routes — a shared bead auto-closed under a pushed branch, a polecat
// closing its own bead before submitting, an MR rejected after the bead closed,
// a bead closed with no MR ever created, and twice a `gt done` that refused to
// submit and left no trace of the refusal. Five were found by an agent choosing
// to look; one by hand. Zero by tooling.
//
// All six present identically: a branch on origin that is not contained in the
// target. That is what this sweep looks for, and looking for the SHAPE rather
// than the route is the whole point — it does not need to know how the state
// was reached, so it catches routes nobody has thought of yet.
//
// Two rules constrain it, and both are load-bearing.
//
// FIRST: ancestry proves containment one way only. A branch that is not an
// ancestor of the target may still have landed by squash or cherry-pick. That
// is exactly what gt-aqk turned out to be: superseded by gt-u5c, net
// contribution to the target measured at zero. A sweep that reported it as lost
// work would be wrong, so containment is decided by
// git.PushRemoteRefTargetStatus, which tries ancestry, then a no-op merge tree,
// then patch identity (`git cherry`) before concluding a branch is unmerged.
//
// SECOND, and it follows from the first: this CANNOT distinguish stranded from
// superseded. Both present as "branch on origin, not contained, bead closed".
// The disambiguator is a rehearsal merge — resolve to the target's side and
// measure the residual — which is expensive and belongs with a human or the
// refinery. So the sweep produces a SHORT LIST of branches to CHECK. It never
// says work is lost, and it writes nothing: no beads filed, no issues reopened,
// no branches deleted. A sweep that cried stranding at every superseded branch
// would be ignored within a day, and then it would catch nothing.

// THIRD, added by gt-l65a: "landed" is two different situations wearing one
// name, and the difference decides who — if anyone — can act on the branch.
//
// The git-hygiene plugin deletes a remote branch when
// `git merge-base --is-ancestor origin/<branch> origin/main` succeeds. Ancestry
// is the ONLY containment proof it acts on. This sweep proves containment three
// ways: ancestry, an empty merge, and patch identity. So a branch that was
// rebased before landing — its patch in the target, its commit never in the
// target's history — is landed, is not an ancestor, and is therefore reachable
// by nothing: hygiene will not delete it because it is not an ancestor, and the
// short list will not raise it because it is not a check. It is reported as
// landed on every future sweep, forever, by a tool whose value depends on its
// output being short.
//
// And it is the SAFEST deletion candidate in the whole listing, which is the
// part worth sitting with, because the classes imply the opposite priority:
//
//	landed by patch identity  `git cherry` '-'. Patch-id EQUALITY. The RELIABLE
//	                          direction: the same patch is provably in the
//	                          target. Positive evidence, cheap, and it scales.
//
//	the check rows            `git cherry` '+'. Patch-id INEQUALITY. The
//	                          UNRELIABLE direction: could be unmerged, could be
//	                          superseded by a different implementation. Settling
//	                          it means inspecting content by hand.
//
// So the branch carrying the strongest evidence of redundancy got the least
// attention. The fix is routing, not classification — the classification was
// already right, and the distinction was already computed and thrown away.
// HygieneUnreachable carries it to the output, and `gt patrol branches
// --deletable` is the short list an operator can act on. Nothing here deletes:
// these are shared remote refs, and emitting evidence rather than verdicts is
// the whole design.

// branchHygieneEvidence is the one containment proof the git-hygiene plugin
// acts on (plugins/git-hygiene/run.sh, "Delete merged remote branches"). Keep
// this in step with that check: containment proved any other way is invisible
// to hygiene, and this constant is what says so.
const branchHygieneEvidence = "ancestor"

// BranchSweepClass is the verdict for one unmerged branch. The classes exist to
// separate the cases that are cheaply explainable from the ones that need a
// person, so the short list stays short.
type BranchSweepClass string

const (
	// BranchSweepLanded means the branch's content is in the target, by
	// ancestry, by an empty merge, or by patch identity. No decision is
	// needed — but "no decision" is not "nothing to do": only the ancestry
	// case is collected by branch hygiene, so check HygieneUnreachable before
	// reading a landed row as finished.
	BranchSweepLanded BranchSweepClass = "landed"

	// BranchSweepQueued means an open merge request is holding this branch.
	// The work has a path to land; it is queued, not stranded. On the night
	// this sweep was specified, that alone cleared 4 of 7 hits.
	BranchSweepQueued BranchSweepClass = "queued"

	// BranchSweepActive means the work bead is still non-terminal, so it can
	// still be re-slung. Nothing is stranded while a bead can be dispatched.
	BranchSweepActive BranchSweepClass = "active"

	// BranchSweepReported means an open stranded-rejection report already
	// names this issue. The forward path (gt-h1cw) got here first; re-listing
	// it as a fresh finding would double-count a decision already queued.
	BranchSweepReported BranchSweepClass = "reported"

	// BranchSweepCheck is the short list: unmerged content, no open MR, a
	// terminal bead, and nobody has reported it. Needs a human decision — it
	// is NOT a claim that work was lost.
	BranchSweepCheck BranchSweepClass = "check"

	// BranchSweepUnknown means the branch could not be classified because a
	// git or beads lookup failed. It is deliberately not folded into any
	// other class: "I could not tell" must never read as "nothing to see".
	BranchSweepUnknown BranchSweepClass = "unknown"

	// BranchSweepSuperseded means somebody already settled this branch AT THIS
	// TIP and wrote the derivation down (gt-8xcg). It is the one class this
	// sweep does not compute: every other verdict is re-derived from the
	// repository on each run, and that is exactly the problem — the same 21
	// branches were re-derived on every cycle because a correct answer had
	// nowhere to live.
	//
	// It replaces whatever the branch would otherwise have been, and the
	// replaced class travels on in UnderlyingClass so the measurement is not
	// lost, only routed. Suppression is conditional on the marker still naming
	// the listed tip: a branch pushed to after being marked is NOT superseded,
	// and says so loudly.
	BranchSweepSuperseded BranchSweepClass = "superseded"
)

// NeedsAttention reports whether a class belongs on the short list an operator
// actually reads. Unknown counts: an unclassifiable branch is a question, and
// answering it silently with "fine" is the failure mode this sweep exists to
// end.
func (c BranchSweepClass) NeedsAttention() bool {
	return c == BranchSweepCheck || c == BranchSweepUnknown
}

// BranchSweepFinding is one branch on the remote and everything cheap that is
// known about it. The four fields the specification asks for — branch, bead,
// bead status, MR and its status — are here because together they separate most
// cases without a rehearsal merge.
type BranchSweepFinding struct {
	Branch    string `json:"branch"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Polecat   string `json:"polecat,omitempty"`

	IssueID     string `json:"issue_id,omitempty"`
	IssueStatus string `json:"issue_status,omitempty"`

	MRID          string `json:"mr_id,omitempty"`
	MRStatus      string `json:"mr_status,omitempty"`
	MRCloseReason string `json:"mr_close_reason,omitempty"`

	Class BranchSweepClass `json:"class"`

	// Evidence names how containment was decided when Class is landed:
	// "ancestor", "merge_tree_noop" or "cherry". Empty when the branch is not
	// contained, because there is no positive evidence to name.
	Evidence string `json:"evidence,omitempty"`

	// ContainedIn names which target held the content, when more than one was
	// checked. "Landed" is not a fact about the repository until you know
	// which trunk it landed on.
	ContainedIn string `json:"contained_in,omitempty"`

	// HygieneUnreachable marks landed content that branch hygiene will never
	// delete: the branch is contained in the target, but not by ancestry, and
	// ancestry is the only proof hygiene acts on. Nothing else routes it either
	// — it is not on the short list, because it is not a check — so it is a
	// permanent row in a listing whose worth is its shortness (gt-l65a).
	//
	// It is deliberately NOT omitempty. A false here means "landed, and hygiene
	// has it", which is a measurement; an absent key would read the same as a
	// row from a version that never looked.
	HygieneUnreachable bool `json:"hygiene_unreachable"`

	// UnpreservedPatches is the `git cherry` count of patches on the branch
	// that are not on the target. It is a size hint for triage, not a verdict.
	UnpreservedPatches int `json:"unpreserved_patches,omitempty"`

	// ReportBead names the open stranded-rejection report that already covers
	// this issue, when Class is reported.
	ReportBead string `json:"report_bead,omitempty"`

	// Note explains the class in one sentence, for the human column.
	Note string `json:"note,omitempty"`

	// Err records a lookup that failed for this branch specifically.
	Err string `json:"error,omitempty"`

	// Superseded is the durable settlement marker read out of git, when one
	// applies to this branch at this tip. Its presence is what moves Class to
	// superseded; its Reason is the derivation that would otherwise have been
	// re-done.
	Superseded *git.SupersededMark `json:"superseded,omitempty"`

	// UnderlyingClass is what the branch would have been classified as if it
	// carried no marker. It is recorded because suppressing a row must not
	// destroy the measurement behind it: an operator auditing the markers needs
	// to know whether a settled branch was a check or a landed one, and a
	// marker that erased that would be indistinguishable from one hiding a
	// stranding.
	UnderlyingClass BranchSweepClass `json:"underlying_class,omitempty"`

	// SupersededStale marks a branch that HAS a marker which no longer applies:
	// the tip moved after it was written. The row is NOT suppressed — the
	// settlement was about content that is no longer there — and this field is
	// what lets the reader be told why a marked branch is still on the list,
	// rather than concluding the marker was ignored.
	SupersededStale bool `json:"superseded_stale,omitempty"`
}

// BranchSweepResult is the whole sweep.
type BranchSweepResult struct {
	Remote string `json:"remote"`
	// Target is the primary comparison ref — the one quoted in guidance — and
	// is always fully qualified. A bare "main" resolves to whichever ref git
	// finds first, and on a fork-backed rig that can be a trunk hundreds of
	// commits behind the one work actually lands on.
	Target string `json:"target"`
	// Targets is every ref containment was tested against, primary first.
	// Recording it keeps a reader from having to trust that the right trunk
	// was chosen: a short list is only interpretable next to what it was
	// measured against.
	Targets []string `json:"targets,omitempty"`

	Scanned  int                  `json:"scanned"`
	Findings []BranchSweepFinding `json:"findings"`

	// Errors are sweep-wide failures (listing refs, listing MRs). A sweep that
	// could not list merge requests still classifies, but every "no MR" in it
	// is unmeasured rather than measured, so the reader must be told.
	Errors []string `json:"errors,omitempty"`

	// MRsMeasured is false when the merge-request listing failed. Without it,
	// an absent MR is not evidence there is no MR.
	MRsMeasured bool `json:"mrs_measured"`

	// MarksMeasured is false when the superseded markers could not be read.
	// Without it a sweep that lost its markers looks exactly like a rig that
	// has none — and the two differ by 21 rows on the one that motivated them.
	// A failed read over-reports rather than under-reports, which is the safe
	// direction, but the reader still has to be told which they are looking at.
	MarksMeasured bool `json:"marks_measured"`
}

// AttentionCount is the size of the short list.
func (r *BranchSweepResult) AttentionCount() int {
	if r == nil {
		return 0
	}
	n := 0
	for _, f := range r.Findings {
		if f.Class.NeedsAttention() {
			n++
		}
	}
	return n
}

// SupersededCount is how many rows a marker settled on this run.
//
// It is reported alongside every other tally rather than folded away, because a
// suppressed row is still a measurement: "0 to check" out of 36 scanned means
// something different when 21 of them were settled by hand than when none were,
// and a summary that showed only the first number would make a marker look like
// a repository that fixed itself.
func (r *BranchSweepResult) SupersededCount() int {
	if r == nil {
		return 0
	}
	n := 0
	for _, f := range r.Findings {
		if f.Class == BranchSweepSuperseded {
			n++
		}
	}
	return n
}

// StaleMarkCount is how many branches carry a marker that no longer applies
// because the tip moved after it was written. Those rows are on the short list,
// not off it, and this is what says so out loud: a marked branch that is still
// being reported looks like a marker that was ignored unless the reason is
// named.
func (r *BranchSweepResult) StaleMarkCount() int {
	if r == nil {
		return 0
	}
	n := 0
	for _, f := range r.Findings {
		if f.SupersededStale {
			n++
		}
	}
	return n
}

// HygieneUnreachableCount is how many landed branches nothing will ever delete.
//
// It is counted apart from AttentionCount because the two ask for different
// things. A check row asks for a DECISION — superseded or stranded, and the
// sweep cannot tell. These rows need no decision: containment is already
// proved. They ask for a DELETION, and they will keep asking on every sweep
// until someone performs it.
//
// A branch settled by a marker is NOT counted, and the row keeps
// HygieneUnreachable set: the measurement is still true — hygiene still cannot
// delete it — but the deletion it was asking for has been answered. On the rig
// that motivated the marker the answer was explicitly "keep the branch": the
// commits are the only remaining copy of the work AS ORIGINALLY AUTHORED, since
// the substance landed via different commits. Counting it would go on demanding
// a deletion that has been ruled out, which is how a listing gets ignored.
func (r *BranchSweepResult) HygieneUnreachableCount() int {
	if r == nil {
		return 0
	}
	n := 0
	for _, f := range r.Findings {
		if f.HygieneUnreachable && f.Class != BranchSweepSuperseded {
			n++
		}
	}
	return n
}

// CountByClass tallies findings per class, for a one-line summary.
func (r *BranchSweepResult) CountByClass() map[BranchSweepClass]int {
	counts := map[BranchSweepClass]int{}
	if r == nil {
		return counts
	}
	for _, f := range r.Findings {
		counts[f.Class]++
	}
	return counts
}

// BranchSweepGit is the slice of git this sweep needs.
type BranchSweepGit interface {
	// ListPushRemoteRefsWithHashes must read the PUSH url. Split fetch/push
	// remotes are configured on these rigs, and resolving branches against the
	// fetch side gives a false clean: the branch was written to the push side.
	ListPushRemoteRefsWithHashes(remote, prefix string) ([]git.RemoteRef, error)
	// PushRemoteRefTargetStatusAny fetches the exact listed hash before
	// deciding, so a remote-only tip is classified against what is actually on
	// the remote rather than against a stale tracking ref. It takes a LIST of
	// targets because a rig whose origin is a fork has two refs that are
	// honestly the trunk, and comparing against only one of them reports
	// everything the other has as unmerged work.
	PushRemoteRefTargetStatusAny(remote string, ref git.RemoteRef, targets []string) (git.BranchPreservationStatus, error)
}

// BranchSweepBeads is the slice of beads this sweep needs. Every method is a
// read: the sweep has no write path by construction, not by discipline.
type BranchSweepBeads interface {
	Show(id string) (*beads.Issue, error)
	ListMergeRequests(opts beads.ListOptions) ([]*beads.Issue, error)
	Search(opts beads.SearchOptions) ([]*beads.Issue, error)
}

// BranchSweepOptions parameterises one sweep.
type BranchSweepOptions struct {
	// Remote is the remote whose push url is listed. Defaults to "origin".
	Remote string
	// Targets are the resolved comparison refs, primary first, e.g.
	// ["origin/main", "upstream/main"]. Callers must fully qualify them: a
	// bare branch name resolves ambiguously, and on a fork-backed rig it can
	// resolve to a trunk hundreds of commits behind the live one, which turns
	// landed work into apparent strandings.
	//
	// Containment in ANY of them counts as landed. Passing more than one is
	// how a rig with both an origin trunk and an upstream trunk avoids
	// reporting everything one has and the other lacks.
	Targets []string
	// Prefix limits which refs are swept. Defaults to refs/heads/polecat/.
	Prefix string

	// Superseded are the durable settlement markers, keyed by branch name
	// (without refs/heads/). They are passed IN rather than read here so the
	// sweep stays a pure classifier over facts the caller gathered, and so a
	// failure to read them is reported by the caller as an unmeasured column
	// rather than silently becoming "no branch is settled".
	//
	// A marker only suppresses when it names the tip the sweep actually listed.
	Superseded map[string]git.SupersededMark
}

const defaultPolecatRefPrefix = "refs/heads/polecat/"

// SweepUnmergedPolecatBranches lists polecat branches on the remote and
// classifies each one. It reads; it never writes.
//
// bd may be nil: the git half still runs, and every branch that is not
// contained in the target comes back unknown rather than silently clean.
func SweepUnmergedPolecatBranches(g BranchSweepGit, bd BranchSweepBeads, opts BranchSweepOptions) (*BranchSweepResult, error) {
	remote := strings.TrimSpace(opts.Remote)
	if remote == "" {
		remote = "origin"
	}
	prefix := strings.TrimSpace(opts.Prefix)
	if prefix == "" {
		prefix = defaultPolecatRefPrefix
	}
	var targets []string
	for _, t := range opts.Targets {
		if t = strings.TrimSpace(t); t != "" {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("branch sweep needs at least one resolved target ref (e.g. origin/main); a bare branch name resolves ambiguously on fork-backed rigs")
	}
	if g == nil {
		return nil, fmt.Errorf("branch sweep needs a git handle")
	}

	result := &BranchSweepResult{Remote: remote, Target: targets[0], Targets: targets}

	refs, err := g.ListPushRemoteRefsWithHashes(remote, prefix)
	if err != nil {
		// Listing is the sweep. Without it there is no result to report on,
		// and returning an empty one would read as "no unmerged branches".
		return nil, fmt.Errorf("listing %s on the push url of %s: %w", prefix, remote, err)
	}

	mrsByBranch, mrErr := loadMRsByBranch(bd)
	result.MRsMeasured = mrErr == nil && bd != nil
	if mrErr != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("listing merge requests: %v (every 'no MR' below is UNMEASURED)", mrErr))
	} else if bd == nil {
		result.Errors = append(result.Errors, "no beads handle: bead and MR columns are UNMEASURED")
	}

	// remoteDown records the first deadline kill. A remote that has stopped
	// answering has not stopped answering for ONE branch: comparison fetches a
	// candidate ref per branch, so the network deadline that ends the hang is
	// paid once per branch, and a sweep of nine against a blackholed remote
	// costs nine full deadlines. That is the same witness, parked for nearly as
	// long, by a fix — the timeout would have RELOCATED the stall rather than
	// removed it. So the first kill is read as a fact about the remote and the
	// rest are classified without touching the network.
	//
	// They are classified UNKNOWN, never landed and never check: declining to
	// ask must produce a smaller bill, never a verdict.
	var remoteDown string
	for _, ref := range refs {
		if !strings.HasPrefix(ref.Name, "refs/heads/") {
			continue
		}
		result.Scanned++
		if remoteDown != "" {
			notCompared := notComparedFinding(ref, remoteDown)
			applySupersededMark(&notCompared, ref, opts.Superseded)
			result.Findings = append(result.Findings, notCompared)
			continue
		}
		// Landed branches are returned too — the caller decides whether to
		// show them, and their presence is what makes a zero short list
		// interpretable rather than merely empty.
		finding, cmpErr := classifyBranch(g, bd, remote, targets, ref, mrsByBranch, result.MRsMeasured)
		if git.IsRemoteUnresponsive(cmpErr) {
			remoteDown = cmpErr.Error()
			result.Errors = append(result.Errors, fmt.Sprintf(
				"%s stopped responding while comparing %s: %v — every branch after it was classified UNKNOWN WITHOUT being compared, so this sweep is a partial measurement",
				remote, strings.TrimPrefix(ref.Name, "refs/heads/"), cmpErr))
		}
		applySupersededMark(&finding, ref, opts.Superseded)
		result.Findings = append(result.Findings, finding)
	}

	sortFindings(result.Findings)
	return result, nil
}

// classifyBranch decides one branch.
//
// Every finding carries all four columns — branch, bead, bead status, MR and
// its status — whatever its class, so the facts are gathered before the class
// is decided rather than only along the path that happens to need them. A
// `queued` row with a blank bead status reads as "the bead is gone", which is a
// different and much worse claim than "we did not look".
//
// The classification tests themselves run containment first, because it is the
// only one that can prove there is nothing to do, then the cheap explanations,
// then the short list.
//
// The second return is the raw containment error, unwrapped, and it exists for
// exactly one caller decision: a network deadline kill is a fact about the
// REMOTE, and the sweep must be able to see that through the finding's prose.
// It is nil whenever the comparison ran, whatever the class.
func classifyBranch(
	g BranchSweepGit,
	bd BranchSweepBeads,
	remote string,
	targets []string,
	ref git.RemoteRef,
	mrsByBranch map[string]*branchMR,
	mrsMeasured bool,
) (BranchSweepFinding, error) {
	branch := strings.TrimPrefix(ref.Name, "refs/heads/")
	finding := BranchSweepFinding{Branch: branch, CommitSHA: ref.Hash}
	if meta, ok := polecat.ParseBranchName(branch); ok {
		finding.Polecat = meta.Polecat
		finding.IssueID = meta.Issue
	}

	// An MR explains a branch whether or not it landed, and it also names the
	// source issue for branches whose name does not encode one.
	mr := mrsByBranch[branch]
	if mr != nil {
		finding.MRID = mr.ID
		finding.MRStatus = mr.Status
		finding.MRCloseReason = mr.CloseReason
		if finding.IssueID == "" {
			finding.IssueID = mr.SourceIssue
		}
	}

	var issue *beads.Issue
	var issueErr error
	if bd != nil && finding.IssueID != "" {
		issue, issueErr = bd.Show(finding.IssueID)
		if issueErr == nil && issue != nil {
			finding.IssueStatus = strings.TrimSpace(issue.Status)
		}
	}

	status, err := g.PushRemoteRefTargetStatusAny(remote, ref, targets)
	if err != nil {
		// Could not compare. Not clean, not stranded — unknown, and said so.
		//
		// The reason travels in the NOTE and not only in Err, because the human
		// table shows the note alone. "could not compare" on its own is what let
		// a fetch-level fault (gt-880s) read as a property of the branch: the
		// class is the same for a tip that moved, a store that would not answer
		// and a git that would not run, and those want different responses.
		finding.Class = BranchSweepUnknown
		finding.Err = err.Error()
		finding.Note = "could not compare against " + strings.Join(targets, " or ") + ": " + err.Error()
		return finding, err
	}
	finding.Evidence = status.Evidence
	finding.UnpreservedPatches = status.UnpreservedPatchCount

	if status.Preserved {
		finding.Class = BranchSweepLanded
		// Which target held it is part of the finding: with more than one
		// trunk in play, "landed" is incomplete without saying landed where.
		finding.ContainedIn = strings.TrimSpace(status.ComparisonBase)
		if finding.ContainedIn == "" {
			finding.ContainedIn = targets[0]
		}
		// How containment was proved decides who can act on the branch, so it
		// is recorded as a routing fact and not only as a label. Anything that
		// is not ancestry — an empty merge, patch identity, or an evidence
		// string this build does not recognise — is out of hygiene's reach; the
		// safe direction for an unrecognised value is to name the branch rather
		// than assume something else will collect it.
		finding.HygieneUnreachable = strings.TrimSpace(status.Evidence) != branchHygieneEvidence
		finding.Note = "content is in " + finding.ContainedIn + " (" + evidenceLabel(status.Evidence) + ")"
		if finding.HygieneUnreachable {
			finding.Note += "; NOT an ancestor of " + finding.ContainedIn + " — branch hygiene cannot delete it"
		}
		if mr != nil && mr.Open() {
			finding.Note += "; open MR " + mr.ID + " is still queued for content that already landed"
		}
		return finding, nil
	}

	if mr != nil && mr.Open() {
		finding.Class = BranchSweepQueued
		finding.Note = "open MR " + mr.ID + " — queued, not stranded"
		return finding, nil
	}

	if bd == nil {
		finding.Class = BranchSweepUnknown
		finding.Note = "unmerged, and bead state was not measured"
		return finding, nil
	}

	if finding.IssueID == "" {
		finding.Class = BranchSweepCheck
		finding.Note = "unmerged, and no work bead could be identified from the branch name or an MR"
		return finding, nil
	}

	switch {
	case issueErr != nil && isBeadNotFound(issueErr):
		finding.Class = BranchSweepCheck
		finding.Note = "unmerged, and work bead " + finding.IssueID + " no longer exists"
		return finding, nil
	case issueErr != nil:
		finding.Class = BranchSweepUnknown
		finding.Err = issueErr.Error()
		finding.Note = "unmerged, and bead " + finding.IssueID + " could not be read"
		return finding, nil
	case issue == nil:
		finding.Class = BranchSweepCheck
		finding.Note = "unmerged, and work bead " + finding.IssueID + " no longer exists"
		return finding, nil
	}

	if !beads.IssueStatus(finding.IssueStatus).IsTerminal() {
		// Open, hooked, in_progress, blocked, deferred — all re-slingable, so
		// the branch has a route back to work. Same reasoning the forward path
		// uses to decide a rejection stranded nothing.
		finding.Class = BranchSweepActive
		finding.Note = "bead is " + finding.IssueStatus + " — still re-slingable"
		return finding, nil
	}

	if report := findOpenStrandedReport(bd, finding.IssueID); report != "" {
		finding.Class = BranchSweepReported
		finding.ReportBead = report
		finding.Note = "already reported as stranded by " + report
		return finding, nil
	}

	finding.Class = BranchSweepCheck
	finding.Note = branchCheckNote(finding, mrsMeasured)
	return finding, nil
}

// applySupersededMark folds a durable settlement marker into a finding.
//
// Three outcomes, and the middle one is the whole reason the marker records a
// commit at all:
//
//   - No marker: the finding is untouched. This is every branch on a rig where
//     nobody has settled anything, so it must cost nothing and change nothing.
//
//   - A marker naming a DIFFERENT tip than the one just listed: the branch was
//     pushed to after it was settled, so the settlement was about content that
//     is no longer there. The row keeps its computed class and goes on the short
//     list, and SupersededStale is what stops that reading as "the marker was
//     ignored". A marker that suppressed by branch NAME would hide live work
//     behind a verdict about a commit nobody is looking at any more — the one
//     failure mode that would make this feature worse than the noise it removes.
//
//   - A marker naming the listed tip: the class becomes superseded, the
//     computed class is preserved in UnderlyingClass, and the reason travels in
//     the note. Nothing is deleted and nothing is recomputed; the row simply
//     stops asking a question that has been answered, in the words of whoever
//     answered it.
//
// It never marks a finding superseded on its own judgement. Every suppression
// here is the replay of a decision a person or agent already made and wrote
// down, which is the only kind of suppression this sweep can afford.
func applySupersededMark(finding *BranchSweepFinding, ref git.RemoteRef, marks map[string]git.SupersededMark) {
	if finding == nil || len(marks) == 0 {
		return
	}
	mark, ok := marks[finding.Branch]
	if !ok {
		return
	}

	if mark.StaleFor(ref.Hash) {
		finding.SupersededStale = true
		finding.Note += supersededStaleNote(mark, ref.Hash)
		return
	}

	finding.UnderlyingClass = finding.Class
	finding.Class = BranchSweepSuperseded
	markCopy := mark
	finding.Superseded = &markCopy
	finding.Note = supersededNote(mark, finding.UnderlyingClass)
}

// supersededNote is the human column for a settled branch: the reason first,
// because the reason is the artifact, then who settled it and when, then what
// the sweep would have said without it.
func supersededNote(mark git.SupersededMark, underlying BranchSweepClass) string {
	var b strings.Builder
	b.WriteString("settled: ")
	reason := strings.TrimSpace(mark.Reason)
	if reason == "" {
		// A marker whose blob would not parse still suppresses — the ref is the
		// decision — but it must not pretend to carry a derivation it lost.
		reason = "(marker is present but its reason could not be read)"
	}
	b.WriteString(reason)
	if by := strings.TrimSpace(mark.MarkedBy); by != "" {
		b.WriteString(" [by ")
		b.WriteString(by)
		if at := strings.TrimSpace(mark.MarkedAt); at != "" {
			b.WriteString(" on ")
			b.WriteString(at)
		}
		b.WriteString("]")
	} else if at := strings.TrimSpace(mark.MarkedAt); at != "" {
		b.WriteString(" [on ")
		b.WriteString(at)
		b.WriteString("]")
	}
	if underlying != "" {
		b.WriteString("; would otherwise be ")
		b.WriteString(string(underlying))
	}
	return b.String()
}

// supersededStaleNote explains why a marked branch is STILL on the list.
func supersededStaleNote(mark git.SupersededMark, tip string) string {
	marked := strings.TrimSpace(mark.Commit)
	if marked == "" {
		marked = "an unrecorded commit"
	} else {
		marked = shortSHA(marked)
	}
	return " — NOTE: a superseded marker exists but names " + marked +
		", and the tip is now " + shortSHA(tip) +
		", so the branch was pushed to after it was settled and the marker does NOT apply"
}

// shortSHA abbreviates for the human column without hiding that it is a SHA.
func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "(no commit)"
	}
	return sha
}

// notComparedFinding is the row for a branch the sweep deliberately did not ask
// the remote about, after an earlier branch's fetch was killed on its deadline.
//
// Everything it does not know is left EMPTY rather than filled with a plausible
// default. A blank bead column reads as "we did not look", which is what
// happened; a landed or check verdict here would be a claim manufactured out of
// a network failure, and the class exists precisely so that "I could not tell"
// never renders as "nothing to see".
func notComparedFinding(ref git.RemoteRef, cause string) BranchSweepFinding {
	branch := strings.TrimPrefix(ref.Name, "refs/heads/")
	finding := BranchSweepFinding{
		Branch:    branch,
		CommitSHA: ref.Hash,
		Class:     BranchSweepUnknown,
		Err:       cause,
	}
	if meta, ok := polecat.ParseBranchName(branch); ok {
		finding.Polecat = meta.Polecat
		finding.IssueID = meta.Issue
	}
	finding.Note = "NOT COMPARED: the remote stopped responding earlier in this sweep (" + cause + ") — re-run when it answers"
	return finding
}

// branchCheckNote states what was observed, in terms that do not overclaim.
// "CHECK" is a request to look, not a verdict that work is lost — the sweep
// cannot tell a prematurely closed bead from a correctly closed one whose
// branch is redundant, and saying otherwise is how a detector gets ignored.
func branchCheckNote(f BranchSweepFinding, mrsMeasured bool) string {
	var b strings.Builder
	b.WriteString("bead ")
	b.WriteString(f.IssueID)
	b.WriteString(" is ")
	b.WriteString(f.IssueStatus)
	switch {
	case !mrsMeasured:
		b.WriteString(", MR state UNMEASURED")
	case f.MRID == "":
		b.WriteString(", no MR was ever created")
	default:
		b.WriteString(", MR ")
		b.WriteString(f.MRID)
		b.WriteString(" is ")
		b.WriteString(f.MRStatus)
		if f.MRCloseReason != "" {
			b.WriteString(" (")
			b.WriteString(f.MRCloseReason)
			b.WriteString(")")
		}
	}
	b.WriteString(" — check whether this was superseded or stranded")
	return b.String()
}

func evidenceLabel(evidence string) string {
	switch strings.TrimSpace(evidence) {
	case "ancestor":
		return "ancestor"
	case "merge_tree_noop":
		return "empty merge"
	case "cherry":
		return "same patches, squashed or cherry-picked"
	case "":
		return "contained"
	default:
		return evidence
	}
}

// branchMR is the useful part of a merge-request bead for this sweep.
type branchMR struct {
	ID          string
	Status      string
	Branch      string
	SourceIssue string
	CloseReason string
}

// Open reports whether the MR still holds a slot in the queue.
func (m *branchMR) Open() bool {
	return m != nil && !strings.EqualFold(strings.TrimSpace(m.Status), "closed")
}

// loadMRsByBranch indexes every merge request, open and closed, by its branch.
//
// One listing rather than a lookup per branch: the sweep runs on a patrol
// cadence and each lookup is a Dolt round trip. Closed MRs are included because
// "the MR was rejected" is one of the routes that produces this state, and an
// open-only view reports it as "no MR ever created" — a different fault with a
// different fix.
//
// Within a branch, an open MR beats a closed one and later beats earlier,
// matching how submissions supersede.
func loadMRsByBranch(bd BranchSweepBeads) (map[string]*branchMR, error) {
	if bd == nil {
		return nil, nil
	}
	issues, err := bd.ListMergeRequests(beads.ListOptions{Status: "all", Label: "gt:merge-request"})
	if err != nil {
		return nil, err
	}
	byBranch := make(map[string]*branchMR, len(issues))
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		fields := beads.ParseMRFields(issue)
		if fields == nil || fields.Branch == "" {
			continue
		}
		candidate := &branchMR{
			ID:          issue.ID,
			Status:      issue.Status,
			Branch:      fields.Branch,
			SourceIssue: fields.SourceIssue,
			CloseReason: fields.CloseReason,
		}
		if existing := byBranch[fields.Branch]; existing != nil && existing.Open() && !candidate.Open() {
			continue
		}
		byBranch[fields.Branch] = candidate
	}
	return byBranch, nil
}

// findOpenStrandedReport returns the ID of an open stranded-rejection report
// naming this issue, or "" when there is none.
//
// A search failure returns "" — the finding then lands on the short list as a
// duplicate of something already reported, which wastes a reader's minute. The
// opposite failure would suppress a real stranding, so the bias is deliberate.
func findOpenStrandedReport(bd BranchSweepBeads, issueID string) string {
	if bd == nil || strings.TrimSpace(issueID) == "" {
		return ""
	}
	reports, err := bd.Search(beads.SearchOptions{
		Query:  issueID,
		Status: "open",
		Label:  "gt:bug",
		Limit:  20,
	})
	if err != nil {
		return ""
	}
	for _, report := range reports {
		if report == nil {
			continue
		}
		if beads.IsStrandedRejectTitleFor(report.Title, issueID) {
			return report.ID
		}
	}
	return ""
}

// isBeadNotFound distinguishes a bead that is gone from a store that could not
// be read. A missing bead is a finding; an unreadable store is not.
func isBeadNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, beads.ErrNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no such issue")
}

// sortFindings puts the short list first, then orders by branch so repeated
// runs are diffable.
func sortFindings(findings []BranchSweepFinding) {
	rank := map[BranchSweepClass]int{
		BranchSweepCheck:    0,
		BranchSweepUnknown:  1,
		BranchSweepReported: 2,
		BranchSweepQueued:   3,
		BranchSweepActive:   4,
		BranchSweepLanded:   5,
		// Last, and it is the point: a settled branch is the one row nobody
		// needs to read again.
		BranchSweepSuperseded: 6,
	}
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := rank[findings[i].Class], rank[findings[j].Class]
		if ri != rj {
			return ri < rj
		}
		return findings[i].Branch < findings[j].Branch
	})
}

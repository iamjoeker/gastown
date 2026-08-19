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

// BranchSweepClass is the verdict for one unmerged branch. The classes exist to
// separate the cases that are cheaply explainable from the ones that need a
// person, so the short list stays short.
type BranchSweepClass string

const (
	// BranchSweepLanded means the branch's content is in the target, by
	// ancestry, by an empty merge, or by patch identity. Nothing to do.
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

	for _, ref := range refs {
		if !strings.HasPrefix(ref.Name, "refs/heads/") {
			continue
		}
		result.Scanned++
		// Landed branches are returned too — the caller decides whether to
		// show them, and their presence is what makes a zero short list
		// interpretable rather than merely empty.
		result.Findings = append(result.Findings, classifyBranch(g, bd, remote, targets, ref, mrsByBranch, result.MRsMeasured))
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
func classifyBranch(
	g BranchSweepGit,
	bd BranchSweepBeads,
	remote string,
	targets []string,
	ref git.RemoteRef,
	mrsByBranch map[string]*branchMR,
	mrsMeasured bool,
) BranchSweepFinding {
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
		finding.Class = BranchSweepUnknown
		finding.Err = err.Error()
		finding.Note = "could not compare against " + strings.Join(targets, " or ")
		return finding
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
		finding.Note = "content is in " + finding.ContainedIn + " (" + evidenceLabel(status.Evidence) + ")"
		if mr != nil && mr.Open() {
			finding.Note += "; open MR " + mr.ID + " is still queued for content that already landed"
		}
		return finding
	}

	if mr != nil && mr.Open() {
		finding.Class = BranchSweepQueued
		finding.Note = "open MR " + mr.ID + " — queued, not stranded"
		return finding
	}

	if bd == nil {
		finding.Class = BranchSweepUnknown
		finding.Note = "unmerged, and bead state was not measured"
		return finding
	}

	if finding.IssueID == "" {
		finding.Class = BranchSweepCheck
		finding.Note = "unmerged, and no work bead could be identified from the branch name or an MR"
		return finding
	}

	switch {
	case issueErr != nil && isBeadNotFound(issueErr):
		finding.Class = BranchSweepCheck
		finding.Note = "unmerged, and work bead " + finding.IssueID + " no longer exists"
		return finding
	case issueErr != nil:
		finding.Class = BranchSweepUnknown
		finding.Err = issueErr.Error()
		finding.Note = "unmerged, and bead " + finding.IssueID + " could not be read"
		return finding
	case issue == nil:
		finding.Class = BranchSweepCheck
		finding.Note = "unmerged, and work bead " + finding.IssueID + " no longer exists"
		return finding
	}

	if !beads.IssueStatus(finding.IssueStatus).IsTerminal() {
		// Open, hooked, in_progress, blocked, deferred — all re-slingable, so
		// the branch has a route back to work. Same reasoning the forward path
		// uses to decide a rejection stranded nothing.
		finding.Class = BranchSweepActive
		finding.Note = "bead is " + finding.IssueStatus + " — still re-slingable"
		return finding
	}

	if report := findOpenStrandedReport(bd, finding.IssueID); report != "" {
		finding.Class = BranchSweepReported
		finding.ReportBead = report
		finding.Note = "already reported as stranded by " + report
		return finding
	}

	finding.Class = BranchSweepCheck
	finding.Note = branchCheckNote(finding, mrsMeasured)
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
	}
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := rank[findings[i].Class], rank[findings[j].Class]
		if ri != rj {
			return ri < rj
		}
		return findings[i].Branch < findings[j].Branch
	})
}

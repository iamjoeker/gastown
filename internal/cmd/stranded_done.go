package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// gt done refuses to create a merge request when the source issue is already
// closed (gt-7qm). The refusal is correct — a closed bead cannot carry work
// through the queue — but until gt-rbul it said nothing about the branch it was
// refusing on behalf of. The polecat read "Skipping MR creation" as procedural
// bookkeeping, `gt done` nuked the sandbox, and the commits sat on origin
// unmerged with a closed bead above them: a state every surface reads as
// finished.
//
// Two independent strandings on 2026-08-18 reached that state by different
// routes and neither was caught by tooling — both were found by a witness who
// chose to run an ancestry sweep:
//
//	COLLATERAL  another polecat's merge auto-closed a shared class bead 120
//	            seconds after this polecat pushed. Nobody did anything wrong.
//	ORDERING    the polecat closed its own bead, then ran gt done. The gate
//	            correctly refused.
//
// gt done already holds both facts it needs at the moment it refuses: the
// source issue is closed, AND the branch is N commits ahead of the target and
// not an ancestor of it. It refused on the first and never looked at the second.
//
// What it does about it follows the precedent gt-h1cw set on the refinery's
// rejection path (internal/refinery/stranded_reject.go): REPORT, do not
// correct. A closed bead under a live branch has two readings and gt done
// cannot tell them apart —
//
//	(a) the work landed by another route, the bead is correctly closed, and
//	    reopening it resurrects finished work;
//	(b) the bead closed prematurely underneath live work, which is stranded.
//
// Reopening the issue is only correct under (b), so gt done leaves the issue
// alone and files a durable, deduplicated bead for the rig's witness instead.
// The DONE_MR_REFUSED mail was already sent before this change and was the only
// trace both strandings left; mail nothing handles is not a surface, which is
// why the report is a bead.
//
// Ancestry PROVES reading (a) and so suppresses the report — but only one way.
// A non-ancestor branch may still have landed by squash or cherry-pick, so a
// definite "no", a probe error, a missing commit SHA and a nil git client all
// still report. The `git cherry` command that separates those cases is printed
// rather than run: it answers by patch-id, and gt done is not the place to make
// that call for a human.

// doneStrandingProbe is the slice of the git client this path needs.
type doneStrandingProbe interface {
	IsAncestor(ancestor, descendant string) (bool, error)
	CommitsAhead(base, branch string) (int, error)
}

// doneStranding is what the ancestry probe concluded about a refused branch.
type doneStranding struct {
	// BaseRef is the fully qualified ref the branch was compared against
	// (origin/main, upstream/main, ...).
	BaseRef string
	// AlreadyMerged is true only when ancestry proved the commit is contained
	// in BaseRef. Nothing is stranded and nothing is reported.
	AlreadyMerged bool
	// Stranded is true when the refusal left commits outside the target.
	Stranded bool
	// Ahead counts commits on the branch that BaseRef does not have. Valid
	// only when AheadKnown; the count is a detail of the warning, never the
	// thing that decides it.
	Ahead      int
	AheadKnown bool
	// ProbeErr records why the ancestry question could not be answered. It
	// never suppresses the report — an unanswerable probe strands by default.
	ProbeErr error
}

// assessDoneStranding answers whether refusing an MR for branch abandons work.
//
// It must report (Stranded) rather than stay quiet whenever it cannot prove
// containment: a false alarm costs a witness one `git merge-base` invocation,
// while silence costs the work.
func assessDoneStranding(g doneStrandingProbe, baseRef, branch, commitSHA string) doneStranding {
	st := doneStranding{BaseRef: strings.TrimSpace(baseRef)}

	if g == nil {
		st.Stranded = true
		st.ProbeErr = errors.New("no git client available")
		return st
	}
	if st.BaseRef == "" {
		st.Stranded = true
		st.ProbeErr = errors.New("no target ref to compare against")
		return st
	}
	commitSHA = strings.TrimSpace(commitSHA)
	if commitSHA == "" {
		st.Stranded = true
		st.ProbeErr = errors.New("no commit SHA recorded for the branch")
		return st
	}

	contained, err := g.IsAncestor(commitSHA, st.BaseRef)
	switch {
	case err != nil:
		st.Stranded = true
		st.ProbeErr = fmt.Errorf("ancestry probe against %s: %w", st.BaseRef, err)
	case contained:
		// Reading (a), proven. The work is in the target; the closed bead is
		// right and the branch is redundant.
		st.AlreadyMerged = true
		return st
	default:
		st.Stranded = true
	}

	if branch = strings.TrimSpace(branch); branch != "" {
		// A zero here contradicts the containment answer above — the branch ref
		// and the recorded HEAD have diverged, or the count failed silently.
		// Either way "0 commits ahead" would read as "no work", which is the
		// conclusion the branch disproves, so an implausible count is treated as
		// no count at all.
		if ahead, aheadErr := g.CommitsAhead(st.BaseRef, branch); aheadErr == nil && ahead > 0 {
			st.Ahead = ahead
			st.AheadKnown = true
		}
	}
	return st
}

// strandedDoneFiler is the slice of the beads client the report needs.
type strandedDoneFiler interface {
	CreateIfNoDuplicate(opts beads.CreateOptions) (*beads.Issue, bool, error)
	Update(id string, opts beads.UpdateOptions) error
}

// strandedDoneRequest describes the refusal being reported.
type strandedDoneRequest struct {
	IssueID   string
	Branch    string
	BaseRef   string
	CommitSHA string
	Worker    string
	Rig       string
	// Refusal is closedSourceIssueRefusal's sentence, carried verbatim so the
	// report says why no MR exists without the reader reconstructing it.
	Refusal string
}

// strandedDoneReport records what the report path managed to do. Every field is
// advisory: the completion proceeds either way, because failing to file a
// report must not turn a recoverable stranding into a failed gt done.
type strandedDoneReport struct {
	BeadID  string
	Created bool
	// Err is non-fatal and belongs in the operator's output, not in the exit
	// status.
	Err error
	// RigBindErr is set when the report could not be bound to the rig's
	// database and was filed unbound instead. The report exists; it is just
	// somewhere the rig's witness may not look.
	RigBindErr error
}

// strandedDoneTitle is deterministic in the issue and branch so a repeated
// gt done collapses onto the open report instead of filing a second one. It is
// also the string a witness greps for.
func strandedDoneTitle(issueID, branch string) string {
	return fmt.Sprintf("Stranded by gt done: %s is closed, branch %s left unmerged", issueID, branch)
}

// reportStrandedDone files the report. It never writes to the source issue:
// reopening is correct only under reading (b) and gt done cannot tell (b) from
// (a) — see the package comment above.
func reportStrandedDone(work strandedDoneFiler, req strandedDoneRequest) strandedDoneReport {
	var report strandedDoneReport
	if work == nil {
		report.Err = errors.New("no beads client available to file a stranded-work report")
		return report
	}
	issueID := strings.TrimSpace(req.IssueID)
	if issueID == "" {
		report.Err = errors.New("no source issue to report a stranding against")
		return report
	}

	opts := beads.CreateOptions{
		Title:       strandedDoneTitle(issueID, strings.TrimSpace(req.Branch)),
		Labels:      []string{"gt:bug"},
		Priority:    1,
		Description: strandedDoneDescription(req),
		Actor:       strings.TrimSpace(req.Worker),
		Rig:         strings.TrimSpace(req.Rig),
	}

	issue, created, err := work.CreateIfNoDuplicate(opts)
	if err != nil && opts.Rig != "" {
		// Rig binding is how the report reaches the witness who reads that
		// database (gt-7y7), but it is a routing preference, not the point. An
		// unresolvable rig alias must not turn a work-loss report into a log
		// line: retry unbound, and say where it actually landed.
		unbound := opts
		unbound.Rig = ""
		if fallback, fallbackCreated, fallbackErr := work.CreateIfNoDuplicate(unbound); fallbackErr == nil && fallback != nil {
			report.RigBindErr = err
			issue, created, err = fallback, fallbackCreated, nil
		}
	}
	if err != nil {
		report.Err = fmt.Errorf("filing stranded-work report: %w", err)
		return report
	}
	if issue == nil {
		report.Err = errors.New("filing stranded-work report: no bead returned")
		return report
	}
	report.BeadID = issue.ID
	report.Created = created

	// Only a fresh report needs routing; re-assigning an existing one would
	// stomp whoever has already picked it up.
	if created {
		if witness := doneWitnessAddress(req.Rig); witness != "" {
			if err := work.Update(report.BeadID, beads.UpdateOptions{Assignee: &witness}); err != nil {
				report.Err = fmt.Errorf("assigning %s to %s: %w", report.BeadID, witness, err)
			}
		}
	}
	return report
}

// doneWitnessAddress is where a stranded completion is queued for a decision.
func doneWitnessAddress(rigName string) string {
	rigName = strings.TrimSpace(rigName)
	if rigName == "" {
		return ""
	}
	return rigName + "/witness"
}

// strandedDoneDescription names every fact a reader needs to decide between the
// two readings, plus the commands that separate them.
func strandedDoneDescription(req strandedDoneRequest) string {
	branch := valueOrPlaceholder(req.Branch, "(none recorded)")
	baseRef := valueOrPlaceholder(req.BaseRef, "origin/main")
	commit := valueOrPlaceholder(req.CommitSHA, "(none recorded)")
	worker := valueOrPlaceholder(req.Worker, "(unknown)")
	refusal := valueOrPlaceholder(req.Refusal, "the source issue is closed")

	return fmt.Sprintf(`gt done refused to create a merge request because the source issue was already
closed, and the branch it refused on behalf of is not contained in the target.

source_issue: %s
branch: %s
commit_sha: %s
target: %s
worker: %s
refusal: %s

The refusal is correct — a closed bead cannot carry work through the queue — but
it leaves a branch on origin under a closed bead, which is the state that reads
as finished on every surface. Nothing re-slings a closed bead, so this needs a
decision gt done cannot make for itself (gt-rbul).

TWO READINGS, and the commands that separate them:

    git merge-base --is-ancestor %s %s && echo MERGED || echo NOT-AN-ANCESTOR
    git cherry %s %s        # '-' prefixed lines already landed by squash/cherry-pick

(a) The work landed by another route — a sibling MR, a squash, a cherry-pick.
    The bead is correctly closed and the branch is redundant. Close this report;
    delete the branch if you like.
(b) The bead closed prematurely underneath live work. Reopen it and resubmit:

        bd update %s --status=open
        gt mq submit --branch %s --issue %s

    Or, when the close itself was the mistake and reopening is not wanted:

        gt mq submit --branch %s --issue %s --allow-closed-issue

gt done did NOT reopen the issue itself: ancestry disproves (a) only one way, so
a non-ancestor branch may still have landed by squash, and reopening under (a)
resurrects finished work.`,
		req.IssueID, branch, commit, baseRef, worker, refusal,
		commit, baseRef,
		baseRef, branch,
		req.IssueID,
		branch, req.IssueID,
		branch, req.IssueID,
	)
}

// handleClosedSourceRefusal is the whole gt-rbul response to a closed-source
// refusal: probe containment, file a report when the branch is stranded, and
// print what it concluded. It is one function rather than three inline calls in
// runDone so the composition — probe, report, warn — can be tested; the pieces
// being individually correct is not the property that matters here, since the
// bug this fixes was that nobody asked the second question at all.
//
// It returns the mail note for the witness notification. Nothing it does can
// fail the completion: the branch is already pushed, and refusing to complete
// would strand the work more thoroughly than reporting it.
func handleClosedSourceRefusal(g doneStrandingProbe, work strandedDoneFiler, req strandedDoneRequest, out io.Writer) string {
	stranding := assessDoneStranding(g, req.BaseRef, req.Branch, req.CommitSHA)

	var report strandedDoneReport
	if stranding.Stranded {
		report = reportStrandedDone(work, req)
	}
	for _, line := range doneStrandingConsoleLines(req, stranding, report) {
		fmt.Fprintln(out, line)
	}
	return doneStrandingMailNote(req, stranding, report)
}

// doneStrandingConsoleLines is what the refusing polecat sees. It is the last
// output before the sandbox is nuked, so it carries the recovery commands
// rather than pointing at a bead the reader is about to lose access to.
func doneStrandingConsoleLines(req strandedDoneRequest, st doneStranding, report strandedDoneReport) []string {
	branch := valueOrPlaceholder(req.Branch, "(unnamed branch)")
	baseRef := valueOrPlaceholder(st.BaseRef, "the target")

	if st.AlreadyMerged {
		return []string{
			fmt.Sprintf("  Nothing is stranded: %s is already contained in %s — the work landed.", valueOrPlaceholder(shortSHA(req.CommitSHA), "the branch head"), baseRef),
		}
	}

	lines := []string{
		fmt.Sprintf("  WARNING: branch %s is %s and is NOT contained in %s.", branch, aheadPhrase(st), baseRef),
		"  This work will not land unless someone acts on it.",
	}
	if st.ProbeErr != nil {
		lines = append(lines, fmt.Sprintf("  (containment could not be proven: %v — treating the branch as unmerged)", st.ProbeErr))
	}
	switch {
	case report.BeadID != "" && report.Created:
		lines = append(lines, fmt.Sprintf("  Filed %s for %s.", report.BeadID, doneWitnessAddress(req.Rig)))
	case report.BeadID != "":
		lines = append(lines, fmt.Sprintf("  Report %s already open for this branch.", report.BeadID))
	case report.Err != nil:
		lines = append(lines, fmt.Sprintf("  Could not file a stranded-work report: %v", report.Err))
	}
	if report.RigBindErr != nil {
		lines = append(lines, fmt.Sprintf("  Report is NOT bound to rig %s (%v) — %s may not see it.",
			req.Rig, report.RigBindErr, doneWitnessAddress(req.Rig)))
	}
	lines = append(lines,
		"  To recover, reopen the issue and resubmit:",
		fmt.Sprintf("    bd update %s --status=open", req.IssueID),
		fmt.Sprintf("    gt mq submit --branch %s --issue %s", branch, req.IssueID),
		"  Or, when the close itself was the mistake and reopening is not wanted:",
		fmt.Sprintf("    gt mq submit --branch %s --issue %s --allow-closed-issue", branch, req.IssueID),
		"  Or confirm the work already landed by another route:",
		fmt.Sprintf("    git cherry %s %s", baseRef, branch),
	)
	return lines
}

// doneStrandingMailNote is appended to the DONE_MR_REFUSED mail so the witness
// can tell a benign refusal from one that abandoned commits without opening the
// report bead first.
func doneStrandingMailNote(req strandedDoneRequest, st doneStranding, report strandedDoneReport) string {
	baseRef := valueOrPlaceholder(st.BaseRef, "the target")
	if st.AlreadyMerged {
		return fmt.Sprintf("Nothing is stranded: %s is already contained in %s, so the closed issue is correct.",
			valueOrPlaceholder(shortSHA(req.CommitSHA), "the branch head"), baseRef)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "STRANDED WORK: branch %s is %s and is NOT contained in %s.\n",
		valueOrPlaceholder(req.Branch, "(unnamed branch)"), aheadPhrase(st), baseRef)
	if st.ProbeErr != nil {
		fmt.Fprintf(&b, "Containment could not be proven (%v), so the branch is treated as unmerged.\n", st.ProbeErr)
	}
	switch {
	case report.BeadID != "" && report.Created:
		fmt.Fprintf(&b, "Filed %s with the full recovery procedure.\n", report.BeadID)
	case report.BeadID != "":
		fmt.Fprintf(&b, "Report %s was already open for this branch.\n", report.BeadID)
	case report.Err != nil:
		fmt.Fprintf(&b, "NO REPORT BEAD WAS FILED (%v) — this mail is the only trace.\n", report.Err)
	}
	if report.RigBindErr != nil {
		fmt.Fprintf(&b, "The report is not bound to rig %s (%v); look for it in the default database.\n", req.Rig, report.RigBindErr)
	}
	return strings.TrimRight(b.String(), "\n")
}

// aheadPhrase renders the commit count when it is known and stays silent about
// it when it is not, rather than printing a zero that would read as "no work".
func aheadPhrase(st doneStranding) string {
	if !st.AheadKnown {
		return "unmerged"
	}
	if st.Ahead == 1 {
		return "1 commit ahead"
	}
	return fmt.Sprintf("%d commits ahead", st.Ahead)
}

func valueOrPlaceholder(s, placeholder string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return placeholder
}

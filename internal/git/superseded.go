package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/util"
)

// A durable "this branch is settled" marker, stored in git (gt-8xcg).
//
// `gt patrol branches` re-derives the same verdict on the same branches on
// every cycle, because it has nowhere to record one. Measured on gastown: 36
// branches scanned, and 21 of them — 5 on the CHECK list, 16 landed-but-not-an-
// ancestor — came back unchanged across cycles 1, 6 and 16 of a single witness
// session. Two agents independently settled the same five by different
// instruments within an hour of each other, and neither could write the answer
// anywhere the next reader would find it. Both derivations were correct; both
// were discarded.
//
// WHY GIT AND NOT BEADS. The governing constraint is a lifetime one:
//
//	THE MARKER MUST LIVE AT LEAST AS LONG AS THE THING IT MARKS.
//
// A branch persists indefinitely. A wisp is purged at purge_age=168h, and the
// purge takes wisp_events, wisp_comments AND wisp_labels with it — the labels
// are in the same auxTables delete list, so a label on the MR wisp is
// guaranteed to die before the branch it describes, by configuration rather
// than by bug. An issue survives (issues are auto-closed at 720h, not deleted)
// but puts the marker in a different system with a different lifetime from the
// thing it marks, which is the coupling this is meant to escape.
//
// A ref in the same repository as the branch shares a lifetime with it
// STRUCTURALLY: it cannot be purged while the branch stands, it travels with
// the repo, it survives every Dolt operation, and the sweep already enumerates
// refs so it can be read with no database round trip.
//
// WHY A BLOB AND NOT JUST A REF TO THE COMMIT. A ref alone records only "this
// branch was marked", and that is the marker that would have caused the harm it
// is meant to prevent: a branch marked settled, then pushed to again, would go
// on being suppressed while genuinely unmerged work sat on it. The marker
// therefore names the exact commit it settles, and the sweep compares it to the
// tip it actually listed. Different tip, no suppression — see StaleFor.
//
// The reason is required for the same reason the marker exists at all. A
// marker that says "settled" without saying how it was settled reproduces the
// original defect one level up: the next reader still cannot tell a superseded
// branch from a prematurely abandoned one, and has to re-derive it.
const SupersededRefPrefix = "refs/gt/superseded/"

// SupersededMark is one branch's settlement record.
//
// It is serialised as JSON into a blob, and a ref under SupersededRefPrefix
// points at that blob. The ref name carries the branch; the blob carries
// everything a later reader needs in order NOT to re-derive the verdict.
type SupersededMark struct {
	// Branch is the branch this settles, without refs/heads/.
	Branch string `json:"branch"`

	// Commit is the branch tip that was settled. Suppression is conditional on
	// this still being the tip: a marker is a statement about a specific state
	// of a branch, not a permanent exemption for the name.
	//
	// It is deliberately NOT omitempty. An absent commit and a marker written
	// by a version that never recorded one must not render identically — the
	// first is unreadable and the second would be trusted.
	Commit string `json:"commit"`

	// Reason is why the branch is settled, in the marker's author's words. It
	// is required: the whole value of this record is the derivation, not the
	// verdict.
	Reason string `json:"reason"`

	// MarkedBy is the agent or person who settled it, for provenance.
	MarkedBy string `json:"marked_by,omitempty"`

	// MarkedAt is when, in RFC3339.
	MarkedAt string `json:"marked_at,omitempty"`
}

// StaleFor reports whether this marker no longer describes the branch as it now
// stands on the remote — the branch was pushed to after being marked, so the
// settlement was about content that is no longer the tip.
//
// A stale marker must never suppress. Comparison is by full SHA and is
// case-insensitive; a marker with no recorded commit is stale by construction,
// because "settled at some unknown state" cannot be checked against anything.
func (m SupersededMark) StaleFor(tip string) bool {
	marked := strings.TrimSpace(m.Commit)
	tip = strings.TrimSpace(tip)
	if marked == "" || tip == "" {
		return true
	}
	return !strings.EqualFold(marked, tip)
}

// SupersededRef is the ref that holds branch's marker.
func SupersededRef(branch string) string {
	return SupersededRefPrefix + strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
}

// MarkSuperseded records that branch is settled at mark.Commit.
//
// It writes a blob and points a ref at it, both local to this repository. It
// does not contact the remote and it does not touch the branch: marking is a
// note about a branch, never an action on one.
//
// Re-marking an already-marked branch overwrites the marker, which is the right
// behaviour for a branch that moved and was settled again — but the CALLER is
// the one that has to decide that, because overwriting silently would discard a
// derivation, which is the exact loss this exists to stop.
func (g *Git) MarkSuperseded(mark SupersededMark) error {
	branch := strings.TrimPrefix(strings.TrimSpace(mark.Branch), "refs/heads/")
	if branch == "" {
		return fmt.Errorf("superseded marker needs a branch")
	}
	mark.Branch = branch
	if strings.TrimSpace(mark.Commit) == "" {
		return fmt.Errorf("superseded marker for %s needs the commit it settles: a marker that does not name a tip cannot be invalidated when the branch moves, and would suppress live work", branch)
	}
	if strings.TrimSpace(mark.Reason) == "" {
		return fmt.Errorf("superseded marker for %s needs a reason: a marker recording only the verdict leaves the next reader to re-derive it, which is what markers exist to prevent", branch)
	}

	ref := SupersededRef(branch)
	if _, err := g.run("check-ref-format", ref); err != nil {
		return fmt.Errorf("branch %q does not make a usable marker ref (%s): %w", branch, ref, err)
	}

	payload, err := json.Marshal(mark)
	if err != nil {
		return fmt.Errorf("encoding superseded marker for %s: %w", branch, err)
	}
	blob, err := g.runWithInput(string(payload)+"\n", "hash-object", "-w", "--stdin")
	if err != nil {
		return fmt.Errorf("writing superseded marker for %s: %w", branch, err)
	}
	blob = strings.TrimSpace(blob)
	if blob == "" {
		return fmt.Errorf("writing superseded marker for %s: git hash-object returned no object id", branch)
	}
	if _, err := g.run("update-ref", ref, blob); err != nil {
		return fmt.Errorf("pointing %s at the superseded marker: %w", ref, err)
	}

	// Verify rather than trust the exit code. `git push --delete` printing
	// "[deleted]" for a ref it had not deleted (gt-wkcz) is the same shape of
	// failure, and a marker that reports success without existing is worse than
	// no marker: the operator stops re-deriving and nothing suppresses.
	written, err := g.SupersededMarkFor(branch)
	if err != nil {
		return fmt.Errorf("wrote %s but could not read it back: %w", ref, err)
	}
	if written == nil {
		return fmt.Errorf("wrote %s but it does not exist on read-back", ref)
	}
	return nil
}

// UnmarkSuperseded removes branch's marker. It reports whether one was there:
// a caller that says "unmarked" for a branch that never had a marker is
// answering a question nobody asked.
func (g *Git) UnmarkSuperseded(branch string) (bool, error) {
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if branch == "" {
		return false, fmt.Errorf("unmark needs a branch")
	}
	ref := SupersededRef(branch)
	existing, err := g.SupersededMarkFor(branch)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	if _, err := g.run("update-ref", "-d", ref); err != nil {
		return false, fmt.Errorf("deleting %s: %w", ref, err)
	}
	// Same read-back discipline as MarkSuperseded, and for the sharper reason:
	// a delete that reports success while the ref stands leaves a branch
	// suppressed by a marker its owner believes they removed.
	after, err := g.SupersededMarkFor(branch)
	if err != nil {
		return false, fmt.Errorf("deleted %s but could not confirm it is gone: %w", ref, err)
	}
	if after != nil {
		return false, fmt.Errorf("git reported %s deleted but it is still present", ref)
	}
	return true, nil
}

// SupersededMarkFor reads one branch's marker. A nil mark with a nil error means
// there is no marker — which is a measurement, not a failure.
func (g *Git) SupersededMarkFor(branch string) (*SupersededMark, error) {
	marks, err := g.SupersededMarks()
	if err != nil {
		return nil, err
	}
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if mark, ok := marks[branch]; ok {
		return &mark, nil
	}
	return nil, nil
}

// SupersededMarks reads every marker in this repository, keyed by branch.
//
// A marker whose blob cannot be parsed is returned with the branch and commit
// it can still be trusted for — the ref name — and an empty reason, so a
// corrupt marker degrades into a visible one rather than into an absence. It is
// never dropped: a silently missing marker reads as "never marked", and the
// difference between that and "marked, unreadable" is the difference between
// re-deriving a verdict and knowing one was reached.
func (g *Git) SupersededMarks() (map[string]SupersededMark, error) {
	out, err := g.run("for-each-ref", "--format=%(refname)%09%(objectname)", SupersededRefPrefix)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", SupersededRefPrefix, err)
	}
	marks := map[string]SupersededMark{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		refName, objectID, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		branch := strings.TrimPrefix(refName, SupersededRefPrefix)
		if branch == "" {
			continue
		}
		mark := SupersededMark{Branch: branch}
		if blob, blobErr := g.run("cat-file", "blob", strings.TrimSpace(objectID)); blobErr == nil {
			var parsed SupersededMark
			if json.Unmarshal([]byte(blob), &parsed) == nil {
				mark = parsed
			}
		}
		// The ref name is authoritative for the branch: it is what the sweep
		// looks the marker up by, and a blob whose branch field disagrees with
		// the ref it is stored under would silently mark a different branch.
		mark.Branch = branch
		marks[branch] = mark
	}
	return marks, nil
}

// runWithInput is run() with something on stdin. Only the object writer needs
// it, and it keeps the town-root mutation guard that every other git call gets.
func (g *Git) runWithInput(input string, args ...string) (string, error) {
	if err := g.guardUnsafeTownRootMutation(args); err != nil {
		return "", err
	}
	if g.gitDir != "" {
		args = append([]string{"--git-dir=" + g.gitDir}, args...)
	}

	cmd := exec.Command("git", args...)
	util.SetDetachedProcessGroup(cmd)
	if g.workDir != "" {
		cmd.Dir = g.workDir
	}
	cmd.Stdin = strings.NewReader(input)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", g.wrapError(err, stdout.String(), stderr.String(), args)
	}
	return strings.TrimSpace(stdout.String()), nil
}

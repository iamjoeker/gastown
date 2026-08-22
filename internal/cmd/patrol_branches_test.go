package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/witness"
)

func sweepFixture() *witness.BranchSweepResult {
	return &witness.BranchSweepResult{
		Remote:      "origin",
		Target:      "origin/main",
		Scanned:     4,
		MRsMeasured: true,
		Findings: []witness.BranchSweepFinding{
			{
				Branch: "polecat/dust/gt-k3v+aaa", CommitSHA: "sha1",
				IssueID: "gt-k3v", IssueStatus: "closed",
				Class: witness.BranchSweepCheck,
				Note:  "bead gt-k3v is closed, no MR was ever created — check whether this was superseded or stranded",
			},
			{
				Branch: "polecat/foundation/gt-q+bbb", CommitSHA: "sha2",
				IssueID: "gt-q", IssueStatus: "closed",
				MRID: "gt-wisp-mr2", MRStatus: "open",
				Class: witness.BranchSweepQueued,
				Note:  "open MR gt-wisp-mr2 — queued, not stranded",
			},
			{
				Branch: "polecat/mirelurk/gt-live+ccc", CommitSHA: "sha3",
				IssueID: "gt-live", IssueStatus: "hooked",
				Class: witness.BranchSweepActive,
				Note:  "bead is hooked — still re-slingable",
			},
			{
				Branch: "polecat/refinery/gt-aqk+ddd", CommitSHA: "sha4",
				IssueID: "gt-aqk", IssueStatus: "closed",
				Class: witness.BranchSweepLanded, Evidence: "cherry",
				ContainedIn: "origin/main", HygieneUnreachable: true,
				Note: "content is in origin/main (same patches, squashed or cherry-picked); NOT an ancestor of origin/main — branch hygiene cannot delete it",
			},
		},
	}
}

func TestPatrolBranchesHumanShowsOnlyTheShortListByDefault(t *testing.T) {
	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", sweepFixture(), false); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "polecat/dust/gt-k3v+aaa") {
		t.Errorf("short list omits the check branch:\n%s", out)
	}
	for _, hidden := range []string{"polecat/foundation/gt-q+bbb", "polecat/mirelurk/gt-live+ccc", "polecat/refinery/gt-aqk+ddd"} {
		if strings.Contains(out, hidden) {
			t.Errorf("default output includes %s, which is not on the short list:\n%s", hidden, out)
		}
	}
	// The tally still accounts for every branch, so a one-line short list is
	// not mistaken for a rig with one branch.
	if !strings.Contains(out, "4 scanned") {
		t.Errorf("summary does not account for all scanned branches:\n%s", out)
	}
	if !strings.Contains(out, "1 landed") || !strings.Contains(out, "1 queued") {
		t.Errorf("summary hides the classified branches:\n%s", out)
	}
}

func TestPatrolBranchesHumanAllShowsEveryBranch(t *testing.T) {
	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", sweepFixture(), true); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, branch := range []string{
		"polecat/dust/gt-k3v+aaa",
		"polecat/foundation/gt-q+bbb",
		"polecat/mirelurk/gt-live+ccc",
		"polecat/refinery/gt-aqk+ddd",
	} {
		if !strings.Contains(out, branch) {
			t.Errorf("--all output omits %s:\n%s", branch, out)
		}
	}
}

// The four fields that separate most cases cheaply must all be on the line.
func TestPatrolBranchesHumanCarriesTheFourFields(t *testing.T) {
	result := sweepFixture()
	result.Findings[0] = witness.BranchSweepFinding{
		Branch:  "polecat/guzzle/gt-1jrl+bbb",
		IssueID: "gt-1jrl", IssueStatus: "closed",
		MRID: "gt-wisp-mr1", MRStatus: "closed", MRCloseReason: "rejected",
		Class: witness.BranchSweepCheck,
		Note:  "bead gt-1jrl is closed, MR gt-wisp-mr1 is closed (rejected) — check whether this was superseded or stranded",
	}

	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", result, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"polecat/guzzle/gt-1jrl+bbb", "gt-1jrl", "closed", "gt-wisp-mr1", "closed:rejected"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// The command must not present the short list as proven work loss, and must say
// plainly that it changed nothing.
func TestPatrolBranchesHumanDoesNotOverclaim(t *testing.T) {
	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", sweepFixture(), false); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := strings.ToLower(buf.String())

	// "lost" may appear only in the sentence that disclaims it. Anywhere else
	// it is the overclaim this sweep must never make.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "lost") && !strings.Contains(line, "not a claim") {
			t.Errorf("output overclaims: %q", strings.TrimSpace(line))
		}
	}
	if !strings.Contains(out, "not a claim that work was lost") {
		t.Errorf("output does not qualify the short list:\n%s", out)
	}
	if !strings.Contains(out, "writes nothing") {
		t.Errorf("output does not say the command changed nothing:\n%s", out)
	}
	if !strings.Contains(out, "superseded") {
		t.Errorf("output does not name the other reading:\n%s", out)
	}
}

// An unclassifiable branch must be counted apart from a genuine check, or a
// failed lookup gets read as a finding — and a run of them as a crisis.
func TestPatrolBranchesHumanSeparatesUnknownFromCheck(t *testing.T) {
	result := sweepFixture()
	result.Findings = append(result.Findings, witness.BranchSweepFinding{
		Branch: "polecat/refuge/gt-wz3y+eee",
		Class:  witness.BranchSweepUnknown,
		Err:    "remote tip for refs/heads/polecat/refuge/gt-wz3y+eee moved between listing and fetch: listed 89db0051, fetched dd7e98ec (re-run to classify it)",
		Note:   "could not compare against origin/main or upstream/main: remote tip moved between listing and fetch",
	})
	result.Scanned = 5

	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", result, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1 to CHECK") {
		t.Errorf("check count not separated:\n%s", out)
	}
	if !strings.Contains(out, "1 that could not be classified") {
		t.Errorf("unknown count not separated:\n%s", out)
	}
	if !strings.Contains(out, "not an all-clear") {
		t.Errorf("output does not say what an unclassified branch means:\n%s", out)
	}
	// The reason must survive into the table. Without it every unknown reads as a
	// property of the branch, which is how a clobbered FETCH_HEAD got reported as
	// an unclassifiable branch for as long as it did (gt-880s).
	if !strings.Contains(out, "moved between listing and fetch") {
		t.Errorf("the note's reason did not reach the table:\n%s", out)
	}
}

// A clean rig must be distinguishable from a rig that was not measured.
func TestPatrolBranchesHumanDistinguishesEmptyFromClean(t *testing.T) {
	t.Run("no branches at all", func(t *testing.T) {
		var buf bytes.Buffer
		empty := &witness.BranchSweepResult{Remote: "origin", Target: "origin/main", MRsMeasured: true}
		if err := writePatrolBranchesHuman(&buf, "gastown", empty, false); err != nil {
			t.Fatalf("render: %v", err)
		}
		if !strings.Contains(buf.String(), "no polecat branches") {
			t.Errorf("an empty listing does not say so:\n%s", buf.String())
		}
	})

	t.Run("branches present, none needing a decision", func(t *testing.T) {
		var buf bytes.Buffer
		clean := sweepFixture()
		clean.Findings = clean.Findings[1:] // drop the check
		clean.Scanned = 3
		if err := writePatrolBranchesHuman(&buf, "gastown", clean, false); err != nil {
			t.Fatalf("render: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "nothing needs a decision") {
			t.Errorf("clean sweep does not say so:\n%s", out)
		}
		if !strings.Contains(out, "3 scanned") {
			t.Errorf("clean sweep does not show what it measured:\n%s", out)
		}
	})
}

// An unmeasured MR column must be visible in the summary: without it, "no MR"
// reads as a measurement.
func TestPatrolBranchesHumanFlagsUnmeasuredMRs(t *testing.T) {
	result := sweepFixture()
	result.MRsMeasured = false
	result.Errors = []string{"listing merge requests: wisps table unreachable (every 'no MR' below is UNMEASURED)"}

	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", result, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "MR column UNMEASURED") {
		t.Errorf("summary does not flag the unmeasured MR column:\n%s", out)
	}
	if !strings.Contains(out, "wisps table unreachable") {
		t.Errorf("sweep-wide error not surfaced:\n%s", out)
	}
}

// The resolved comparison ref is printed because a bare "main" resolves to the
// stale upstream on a fork-backed rig, and a reader must not have to assume.
func TestPatrolBranchesHumanNamesTheResolvedTarget(t *testing.T) {
	result := sweepFixture()
	result.Target = "upstream/main"

	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", result, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "upstream/main") {
		t.Errorf("output does not name the resolved target:\n%s", buf.String())
	}
}

func TestPatrolBranchesJSONShape(t *testing.T) {
	var buf bytes.Buffer
	if err := writePatrolBranchesJSON(&buf, "gastown", sweepFixture()); err != nil {
		t.Fatalf("render: %v", err)
	}

	var out struct {
		Rig       string `json:"rig"`
		Target    string `json:"target"`
		Scanned   int    `json:"scanned"`
		Attention int    `json:"attention"`
		Findings  []struct {
			Branch      string `json:"branch"`
			Class       string `json:"class"`
			IssueID     string `json:"issue_id"`
			IssueStatus string `json:"issue_status"`
			MRID        string `json:"mr_id"`
			MRStatus    string `json:"mr_status"`
		} `json:"findings"`
		MRsMeasured bool `json:"mrs_measured"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if out.Rig != "gastown" || out.Target != "origin/main" || out.Scanned != 4 {
		t.Fatalf("header fields wrong: %+v", out)
	}
	if out.Attention != 1 {
		t.Fatalf("Attention = %d, want 1", out.Attention)
	}
	if !out.MRsMeasured {
		t.Fatalf("mrs_measured lost in serialisation")
	}
	// JSON carries every branch, classified — the filtering is a human-output
	// concern only, so a consumer can decide for itself.
	if len(out.Findings) != 4 {
		t.Fatalf("findings = %d, want all 4", len(out.Findings))
	}
	if out.Findings[1].MRID != "gt-wisp-mr2" || out.Findings[1].MRStatus != "open" {
		t.Fatalf("MR fields lost in serialisation: %+v", out.Findings[1])
	}
}

// fakeRefResolver answers target selection from a fixed set of refs and a
// fixed ancestry relation.
type fakeRefResolver struct {
	defaultBranch string
	exists        map[string]bool
	// behind[x] = y means x is an ancestor of y.
	behind map[string]string
	clean  string
}

func (f *fakeRefResolver) RemoteDefaultBranch() string { return f.defaultBranch }

func (f *fakeRefResolver) CleanBaseRef(remote, defaultBranch, target string) string {
	if f.clean != "" {
		return f.clean
	}
	if target != "" {
		return target
	}
	return remote + "/" + defaultBranch
}

func (f *fakeRefResolver) RefExists(ref string) (bool, error) { return f.exists[ref], nil }

func (f *fakeRefResolver) IsAncestor(ancestor, descendant string) (bool, error) {
	return f.behind[ancestor] == descendant, nil
}

// Both trunks are checked when both exist. Checking only one of them is what
// put three already-landed branches on gastown's short list.
func TestBranchSweepTargetsChecksBothTrunks(t *testing.T) {
	g := &fakeRefResolver{
		defaultBranch: "main",
		exists:        map[string]bool{"origin/main": true, "upstream/main": true},
	}
	got := branchSweepTargets(g, "origin", "")
	if len(got) != 2 {
		t.Fatalf("targets = %v, want both trunks", got)
	}
	if !slicesContains(got, "origin/main") || !slicesContains(got, "upstream/main") {
		t.Fatalf("targets = %v, want origin/main and upstream/main", got)
	}
}

// The ref quoted back in the guidance must be the one work actually lands on,
// not whichever the fork heuristic prefers.
func TestBranchSweepTargetsPutsTheMostAdvancedTrunkFirst(t *testing.T) {
	g := &fakeRefResolver{
		defaultBranch: "main",
		exists:        map[string]bool{"origin/main": true, "upstream/main": true},
		// upstream/main is an ancestor of origin/main: origin is ahead.
		behind: map[string]string{"upstream/main": "origin/main"},
	}
	got := branchSweepTargets(g, "origin", "")
	if len(got) != 2 || got[0] != "origin/main" {
		t.Fatalf("targets = %v, want the more advanced origin/main first", got)
	}
}

func TestBranchSweepTargetsHonoursAQualifiedTargetExactly(t *testing.T) {
	g := &fakeRefResolver{
		defaultBranch: "main",
		exists:        map[string]bool{"origin/main": true, "upstream/main": true},
	}
	for _, explicit := range []string{"upstream/main", "origin/release", "refs/heads/main"} {
		got := branchSweepTargets(g, "origin", explicit)
		if len(got) != 1 || got[0] != explicit {
			t.Errorf("branchSweepTargets(%q) = %v, want exactly that ref", explicit, got)
		}
	}
}

// A bare --target names a BRANCH, so it expands over both remotes the same way
// the default does.
func TestBranchSweepTargetsExpandsABareTargetBranch(t *testing.T) {
	g := &fakeRefResolver{
		defaultBranch: "main",
		exists:        map[string]bool{"origin/release": true, "upstream/release": true},
	}
	got := branchSweepTargets(g, "origin", "release")
	if len(got) != 2 || !slicesContains(got, "origin/release") || !slicesContains(got, "upstream/release") {
		t.Fatalf("targets = %v, want both release refs", got)
	}
}

// A rig with no upstream is the normal case and must yield exactly one target.
func TestBranchSweepTargetsSingleTrunk(t *testing.T) {
	g := &fakeRefResolver{
		defaultBranch: "main",
		exists:        map[string]bool{"origin/main": true},
	}
	got := branchSweepTargets(g, "origin", "")
	if len(got) != 1 || got[0] != "origin/main" {
		t.Fatalf("targets = %v, want [origin/main]", got)
	}
}

// When nothing resolves, fall back to the repo's own base rather than inventing
// a ref that does not exist.
func TestBranchSweepTargetsFallsBackWhenNothingResolves(t *testing.T) {
	g := &fakeRefResolver{defaultBranch: "main", exists: map[string]bool{}, clean: "upstream/main"}
	got := branchSweepTargets(g, "origin", "")
	if len(got) != 1 || got[0] != "upstream/main" {
		t.Fatalf("targets = %v, want the repo's own base ref", got)
	}
}

func TestTargetRemotesDedupes(t *testing.T) {
	got := targetRemotes([]string{"origin/main", "upstream/main", "origin/release"}, "origin")
	if len(got) != 2 || got[0] != "origin" || got[1] != "upstream" {
		t.Fatalf("targetRemotes = %v, want [origin upstream]", got)
	}
}

func TestTargetRemotesFallsBackForUnqualifiedRefs(t *testing.T) {
	got := targetRemotes([]string{"refs/heads/main"}, "origin")
	if len(got) != 1 || got[0] != "origin" {
		t.Fatalf("targetRemotes = %v, want [origin]", got)
	}
}

// The header must name every ref containment was tested against.
func TestPatrolBranchesHumanNamesEveryTarget(t *testing.T) {
	result := sweepFixture()
	result.Target = "origin/main"
	result.Targets = []string{"origin/main", "upstream/main"}

	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", result, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "origin/main") || !strings.Contains(out, "upstream/main") {
		t.Errorf("header does not name both targets:\n%s", out)
	}
	if !strings.Contains(out, "targets") {
		t.Errorf("header does not pluralise for multiple targets:\n%s", out)
	}
}

func TestPatrolBranchesCommandIsRegisteredAndReadOnly(t *testing.T) {
	var found *cobraCommandShim
	for _, sub := range patrolCmd.Commands() {
		if sub.Name() == "branches" {
			found = &cobraCommandShim{Use: sub.Use, Short: sub.Short, Long: sub.Long}
		}
	}
	if found == nil {
		t.Fatalf("gt patrol branches is not registered")
	}
	// The read-only promise is part of the contract with whoever runs this on
	// a patrol cadence; it belongs in the help text, not only in the code.
	if !strings.Contains(found.Long, "WRITES NOTHING") {
		t.Errorf("help text does not state the read-only guarantee:\n%s", found.Long)
	}
	for _, flag := range []string{"json", "all", "rig", "target", "remote", "no-fetch"} {
		if patrolBranchesCmd.Flags().Lookup(flag) == nil {
			t.Errorf("flag --%s is missing", flag)
		}
	}
}

type cobraCommandShim struct {
	Use   string
	Short string
	Long  string
}

// landedAncestorFinding is a branch whose commit is in the target's history:
// the half of "landed" that branch hygiene collects on its own.
func landedAncestorFinding() witness.BranchSweepFinding {
	return witness.BranchSweepFinding{
		Branch: "polecat/chrome/gt-yb33+eee", CommitSHA: "sha5",
		IssueID: "gt-yb33", IssueStatus: "closed",
		Class: witness.BranchSweepLanded, Evidence: "ancestor",
		ContainedIn: "origin/main", HygieneUnreachable: false,
		Note: "content is in origin/main (ancestor)",
	}
}

// The default view hides landed rows entirely, which is exactly why the notice
// belongs there: without it a reader who never passes --all never learns these
// branches exist, and nothing else will ever remove them (gt-l65a).
func TestPatrolBranchesHumanRaisesHygieneUnreachableInTheDefaultView(t *testing.T) {
	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", sweepFixture(), false); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "NOT ancestors of origin/main") {
		t.Errorf("default view does not raise the uncollectable landed branch:\n%s", out)
	}
	if !strings.Contains(out, "--deletable") {
		t.Errorf("default view does not route the reader anywhere:\n%s", out)
	}
	// The routing claim is the finding. Saying only "landed" is what left these
	// rows in every future sweep.
	if !strings.Contains(out, "branch hygiene deletes by ancestry") {
		t.Errorf("default view does not say why nothing collects them:\n%s", out)
	}
}

// A rig whose landed branches are all ancestors has nothing to route, and must
// not be told to go look at an empty list.
func TestPatrolBranchesHumanOmitsTheNoticeWhenEveryLandedBranchIsAnAncestor(t *testing.T) {
	result := sweepFixture()
	result.Findings[3] = landedAncestorFinding()

	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", result, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "--deletable") {
		t.Errorf("notice printed with nothing to route:\n%s", out)
	}
	if !strings.Contains(out, "1 landed (1 ancestor, 0 not an ancestor)") {
		t.Errorf("summary does not split the landed tally:\n%s", out)
	}
}

// One number for "landed" hides the half that accumulates, so the tally splits.
func TestPatrolBranchesHumanSplitsTheLandedTally(t *testing.T) {
	result := sweepFixture()
	result.Findings = append(result.Findings, landedAncestorFinding())
	result.Scanned = 5

	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", result, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "2 landed (1 ancestor, 1 not an ancestor)") {
		t.Errorf("summary does not split the landed tally:\n%s", buf.String())
	}
}

// In --all the rows are on screen, so the discriminator must be on the row.
func TestPatrolBranchesHumanMarksTheUncollectableRow(t *testing.T) {
	result := sweepFixture()
	result.Findings = append(result.Findings, landedAncestorFinding())
	result.Scanned = 5

	var buf bytes.Buffer
	if err := writePatrolBranchesHuman(&buf, "gastown", result, true); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "landed*") {
		t.Errorf("--all does not mark the uncollectable landed row:\n%s", out)
	}
	if !strings.Contains(out, "Marked landed* in the table above") {
		t.Errorf("--all does not explain the mark:\n%s", out)
	}
	// The ancestor row must not wear the mark: it is collected already.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "polecat/chrome/gt-yb33+eee") && strings.Contains(line, "landed*") {
			t.Errorf("ancestor row marked uncollectable: %q", strings.TrimSpace(line))
		}
	}
}

// --deletable is the short list an operator can act on: exactly the landed rows
// hygiene cannot reach, and nothing else.
func TestPatrolBranchesDeletableListsOnlyTheUncollectableRows(t *testing.T) {
	result := sweepFixture()
	result.Findings = append(result.Findings, landedAncestorFinding())
	result.Scanned = 5

	var buf bytes.Buffer
	if err := writePatrolBranchesDeletable(&buf, "gastown", result); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "polecat/refinery/gt-aqk+ddd") {
		t.Errorf("--deletable omits the uncollectable branch:\n%s", out)
	}
	for _, hidden := range []string{
		"polecat/dust/gt-k3v+aaa",      // check: unmerged, must never be offered for deletion
		"polecat/foundation/gt-q+bbb",  // queued
		"polecat/mirelurk/gt-live+ccc", // active
		"polecat/chrome/gt-yb33+eee",   // landed, and hygiene already has it
	} {
		if strings.Contains(out, hidden) {
			t.Errorf("--deletable includes %s, which is not deletable by this route:\n%s", hidden, out)
		}
	}
}

// It must print the verification before the deletion, and it must perform
// neither: these are shared remote refs.
func TestPatrolBranchesDeletableGivesCommandsAndPerformsNothing(t *testing.T) {
	var buf bytes.Buffer
	if err := writePatrolBranchesDeletable(&buf, "gastown", sweepFixture()); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()

	verify := strings.Index(out, "git cherry origin/main origin/polecat/refinery/gt-aqk+ddd")
	del := strings.Index(out, "git push origin --delete polecat/refinery/gt-aqk+ddd")
	if verify < 0 {
		t.Errorf("no verification command:\n%s", out)
	}
	if del < 0 {
		t.Errorf("no deletion command:\n%s", out)
	}
	if verify >= 0 && del >= 0 && verify > del {
		t.Errorf("deletion is printed before its verification:\n%s", out)
	}
	if !strings.Contains(out, "deletes nothing") {
		t.Errorf("--deletable does not state that it performed nothing:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "stop and check") {
		t.Errorf("--deletable does not say how to read the verification:\n%s", out)
	}
}

// A verification must be run against the ref that actually contains the branch.
// On a fork-backed rig that is not always the primary target.
func TestPatrolBranchesDeletableVerifiesAgainstTheContainingRef(t *testing.T) {
	result := sweepFixture()
	result.Targets = []string{"origin/main", "upstream/main"}
	result.Findings[3].ContainedIn = "upstream/main"

	var buf bytes.Buffer
	if err := writePatrolBranchesDeletable(&buf, "gastown", result); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "git cherry upstream/main origin/polecat/refinery/gt-aqk+ddd") {
		t.Errorf("verification does not use the ref that contains the branch:\n%s", buf.String())
	}
}

// An empty --deletable must say what it measured. "Nothing to delete" and "no
// landed branches at all" are different facts about the rig.
func TestPatrolBranchesDeletableEmptySaysWhatItMeasured(t *testing.T) {
	result := sweepFixture()
	result.Findings[3] = landedAncestorFinding()

	var buf bytes.Buffer
	if err := writePatrolBranchesDeletable(&buf, "gastown", result); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "4 scanned") || !strings.Contains(out, "1 landed") {
		t.Errorf("empty --deletable does not report what it measured:\n%s", out)
	}
	if !strings.Contains(out, "every one is an ancestor of origin/main") {
		t.Errorf("empty --deletable does not say why the list is empty:\n%s", out)
	}
}

// A rig with no polecat branches at all must say so rather than render an empty
// deletable list, which reads as "measured, and clean".
func TestPatrolBranchesDeletableDistinguishesAnUnscannedRig(t *testing.T) {
	var buf bytes.Buffer
	empty := &witness.BranchSweepResult{Remote: "origin", Target: "origin/main", MRsMeasured: true}
	if err := writePatrolBranchesDeletable(&buf, "gastown", empty); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), "no polecat branches") {
		t.Errorf("an empty listing does not say so:\n%s", buf.String())
	}
}

// The JSON carries the routing fact per row and as a total, so a consumer never
// has to re-derive it from the evidence string or re-run with a flag.
func TestPatrolBranchesJSONCarriesHygieneReachability(t *testing.T) {
	result := sweepFixture()
	result.Findings = append(result.Findings, landedAncestorFinding())
	result.Scanned = 5

	var buf bytes.Buffer
	if err := writePatrolBranchesJSON(&buf, "gastown", result); err != nil {
		t.Fatalf("render: %v", err)
	}

	var out struct {
		Attention          int `json:"attention"`
		HygieneUnreachable int `json:"hygiene_unreachable"`
		Findings           []struct {
			Branch             string `json:"branch"`
			Class              string `json:"class"`
			Evidence           string `json:"evidence"`
			HygieneUnreachable *bool  `json:"hygiene_unreachable"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if out.HygieneUnreachable != 1 {
		t.Fatalf("hygiene_unreachable total = %d, want 1", out.HygieneUnreachable)
	}
	if out.Attention != 1 {
		t.Fatalf("attention = %d, want 1 — the two totals are separate", out.Attention)
	}
	if len(out.Findings) != 5 {
		t.Fatalf("findings = %d, want all 5 — JSON is never filtered", len(out.Findings))
	}
	for _, f := range out.Findings {
		// A false must serialise, not vanish: an absent key would read the same
		// as a row from a build that never looked.
		if f.HygieneUnreachable == nil {
			t.Fatalf("%s omits hygiene_unreachable", f.Branch)
		}
		want := f.Class == "landed" && f.Evidence != "ancestor"
		if *f.HygieneUnreachable != want {
			t.Errorf("%s hygiene_unreachable = %v, want %v (class %q, evidence %q)",
				f.Branch, *f.HygieneUnreachable, want, f.Class, f.Evidence)
		}
	}
}

func TestPatrolBranchesDeletableFlagIsRegistered(t *testing.T) {
	flag := patrolBranchesCmd.Flags().Lookup("deletable")
	if flag == nil {
		t.Fatalf("flag --deletable is missing")
	}
	// The flag lists; it must not read as one that deletes.
	if !strings.Contains(flag.Usage, "deletes nothing") {
		t.Errorf("--deletable help does not disclaim acting:\n%s", flag.Usage)
	}
	if !strings.Contains(patrolBranchesCmd.Long, "--deletable") {
		t.Errorf("help text does not document --deletable:\n%s", patrolBranchesCmd.Long)
	}
}

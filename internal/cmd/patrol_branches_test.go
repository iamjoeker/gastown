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
				Note: "content is in origin/main (same patches, squashed or cherry-picked)",
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
		Err:    "candidate refs/heads/polecat/refuge/gt-wz3y+eee changed while pruning",
		Note:   "could not compare against origin/main or upstream/main",
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

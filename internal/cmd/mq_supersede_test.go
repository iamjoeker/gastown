package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// mrForBranch builds an open MR bead the way gt mq submit writes one, so
// planSupersede is exercised through the real ParseMRFields rather than a
// hand-set field it would not see in production.
func mrForBranch(id, branch, sourceIssue string) *beads.Issue {
	return &beads.Issue{
		ID:     id,
		Status: "open",
		Description: "branch: " + branch + "\n" +
			"target: main\n" +
			"source_issue: " + sourceIssue + "\n",
	}
}

func supersedeIDs(plan supersedePlan) string { return strings.Join(plan.Supersede, ",") }

func keptIDs(plan supersedePlan) string {
	ids := make([]string, 0, len(plan.Keep))
	for _, k := range plan.Keep {
		ids = append(ids, k.ID)
	}
	return strings.Join(ids, ",")
}

// A resubmission of the SAME branch retires the old record. This is the case
// supersede exists for, and it is the control: a rule that keeps everything
// would pass the defect tests below while breaking the queue, so the fix is not
// believable without a supersede that still happens.
func TestPlanSupersedeRetiresSameBranch(t *testing.T) {
	branch := "polecat/capable/bd-4xn+mt9iznfy"
	old := mrForBranch("bd-wisp-u2oy", branch, "bd-4xn")

	plan := planSupersede([]*beads.Issue{old}, "bd-wisp-fdo4", branch)

	if got := supersedeIDs(plan); got != "bd-wisp-u2oy" {
		t.Errorf("supersede = %q, want the same-branch MR bd-wisp-u2oy", got)
	}
	if got := keptIDs(plan); got != "" {
		t.Errorf("kept = %q, want nothing kept for a same-branch resubmission", got)
	}
}

// The defect: FindOpenMRsForIssue keys on the source issue alone, so a second
// polecat's complementary branch was force-closed "superseded by X" and then
// LANDED, leaving a merge on main with no merged-record. Four times in rig
// beads (gt-fe1e).
func TestPlanSupersedeKeepsDifferentBranch(t *testing.T) {
	old := mrForBranch("bd-wisp-u2oy", "polecat/capable/bd-4xn+mt9iznfy", "bd-4xn")

	plan := planSupersede([]*beads.Issue{old}, "bd-wisp-fdo4", "polecat/dementus/bd-4xn+mt9jkqgx")

	if got := supersedeIDs(plan); got != "" {
		t.Errorf("supersede = %q, want nothing: a different branch may still land", got)
	}
	if len(plan.Keep) != 1 || plan.Keep[0].ID != "bd-wisp-u2oy" {
		t.Fatalf("kept = %+v, want bd-wisp-u2oy held open", plan.Keep)
	}
	if plan.Keep[0].Branch != "polecat/capable/bd-4xn+mt9iznfy" {
		t.Errorf("kept branch = %q, want the branch that is the reason it was kept",
			plan.Keep[0].Branch)
	}
}

// The u2oy -> fdo4 -> gjyc -> gbj0 chain, as one submission sees it: three
// prior MRs on one bead, one of them this submission's own branch. Exactly one
// is retired and the two live branches survive.
func TestPlanSupersedeChainRetiresOnlyOwnBranch(t *testing.T) {
	mine := "polecat/dementus/bd-4xn+mt9jkqgx"
	olds := []*beads.Issue{
		mrForBranch("bd-wisp-u2oy", "polecat/capable/bd-4xn+mt9iznfy", "bd-4xn"),
		mrForBranch("bd-wisp-fdo4", mine, "bd-4xn"),
		mrForBranch("bd-wisp-ej9r", "polecat/ace/bd-vm6+mszv0blj", "bd-4xn"),
	}

	plan := planSupersede(olds, "bd-wisp-gbj0", mine)

	if got := supersedeIDs(plan); got != "bd-wisp-fdo4" {
		t.Errorf("supersede = %q, want only the MR on this submission's own branch", got)
	}
	if got := keptIDs(plan); got != "bd-wisp-u2oy,bd-wisp-ej9r" {
		t.Errorf("kept = %q, want both live branches held open", got)
	}
}

// The MR just created is never its own predecessor.
func TestPlanSupersedeSkipsTheNewMR(t *testing.T) {
	branch := "polecat/dust/gt-fe1e+mta9hico"
	plan := planSupersede([]*beads.Issue{mrForBranch("gt-wisp-new", branch, "gt-fe1e")}, "gt-wisp-new", branch)

	if len(plan.Supersede) != 0 || len(plan.Keep) != 0 {
		t.Errorf("plan = %+v, want the new MR excluded from both halves", plan)
	}
}

// A record naming no branch can never be the record behind a merge, so keeping
// it buys nothing and jams the queue. Retire it.
func TestPlanSupersedeRetiresBranchlessRecord(t *testing.T) {
	old := &beads.Issue{ID: "gt-wisp-nobranch", Status: "open", Description: "source_issue: gt-fe1e\n"}

	plan := planSupersede([]*beads.Issue{old}, "gt-wisp-new", "polecat/dust/gt-fe1e+mta9hico")

	if got := supersedeIDs(plan); got != "gt-wisp-nobranch" {
		t.Errorf("supersede = %q, want the branchless record retired", got)
	}
}

// A submission that cannot name its own branch must not retire records by
// matching "" against them.
func TestPlanSupersedeWithoutNewBranchKeepsNamedBranches(t *testing.T) {
	old := mrForBranch("gt-wisp-live", "polecat/ace/gt-fe1e+abc", "gt-fe1e")

	plan := planSupersede([]*beads.Issue{old}, "gt-wisp-new", "  ")

	if got := supersedeIDs(plan); got != "" {
		t.Errorf("supersede = %q, want nothing retired when the new branch is unknown", got)
	}
	if got := keptIDs(plan); got != "gt-wisp-live" {
		t.Errorf("kept = %q, want gt-wisp-live", got)
	}
}

// Skipping silently is indistinguishable from finding nothing to skip. The
// notice has to name each MR, its branch, and why it survived.
func TestSupersedeKeptNoticeNamesWhatSurvived(t *testing.T) {
	plan := supersedePlan{Keep: []keptMR{{ID: "bd-wisp-u2oy", Branch: "polecat/capable/bd-4xn+mt9iznfy"}}}

	notice := supersedeKeptNotice(plan, "polecat/dementus/bd-4xn+mt9jkqgx")

	for _, want := range []string{
		"bd-wisp-u2oy",
		"polecat/capable/bd-4xn+mt9iznfy",
		"polecat/dementus/bd-4xn+mt9jkqgx",
		"not superseded",
		"gt-fe1e",
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("kept notice missing %q:\n%s", want, notice)
		}
	}
}

func TestSupersedeKeptNoticeEmptyWhenNothingKept(t *testing.T) {
	if notice := supersedeKeptNotice(supersedePlan{Supersede: []string{"a"}}, "b"); notice != "" {
		t.Errorf("kept notice = %q, want empty so callers can print it unconditionally", notice)
	}
}

// mqListJSONShape mirrors the two anonymous structs runMQList marshals. If
// either of those changes shape this test keeps compiling and stops meaning
// anything, so it asserts on the KEYS in the encoded bytes rather than on a
// decoded value.
func TestMQListJSONEmitsBothCloseReasonCarriers(t *testing.T) {
	// The exact shape of bd-wisp-u2oy: the bead field records the supersede,
	// the description records nothing at all. Reading either alone undercounts.
	issue := &beads.Issue{
		ID:          "bd-wisp-u2oy",
		Status:      "closed",
		CloseReason: "superseded by bd-wisp-fdo4",
		Description: "branch: polecat/capable/bd-4xn+mt9iznfy\ntarget: main\nsource_issue: bd-4xn\n",
	}
	fields := beads.ParseMRFields(issue)

	type listedIssue struct {
		*beads.Issue
		CloseReason   string `json:"close_reason"`
		MRCloseReason string `json:"mr_close_reason"`
	}
	li := listedIssue{Issue: issue}
	li.CloseReason, li.MRCloseReason = closeReasonCarriers(issue, fields)

	raw, err := json.Marshal(li)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Both keys PRESENT. beads.Issue also tags a close_reason field; two
	// embedded fields claiming one JSON name are dropped by encoding/json
	// without an error, which would ship a projection carrying neither carrier.
	for _, key := range []string{"close_reason", "mr_close_reason"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("%s missing from gt mq list --json:\n%s", key, raw)
		}
	}

	var got listedIssue
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if got.CloseReason != "superseded by bd-wisp-fdo4" {
		t.Errorf("close_reason = %q, want the bead field's supersede", got.CloseReason)
	}
	if got.MRCloseReason != "" {
		t.Errorf("mr_close_reason = %q, want empty — this record's description carries no outcome", got.MRCloseReason)
	}
}

// An open MR still emits both keys. Absent and empty are the same bytes to a
// reader, and "records no outcome" must not read as "this tool does not report
// outcomes" (the omitempty trap the widening exists to avoid).
func TestMQListJSONEmitsCloseReasonKeysWhenEmpty(t *testing.T) {
	issue := &beads.Issue{ID: "gt-wisp-open", Status: "open", Description: "branch: b\n"}

	type listedIssue struct {
		*beads.Issue
		CloseReason   string `json:"close_reason"`
		MRCloseReason string `json:"mr_close_reason"`
	}
	li := listedIssue{Issue: issue}
	li.CloseReason, li.MRCloseReason = closeReasonCarriers(issue, beads.ParseMRFields(issue))

	raw, err := json.Marshal(li)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"close_reason":""`, `"mr_close_reason":""`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("open MR JSON missing %s:\n%s", want, raw)
		}
	}
}

func TestCloseReasonCarriersReadsBothSides(t *testing.T) {
	issue := &beads.Issue{
		ID:          "gt-wisp-both",
		CloseReason: "superseded by gt-wisp-next",
		Description: "branch: b\nclose_reason: merged\nmerge_commit: abc123\n",
	}

	closeReason, mrCloseReason := closeReasonCarriers(issue, beads.ParseMRFields(issue))

	if closeReason != "superseded by gt-wisp-next" {
		t.Errorf("close_reason = %q, want the bead field", closeReason)
	}
	if mrCloseReason != "merged" {
		t.Errorf("mr_close_reason = %q, want the description line", mrCloseReason)
	}
}

func TestCloseReasonCarriersTolerateNils(t *testing.T) {
	if a, b := closeReasonCarriers(nil, nil); a != "" || b != "" {
		t.Errorf("closeReasonCarriers(nil, nil) = (%q, %q), want empty", a, b)
	}
}

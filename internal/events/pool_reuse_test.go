package events

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPoolReuseOutcomePayloadRecordsTheOutcome covers gt-ibtb.
//
// The payload used to carry rig, considered and rejected and nothing else, so
// the ONLY way to tell a successful reuse from a total refusal was to compute
// considered - len(rejected) and know that the gate short-circuits. Nothing in
// the event said so. Two witnesses and a deacon each read the rejections at
// face value and jointly scoped a P1 on it (gt-uapr).
func TestPoolReuseOutcomePayloadRecordsTheOutcome(t *testing.T) {
	reuse := PoolReuseOutcomePayload(PoolReuseOutcome{
		Rig:           "gastown",
		Considered:    2,
		Rejections:    []string{"brahmin=push-failed state=done"},
		ReusedPolecat: "chrome",
		GateAccepted:  true,
	})

	if reuse["reused"] != true {
		t.Fatalf("reused = %v, want true", reuse["reused"])
	}
	if reuse["reused_polecat"] != "chrome" {
		t.Fatalf("reused_polecat = %v, want chrome", reuse["reused_polecat"])
	}
	// The compounding half: nine rejections is not a survey of nine polecats
	// turned down, it is a prefix that ended when the tenth was accepted.
	if reuse["candidate_list"] != CandidateListPrefix {
		t.Fatalf("candidate_list = %v, want %q", reuse["candidate_list"], CandidateListPrefix)
	}

	refusal := PoolReuseOutcomePayload(PoolReuseOutcome{
		Rig:        "gastown",
		Considered: 3,
		Rejections: []string{"a=git-dirty state=idle", "b=git-dirty state=idle", "c=git-dirty state=idle"},
	})
	if refusal["reused"] != false {
		t.Fatalf("reused = %v, want false", refusal["reused"])
	}
	if _, ok := refusal["reused_polecat"]; ok {
		t.Fatalf("reused_polecat must be absent when nothing was reused, got %v", refusal["reused_polecat"])
	}
	if refusal["candidate_list"] != CandidateListComplete {
		t.Fatalf("candidate_list = %v, want %q", refusal["candidate_list"], CandidateListComplete)
	}

	// "reused" must survive a JSON round trip as a present false. An omitempty
	// here would make "never recorded" and "recorded false" the same bytes,
	// which is the ambiguity this field exists to end.
	encoded, err := json.Marshal(refusal)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, present := decoded["reused"]
	if !present {
		t.Fatalf("reused key dropped by the round trip: %s", encoded)
	}
	if got != false {
		t.Fatalf("reused = %v after round trip, want false", got)
	}
}

// TestPoolReuseOutcomeSeparatesGateFromReuse pins the case that made
// GateAccepted a field of its own rather than something derived from the reuse.
//
// The gate can clear a candidate that ReuseIdlePolecat then fails on. That is a
// refusal — control falls through to a fresh worktree — but it is a refusal
// reported over a PREFIX, because the gate short-circuited and never evaluated
// the candidates after the one it cleared. Deriving "prefix" from "reused"
// would call that list complete and hand the reader the same wrong picture from
// the other direction.
func TestPoolReuseOutcomeSeparatesGateFromReuse(t *testing.T) {
	p := PoolReuseOutcomePayload(PoolReuseOutcome{
		Rig:        "gastown",
		Considered: 2,
		Rejections: []string{"brahmin=push-failed state=done", "chrome=reuse-failed state=idle err=branch in use"},
		// The gate accepted chrome; reuse then failed on it.
		GateAccepted: true,
	})
	if p["reused"] != false {
		t.Fatalf("reused = %v, want false — the reuse attempt failed", p["reused"])
	}
	if p["candidate_list"] != CandidateListPrefix {
		t.Fatalf("candidate_list = %v, want %q — the gate short-circuited before evaluating the rest", p["candidate_list"], CandidateListPrefix)
	}
}

// TestPoolReuseSummaryStatesTheOutcome is the bead's acceptance criterion: a
// reader of the feed alone, with no source access, can state for any event
// whether a polecat was reused and which one.
func TestPoolReuseSummaryStatesTheOutcome(t *testing.T) {
	reuse := PoolReuseSummary(TypePoolReuseSkipped, PoolReuseOutcomePayload(PoolReuseOutcome{
		Rig:           "gastown",
		Considered:    2,
		Rejections:    []string{"brahmin=push-failed state=done"},
		ReusedPolecat: "chrome",
		GateAccepted:  true,
	}))
	for _, want := range []string{"REUSED", "chrome", "PREFIX", "brahmin=push-failed"} {
		if !strings.Contains(reuse, want) {
			t.Fatalf("reuse summary %q must contain %q", reuse, want)
		}
	}
	// The word that started the P1 must not appear on a line describing a
	// success — this is the assertion that would have caught the original bug.
	if strings.Contains(reuse, "REFUSED") {
		t.Fatalf("reuse summary %q calls a successful reuse a refusal", reuse)
	}

	refusal := PoolReuseSummary(TypePoolReuseRefused, PoolReuseOutcomePayload(PoolReuseOutcome{
		Rig:        "gastown",
		Considered: 1,
		Rejections: []string{"brahmin=git-dirty state=idle"},
	}))
	if !strings.Contains(refusal, "REFUSED") || !strings.Contains(refusal, "fresh worktree") {
		t.Fatalf("refusal summary %q must name the refusal and its cost", refusal)
	}
	if strings.Contains(refusal, "REUSED") {
		t.Fatalf("refusal summary %q claims a reuse", refusal)
	}
	// Nothing was accepted, so this list really is the whole roster and must
	// not be hedged as a prefix.
	if strings.Contains(refusal, "PREFIX") {
		t.Fatalf("refusal summary %q calls a complete survey a prefix", refusal)
	}
}

// TestPoolReuseSummaryReadsJSONDecodedPayloads guards the surface that actually
// renders: the feed file is JSON, so `considered` arrives as float64 and
// `rejected` as []interface{}. A summary that only handled the in-process types
// would silently report 0 considered and no rejections on every real event —
// a fresh way to render a confident wrong answer.
func TestPoolReuseSummaryReadsJSONDecodedPayloads(t *testing.T) {
	encoded, err := json.Marshal(PoolReuseOutcomePayload(PoolReuseOutcome{
		Rig:           "gastown",
		Considered:    2,
		Rejections:    []string{"brahmin=push-failed state=done"},
		ReusedPolecat: "chrome",
		GateAccepted:  true,
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := PoolReuseSummary(TypePoolReuseSkipped, payload)
	for _, want := range []string{"chrome", "1 of 2", "brahmin=push-failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q must contain %q after a JSON round trip", got, want)
		}
	}
}

// TestPoolReuseSummaryDoesNotGuessLegacyOutcomes covers the 21 events already
// in the feed, which carry no "reused" key. The arithmetic tell
// (considered == len(rejected)+1) does identify them as successes, but a
// renderer that applied it would be teaching readers the short-circuit contract
// as the price of reading a log line — the exact burden this bead removes. Say
// the outcome is unrecorded instead.
func TestPoolReuseSummaryDoesNotGuessLegacyOutcomes(t *testing.T) {
	legacy := map[string]interface{}{
		"rig":        "gastown",
		"considered": 2,
		"rejected":   []string{"brahmin=push-failed state=done"},
	}
	got := PoolReuseSummary(TypePoolReuseRefused, legacy)
	if !strings.Contains(got, "NOT RECORDED") {
		t.Fatalf("legacy summary %q must say the outcome is unrecorded", got)
	}
	if strings.Contains(got, "REUSED") || strings.Contains(got, "REFUSED") {
		t.Fatalf("legacy summary %q asserts an outcome the event never carried", got)
	}

	// A post-split type with a legacy-shaped payload cannot be ambiguous: the
	// type itself only exists for the reuse case.
	skipped := PoolReuseSummary(TypePoolReuseSkipped, legacy)
	if !strings.Contains(skipped, "REUSED") {
		t.Fatalf("summary %q must read the outcome off the type when the payload omits it", skipped)
	}
}

// TestPoolReuseSummaryCarriesTheLookupError keeps the third silent failure from
// gt-49dp visible: FindIdlePolecat's error used to be discarded unexamined.
func TestPoolReuseSummaryCarriesTheLookupError(t *testing.T) {
	got := PoolReuseSummary(TypePoolReuseRefused, PoolReuseOutcomePayload(PoolReuseOutcome{
		Rig:         "gastown",
		LookupError: "listing polecats: permission denied",
	}))
	if !strings.Contains(got, "permission denied") {
		t.Fatalf("summary %q must carry the lookup error", got)
	}
}

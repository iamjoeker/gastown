package events

import (
	"testing"
)

func TestRigOfIdentity(t *testing.T) {
	tests := []struct {
		identity string
		want     string
		why      string
	}{
		// Path form — the common case.
		{"gastown/witness", "gastown", "rig agent"},
		{"gastown/polecats/dust", "gastown", "rig polecat"},
		{"beads/refinery", "beads", "another rig"},
		{"duly_noted/witness", "duly_noted", "underscore in rig name"},
		{"testrig/refinery", "testrig", "rig that only exists in old feed lines"},

		// Agent-bead form.
		{"gt-gastown-witness", "gastown", "agent bead ID"},
		{"gt-gastown-crew-joe", "gastown", "four-segment agent bead ID"},
		{"gt-mycat", "", "two segments: a bare name, no rig in it"},

		// Town-scoped identities must never attribute to a rig, or the agents
		// that use them would filter themselves into silence.
		{"mayor/", "", "mayor is town-scoped"},
		{"mayor", "", "mayor without trailing slash"},
		{"deacon/", "", "deacon is town-scoped"},
		{"deacon/dogs/alpha", "", "deacon dogs are town-scoped"},
		{"boot", "", "boot watchdog is town-scoped"},
		{"gt", "", "gt itself is not a rig"},
		{"unknown", "", "unattributed emitter"},
		{"hq-mayor", "", "hq bead ID"},
		{"hq-wisp-si3dk4", "", "wisp bead ID"},
		{"gt-wisp-d74rh", "", "wisp bead ID in gt- form must not read as rig 'wisp'"},

		// Degenerate input. Anything that cannot be a rig directory name must
		// come back "" so the event falls into the fail-open path rather than
		// being attributed to a rig that does not exist.
		{"", "", "empty"},
		{"   ", "", "whitespace"},
		{"/witness", "", "leading slash leaves an empty first segment"},
		{"channel:refinery", "", "gt nudge puts channel:<name> in payload.target"},
		{"-leading-dash", "", "not a plausible directory name"},
		{"a b", "", "space is not a plausible directory name"},
	}

	for _, tt := range tests {
		if got := RigOfIdentity(tt.identity); got != tt.want {
			t.Errorf("RigOfIdentity(%q) = %q, want %q (%s)", tt.identity, got, tt.want, tt.why)
		}
	}
}

// event builds an Event the way the feed writes them, so the tests below read
// against the real shape rather than an invented one.
func event(typ, actor string, payload map[string]interface{}) Event {
	return Event{
		Timestamp:  "2026-08-23T01:45:06Z",
		Source:     "gt",
		Type:       typ,
		Actor:      actor,
		Payload:    payload,
		Visibility: VisibilityFeed,
	}
}

func TestEventRigs(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  []string
	}{
		{
			name:  "spawn declares its rig in the payload",
			event: event(TypeSpawn, "gt", map[string]interface{}{"rig": "gastown", "polecat": "dust"}),
			want:  []string{"gastown"},
		},
		{
			name:  "sling has no rig field; the target names it",
			event: event(TypeSling, "unknown", map[string]interface{}{"bead": "gt-p54t", "target": "gastown/polecats/dust"}),
			want:  []string{"gastown"},
		},
		{
			name:  "session_start attributes through the actor",
			event: event(TypeSessionStart, "beads/polecats/ace", map[string]interface{}{"role": "beads/polecats/ace"}),
			want:  []string{"beads"},
		},
		{
			name: "cross-rig mail concerns BOTH rigs",
			event: event(TypeMail, "beads/polecats/ace", map[string]interface{}{
				"to": "gastown/witness", "subject": "heads up",
			}),
			want: []string{"beads", "gastown"},
		},
		{
			name:  "mayor to deacon names no rig at all",
			event: event(TypeMail, "deacon/", map[string]interface{}{"to": "mayor/", "subject": "patrol findings"}),
			want:  nil,
		},
		{
			name:  "boot session names no rig",
			event: event(TypeSessionStart, "boot", map[string]interface{}{"role": "boot", "cwd": "/home/x/gt/deacon/dogs/boot"}),
			want:  nil,
		},
		{
			name: "payload.rig carrying a wisp ID is not read as a rig",
			event: event(TypeEscalationSent, "mayor/", map[string]interface{}{
				"rig": "hq-wisp-si3dk4", "reason": "x",
			}),
			want: nil,
		},
		{
			name: "empty payload.rig falls back to the actor",
			event: event(TypeNudge, "beads/ace", map[string]interface{}{
				"rig": "", "target": "hq-mayor", "reason": "x",
			}),
			want: []string{"beads"},
		},
		{
			name: "session_death attributes through the agent field",
			event: event(TypeSessionDeath, "gt", map[string]interface{}{
				"session": "gt-gastown-polecats-toast", "agent": "gastown/polecats/toast",
				"reason": "zombie cleanup", "caller": "daemon",
			}),
			want: []string{"gastown"},
		},
		{
			name:  "no payload at all",
			event: event(TypeHalt, "gt", nil),
			want:  nil,
		},
		{
			// gt nudge to a channel: the only identity besides a town-scoped
			// actor is "channel:<name>". Reading that as a rig would suppress
			// the event for every real rig, so it must attribute to none and
			// fall into the fail-open path.
			name: "channel nudge from a town actor attributes to no rig",
			event: event(TypeNudge, "mayor/", map[string]interface{}{
				"rig": "", "target": "channel:refinery", "reason": "wake up",
			}),
			want: nil,
		},
		{
			name: "channel nudge from a rig agent still attributes to that rig",
			event: event(TypeNudge, "gastown/witness", map[string]interface{}{
				"rig": "", "target": "channel:refinery", "reason": "wake up",
			}),
			want: []string{"gastown"},
		},
		{
			// gt escalate passes the bead ID where the payload helper expects a
			// rig, and the escalating agent lands in "target".
			name: "escalation attributes through target, not the bead ID in rig",
			event: event(TypeEscalationSent, "gastown/witness", map[string]interface{}{
				"rig": "gt-wisp-abc", "target": "gastown/witness", "to": "mayor/",
			}),
			want: []string{"gastown"},
		},
		{
			name: "escalation ack attributes through acked_by",
			event: event(TypeEscalationAcked, "unknown", map[string]interface{}{
				"escalation_id": "hq-1agr", "acked_by": "beads/witness",
			}),
			want: []string{"beads"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EventRigs(tt.event)
			if len(got) != len(tt.want) {
				t.Fatalf("EventRigs() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("EventRigs() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestLineWakesRig(t *testing.T) {
	const (
		gastownSpawn  = `{"ts":"2026-08-23T01:45:06Z","source":"gt","type":"spawn","actor":"gt","payload":{"polecat":"dust","rig":"gastown"},"visibility":"feed"}`
		beadsSpawn    = `{"ts":"2026-08-23T01:45:06Z","source":"gt","type":"spawn","actor":"gt","payload":{"polecat":"ace","rig":"beads"},"visibility":"feed"}`
		beadsToGastwn = `{"ts":"2026-08-23T01:41:54Z","source":"gt","type":"mail","actor":"beads/polecats/ace","payload":{"subject":"cross-rig","to":"gastown/witness"},"visibility":"feed"}`
		townMail      = `{"ts":"2026-08-23T01:42:22Z","source":"gt","type":"mail","actor":"deacon/","payload":{"subject":"digest","to":"mayor/"},"visibility":"feed"}`
		garbage       = `not json at all`
	)

	tests := []struct {
		name string
		line string
		rig  string
		want bool
		why  string
	}{
		{"own rig wakes", gastownSpawn, "gastown", true, "the event is mine"},
		{"foreign rig does not wake", beadsSpawn, "gastown", false, "confined to beads — this is the defect being fixed"},
		{"foreign rig wakes its owner", beadsSpawn, "beads", true, "control: the same line still wakes beads"},
		{"cross-rig mail wakes the addressee", beadsToGastwn, "gastown", true, "mail wakes are town-wide by design"},
		{"cross-rig mail wakes the sender's rig too", beadsToGastwn, "beads", true, "the sender's rig also has a stake"},
		{"cross-rig mail does not wake a third rig", beadsToGastwn, "duly_noted", false, "neither party is duly_noted"},
		{"town mail wakes every rig", townMail, "gastown", true, "town-scoped, not confined to another rig"},
		{"unparseable line wakes", garbage, "gastown", true, "fail open: never drop a signal we cannot read"},
		{"blank line wakes", "", "gastown", true, "fail open"},
		{"town-wide watcher takes everything", beadsSpawn, "", true, "empty rig means no filtering"},
		{"town-wide watcher takes town mail", townMail, "", true, "empty rig means no filtering"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LineWakesRig(tt.line, tt.rig); got != tt.want {
				t.Errorf("LineWakesRig(rig=%q) = %v, want %v (%s)", tt.rig, got, tt.want, tt.why)
			}
		})
	}
}

// feedSample is a slice of the shapes the live feed actually produces, in the
// proportions it produces them: mail dominates, then session churn, then
// dispatch. Counting wakes over a fixed corpus is what turns "the filter works"
// into a number (gt-p54t's acceptance criteria).
var feedSample = []string{
	// --- confined to beads (should NOT wake gastown) ---
	`{"type":"mail","actor":"beads/witness","payload":{"to":"beads/refinery","subject":"MERGE_READY"}}`,
	`{"type":"mail","actor":"beads/refinery","payload":{"to":"beads/witness","subject":"merged"}}`,
	`{"type":"session_death","actor":"gt","payload":{"agent":"beads/polecats/slit","reason":"zombie cleanup","caller":"daemon"}}`,
	`{"type":"done","actor":"beads/polecats/ace","payload":{"bead":"bd-1","branch":"polecat/ace/bd-1"}}`,
	`{"type":"spawn","actor":"gt","payload":{"rig":"beads","polecat":"capable"}}`,
	`{"type":"scheduler_dispatch","actor":"unknown","payload":{"bead":"bd-2","rig":"beads","polecat":"capable"}}`,
	`{"type":"sling","actor":"unknown","payload":{"bead":"bd-2","target":"beads/polecats/capable"}}`,
	// --- confined to duly_noted (should NOT wake gastown) ---
	`{"type":"mail","actor":"duly_noted/witness","payload":{"to":"duly_noted/refinery","subject":"queue"}}`,
	`{"type":"session_start","actor":"duly_noted/witness","payload":{"role":"duly_noted/witness"}}`,
	// --- town-scoped (SHOULD wake gastown: not confined to another rig) ---
	`{"type":"mail","actor":"deacon/","payload":{"to":"mayor/","subject":"Wisp Compaction"}}`,
	`{"type":"escalation_closed","actor":"mayor/","payload":{"escalation_id":"hq-1agr","closed_by":"mayor/","reason":"RESOLVED"}}`,
	`{"type":"session_start","actor":"boot","payload":{"role":"boot"}}`,
	// --- gastown's own (SHOULD wake gastown) ---
	`{"type":"spawn","actor":"gt","payload":{"rig":"gastown","polecat":"dust"}}`,
	`{"type":"sling","actor":"unknown","payload":{"bead":"gt-p54t","target":"gastown/polecats/dust"}}`,
	// --- cross-rig mail addressed to gastown (MUST wake gastown) ---
	`{"type":"mail","actor":"beads/polecats/ace","payload":{"to":"gastown/witness","subject":"FYI"}}`,
}

func countWakes(corpus []string, rig string) int {
	n := 0
	for _, line := range corpus {
		if LineWakesRig(line, rig) {
			n++
		}
	}
	return n
}

// TestWakeCountsOverFeedSample measures the wake count for the same event
// volume before and after filtering, and pins BOTH halves: the suppression and
// the known-positive that must still fire. A zero suppression count and a
// swallowed cross-rig mail would both pass a test that only asserted one half.
func TestWakeCountsOverFeedSample(t *testing.T) {
	total := len(feedSample)

	// Before: no filter. Every line in the town's feed is a wake.
	townWide := countWakes(feedSample, "")
	if townWide != total {
		t.Fatalf("town-wide wake count = %d, want %d (all events): the unfiltered "+
			"path must be unchanged, or the comparison below is against a moving baseline",
			townWide, total)
	}

	// After: scoped to gastown.
	gastown := countWakes(feedSample, "gastown")
	suppressed := total - gastown

	t.Logf("same %d events: town-wide wakes=%d, gastown-scoped wakes=%d, suppressed=%d (%.0f%%)",
		total, townWide, gastown, suppressed, 100*float64(suppressed)/float64(total))

	if suppressed == 0 {
		t.Fatal("no events suppressed: the filter is inert")
	}
	if gastown == 0 {
		t.Fatal("no events wake gastown: the filter is deaf, which is worse than the defect")
	}

	// The known-positive half, stated as its own assertion so a regression
	// naming it appears in the failure output.
	crossRigMail := feedSample[len(feedSample)-1]
	if !LineWakesRig(crossRigMail, "gastown") {
		t.Error("cross-rig mail addressed to gastown/witness did not wake gastown: " +
			"mail wakes are town-wide by design and this filter must not swallow them")
	}

	// An idle rig with nothing of its own in the corpus still hears town
	// traffic but not the two busy rigs' private traffic.
	idle := countWakes(feedSample, "duly_noted")
	t.Logf("idle rig duly_noted: wakes=%d of %d", idle, total)
	if idle >= gastown {
		t.Errorf("idle rig duly_noted woke %d times vs busy rig gastown's %d: "+
			"an idle rig must not pay a busier rig's event volume", idle, gastown)
	}
}

package formula

import (
	"strings"
	"testing"
)

// A generic gt nudge (free-form text injected straight into the refinery's
// pane, not one of the structured events await-event watches for) was
// observed twice in one night to get acknowledged in prose ("Ready to
// process incoming MRs") without the queue ever being touched. Only a
// highly explicit step-by-step nudge reliably triggered real work. See
// gt-sbog.
//
// The fix is a formula/prompt fix: the inbox-check step's handling of
// "PATROL: Wake up" (and, by extension, any other nudge received while
// parked) must say explicitly to continue on to queue-scan rather than
// merely acknowledging and archiving.

// TestInboxCheckTreatsWakeAsSignalNotSmallTalk pins the "PATROL: Wake up"
// step to explicitly continuing to queue-scan, not just archiving.
func TestInboxCheckTreatsWakeAsSignalNotSmallTalk(t *testing.T) {
	body := readRefineryPatrol(t)

	const marker = "**PATROL: Wake up**"
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("refinery patrol formula: %q section not found", marker)
	}
	// Bound to the rest of the inbox-check step so a match elsewhere in the
	// formula cannot satisfy this.
	section := body[idx:]
	if end := strings.Index(section, "**HELP / Blocked**"); end >= 0 {
		section = section[:end]
	}

	if !strings.Contains(section, "gt mq list") {
		t.Errorf("PATROL: Wake up handling does not tell the refinery to run "+
			"`gt mq list` — without it, archiving the wake message is the whole "+
			"response and the queue never gets checked. See gt-sbog. Section:\n%s",
			section)
	}
	if !strings.Contains(section, "gt-sbog") {
		t.Error("PATROL: Wake up handling does not cite gt-sbog, so a future " +
			"editor has no signal this text is load-bearing against a known " +
			"regression")
	}
	// The regression was specifically an acknowledge-only reply. Guard against
	// the step text narrowing back to "acknowledge" without also directing
	// further action.
	if !strings.Contains(strings.ToLower(section), "wake signal") {
		t.Error("PATROL: Wake up handling no longer frames the message as a " +
			"wake signal requiring action, not a chat reply")
	}
}

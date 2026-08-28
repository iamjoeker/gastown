package beads

import (
	"encoding/json"
	"testing"
)

// TestIssueDecodesCloseReason pins the WIRING, not the struct. Adding a field is
// not the same as receiving one, and the whole gt-xm6w discharge is inert if
// close_reason arrives as the empty string — which is byte-identical to a bead
// closed with no reason at all, and therefore fails silently in the safe
// direction, where nobody would look for it.
//
// The payload is the shape `bd show <id> --json` actually returns: a
// single-element array, with the reason as recorded on gt-05a.
func TestIssueDecodesCloseReason(t *testing.T) {
	const payload = `[{"id":"gt-05a","title":"t","status":"closed","issue_type":"bug",
	  "created_at":"2026-08-01T00:00:00Z","updated_at":"2026-08-02T00:00:00Z",
	  "closed_at":"2026-08-02T00:00:00Z",
	  "close_reason":"superseded by gt-k3h (same two defects)"}]`

	var issues []*Issue
	if err := json.Unmarshal([]byte(payload), &issues); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}
	if got, want := issues[0].CloseReason, "superseded by gt-k3h (same two defects)"; got != want {
		t.Fatalf("CloseReason = %q, want %q", got, want)
	}

	// The negative control: a closed bead with no reason must NOT come back
	// carrying one. Without this the test above passes on any non-empty default.
	var bare []*Issue
	if err := json.Unmarshal([]byte(`[{"id":"gt-05p1","status":"closed"}]`), &bare); err != nil {
		t.Fatalf("unmarshal bare: %v", err)
	}
	if bare[0].CloseReason != "" {
		t.Fatalf("bare CloseReason = %q, want empty", bare[0].CloseReason)
	}
}

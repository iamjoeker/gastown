package refinery

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

type fakeReopenStore struct {
	issues       map[string]*beads.Issue
	reopenCalls  []string
	lastReason   string
	showErr      error
	reopenErr    error
	reopenCalled int
}

func newFakeReopenStore() *fakeReopenStore {
	return &fakeReopenStore{issues: map[string]*beads.Issue{}}
}

func (f *fakeReopenStore) add(issue *beads.Issue) {
	f.issues[issue.ID] = issue
}

func (f *fakeReopenStore) Show(id string) (*beads.Issue, error) {
	if f.showErr != nil {
		return nil, f.showErr
	}
	issue, ok := f.issues[id]
	if !ok {
		return nil, beads.ErrNotFound
	}
	return issue, nil
}

func (f *fakeReopenStore) Reopen(id, reason string) error {
	f.reopenCalled++
	f.reopenCalls = append(f.reopenCalls, id)
	f.lastReason = reason
	if f.reopenErr != nil {
		return f.reopenErr
	}
	if issue, ok := f.issues[id]; ok {
		issue.Status = string(beads.StatusOpen)
	}
	return nil
}

// A rejection means the work is not done, so a closed source issue must come
// back open — otherwise the branch sits on origin with nothing able to re-sling
// it (gt-a46b).
func TestReopenRejectedWorkBead_ClosedIssueIsReopened(t *testing.T) {
	store := newFakeReopenStore()
	store.add(&beads.Issue{ID: "gt-egq9", Title: "feature", Type: "bug", Status: string(beads.StatusClosed)})

	result := reopenRejectedWorkBead(store, "gt-egq9", "mr-nux-1", "gates failed")

	if !result.Reopened {
		t.Fatalf("Reopened = false, want true (err: %v, skip: %q)", result.Err, result.SkipReason)
	}
	if result.Status != string(beads.StatusOpen) {
		t.Errorf("Status = %q, want open", result.Status)
	}
	if result.Err != nil {
		t.Errorf("Err = %v, want nil", result.Err)
	}
	if got := store.issues["gt-egq9"].Status; got != string(beads.StatusOpen) {
		t.Errorf("issue status = %q, want open", got)
	}
	if !strings.Contains(store.lastReason, "mr-nux-1") || !strings.Contains(store.lastReason, "gates failed") {
		t.Errorf("reopen reason = %q, want it to name the MR and the rejection reason", store.lastReason)
	}
}

// The control from gt-a46b: an issue that was OPEN before its rejection is
// still open after, and reject writes nothing. This is what separates "reject
// closes beads" (false) from "reject cannot reopen them" (the defect).
func TestReopenRejectedWorkBead_OpenIssueIsUntouched(t *testing.T) {
	store := newFakeReopenStore()
	store.add(&beads.Issue{ID: "gt-1jrl", Title: "feature", Type: "bug", Status: string(beads.StatusOpen)})

	result := reopenRejectedWorkBead(store, "gt-1jrl", "mr-nux-1", "gates failed")

	if result.Reopened {
		t.Errorf("Reopened = true, want false for an already-open issue")
	}
	if store.reopenCalled != 0 {
		t.Errorf("Reopen called %d times, want 0", store.reopenCalled)
	}
	if result.Status != string(beads.StatusOpen) {
		t.Errorf("Status = %q, want open", result.Status)
	}
}

func TestReopenRejectedWorkBead_NonClosedStatusesReportedNotRewritten(t *testing.T) {
	for _, status := range []string{
		string(beads.StatusInProgress),
		string(beads.IssueStatusHooked),
		string(beads.StatusBlocked),
		string(beads.StatusDeferred),
	} {
		t.Run(status, func(t *testing.T) {
			store := newFakeReopenStore()
			store.add(&beads.Issue{ID: "gt-abc", Title: "feature", Type: "bug", Status: status})

			result := reopenRejectedWorkBead(store, "gt-abc", "mr-nux-1", "gates failed")

			if store.reopenCalled != 0 {
				t.Errorf("Reopen called for %s issue", status)
			}
			if result.Status != status {
				t.Errorf("Status = %q, want %q", result.Status, status)
			}
			if result.SkipReason != "" {
				t.Errorf("SkipReason = %q, want empty (%s is re-slingable)", result.SkipReason, status)
			}
		})
	}
}

// Tombstones are terminal but are not ours to resurrect: report, do not write.
func TestReopenRejectedWorkBead_TombstoneIsReportedNotReopened(t *testing.T) {
	store := newFakeReopenStore()
	store.add(&beads.Issue{ID: "gt-abc", Title: "feature", Type: "bug", Status: string(beads.StatusTombstone)})

	result := reopenRejectedWorkBead(store, "gt-abc", "mr-nux-1", "gates failed")

	if store.reopenCalled != 0 {
		t.Errorf("Reopen called on a tombstoned issue")
	}
	if result.Reopened {
		t.Errorf("Reopened = true, want false")
	}
	if !strings.Contains(result.SkipReason, string(beads.StatusTombstone)) {
		t.Errorf("SkipReason = %q, want it to name the tombstone status", result.SkipReason)
	}
}

// Wisps, agent beads, and other runtime state are not work beads. The close
// path refuses them; the reopen path refuses them for the same reason.
func TestReopenRejectedWorkBead_NonConcreteIssueIsSkipped(t *testing.T) {
	store := newFakeReopenStore()
	store.add(&beads.Issue{ID: "gt-wisp-abc", Title: "wisp", Type: "wisp", Status: string(beads.StatusClosed)})

	result := reopenRejectedWorkBead(store, "gt-wisp-abc", "mr-nux-1", "gates failed")

	if store.reopenCalled != 0 {
		t.Errorf("Reopen called on a wisp")
	}
	if result.SkipReason == "" {
		t.Errorf("SkipReason empty, want a reason naming why the wisp was left alone")
	}
}

// "null" is a real value in MR descriptions; it must not become a bead ID.
func TestReopenRejectedWorkBead_NoWorkBead(t *testing.T) {
	for _, id := range []string{"", "  ", "null"} {
		store := newFakeReopenStore()

		result := reopenRejectedWorkBead(store, id, "mr-nux-1", "gates failed")

		if result.WorkBeadID != "" {
			t.Errorf("WorkBeadID = %q for input %q, want empty", result.WorkBeadID, id)
		}
		if result.Err != nil {
			t.Errorf("Err = %v for input %q, want nil", result.Err, id)
		}
		if store.reopenCalled != 0 {
			t.Errorf("Reopen called for input %q", id)
		}
	}
}

// A read failure must surface as an error, not as a silent "looks fine": the
// whole defect was a line that read as an assertion about a state nobody checked.
func TestReopenRejectedWorkBead_ShowFailureIsReported(t *testing.T) {
	store := newFakeReopenStore()
	store.showErr = errors.New("dolt unreachable")

	result := reopenRejectedWorkBead(store, "gt-abc", "mr-nux-1", "gates failed")

	if result.Err == nil {
		t.Fatalf("Err = nil, want the read failure surfaced")
	}
	if result.Reopened {
		t.Errorf("Reopened = true after a failed read")
	}
	if result.Status != "" {
		t.Errorf("Status = %q, want empty when the status could not be read", result.Status)
	}
}

func TestReopenRejectedWorkBead_ReopenFailureIsReported(t *testing.T) {
	store := newFakeReopenStore()
	store.add(&beads.Issue{ID: "gt-abc", Title: "feature", Type: "bug", Status: string(beads.StatusClosed)})
	store.reopenErr = errors.New("permission denied")

	result := reopenRejectedWorkBead(store, "gt-abc", "mr-nux-1", "gates failed")

	if result.Err == nil {
		t.Fatalf("Err = nil, want the reopen failure surfaced")
	}
	if result.Reopened {
		t.Errorf("Reopened = true after a failed reopen")
	}
	if result.Status != string(beads.StatusClosed) {
		t.Errorf("Status = %q, want the issue's real (still closed) status", result.Status)
	}
}

func TestReopenRejectedWorkBead_MissingIssueIsReported(t *testing.T) {
	store := newFakeReopenStore()

	result := reopenRejectedWorkBead(store, "gt-gone", "mr-nux-1", "gates failed")

	if result.Err == nil {
		t.Fatalf("Err = nil, want an error for a missing issue")
	}
	if result.Reopened {
		t.Errorf("Reopened = true for a missing issue")
	}
}

package doltserver

import "testing"

// The balk thresholds moved here from internal/cmd because internal/doctor
// cannot import internal/cmd, so `gt doctor --fix` reached the same bulk
// deletion with no threshold check of any kind. These tests pin the predicate
// both callers now share. (gt-baj6)

func TestOrphanRatioBalks(t *testing.T) {
	cases := []struct {
		name    string
		orphans int
		total   int
		want    bool
	}{
		// The shape gt-ti84 measured: a small town with real test pollution.
		{"six of eleven", 6, 11, true},
		{"exactly half does not trip", 5, 10, false},
		{"just over half", 6, 10, true},
		{"minority", 3, 11, false},
		{"few orphans in a tiny town", 3, 5, false},
		{"one more than the minimum in a tiny town", 4, 5, true},
		{"every database flagged", 11, 11, true},
		// Not "the ratio is fine" — the caller could not count the town, and
		// each caller decides what to do about that.
		{"no total", 4, 0, false},
		{"negative total", 4, -1, false},
		{"no orphans", 0, 11, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OrphanRatioBalks(tc.orphans, tc.total); got != tc.want {
				t.Errorf("OrphanRatioBalks(%d, %d) = %v, want %v", tc.orphans, tc.total, got, tc.want)
			}
		})
	}
}

func TestEvaluateCleanupBalkRatioIsClearedByForce(t *testing.T) {
	if got := EvaluateCleanupBalk(6, 11, false); got != BalkOrphanRatio {
		t.Errorf("6 of 11 must balk on the ratio, got %v", got)
	}
	if got := EvaluateCleanupBalk(6, 11, true); got != BalkNone {
		t.Errorf("--force must clear the ratio balk, got %v", got)
	}
}

func TestEvaluateCleanupBalkVolumeSurvivesForce(t *testing.T) {
	// The SQL-cleanup ceiling is about how long DROP takes, not about whether
	// the operator trusts the detection, so no override clears it.
	if got := EvaluateCleanupBalk(MaxSQLCleanup+1, 200, false); got != BalkTooManyOrphans {
		t.Errorf("past the SQL ceiling must balk, got %v", got)
	}
	if got := EvaluateCleanupBalk(MaxSQLCleanup+1, 200, true); got != BalkTooManyOrphans {
		t.Errorf("--force must not clear the SQL ceiling, got %v", got)
	}
	if got := EvaluateCleanupBalk(MaxSQLCleanup, 200, false); got != BalkNone {
		t.Errorf("exactly the ceiling must proceed, got %v", got)
	}
}

func TestEvaluateCleanupBalkOrderWhenBothTrip(t *testing.T) {
	// Order is part of the answer: surfaces that only report which refusal will
	// be raised must be able to ask this instead of re-deriving it.
	if got := EvaluateCleanupBalk(MaxSQLCleanup+1, MaxSQLCleanup+1, false); got != BalkOrphanRatio {
		t.Errorf("with both tripped the ratio comes first, got %v", got)
	}
	if got := EvaluateCleanupBalk(MaxSQLCleanup+1, MaxSQLCleanup+1, true); got != BalkTooManyOrphans {
		t.Errorf("with the ratio cleared by force the volume balk remains, got %v", got)
	}
}

func TestEvaluateCleanupBalkClean(t *testing.T) {
	if got := EvaluateCleanupBalk(2, 11, false); got != BalkNone {
		t.Errorf("2 of 11 must proceed, got %v", got)
	}
	if got := EvaluateCleanupBalk(0, 11, false); got != BalkNone {
		t.Errorf("nothing to delete must proceed, got %v", got)
	}
}

// TestEvaluateCleanupBalkUnknownTotalIsNotAClearRatio records that an unknown
// total does not raise the ratio balk here. That is deliberate and it is why
// both callers check the count themselves: `gt dolt cleanup` fails closed on the
// ListDatabases error, and the doctor check refuses when it has no count. If
// this ever starts returning BalkOrphanRatio, those two guards become dead code
// rather than wrong — but they must not be removed on the strength of this
// function alone.
func TestEvaluateCleanupBalkUnknownTotalIsNotAClearRatio(t *testing.T) {
	if got := EvaluateCleanupBalk(6, 0, false); got != BalkNone {
		t.Errorf("unknown total returns BalkNone from the predicate, got %v", got)
	}
}

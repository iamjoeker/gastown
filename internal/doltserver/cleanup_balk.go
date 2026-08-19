package doltserver

// This file holds the refusals that stand in front of a BULK orphan deletion,
// as opposed to the per-database guards in RemoveDatabase.
//
// It lives here, next to FindOrphanedDatabases, because more than one command
// reaches the same deletion: `gt dolt cleanup` and `gt doctor --fix`. The balk
// used to live in internal/cmd, which internal/doctor cannot import, so
// `gt doctor --fix` walked past all of it and force-deleted every orphan with
// no threshold check of any kind — the pattern where a guard installed on one
// path is inert on the others. (gt-baj6)
//
// Callers render their own operator-facing text; the decision itself is made
// once, here.

// Thresholds for the orphan-ratio balk (gt-xvh). A high ratio means the run is
// about to delete most of the town, which is worth a stop either way — but the
// ratio is NOT evidence that detection broke: it crosses 50% because a town is
// small, so a handful of genuine test fixtures is already a majority. Surfaces
// that explain this refusal must not rank the two explanations. (gt-ti84)
const (
	OrphanRatioBalkFraction = 0.5
	OrphanRatioBalkMinimum  = 3
)

// MaxSQLCleanup is the number of orphans past which DROP DATABASE per orphan
// takes hours against an overloaded server, so cleanup sends the operator to
// the filesystem instead. (Clown Show #18: 245 orphans at 27s latency)
const MaxSQLCleanup = 50

// CleanupBalkKind names which refusal a bulk orphan deletion raises, so a
// surface that only needs to say THAT the deletion will refuse does not have to
// re-derive it from the thresholds and get a different answer than
// EvaluateCleanupBalk did. (gt-xhjb)
type CleanupBalkKind int

const (
	// BalkNone: no refusal — the deletion would proceed.
	BalkNone CleanupBalkKind = iota
	// BalkOrphanRatio: too large a share of the town's databases is flagged.
	BalkOrphanRatio
	// BalkTooManyOrphans: more orphans than DROP DATABASE can clear by SQL.
	BalkTooManyOrphans
)

// OrphanRatioBalks reports whether orphanCount out of totalDBs databases is too
// large a share to delete without an operator override.
//
// totalDBs <= 0 means the caller could not count the town, which is not the
// same as "the ratio is fine". It reports false here so that callers make that
// decision explicitly rather than inheriting a silent no-balk; both current
// callers refuse when the count is unknown.
func OrphanRatioBalks(orphanCount, totalDBs int) bool {
	if totalDBs <= 0 || orphanCount <= OrphanRatioBalkMinimum {
		return false
	}
	return float64(orphanCount)/float64(totalDBs) > OrphanRatioBalkFraction
}

// EvaluateCleanupBalk returns the first refusal a bulk deletion of orphanCount
// databases would hit, or BalkNone if it would proceed to delete.
//
// The order the two balks are evaluated in is part of the answer: when both
// trip, this reports the ratio, because that is the one an operator can clear.
//
// force clears the ratio balk only. The SQL-cleanup ceiling is about how long
// DROP takes, not about whether the operator trusts the detection, so no
// override clears it.
func EvaluateCleanupBalk(orphanCount, totalDBs int, force bool) CleanupBalkKind {
	if !force && OrphanRatioBalks(orphanCount, totalDBs) {
		return BalkOrphanRatio
	}
	if orphanCount > MaxSQLCleanup {
		return BalkTooManyOrphans
	}
	return BalkNone
}

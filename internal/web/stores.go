package web

// The dashboard reads beads from more than one store: the town root holds hq,
// and every registered rig holds its own. A panel that queries the town root
// alone is not showing a small number — it is showing the wrong number, because
// the rows it is missing were never in the store it asked.
//
// This file holds the one union query the panels share, so the rule that a
// source which errors gets NAMED rather than silently dropped is written once
// instead of eleven times. The pattern is lifted from FetchMergeQueue, which
// already unions rigs and already carries FailedRigs.

import (
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
)

// townStoreName labels rows and failures that came from the town root store.
// A rig may legitimately be named "town"; that collision is cosmetic, and
// storeSource.IsTown is the reliable discriminator.
const townStoreName = "town"

// rigsConfigStoreName is the failure label used when mayor/rigs.json itself
// cannot be read. Every rig store is missing in that case, so it is reported as
// a failed source rather than swallowed: the town rows still render, and the
// reason the rig rows are absent stays on screen.
const rigsConfigStoreName = "mayor/rigs.json"

// storeBudgetUnlimited disables the total row budget on a union query.
const storeBudgetUnlimited = 0

// storeStarved is the allowance for a store the budget can no longer pay for.
// It is distinct from storeBudgetUnlimited because zero already means "no
// limit": a starved store must be recorded as unread, not queried without a cap.
const storeStarved = -1

// storeSource is one beads store the dashboard can read.
type storeSource struct {
	// Name is the rig name, or townStoreName for the town root. Callers stamp
	// it onto rows so a panel can display where each row came from.
	Name string

	// Dir is the directory a query runs from; bd resolves .beads from here.
	Dir string

	// IsTown distinguishes the town root from a rig that happens to be named
	// townStoreName.
	IsTown bool
}

// StoreResult is the outcome of one union query across every store.
//
// Rows is a floor, not a total, whenever Partial reports true. A caller that
// ignores the failure fields renders "could not read" as "nothing there" —
// which is the exact confusion this type exists to prevent. Warning renders the
// caveat for display.
type StoreResult[T any] struct {
	// Rows is the union across every store that answered.
	Rows []T

	// FailedStores names stores whose query errored, BY NAME. Their rows are
	// missing from Rows entirely.
	FailedStores []string

	// TruncatedStores names stores that filled their whole row allowance and so
	// may have had more to give.
	TruncatedStores []string

	// UnreadStores names stores that were never queried because the row budget
	// was already spent when their turn came.
	UnreadStores []string

	// ReadStores names stores that answered, whether or not they had rows to
	// give. It is what separates "every store said there is nothing" from "no
	// store said anything": both leave Rows empty, and only the first is a
	// zero. Without it a panel can report an incomplete count but never report
	// no count at all.
	ReadStores []string
}

// Partial reports whether Rows is known to be incomplete. An empty Rows with
// Partial false means the stores really are empty; with Partial true it means
// the dashboard could not find out.
func (r StoreResult[T]) Partial() bool {
	return len(r.FailedStores) > 0 || len(r.TruncatedStores) > 0 || len(r.UnreadStores) > 0
}

// Unreadable reports that no store answered at all, so Rows is empty because
// nothing could be read rather than because there is nothing there.
//
// Partial and Unreadable are degrees of the same failure and a panel needs
// both: Partial means the count is a floor, Unreadable means there is no count.
// A panel that renders only the floor tells an operator "0 so far" on a page
// where every source is down.
//
// Rows short-circuits the answer so a result assembled by hand — a test mock,
// or any caller that fills Rows without naming the stores they came from —
// cannot be read as unreadable while it is visibly holding rows.
func (r StoreResult[T]) Unreadable() bool {
	if len(r.Rows) > 0 || len(r.ReadStores) > 0 {
		return false
	}
	return len(r.FailedStores) > 0 || len(r.UnreadStores) > 0
}

// merge folds a second union of the same row type into this one.
//
// A panel that asks each store more than one question — the Work panel lists
// open beads and hooked beads — runs one union per question and joins them
// here, so a store that failed either question is named once rather than twice
// or, worse, only in the half the caller happened to keep.
func (r StoreResult[T]) merge(other StoreResult[T]) StoreResult[T] {
	return StoreResult[T]{
		Rows:            append(r.Rows, other.Rows...),
		FailedStores:    appendUnique(r.FailedStores, other.FailedStores),
		TruncatedStores: appendUnique(r.TruncatedStores, other.TruncatedStores),
		UnreadStores:    appendUnique(r.UnreadStores, other.UnreadStores),
		ReadStores:      appendUnique(r.ReadStores, other.ReadStores),
	}
}

// mapStoreRows converts a union's rows, keeping the failure labels intact.
// convert returns false for a row to drop.
//
// Panels that discard rows after fetching must do it here rather than inside
// the fetch, because the resolver decides truncation from the number of rows a
// store returned. A store that fills its whole allowance with rows the panel
// then hides is still a truncated store, and dropping them before the resolver
// counts them is how that becomes invisible.
func mapStoreRows[A, B any](r StoreResult[A], convert func(A) (B, bool)) StoreResult[B] {
	rows := make([]B, 0, len(r.Rows))
	for _, row := range r.Rows {
		if converted, keep := convert(row); keep {
			rows = append(rows, converted)
		}
	}
	return StoreResult[B]{
		Rows:            rows,
		FailedStores:    r.FailedStores,
		TruncatedStores: r.TruncatedStores,
		UnreadStores:    r.UnreadStores,
		// Carried, not recomputed: a store whose every row the panel just
		// filtered out still answered, and forgetting that turns a store full
		// of beads the panel hides into a store that could not be read.
		ReadStores: r.ReadStores,
	}
}

// appendUnique appends the names not already present, preserving store order.
func appendUnique(dst, src []string) []string {
	for _, name := range src {
		if !slices.Contains(dst, name) {
			dst = append(dst, name)
		}
	}
	return dst
}

// Warning renders the incompleteness as a single display line, or "" when the
// union is complete.
func (r StoreResult[T]) Warning() string {
	var parts []string
	if len(r.FailedStores) > 0 {
		parts = append(parts, "unreadable: "+strings.Join(r.FailedStores, ", "))
	}
	if len(r.TruncatedStores) > 0 {
		parts = append(parts, "truncated: "+strings.Join(r.TruncatedStores, ", "))
	}
	if len(r.UnreadStores) > 0 {
		parts = append(parts, "not queried: "+strings.Join(r.UnreadStores, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "partial results (" + strings.Join(parts, "; ") + ")"
}

// UnavailableReason renders why the union has no answer at all, or "" when at
// least one store answered.
//
// It names the stores for the same reason Warning does: the operator's next
// move differs by which source is down, and "unavailable" alone sends them
// looking at the dashboard instead of at bd.
func (r StoreResult[T]) UnavailableReason() string {
	if !r.Unreadable() {
		return ""
	}
	var parts []string
	if len(r.FailedStores) > 0 {
		parts = append(parts, "unreadable: "+strings.Join(r.FailedStores, ", "))
	}
	if len(r.UnreadStores) > 0 {
		parts = append(parts, "not queried: "+strings.Join(r.UnreadStores, ", "))
	}
	return "no store could be read (" + strings.Join(parts, "; ") + ")"
}

// storeSources returns every store to union: the town root first, then each
// registered rig in sorted order so rows render in a stable order.
//
// The town root is returned even when the rigs config fails to load. Losing the
// rig list is a reason to name a failure, not a reason to render nothing.
func (f *LiveConvoyFetcher) storeSources() ([]storeSource, error) {
	sources := []storeSource{{Name: townStoreName, Dir: f.townRoot, IsTown: true}}

	rigsConfig, err := config.LoadRigsConfig(filepath.Join(f.townRoot, "mayor", "rigs.json"))
	if err != nil {
		return sources, fmt.Errorf("loading rigs config: %w", err)
	}

	rigNames := make([]string, 0, len(rigsConfig.Rigs))
	for rigName := range rigsConfig.Rigs {
		rigNames = append(rigNames, rigName)
	}
	sort.Strings(rigNames)

	for _, rigName := range rigNames {
		sources = append(sources, storeSource{
			Name: rigName,
			Dir:  filepath.Join(f.townRoot, rigName),
		})
	}

	return sources, nil
}

// storeLimiter decides the row allowance for the next store, given how many
// rows the union already holds. It returns storeBudgetUnlimited for "no limit"
// and storeStarved for "the budget cannot pay for this store at all".
type storeLimiter func(rowsSoFar int) int

// sharedBudget spends one total allowance across the stores in order. It is
// what stops four stores at --limit=50 from quietly returning 200 rows under a
// cap of 50.
func sharedBudget(budget int) storeLimiter {
	return func(rowsSoFar int) int {
		if budget <= storeBudgetUnlimited {
			return storeBudgetUnlimited
		}
		if remaining := budget - rowsSoFar; remaining > 0 {
			return remaining
		}
		return storeStarved
	}
}

// perStoreLimit gives every store the same allowance instead of making them
// compete for one.
//
// This is the right policy for a panel whose defect is blindness rather than
// size. The stores are wildly uneven — the town root holds 521 open beads to
// gastown's 65 — so under a shared budget the town root spends the whole
// allowance before any rig is read, and the rig rows land in UnreadStores
// instead of on screen: the panel stays exactly as blind as it was, now with a
// caption. A per-store limit keeps each store's contribution the size it was
// before the union and bounds the page at limit x stores.
func perStoreLimit(limit int) storeLimiter {
	return func(int) int {
		if limit <= storeBudgetUnlimited {
			return storeBudgetUnlimited
		}
		return limit
	}
}

// forEachStore runs fn against every store and unions the rows, spending one
// row budget across them. See forEachStorePerStore for the per-store policy.
func forEachStore[T any](f *LiveConvoyFetcher, budget int, fn func(src storeSource, limit int) ([]T, error)) StoreResult[T] {
	return forEachStoreLimited(f, sharedBudget(budget), fn)
}

// forEachStorePerStore runs fn against every store and unions the rows, giving
// each store the same row limit.
func forEachStorePerStore[T any](f *LiveConvoyFetcher, limit int, fn func(src storeSource, limit int) ([]T, error)) StoreResult[T] {
	return forEachStoreLimited(f, perStoreLimit(limit), fn)
}

// forEachStoreLimited is the shared union loop.
//
// fn receives the store — so rows can be stamped with where they came from —
// and the number of rows it is allowed to return, which the limiter decides.
//
// A store whose query errors is recorded in FailedStores and does not abort the
// others: partial results are acceptable, silent partial results are not.
//
// It is a free function rather than a method because Go methods cannot carry
// type parameters; the fetcher is therefore the first argument.
func forEachStoreLimited[T any](f *LiveConvoyFetcher, limiter storeLimiter, fn func(src storeSource, limit int) ([]T, error)) StoreResult[T] {
	var result StoreResult[T]

	sources, err := f.storeSources()
	if err != nil {
		// The town root is still readable. Name the config as the failed source
		// so the missing rig rows are attributable rather than invisible.
		log.Printf("dashboard: %v", err)
		result.FailedStores = append(result.FailedStores, rigsConfigStoreName)
	}

	for _, src := range sources {
		limit := limiter(len(result.Rows))
		if limit == storeStarved {
			result.UnreadStores = append(result.UnreadStores, src.Name)
			continue
		}

		rows, err := fn(src, limit)
		if err != nil {
			log.Printf("dashboard: store %s failed: %v", src.Name, err)
			result.FailedStores = append(result.FailedStores, src.Name)
			continue
		}

		// A store that filled its whole allowance may have had more. Calling
		// that complete is the silent truncation this guards against; the false
		// positive when it happened to hold exactly `limit` rows is the safe
		// direction to be wrong in.
		if limit > storeBudgetUnlimited && len(rows) >= limit {
			result.TruncatedStores = append(result.TruncatedStores, src.Name)
			rows = rows[:limit]
		}

		// Recorded even when rows is empty: an empty answer is still an answer,
		// and it is the only thing that makes a union's zero a real zero.
		result.ReadStores = append(result.ReadStores, src.Name)
		result.Rows = append(result.Rows, rows...)
	}

	return result
}

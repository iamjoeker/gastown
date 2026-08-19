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
}

// Partial reports whether Rows is known to be incomplete. An empty Rows with
// Partial false means the stores really are empty; with Partial true it means
// the dashboard could not find out.
func (r StoreResult[T]) Partial() bool {
	return len(r.FailedStores) > 0 || len(r.TruncatedStores) > 0 || len(r.UnreadStores) > 0
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

// forEachStore runs fn against every store and unions the rows.
//
// fn receives the store — so rows can be stamped with where they came from —
// and the number of rows it is still allowed to return. A budget of
// storeBudgetUnlimited passes 0 through, meaning "no limit"; otherwise the
// limit shrinks as earlier stores fill it, which is what stops four stores at
// --limit=50 from quietly returning 200 rows under a cap of 50.
//
// A store whose query errors is recorded in FailedStores and does not abort the
// others: partial results are acceptable, silent partial results are not.
//
// It is a free function rather than a method because Go methods cannot carry
// type parameters; the fetcher is therefore the first argument.
func forEachStore[T any](f *LiveConvoyFetcher, budget int, fn func(src storeSource, limit int) ([]T, error)) StoreResult[T] {
	var result StoreResult[T]

	sources, err := f.storeSources()
	if err != nil {
		// The town root is still readable. Name the config as the failed source
		// so the missing rig rows are attributable rather than invisible.
		log.Printf("dashboard: %v", err)
		result.FailedStores = append(result.FailedStores, rigsConfigStoreName)
	}

	for _, src := range sources {
		limit := storeBudgetUnlimited
		if budget > storeBudgetUnlimited {
			limit = budget - len(result.Rows)
			if limit <= 0 {
				result.UnreadStores = append(result.UnreadStores, src.Name)
				continue
			}
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

		result.Rows = append(result.Rows, rows...)
	}

	return result
}

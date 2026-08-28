// Package gcprotect keeps `bd purge`'s label guard in agreement with the town's
// own list of wisps that must never be deleted.
//
// gastown does not implement the purge it runs. `bd purge --force` decides for
// itself what it will spare, and the only thing it spares by label is whatever
// the target database's `gc.protected_labels` config key holds. gastown owns a
// separate list — reaper.ProtectedWispLabels — which its own deleters
// (`gt compact`, the reaper's SQL delete) consult. Nothing made the two agree,
// and they did not (gt-x6yk):
//
//	reaper.ProtectedWispLabels   gt:merge-request, gt:escalation
//	bd's default                 gt:merge-request, gt:message
//
// So `gt:escalation` was protected from every deleter gastown wrote and from
// none of the deletion gastown delegates. Measured on the deployed bd 1.2.2
// against an unpinned closed escalation wisp on hq, with a working control:
//
//	bd purge --dry-run --json --pattern hq-wisp-8w7xgv   -> purge_count 1
//	bd purge --dry-run --json --pattern hq-wisp-0bym5l   -> label_protected_skipped 1
//
// The second row carries gt:message, so the guard fires; the first is an
// escalation record and it does not. Escalation wisps are unversioned and
// dolt-ignored — no history to read AS OF, no backup, and `bd purge` does not
// archive — so that deletion is final by every means the town has.
//
// This package closes the gap from gastown's side, on the binary that is
// actually installed, by making the config key say what gastown requires before
// a destructive purge is allowed to run.
//
// Verified end-to-end against the deployed bd 1.2.2 in an isolated beads
// project, three closed ephemeral beads, one per label, EnsureForArgs driven
// with `gt done`'s real argv and starting from an unset key:
//
//	before Ensure   gt:escalation  purge_count 1   | gt:message  protected | (none) purge_count 1
//	after  Ensure   gt:escalation  protected       | gt:message  protected | (none) purge_count 1
//
// All three arms move the way they must: the escalation becomes protected, bd's
// own default survives the write, and the unlabelled bead is still purgeable —
// so the guard discriminates rather than switching purge off wholesale.
//
// None of this makes the missing kind guard in bd unnecessary. bd's own
// gcprotect.go states the principle this package cannot satisfy — "a guard for
// records with no undo must not have an off switch reachable by configuration"
// — and a config key is exactly such an off switch. Redeploying bd from a
// revision carrying that guard is still the right fix; this is what holds until
// then, and it stays correct afterwards.
package gcprotect

import (
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/reaper"
)

// ConfigKey is the bd config key holding the labels `bd purge` will not delete.
const ConfigKey = "gc.protected_labels"

// notSetMarker is what `bd config get` prints on stdout, at exit status 0, for a
// key that has no value: "gc.protected_labels (not set)". It is a sentence, not
// a value, and a parser that splits it on commas yields two bogus labels — one
// of which ("gc.protected_labels (not") would then be "present" forever and stop
// this package from ever writing anything.
const notSetMarker = "(not set)"

// bdDefaultProtectedLabels is what `bd purge` protects when ConfigKey is unset.
//
// It is restated here because WRITING the key REPLACES bd's default rather than
// extending it, and that is not documented anywhere bd prints. Measured on bd
// 1.2.2 in an isolated fixture, three closed ephemeral beads, one per label:
//
//	ConfigKey unset                gt:escalation  purge_count 1   <- the defect
//	                               gt:message     protected
//	                               (unlabelled)   purge_count 1
//	ConfigKey = "gt:escalation"    gt:escalation  protected
//	                               gt:message     purge_count 1   <- REGRESSION
//	                               (unlabelled)   purge_count 1
//
// So the obvious one-line remedy — set the key to the label that was missing —
// silently unprotects merge-request and message records, trading one class of
// unrecoverable deletion for two. Every write this package makes is therefore a
// union that carries these forward, and it never removes a label it finds.
var bdDefaultProtectedLabels = []string{"gt:merge-request", "gt:message"}

// Runner runs a bd command against the database the purge will run against and
// returns its stdout. It exists so the guard can be exercised without a bd
// process, and so both call sites can supply their own already-hardened way of
// invoking bd (a *beads.Beads handle, or an exec with a pinned env and dir).
//
// Routing matters and is the caller's job: the guard must configure the SAME
// database the purge will delete from, so a Runner that reaches a different one
// makes this package's confirmation a statement about the wrong store.
type Runner func(args ...string) ([]byte, error)

// Required returns every label a purge in this town must refuse to delete: the
// list gastown's own deleters consult, unioned with what bd already protects.
// Sorted so the value written is stable and diffs between runs mean something.
func Required() []string {
	return union(bdDefaultProtectedLabels, reaper.ProtectedWispLabels)
}

// EnsureForArgs makes ConfigKey cover Required() before a purge that can delete,
// and reports an error if it cannot confirm that it does.
//
// A purge argv WITHOUT --force is left completely alone — no read, no write.
// `bd purge` without --force does not delete; measured on bd 1.2.2, it exits
// non-zero with {"error":"would purge 1 bead(s)","hint":"Use --force ..."} and
// the bead is still there afterwards. So a preview cannot destroy anything, and
// a preview that mutates the target database's config is not a preview. This is
// why doltserver.PurgeClosedEphemerals — which passes no --force, and which
// gt maintain and gt dolt sync --gc call over every database including hq —
// makes no config writes: it never needed the protection in the first place.
//
// Deciding on the argv rather than on the caller's say-so is deliberate. The
// four call sites that reach purgeClosedEphemeralBeads all inherit the guard
// without repeating it, and a fifth added later inherits it too — the callee-not-
// call-site rule gt-6dp's post-mortem names as "the whole lesson".
func EnsureForArgs(run Runner, purgeArgs []string) error {
	if !hasForce(purgeArgs) {
		return nil
	}
	return Ensure(run)
}

// Ensure makes ConfigKey cover Required(), then RE-READS it and reports an error
// unless the re-read shows full coverage.
//
// The re-read is the point of the function. `bd config set` on this key exits 0
// and prints "Set gc.protected_labels = ..." on stdout while ALSO warning on
// stderr that it is "not a recognized config key" — bd's key registry does not
// know it even though `bd purge` reads it. A success line from a command that
// simultaneously calls the key unrecognized is not evidence the guard is in
// place, so the only thing trusted here is reading the value back.
func Ensure(run Runner) error {
	required := Required()

	have, err := read(run)
	if err != nil {
		return fmt.Errorf("reading %s: %w", ConfigKey, err)
	}
	if len(missing(have, required)) == 0 {
		return nil
	}

	// Union, never replace: another operator's additions are somebody's
	// deliberate protection, and dropping one here would delete their records.
	want := union(have, required)
	if _, err := run("config", "set", ConfigKey, strings.Join(want, ",")); err != nil {
		return fmt.Errorf("setting %s to %q: %w", ConfigKey, strings.Join(want, ","), err)
	}

	confirmed, err := read(run)
	if err != nil {
		return fmt.Errorf("re-reading %s after setting it: %w", ConfigKey, err)
	}
	if still := missing(confirmed, required); len(still) > 0 {
		return fmt.Errorf("%s does not protect %s after setting it (bd reports %q)",
			ConfigKey, strings.Join(still, ", "), strings.Join(confirmed, ","))
	}
	return nil
}

// read returns the labels ConfigKey currently holds, or nil if it holds nothing.
func read(run Runner) ([]string, error) {
	out, err := run("config", "get", ConfigKey)
	if err != nil {
		return nil, err
	}
	return parseValue(string(out)), nil
}

// parseValue turns `bd config get`'s stdout into a label list.
//
// An unset key is NOT an error and NOT an empty string: bd prints
// "gc.protected_labels (not set)" and exits 0. Treating that line as a value is
// the failure this function exists to prevent.
func parseValue(out string) []string {
	out = strings.TrimSpace(out)
	if out == "" || strings.Contains(out, notSetMarker) {
		return nil
	}
	var labels []string
	for _, field := range strings.Split(out, ",") {
		if field = strings.TrimSpace(field); field != "" {
			labels = append(labels, field)
		}
	}
	return labels
}

// hasForce reports whether a `bd purge` argv can actually delete. `bd purge`
// gates its deletion on DryRun: dryRun || !force, so --force is the whole
// difference between a preview and an unrecoverable write.
func hasForce(args []string) bool {
	for _, arg := range args {
		if arg == "--force" || arg == "-f" {
			return true
		}
	}
	return false
}

// missing returns the entries of want that have does not contain.
func missing(have, want []string) []string {
	present := make(map[string]bool, len(have))
	for _, h := range have {
		present[h] = true
	}
	var out []string
	for _, w := range want {
		if !present[w] {
			out = append(out, w)
		}
	}
	return out
}

// union returns the sorted set union of the given lists.
func union(lists ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, list := range lists {
		for _, item := range list {
			if !seen[item] {
				seen[item] = true
				out = append(out, item)
			}
		}
	}
	sort.Strings(out)
	return out
}

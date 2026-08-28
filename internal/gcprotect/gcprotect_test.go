package gcprotect

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/reaper"
)

// fakeBD is a stand-in for the bd CLI holding one config key. It answers
// `config get` and `config set` the way the deployed bd 1.2.2 does, including
// the two behaviours that make this package necessary: an unset key answers with
// a SENTENCE at exit 0, and a set REPLACES rather than extends.
type fakeBD struct {
	value    string // "" means unset
	set      bool
	calls    [][]string
	setCalls []string // the value passed to each `config set`
	getErr   error
	setErr   error
	// swallowSet makes `config set` report success and change nothing. This is
	// not hypothetical: bd exits 0 and prints "Set gc.protected_labels = ..." on
	// stdout while simultaneously warning on stderr that the key is "not a
	// recognized config key".
	swallowSet bool
}

func (f *fakeBD) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	switch {
	case len(args) >= 3 && args[0] == "config" && args[1] == "get":
		if f.getErr != nil {
			return nil, f.getErr
		}
		if !f.set {
			// Verbatim shape of the deployed binary: stdout, exit 0.
			return []byte(fmt.Sprintf("%s (not set)\n", args[2])), nil
		}
		return []byte(f.value + "\n"), nil
	case len(args) >= 4 && args[0] == "config" && args[1] == "set":
		f.setCalls = append(f.setCalls, args[3])
		if f.setErr != nil {
			return nil, f.setErr
		}
		if !f.swallowSet {
			f.value, f.set = args[3], true
		}
		return []byte("Set " + args[2] + " = " + args[3] + "\n"), nil
	}
	return nil, fmt.Errorf("fakeBD: unexpected argv %v", args)
}

func (f *fakeBD) configSets() int { return len(f.setCalls) }

// ---------------------------------------------------------------------------
// gt-x6yk: the one-label remedy that unprotects two other categories
// ---------------------------------------------------------------------------

// TestEnsureCarriesBDDefaultsForward is the regression guard for the trap that
// makes the obvious fix worse than the bug.
//
// `bd purge` protects by label, and only the labels in `gc.protected_labels`.
// The key was unset on hq, so the effective list was bd's default —
// gt:merge-request and gt:message — and gt:escalation was missing. The one-line
// remedy is to set the key to gt:escalation. Measured on bd 1.2.2 with three
// closed ephemeral beads, one per label:
//
//	key = "gt:escalation"    gt:escalation  protected
//	                         gt:message     purge_count 1   <- newly destroyable
//	                         (unlabelled)   purge_count 1
//
// Writing the key REPLACES bd's default; it does not extend it. So the naive fix
// trades one class of unrecoverable deletion for two, and every symptom anyone
// was watching for (escalations surviving) says it worked.
func TestEnsureCarriesBDDefaultsForward(t *testing.T) {
	f := &fakeBD{} // unset, exactly as hq was found

	if err := Ensure(f.run); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if f.configSets() != 1 {
		t.Fatalf("expected exactly one `config set`, got %d (%v)", f.configSets(), f.setCalls)
	}

	got := parseValue(f.value)
	for _, want := range bdDefaultProtectedLabels {
		if len(missing(got, []string{want})) > 0 {
			t.Errorf("wrote %q, which drops bd's default %q. Setting the key REPLACES "+
				"bd's default, so a value that omits one makes those records "+
				"destroyable — the fix would delete merge-request or message "+
				"records to save escalations.", f.value, want)
		}
	}
	for _, want := range reaper.ProtectedWispLabels {
		if len(missing(got, []string{want})) > 0 {
			t.Errorf("wrote %q, which omits reaper.ProtectedWispLabels entry %q — "+
				"the town's own deleters spare it and bd purge would not.", f.value, want)
		}
	}
}

// TestEnsureNeverDropsForeignLabels pins the union. Another operator's entry in
// this key is somebody's deliberate protection of records that have no backup;
// overwriting it would delete exactly what they were keeping.
func TestEnsureNeverDropsForeignLabels(t *testing.T) {
	f := &fakeBD{value: "gt:message,someones-own-label", set: true}

	if err := Ensure(f.run); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got := parseValue(f.value)
	if len(missing(got, []string{"someones-own-label"})) > 0 {
		t.Errorf("wrote %q, dropping the pre-existing label someones-own-label. "+
			"Ensure must union with what it finds, never replace it.", f.value)
	}
	if len(missing(got, Required())) > 0 {
		t.Errorf("wrote %q, which still does not cover %v", f.value, Required())
	}
}

// TestEnsureIsIdempotent: a key that already covers Required() must produce no
// write at all. Without this, every `gt done` rewrites config on the rig, and a
// no-op that writes is indistinguishable from one that changes something.
func TestEnsureIsIdempotent(t *testing.T) {
	f := &fakeBD{value: strings.Join(Required(), ","), set: true}

	if err := Ensure(f.run); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if f.configSets() != 0 {
		t.Errorf("already-covered key still triggered %d `config set` call(s): %v",
			f.configSets(), f.setCalls)
	}

	// Control: the fake DOES record sets, so the zero above is a real zero and
	// not a fake that cannot count.
	g := &fakeBD{}
	if err := Ensure(g.run); err != nil {
		t.Fatalf("control Ensure: %v", err)
	}
	if g.configSets() == 0 {
		t.Fatal("control failed: an unset key produced no `config set` either, so " +
			"the zero above proves nothing about idempotence")
	}
}

// ---------------------------------------------------------------------------
// The re-read is the whole guarantee
// ---------------------------------------------------------------------------

// TestEnsureRejectsASetThatChangedNothing is why Ensure reads the value back
// instead of trusting the write.
//
// `bd config set gc.protected_labels` exits 0 and prints a "Set ... = ..."
// success line on stdout while ALSO warning on stderr that the key is "not a
// recognized config key" — bd's key registry does not list it even though
// `bd purge` reads it. A success line from a command that calls the key
// unrecognized in the same breath is not evidence the guard is installed.
func TestEnsureRejectsASetThatChangedNothing(t *testing.T) {
	f := &fakeBD{swallowSet: true}

	err := Ensure(f.run)
	if err == nil {
		t.Fatal("Ensure returned nil after a `config set` that exited 0 and changed " +
			"nothing. The caller would go on to run `bd purge --force` believing " +
			"escalations were protected.")
	}
	for _, label := range reaper.ProtectedWispLabels {
		if strings.Contains(err.Error(), label) {
			return
		}
	}
	t.Errorf("error names no missing label, so the operator cannot tell what is "+
		"unprotected: %v", err)
}

// TestEnsurePropagatesReadAndWriteFailures: a guard that cannot reach bd must
// say so rather than returning nil. Returning nil here is the exit-0-on-
// impossible shape — the purge runs and nothing ever established the protection.
func TestEnsurePropagatesReadAndWriteFailures(t *testing.T) {
	boom := errors.New("dolt: connection refused")

	if err := Ensure((&fakeBD{getErr: boom}).run); err == nil {
		t.Error("a failed `config get` returned nil; the purge would proceed unguarded")
	}
	if err := Ensure((&fakeBD{setErr: boom}).run); err == nil {
		t.Error("a failed `config set` returned nil; the purge would proceed unguarded")
	}
}

// ---------------------------------------------------------------------------
// The "(not set)" sentence is not a value
// ---------------------------------------------------------------------------

// TestParseValueRejectsTheNotSetSentence. `bd config get` answers an unset key
// with "gc.protected_labels (not set)" on STDOUT at EXIT 0 — not an error, not
// an empty string. A parser that splits that on commas yields the single bogus
// label "gc.protected_labels (not set)", which covers nothing, and the coverage
// check would then be comparing against garbage rather than against nothing.
func TestParseValueRejectsTheNotSetSentence(t *testing.T) {
	if got := parseValue("gc.protected_labels (not set)\n"); got != nil {
		t.Errorf("parsed the unset sentence as labels %q", got)
	}

	// Control: a real value must still parse, otherwise the test above passes
	// for a parser that returns nil unconditionally.
	got := parseValue(" gt:merge-request, gt:message ,gt:escalation \n")
	want := []string{"gt:merge-request", "gt:message", "gt:escalation"}
	if len(got) != len(want) {
		t.Fatalf("control failed: parsed %q from a real value, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("control failed: element %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// A preview must stay a preview
// ---------------------------------------------------------------------------

// TestEnsureForArgsLeavesPreviewPurgesAlone.
//
// `bd purge` without --force does not delete. Measured on bd 1.2.2:
//
//	bd purge --json --pattern probe-wisp-8e7   (no --force, no --dry-run)
//	-> {"error":"would purge 1 bead(s)","hint":"Use --force to confirm..."}
//
// and the bead was still there afterwards. doltserver.PurgeClosedEphemerals
// passes no --force and runs over EVERY database including hq, so making it
// write config would mean `gt maintain` and `gt dolt sync --gc` mutating stores
// they only ever previewed.
func TestEnsureForArgsLeavesPreviewPurgesAlone(t *testing.T) {
	f := &fakeBD{}
	preview := []string{"purge", "--json", "--older-than", "168h", "--dry-run"}

	if err := EnsureForArgs(f.run, preview); err != nil {
		t.Fatalf("EnsureForArgs on a preview argv: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("preview argv %v triggered %d bd call(s) %v; a preview that writes "+
			"config is not a preview", preview, len(f.calls), f.calls)
	}

	// Control: the same fake, the same package, one flag different. If this does
	// not fire, the zero above says nothing about --force detection.
	g := &fakeBD{}
	destructive := []string{"purge", "--force", "--quiet", "--older-than", "168h"}
	if err := EnsureForArgs(g.run, destructive); err != nil {
		t.Fatalf("EnsureForArgs on a destructive argv: %v", err)
	}
	if g.configSets() == 0 {
		t.Errorf("argv %v contains --force and still installed no protection; the "+
			"guard is inert on the only path that deletes", destructive)
	}
}

// TestHasForceRecognizesTheShortFlag. `bd purge` accepts -f as well as --force,
// so a check that knows only the long form would wave through a real deleter.
func TestHasForceRecognizesTheShortFlag(t *testing.T) {
	if !hasForce([]string{"purge", "-f"}) {
		t.Error("-f not recognized as --force; a purge spelled with the short flag " +
			"deletes with no protection installed")
	}
	if hasForce([]string{"purge", "--json", "--older-than", "168h"}) {
		t.Error("control failed: an argv with no force flag was reported as destructive")
	}
}

// ---------------------------------------------------------------------------
// The list stays shared
// ---------------------------------------------------------------------------

// TestRequiredReadsTheSharedReaperList. reaper.ProtectedWispLabels is the one
// place the town says what must never be deleted; `gt compact` reads it and the
// reaper's SQL delete reads it. A private copy here would be a third list to
// keep in sync, and gt-6dp is the record of what happens when one deleter is
// protected and another is not.
func TestRequiredReadsTheSharedReaperList(t *testing.T) {
	const probe = "gt:test-protected"

	if len(missing(Required(), []string{probe})) == 0 {
		t.Fatalf("control failed: %s is already in Required(), so adding it below "+
			"cannot demonstrate anything", probe)
	}

	original := reaper.ProtectedWispLabels
	reaper.ProtectedWispLabels = append(append([]string{}, original...), probe)
	t.Cleanup(func() { reaper.ProtectedWispLabels = original })

	if len(missing(Required(), []string{probe})) > 0 {
		t.Errorf("after adding %s to reaper.ProtectedWispLabels, Required() = %v; "+
			"gcprotect is not reading the shared list, so a label added there "+
			"would be spared by gt compact and deleted by bd purge",
			probe, Required())
	}
}

package witness

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// fakeBeadStore is a bd stand-in that models the three things this cleanup path
// gets wrong when they are mocked away: children live in two tables, `bd close`
// refuses a step whose predecessor is still open, and a batch close exits 0 as
// long as ANY id settled.
type fakeBeadStore struct {
	// children maps a parent ID to its child IDs, in the order bd returns them.
	children map[string][]string
	status   map[string]string
	// ephemeral marks the IDs that live in the wisps table rather than issues.
	ephemeral map[string]bool
	// blockedBy maps a step to the step that must close before it can.
	blockedBy map[string]string
	// unclosable never closes, however many passes it is given.
	unclosable map[string]bool

	closeCalls int
}

func (f *fakeBeadStore) bd() *BdCli {
	return &BdCli{
		Exec: func(_ string, args ...string) (string, error) { return f.exec(args) },
		Run: func(_ string, args ...string) error {
			_, err := f.exec(args)
			return err
		},
	}
}

func (f *fakeBeadStore) exec(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "list":
		return f.listJSON(argValue(args, "--parent="), false), nil
	case "query":
		parent := ""
		for _, arg := range args {
			if strings.Contains(arg, "parent=") {
				parent = strings.Trim(arg[strings.Index(arg, "parent=")+len("parent="):], `"`)
			}
		}
		return f.listJSON(parent, true), nil
	case "close":
		return "", f.close(args[1:])
	}
	return "", nil
}

func (f *fakeBeadStore) listJSON(parent string, ephemeral bool) string {
	var rows []childBead
	for _, id := range f.children[parent] {
		if f.ephemeral[id] != ephemeral {
			continue
		}
		rows = append(rows, childBead{ID: id, Status: f.statusOf(id)})
	}
	if len(rows) == 0 {
		// bd prints plain text rather than an empty array when nothing matches.
		return "No issues found."
	}
	out, err := json.Marshal(rows)
	if err != nil {
		panic(err)
	}
	return string(out)
}

func (f *fakeBeadStore) statusOf(id string) string {
	if s, ok := f.status[id]; ok {
		return s
	}
	return "open"
}

// close mirrors bd's batch-close semantics: it walks the ids in order, refuses
// any whose blocker is still open, and exits non-zero only when NOTHING settled
// as closed.
func (f *fakeBeadStore) close(args []string) error {
	f.closeCalls++

	var ids []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			break
		}
		ids = append(ids, arg)
	}

	settled, blocked := 0, []string{}
	for _, id := range ids {
		if f.statusOf(id) == "closed" {
			settled++
			continue
		}
		if f.unclosable[id] {
			blocked = append(blocked, id)
			continue
		}
		if blocker, ok := f.blockedBy[id]; ok && f.statusOf(blocker) != "closed" {
			blocked = append(blocked, id)
			continue
		}
		f.status[id] = "closed"
		settled++
	}

	if settled == 0 {
		return &closeRefusedError{ids: blocked}
	}
	return nil
}

type closeRefusedError struct{ ids []string }

func (e *closeRefusedError) Error() string {
	return "cannot close blocked issue: " + strings.Join(e.ids, " ")
}

func argValue(args []string, prefix string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix)
		}
	}
	return ""
}

// chainedSteps builds a molecule whose steps are chained by blocks edges, listed
// in dependency order but closeable only head-first.
func chainedSteps(molecule string, n int, ephemeral bool) *fakeBeadStore {
	f := &fakeBeadStore{
		children:   map[string][]string{},
		status:     map[string]string{},
		ephemeral:  map[string]bool{},
		blockedBy:  map[string]string{},
		unclosable: map[string]bool{},
	}
	for i := 1; i <= n; i++ {
		id := molecule + "-step-" + strconv.Itoa(i)
		f.children[molecule] = append(f.children[molecule], id)
		f.status[id] = "open"
		f.ephemeral[id] = ephemeral
		if i > 1 {
			f.blockedBy[id] = molecule + "-step-" + strconv.Itoa(i-1)
		}
	}
	return f
}

func (f *fakeBeadStore) openIDs() []string {
	var open []string
	for id, status := range f.status {
		if status != "closed" {
			open = append(open, id)
		}
	}
	return open
}

// A single pass over a blocks chain closes only the steps that happen to follow
// their blocker in bd's ordering; the rest are stranded and the root close is
// then refused for having open children (gt-g1q1 / gt-3xmz).
func TestCloseDescendantsViaCLI_ClosesBlockedChainToFixedPoint(t *testing.T) {
	f := chainedSteps("gt-wisp-mol", 5, true)

	closed, err := closeDescendantsViaCLI(f.bd(), t.TempDir(), "gt-wisp-mol")
	if err != nil {
		t.Fatalf("closeDescendantsViaCLI: %v", err)
	}
	if closed != 5 {
		t.Errorf("closed = %d, want 5", closed)
	}
	if open := f.openIDs(); len(open) != 0 {
		t.Errorf("steps still open after close: %v", open)
	}
}

// Reversing bd's listing order exercises the other extreme: every close but the
// last is refused on the first pass, so only repeated passes settle the chain —
// this is the ordering the single-pass version stranded.
func TestCloseDescendantsViaCLI_ClosesChainListedBackwards(t *testing.T) {
	f := chainedSteps("gt-wisp-mol", 4, true)
	kids := f.children["gt-wisp-mol"]
	for i, j := 0, len(kids)-1; i < j; i, j = i+1, j-1 {
		kids[i], kids[j] = kids[j], kids[i]
	}

	closed, err := closeDescendantsViaCLI(f.bd(), t.TempDir(), "gt-wisp-mol")
	if err != nil {
		t.Fatalf("closeDescendantsViaCLI: %v", err)
	}
	if closed != 4 {
		t.Errorf("closed = %d, want 4", closed)
	}
	if open := f.openIDs(); len(open) != 0 {
		t.Errorf("steps still open after close: %v", open)
	}
	if f.closeCalls < 2 {
		t.Errorf("closeCalls = %d, want >1 — this chain cannot settle in one pass", f.closeCalls)
	}
}

// Molecule step children are wisps. A listing that only reads the issues table
// finds nothing, closes nothing, and reports no error (gt-u2u).
func TestCloseDescendantsViaCLI_ReachesWispChildren(t *testing.T) {
	f := chainedSteps("gt-wisp-mol", 3, true)

	// Control: the issues-table listing this path used to rely on is empty.
	if got := f.listJSON("gt-wisp-mol", false); !strings.Contains(got, "No issues found") {
		t.Fatalf("issues-table listing = %q, want empty — the fixture is not testing the wisp path", got)
	}

	closed, err := closeDescendantsViaCLI(f.bd(), t.TempDir(), "gt-wisp-mol")
	if err != nil {
		t.Fatalf("closeDescendantsViaCLI: %v", err)
	}
	if closed != 3 {
		t.Errorf("closed = %d, want 3 wisp steps", closed)
	}
}

// Durable children and wisp children under one parent both have to be collected.
func TestCloseDescendantsViaCLI_MergesBothTables(t *testing.T) {
	f := chainedSteps("gt-mol", 4, true)
	f.ephemeral["gt-mol-step-2"] = false
	f.ephemeral["gt-mol-step-4"] = false

	closed, err := closeDescendantsViaCLI(f.bd(), t.TempDir(), "gt-mol")
	if err != nil {
		t.Fatalf("closeDescendantsViaCLI: %v", err)
	}
	if closed != 4 {
		t.Errorf("closed = %d, want 4 (2 durable + 2 wisp)", closed)
	}
	if open := f.openIDs(); len(open) != 0 {
		t.Errorf("steps still open after close: %v", open)
	}
}

// `bd close a b c` exits 0 when ANY id closed, so the count has to come from
// re-reading the children rather than from len(ids) on a nil error (gt-3xmz).
func TestCloseDescendantsViaCLI_CountsOnlyConfirmedCloses(t *testing.T) {
	f := chainedSteps("gt-wisp-mol", 3, true)
	f.unclosable["gt-wisp-mol-step-2"] = true // blocks step 3 forever

	closed, err := closeDescendantsViaCLI(f.bd(), t.TempDir(), "gt-wisp-mol")
	if closed != 1 {
		t.Errorf("closed = %d, want 1 — only step 1 settled", closed)
	}
	if err == nil {
		t.Fatal("err = nil, want an error naming the steps that are still open")
	}
	for _, want := range []string{"gt-wisp-mol-step-2", "gt-wisp-mol-step-3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %s", err, want)
		}
	}
}

// A stalled set must not spin: once a pass closes nothing, another identical
// pass cannot do better.
func TestCloseChildrenToFixedPoint_StopsWhenAPassClosesNothing(t *testing.T) {
	f := chainedSteps("gt-wisp-mol", 3, true)
	f.unclosable["gt-wisp-mol-step-1"] = true

	closed, stillOpen, err := closeChildrenToFixedPoint(f.bd(), t.TempDir(), "gt-wisp-mol")
	if closed != 0 {
		t.Errorf("closed = %d, want 0", closed)
	}
	if len(stillOpen) != 3 {
		t.Errorf("stillOpen = %v, want all 3 steps", stillOpen)
	}
	if err == nil {
		t.Error("err = nil, want the refusal that stalled the pass")
	}
	if f.closeCalls != 1 {
		t.Errorf("closeCalls = %d, want 1 — a pass that closes nothing ends the loop", f.closeCalls)
	}
}

// Closing the root over surviving steps is what makes them unattributable
// orphans, so the root close is withheld until the steps are confirmed closed.
func TestCloseMoleculeWithDescendants_WithholdsRootWhenStepsSurvive(t *testing.T) {
	f := chainedSteps("gt-wisp-mol", 2, true)
	f.unclosable["gt-wisp-mol-step-1"] = true

	closed, err := closeMoleculeWithDescendants(f.bd(), t.TempDir(), "gt-wisp-mol")
	if err == nil {
		t.Fatal("err = nil, want a refusal to close the molecule over open steps")
	}
	if closed != 0 {
		t.Errorf("closed = %d, want 0", closed)
	}
	if f.statusOf("gt-wisp-mol") == "closed" {
		t.Error("molecule was closed over still-open steps")
	}
}

func TestCloseMoleculeWithDescendants_ClosesRootAfterSteps(t *testing.T) {
	f := chainedSteps("gt-wisp-mol", 3, true)

	closed, err := closeMoleculeWithDescendants(f.bd(), t.TempDir(), "gt-wisp-mol")
	if err != nil {
		t.Fatalf("closeMoleculeWithDescendants: %v", err)
	}
	if closed != 4 {
		t.Errorf("closed = %d, want 4 (3 steps + molecule)", closed)
	}
	if f.statusOf("gt-wisp-mol") != "closed" {
		t.Error("molecule was not closed after its steps settled")
	}
}

// Grandchildren still have to be reached — the recursion survives the rewrite.
func TestCloseDescendantsViaCLI_ClosesGrandchildren(t *testing.T) {
	f := chainedSteps("gt-wisp-mol", 2, true)
	f.children["gt-wisp-mol-step-1"] = []string{"gt-wisp-sub-1"}
	f.status["gt-wisp-sub-1"] = "open"
	f.ephemeral["gt-wisp-sub-1"] = true

	closed, err := closeDescendantsViaCLI(f.bd(), t.TempDir(), "gt-wisp-mol")
	if err != nil {
		t.Fatalf("closeDescendantsViaCLI: %v", err)
	}
	if closed != 3 {
		t.Errorf("closed = %d, want 3 (2 steps + 1 grandchild)", closed)
	}
	if f.statusOf("gt-wisp-sub-1") != "closed" {
		t.Error("grandchild was not closed")
	}
}

// Already-closed children are not counted as this run's work.
func TestCloseDescendantsViaCLI_SkipsAlreadyClosed(t *testing.T) {
	f := chainedSteps("gt-wisp-mol", 3, true)
	f.status["gt-wisp-mol-step-1"] = "closed"

	closed, err := closeDescendantsViaCLI(f.bd(), t.TempDir(), "gt-wisp-mol")
	if err != nil {
		t.Fatalf("closeDescendantsViaCLI: %v", err)
	}
	if closed != 2 {
		t.Errorf("closed = %d, want 2 (the third was already closed)", closed)
	}
}

// A listing failure must not read as "no children" — that is the path that ends
// with the root closed over steps nobody could see.
func TestCloseDescendantsViaCLI_ReportsListingFailure(t *testing.T) {
	bd := &BdCli{
		Exec: func(_ string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "query" {
				return "", errWispQuery
			}
			return "No issues found.", nil
		},
		Run: func(_ string, _ ...string) error { return nil },
	}

	if _, err := closeDescendantsViaCLI(bd, t.TempDir(), "gt-wisp-mol"); err == nil {
		t.Fatal("err = nil, want the wisp listing failure reported")
	}
}

var errWispQuery = &closeRefusedError{ids: []string{"dolt unavailable"}}

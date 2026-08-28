package git

import (
	"os/exec"
	"strings"
	"testing"
)

// supersededTestRepo gives a repository and the SHA of its one commit. It uses
// a real git rather than a fake, because every claim in this file is a claim
// about what git actually stores: a fake would prove the code agrees with
// itself. Two of these tests failed against a plausible-looking implementation
// for reasons no fake would have produced.
func supersededTestRepo(t *testing.T) (*Git, string) {
	t.Helper()
	dir := initTestRepo(t)
	g := NewGit(dir)
	head, err := g.Rev("HEAD")
	if err != nil {
		t.Fatalf("resolving HEAD: %v", err)
	}
	return g, head
}

func TestSupersededMarkRoundTrips(t *testing.T) {
	g, head := supersededTestRepo(t)

	want := SupersededMark{
		Branch:   "polecat/dust/gt-k3v+aaa",
		Commit:   head,
		Reason:   "substance landed as 7a108237 out of band; residual is 2 test files, both since deleted",
		MarkedBy: "gastown/witness",
		MarkedAt: "2026-08-26T06:00:00Z",
	}
	if err := g.MarkSuperseded(want); err != nil {
		t.Fatalf("MarkSuperseded: %v", err)
	}

	got, err := g.SupersededMarkFor(want.Branch)
	if err != nil {
		t.Fatalf("SupersededMarkFor: %v", err)
	}
	if got == nil {
		t.Fatal("marker written but read back as absent")
	}
	if *got != want {
		t.Errorf("marker round trip lost fields:\n got %+v\nwant %+v", *got, want)
	}
}

// The reason is the artifact. A marker that stores only the verdict recreates
// the defect one level up, so it is refused rather than written empty.
func TestMarkSupersededRefusesAMarkerWithNoDerivation(t *testing.T) {
	g, head := supersededTestRepo(t)

	err := g.MarkSuperseded(SupersededMark{Branch: "polecat/dust/gt-k3v+aaa", Commit: head, Reason: "  "})
	if err == nil {
		t.Fatal("a marker with no reason was accepted")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("error does not say what is missing: %v", err)
	}

	marks, listErr := g.SupersededMarks()
	if listErr != nil {
		t.Fatalf("SupersededMarks: %v", listErr)
	}
	if len(marks) != 0 {
		t.Errorf("a refused marker was still written: %+v", marks)
	}
}

// Without a commit the marker cannot be invalidated when the branch moves, so
// it would suppress live work forever. That is the one failure mode that would
// make this feature worse than the noise it removes.
func TestMarkSupersededRefusesAMarkerWithNoCommit(t *testing.T) {
	g, _ := supersededTestRepo(t)

	err := g.MarkSuperseded(SupersededMark{Branch: "polecat/dust/gt-k3v+aaa", Reason: "settled"})
	if err == nil {
		t.Fatal("a marker naming no commit was accepted")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Errorf("error does not say what is missing: %v", err)
	}
}

func TestSupersededMarkIsStaleWhenTheBranchMoved(t *testing.T) {
	mark := SupersededMark{Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}

	if mark.StaleFor("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Error("a marker naming the current tip reported itself stale")
	}
	if mark.StaleFor("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") {
		t.Error("SHA comparison is case-sensitive; the same commit read as a different one")
	}
	if !mark.StaleFor("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Error("a marker naming a different commit did not report itself stale")
	}
	// "Settled at some unknown state" cannot be checked against anything, so
	// the safe reading is stale — never suppress on an unverifiable marker.
	if !(SupersededMark{}).StaleFor("bbbbbbbb") {
		t.Error("a marker with no commit was treated as applying")
	}
	if !mark.StaleFor("") {
		t.Error("an unknown tip was treated as matching")
	}
}

func TestUnmarkSupersededRemovesTheMarkerAndSaysWhetherThereWasOne(t *testing.T) {
	g, head := supersededTestRepo(t)
	branch := "polecat/dust/gt-k3v+aaa"

	// The distinction the return value exists for: a caller that prints
	// "unmarked" for a branch that never had a marker is answering a question
	// nobody asked, and the next reader believes a marker was cleared.
	removed, err := g.UnmarkSuperseded(branch)
	if err != nil {
		t.Fatalf("UnmarkSuperseded on an unmarked branch: %v", err)
	}
	if removed {
		t.Error("unmark reported removing a marker that never existed")
	}

	if err := g.MarkSuperseded(SupersededMark{Branch: branch, Commit: head, Reason: "settled"}); err != nil {
		t.Fatalf("MarkSuperseded: %v", err)
	}
	removed, err = g.UnmarkSuperseded(branch)
	if err != nil {
		t.Fatalf("UnmarkSuperseded: %v", err)
	}
	if !removed {
		t.Error("unmark reported no marker for a branch that had one")
	}

	got, err := g.SupersededMarkFor(branch)
	if err != nil {
		t.Fatalf("SupersededMarkFor after unmark: %v", err)
	}
	if got != nil {
		t.Errorf("marker survived the unmark: %+v", got)
	}
}

func TestSupersededMarksKeysEveryMarkerByBranch(t *testing.T) {
	g, head := supersededTestRepo(t)
	branches := []string{
		"polecat/dust/gt-k3v+aaa",
		"polecat/refinery/gt-aqk+ddd",
		"polecat/deathclaw/gt-8xcg+mt9plri1",
	}
	for _, branch := range branches {
		if err := g.MarkSuperseded(SupersededMark{Branch: branch, Commit: head, Reason: "settled " + branch}); err != nil {
			t.Fatalf("MarkSuperseded(%s): %v", branch, err)
		}
	}

	marks, err := g.SupersededMarks()
	if err != nil {
		t.Fatalf("SupersededMarks: %v", err)
	}
	if len(marks) != len(branches) {
		t.Fatalf("got %d markers, want %d: %+v", len(marks), len(branches), marks)
	}
	for _, branch := range branches {
		mark, ok := marks[branch]
		if !ok {
			t.Errorf("no marker keyed under %q; keys are %v", branch, markKeys(marks))
			continue
		}
		if mark.Reason != "settled "+branch {
			t.Errorf("%s carries the wrong reason: %q", branch, mark.Reason)
		}
	}
}

// A marker whose blob will not parse must degrade into a VISIBLE marker, never
// into an absence. "Never marked" and "marked, unreadable" differ by whether
// the reader knows a verdict was reached.
func TestSupersededMarksKeepsAMarkerWhoseBlobIsUnreadable(t *testing.T) {
	g, _ := supersededTestRepo(t)
	branch := "polecat/dust/gt-k3v+aaa"

	blob := writeBlob(t, g.WorkDir(), "this is not json")
	if _, err := g.run("update-ref", SupersededRef(branch), blob); err != nil {
		t.Fatalf("planting a corrupt marker: %v", err)
	}

	marks, err := g.SupersededMarks()
	if err != nil {
		t.Fatalf("SupersededMarks: %v", err)
	}
	mark, ok := marks[branch]
	if !ok {
		t.Fatalf("a corrupt marker vanished from the listing; keys are %v", markKeys(marks))
	}
	if mark.Branch != branch {
		t.Errorf("branch not recovered from the ref name: %q", mark.Branch)
	}
	// It records no commit, so it is stale against every tip and suppresses
	// nothing — visible, and harmless.
	if !mark.StaleFor("anything") {
		t.Error("an unreadable marker was treated as applying to the current tip")
	}
}

// The ref name is authoritative. A blob claiming a different branch from the
// one it is stored under would silently settle a branch nobody marked.
func TestSupersededMarksTrustTheRefNameOverTheBlob(t *testing.T) {
	g, head := supersededTestRepo(t)
	stored := "polecat/dust/gt-k3v+aaa"

	blob := writeBlob(t, g.WorkDir(),
		`{"branch":"polecat/other/gt-zzz+eee","commit":"`+head+`","reason":"settled"}`)
	if _, err := g.run("update-ref", SupersededRef(stored), blob); err != nil {
		t.Fatalf("planting the marker: %v", err)
	}

	marks, err := g.SupersededMarks()
	if err != nil {
		t.Fatalf("SupersededMarks: %v", err)
	}
	if _, ok := marks["polecat/other/gt-zzz+eee"]; ok {
		t.Error("a blob settled a branch it was not stored under")
	}
	mark, ok := marks[stored]
	if !ok {
		t.Fatalf("the marker is not keyed by the ref it lives at; keys are %v", markKeys(marks))
	}
	if mark.Branch != stored {
		t.Errorf("Branch is %q, want the ref name %q", mark.Branch, stored)
	}
}

// The markers must work in a BARE repository, because that is where the sweep
// reads them: gt resolves a rig to <rig>/.repo.git before anything else. A
// marker store that only worked in a worktree would be one that never ran in
// production.
func TestSupersededMarkersWorkInABareRepo(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	g := NewGitWithDir(dir, "")

	blob := writeBlobBare(t, dir, "seed")
	if err := g.MarkSuperseded(SupersededMark{
		Branch: "polecat/dust/gt-k3v+aaa",
		Commit: blob,
		Reason: "settled in a bare mirror",
	}); err != nil {
		t.Fatalf("MarkSuperseded in a bare repo: %v", err)
	}

	got, err := g.SupersededMarkFor("polecat/dust/gt-k3v+aaa")
	if err != nil {
		t.Fatalf("SupersededMarkFor: %v", err)
	}
	if got == nil || got.Reason != "settled in a bare mirror" {
		t.Fatalf("marker did not survive a bare-repo round trip: %+v", got)
	}
}

// refs/heads/ on the way in is a normal thing for a caller to have, and it must
// not produce a second, differently-named marker for the same branch.
func TestSupersededRefStripsRefsHeads(t *testing.T) {
	if got, want := SupersededRef("refs/heads/polecat/dust/gt-k3v+aaa"), SupersededRefPrefix+"polecat/dust/gt-k3v+aaa"; got != want {
		t.Errorf("SupersededRef = %q, want %q", got, want)
	}
}

func TestMarkSupersededRejectsABranchThatWillNotMakeARef(t *testing.T) {
	g, head := supersededTestRepo(t)

	if err := g.MarkSuperseded(SupersededMark{Branch: "polecat/../etc", Commit: head, Reason: "settled"}); err == nil {
		t.Error("a branch name that cannot form a ref was accepted")
	}
}

func markKeys(marks map[string]SupersededMark) []string {
	keys := make([]string, 0, len(marks))
	for k := range marks {
		keys = append(keys, k)
	}
	return keys
}

func writeBlob(t *testing.T, dir, content string) string {
	t.Helper()
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func writeBlobBare(t *testing.T, gitDir, content string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir="+gitDir, "hash-object", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	return strings.TrimSpace(string(out))
}

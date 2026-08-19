package tmpgc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mkWorkDir creates a directory under root holding one file of the given size,
// then back-dates the whole tree so that the scan sees it as untouched for age.
func mkWorkDir(t *testing.T, root, name string, size int, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "b001"), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	file := filepath.Join(dir, "b001", "_pkg_.a")
	if err := os.WriteFile(file, make([]byte, size), 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	stamp := time.Now().Add(-age)
	for _, p := range []string{file, filepath.Join(dir, "b001"), dir} {
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}
	return dir
}

// noRefs is the "nothing is running" answer from the process table.
func noRefs([]string) (Evidence, error) { return Evidence{Refs: map[string]bool{}}, nil }

// refsTo is the "exactly these paths are in use" answer.
func refsTo(paths ...string) func([]string) (Evidence, error) {
	return func([]string) (Evidence, error) {
		refs := make(map[string]bool, len(paths))
		for _, p := range paths {
			refs[p] = true
		}
		return Evidence{Refs: refs}, nil
	}
}

func byPath(t *testing.T, res *Result, path string) Candidate {
	t.Helper()
	for _, c := range res.Candidates {
		if c.Path == path {
			return c
		}
	}
	t.Fatalf("no candidate for %s (got %d candidates)", path, len(res.Candidates))
	return Candidate{}
}

func TestScanClassifiesOrphansYoungAndLive(t *testing.T) {
	root := t.TempDir()
	orphan := mkWorkDir(t, root, "go-build2674671939", 4096, 3*time.Hour)
	young := mkWorkDir(t, root, "go-build1056130938", 2048, 5*time.Minute)
	live := mkWorkDir(t, root, "go-link-1315979694", 1024, 6*time.Hour)

	res, err := Scan(Options{
		Dir:      root,
		MinAge:   time.Hour,
		liveRefs: refsTo(live),
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if got := byPath(t, res, orphan).Status; got != StatusReclaimable {
		t.Errorf("orphan status = %q, want %q", got, StatusReclaimable)
	}
	if got := byPath(t, res, young).Status; got != StatusYoung {
		t.Errorf("young status = %q, want %q", got, StatusYoung)
	}
	if got := byPath(t, res, live).Status; got != StatusLive {
		t.Errorf("live status = %q, want %q", got, StatusLive)
	}
	if res.ReclaimableBytes != 4096 {
		t.Errorf("ReclaimableBytes = %d, want 4096", res.ReclaimableBytes)
	}
	if res.Removed != 0 {
		t.Errorf("Scan removed %d directories, want 0", res.Removed)
	}
	for _, dir := range []string{orphan, young, live} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("Scan should not remove %s: %v", dir, err)
		}
	}
}

func TestScanIgnoresNonWorkDirectories(t *testing.T) {
	root := t.TempDir()
	// Everything here is old enough to be swept if the name test were loose.
	for _, name := range []string{
		"claude-1000",          // live agent scratchpads
		"go-buildcache",        // no digits: not a work directory
		"go-build",             // bare prefix
		"my-go-build123",       // work-directory name embedded in another
		"go-build123.bak",      // trailing junk
		"dolt-test-server-1.d", // unrelated
		"beads-test-dolt",      // bare prefix, no run suffix
		"beads-bd-tests",       // bare prefix, no run suffix
		"my-beads-bd-tests-1",  // family name embedded in another
	} {
		mkWorkDir(t, root, name, 512, 9*time.Hour)
	}

	res, err := Scan(Options{Dir: root, MinAge: time.Hour, liveRefs: noRefs})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("Scan considered %d non-work directories: %+v", len(res.Candidates), res.Candidates)
	}
}

func TestScanUsesNewestMtimeInTree(t *testing.T) {
	root := t.TempDir()
	// The root is old, but a build is still writing deep inside it. Judging by
	// the root's own mtime alone would call this an orphan and delete a live
	// build's scratch space.
	dir := mkWorkDir(t, root, "go-build777", 128, 8*time.Hour)
	fresh := filepath.Join(dir, "b001", "importcfg")
	if err := os.WriteFile(fresh, []byte("packagefile fmt=..."), 0o600); err != nil {
		t.Fatalf("write %s: %v", fresh, err)
	}

	res, err := Scan(Options{Dir: root, MinAge: time.Hour, liveRefs: noRefs})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := byPath(t, res, dir).Status; got != StatusYoung {
		t.Errorf("status = %q, want %q (a nested write must keep the tree young)", got, StatusYoung)
	}
}

func TestScanRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "go-build42")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	res, err := Scan(Options{Dir: root, MinAge: time.Hour, liveRefs: noRefs})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	c := byPath(t, res, link)
	if c.Status != StatusRefused {
		t.Errorf("status = %q, want %q", c.Status, StatusRefused)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("symlink was removed: %v", err)
	}
}

func TestSweepRemovesOnlyReclaimable(t *testing.T) {
	root := t.TempDir()
	orphan := mkWorkDir(t, root, "go-build2674671939", 4096, 3*time.Hour)
	young := mkWorkDir(t, root, "go-build1056130938", 2048, time.Minute)
	live := mkWorkDir(t, root, "go-build1400416626", 1024, 6*time.Hour)
	bystander := mkWorkDir(t, root, "claude-1000", 8192, 9*time.Hour)

	res, err := Sweep(Options{
		Dir:      root,
		MinAge:   time.Hour,
		liveRefs: refsTo(live),
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if res.Removed != 1 || res.RemovedBytes != 4096 {
		t.Errorf("removed %d dirs / %d bytes, want 1 / 4096", res.Removed, res.RemovedBytes)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan %s survived the sweep (err=%v)", orphan, err)
	}
	for _, dir := range []string{young, live, bystander} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s must survive the sweep: %v", dir, err)
		}
	}
	if res.ReclaimableBytes != 0 {
		t.Errorf("ReclaimableBytes = %d after removing everything eligible, want 0", res.ReclaimableBytes)
	}
}

func TestSweepDryRunRemovesNothing(t *testing.T) {
	root := t.TempDir()
	orphan := mkWorkDir(t, root, "go-build2674671939", 4096, 3*time.Hour)

	res, err := Sweep(Options{Dir: root, MinAge: time.Hour, DryRun: true, liveRefs: noRefs})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Removed != 0 {
		t.Errorf("dry run removed %d directories", res.Removed)
	}
	if res.ReclaimableBytes != 4096 {
		t.Errorf("ReclaimableBytes = %d, want 4096", res.ReclaimableBytes)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("dry run deleted %s: %v", orphan, err)
	}
}

// TestSweepFailsClosedWithoutProcessEvidence is the load-bearing case: an
// unreadable process table means we cannot tell a stranded work directory from
// a live build's, and not being able to look is never permission to delete.
func TestSweepFailsClosedWithoutProcessEvidence(t *testing.T) {
	root := t.TempDir()
	ancient := mkWorkDir(t, root, "go-build2674671939", 4096, 300*time.Hour)

	res, err := Sweep(Options{
		Dir:      root,
		MinAge:   time.Hour,
		liveRefs: func([]string) (Evidence, error) { return Evidence{}, errors.New("permission denied") },
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if !res.Inconclusive {
		t.Error("Inconclusive = false, want true when the process table cannot be read")
	}
	if res.Removed != 0 {
		t.Fatalf("removed %d directories without liveness evidence", res.Removed)
	}
	if _, err := os.Stat(ancient); err != nil {
		t.Errorf("a 300h-old directory was deleted without evidence: %v", err)
	}
	c := byPath(t, res, ancient)
	if c.Status != StatusRefused {
		t.Errorf("status = %q, want %q", c.Status, StatusRefused)
	}
	if len(res.Errors) == 0 {
		t.Error("a refusal must say why")
	}
}

// TestSweepControlRemovesWhenEvidenceIsAvailable is the control for the test
// above: the same directory, the same age, with the evidence gathered — if
// this did not remove it, the fail-closed result would prove nothing.
func TestSweepControlRemovesWhenEvidenceIsAvailable(t *testing.T) {
	root := t.TempDir()
	ancient := mkWorkDir(t, root, "go-build2674671939", 4096, 300*time.Hour)

	res, err := Sweep(Options{Dir: root, MinAge: time.Hour, liveRefs: noRefs})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Inconclusive {
		t.Error("Inconclusive = true with evidence available")
	}
	if res.Removed != 1 {
		t.Fatalf("removed %d directories, want 1", res.Removed)
	}
	if _, err := os.Stat(ancient); !os.IsNotExist(err) {
		t.Errorf("directory survived a conclusive sweep (err=%v)", err)
	}
}

func TestSweepRejectsDangerousRoots(t *testing.T) {
	// Never pass an empty Dir here: empty means os.TempDir(), and a test that
	// sweeps the shared host TMPDIR deletes other agents' directories.
	for _, dir := range []string{"/", "//", string(filepath.Separator)} {
		if _, err := Sweep(Options{Dir: dir, MinAge: time.Hour, liveRefs: noRefs}); err == nil {
			t.Errorf("Sweep(%q) succeeded; want refusal", dir)
		}
	}
	if err := validateSweepDir(""); err == nil {
		t.Error("validateSweepDir(\"\") succeeded; want refusal")
	}
	if _, err := Sweep(Options{Dir: filepath.Join(t.TempDir(), "absent"), liveRefs: noRefs}); err == nil {
		t.Error("Sweep of a missing directory succeeded; want error")
	}
}

func TestEmptyDirMeansTempDir(t *testing.T) {
	if got := (&Options{}).dir(); got != os.TempDir() {
		t.Errorf("default dir = %q, want %q", got, os.TempDir())
	}
}

func TestSweepRejectsNegativeMinAge(t *testing.T) {
	if _, err := Sweep(Options{Dir: t.TempDir(), MinAge: -time.Hour, liveRefs: noRefs}); err == nil {
		t.Error("negative min age accepted; want refusal")
	}
}

// TestSweepRejectsTinyMinAge covers the startup window: for the first seconds
// of a build, $WORK exists and no process names it, so argv evidence alone
// would call a running build's scratch space an orphan.
func TestSweepRejectsTinyMinAge(t *testing.T) {
	root := t.TempDir()
	dir := mkWorkDir(t, root, "go-build2674671939", 4096, time.Minute)

	if _, err := Sweep(Options{Dir: root, MinAge: time.Nanosecond, liveRefs: noRefs}); err == nil {
		t.Fatal("Sweep accepted a 1ns quiet period; want refusal")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("a refused sweep removed %s: %v", dir, err)
	}

	// A dry run classifies at any quiet period: it deletes nothing, and the
	// operator needs to be able to see what a shorter period would catch.
	res, err := Sweep(Options{Dir: root, MinAge: time.Nanosecond, DryRun: true, liveRefs: noRefs})
	if err != nil {
		t.Fatalf("dry run refused a tiny min age: %v", err)
	}
	if got := byPath(t, res, dir).Status; got != StatusReclaimable {
		t.Errorf("dry run status = %q, want %q", got, StatusReclaimable)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dry run removed %s: %v", dir, err)
	}
}

func TestDefaultMinAgeApplies(t *testing.T) {
	root := t.TempDir()
	dir := mkWorkDir(t, root, "go-build999", 64, 30*time.Minute)

	res, err := Scan(Options{Dir: root, liveRefs: noRefs})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.MinAge != DefaultMinAge {
		t.Errorf("MinAge = %s, want %s", res.MinAge, DefaultMinAge)
	}
	if got := byPath(t, res, dir).Status; got != StatusYoung {
		t.Errorf("status = %q, want %q under the default min age", got, StatusYoung)
	}
}

func TestWorkDirPatternMatchesRealNames(t *testing.T) {
	// Names measured on the host that produced gt-yb33.
	for _, name := range []string{
		"go-build1056130938",
		"go-build2674671939",
		"go-link-1315979694",
		"go-link1315979694",
	} {
		if !workDirPattern.MatchString(name) {
			t.Errorf("workDirPattern does not match real name %q", name)
		}
	}
	for _, name := range []string{
		"go-build", "go-buildcache", "go-build123x", "xgo-build123",
		"go-vet123", "claude-1000", "beads-test-dolt-1",
	} {
		if workDirPattern.MatchString(name) {
			t.Errorf("workDirPattern matches %q, which is not a Go work directory", name)
		}
	}
}

// TestLiveReferencesSeesThisProcess exercises the real procfs scanner against
// the one process it is guaranteed to find: this test binary. Its own path is
// in its own argv, so a scanner that reports nothing here is broken — and a
// broken scanner that reports nothing is the shape that would delete a live
// build's work directory.
func TestLiveReferencesSeesThisProcess(t *testing.T) {
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("abs %s: %v", os.Args[0], err)
	}
	absent := filepath.Join(t.TempDir(), "go-build0000000001")

	ev, err := liveReferences([]string{self, absent})
	if errors.Is(err, ErrNoProcEvidence) {
		t.Skipf("no process table on this platform: %v", err)
	}
	if err != nil {
		t.Fatalf("liveReferences: %v", err)
	}
	if !ev.Refs[self] {
		t.Errorf("liveReferences did not find this test binary (%s) in the process table", self)
	}
	if ev.Refs[absent] {
		t.Errorf("liveReferences reported a never-created path (%s) as live", absent)
	}
}

// TestSweepSkipsWorkDirOfARunningBuild joins the two halves: the real procfs
// scanner protecting a real directory whose path a live process names. This
// test binary runs from inside its own go-build work directory, so that
// directory is the fixture.
func TestSweepSkipsWorkDirOfARunningBuild(t *testing.T) {
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("abs %s: %v", os.Args[0], err)
	}
	// os.Args[0] is <tmp>/go-buildNNN/bNNN/pkg.test when `go test` compiled to
	// a temporary work directory. When the binary was installed elsewhere
	// (`go test -c`, a cached rerun), there is no fixture to build from.
	parts := strings.Split(filepath.ToSlash(self), "/")
	idx := -1
	for i, p := range parts {
		if workDirPattern.MatchString(p) {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Skipf("test binary %s does not live in a Go work directory", self)
	}
	workDir := filepath.FromSlash(strings.Join(parts[:idx+1], "/"))
	parent := filepath.Dir(workDir)

	res, err := Sweep(Options{
		Dir: parent,
		// A near-zero quiet period means age offers no protection here, so the
		// only thing standing between a live build and rm -rf is the process
		// evidence. DryRun because this is the shared host TMPDIR: the test
		// asserts the classification that decides removal, and a test has no
		// business deleting other agents' directories as a side effect.
		MinAge: time.Nanosecond,
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	c := byPath(t, res, workDir)
	if c.Removed {
		t.Fatalf("swept the work directory of the running test binary: %s", workDir)
	}
	if c.Status != StatusLive {
		t.Errorf("status = %q (%s), want %q", c.Status, c.Reason, StatusLive)
	}
	if _, err := os.Stat(self); err != nil {
		t.Errorf("running test binary was deleted: %v", err)
	}
}

// mkBeadsFixture builds a beads-style Dolt fixture directory: a LOCK file
// inside a .dolt subdirectory, the shape a running sql-server holds open. The
// whole tree is back-dated so that age alone would make it reclaimable.
func mkBeadsFixture(t *testing.T, root, name string, age time.Duration) (dir, lock string) {
	t.Helper()
	dir = filepath.Join(root, name)
	nomsDir := filepath.Join(dir, ".dolt", "noms")
	if err := os.MkdirAll(nomsDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", nomsDir, err)
	}
	lock = filepath.Join(nomsDir, "LOCK")
	if err := os.WriteFile(lock, []byte("lock"), 0o600); err != nil {
		t.Fatalf("write %s: %v", lock, err)
	}
	stamp := time.Now().Add(-age)
	for _, p := range []string{lock, nomsDir, filepath.Join(dir, ".dolt"), dir} {
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}
	return dir, lock
}

func TestBeadsTestDirPatternMatchesRealNames(t *testing.T) {
	// Names measured under /tmp on the host that produced gt-1gdh, plus the
	// two globs the deacon patrol's shell block used before this package took
	// the job over.
	for _, name := range []string{
		"beads-bd-tests-159428258",
		"beads-bd-tests-4288621347",
		"beads-test-dolt-1",
		"beads-test-dolt-abc123",
	} {
		if !beadsTestDirPattern.MatchString(name) {
			t.Errorf("beadsTestDirPattern does not match real name %q", name)
		}
	}
	// The control. These share a prefix with the family and must not be swept:
	// beads-storage-dolt-tests and beads-test-env are separate families the
	// shell block never covered, and widening silently is how an rm -rf path
	// grows a blast radius nobody reviewed.
	for _, name := range []string{
		"beads-test-dolt", "beads-bd-tests", "beads-bd-test-1",
		"my-beads-bd-tests-1", "beads-storage-dolt-tests-1063402",
		"beads-test-env-0mgESr", "beads-circuit",
		"go-build123", "claude-1000",
	} {
		if beadsTestDirPattern.MatchString(name) {
			t.Errorf("beadsTestDirPattern matches %q, which is not a beads test fixture directory", name)
		}
	}
}

// TestScanClassifiesBeadsTestDirs is the swap this package took over from the
// deacon patrol's inline shell block (gt-1gdh): the beads fixture family must
// be classified by the same rules as the Go family, in the same run.
func TestScanClassifiesBeadsTestDirs(t *testing.T) {
	root := t.TempDir()
	orphan, _ := mkBeadsFixture(t, root, "beads-bd-tests-159428258", 3*time.Hour)
	young, _ := mkBeadsFixture(t, root, "beads-test-dolt-fresh", time.Minute)
	goDir := mkWorkDir(t, root, "go-build2674671939", 4096, 3*time.Hour)

	res, err := Scan(Options{Dir: root, MinAge: time.Hour, liveRefs: noRefs})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := byPath(t, res, orphan).Status; got != StatusReclaimable {
		t.Errorf("orphan beads fixture status = %q, want %q", got, StatusReclaimable)
	}
	if got := byPath(t, res, orphan).Family; got != FamilyBeadsTest.Name {
		t.Errorf("family = %q, want %q", got, FamilyBeadsTest.Name)
	}
	if got := byPath(t, res, young).Status; got != StatusYoung {
		t.Errorf("young beads fixture status = %q, want %q", got, StatusYoung)
	}
	if got := byPath(t, res, goDir).Family; got != FamilyGoWork.Name {
		t.Errorf("family = %q, want %q", got, FamilyGoWork.Name)
	}
	if len(res.Families) != 2 {
		t.Errorf("Families = %v, want both default families named", res.Families)
	}
}

// TestFamiliesRestrictWhatIsConsidered is the control for the test above: with
// only one family selected, the other's directories must not appear at all. A
// scan that returned everything regardless would pass the test above
// vacuously.
func TestFamiliesRestrictWhatIsConsidered(t *testing.T) {
	root := t.TempDir()
	beads, _ := mkBeadsFixture(t, root, "beads-bd-tests-159428258", 3*time.Hour)
	goDir := mkWorkDir(t, root, "go-build2674671939", 4096, 3*time.Hour)

	res, err := Scan(Options{
		Dir: root, MinAge: time.Hour, liveRefs: noRefs,
		Families: []Family{FamilyGoWork},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Path != goDir {
		t.Fatalf("go-work-only scan returned %+v, want only %s", res.Candidates, goDir)
	}
	if _, err := os.Stat(beads); err != nil {
		t.Errorf("a family that was not selected was touched: %v", err)
	}
}

// TestScanIgnoresPIDFilesSharingTheFamilyPrefix guards the seam between this
// sweep and the deacon patrol's separate stale-PID-file cleanup. The PID files
// sit in the same directory and share the prefix, but a PID file is proven
// stale by its process being dead, not by a quiet period — and this sweep must
// not take a position on them.
func TestScanIgnoresPIDFilesSharingTheFamilyPrefix(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"beads-test-dolt-1234.pid",
		"beads-test-dolt-1234",
		"dolt-test-server-abc.pid",
	} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("1234\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		stamp := time.Now().Add(-9 * time.Hour)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}

	res, err := Sweep(Options{Dir: root, MinAge: time.Hour, liveRefs: noRefs})
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("sweep considered plain files: %+v", res.Candidates)
	}
	for _, name := range []string{"beads-test-dolt-1234.pid", "beads-test-dolt-1234", "dolt-test-server-abc.pid"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("sweep removed the file %s: %v", name, err)
		}
	}
}

// TestSweepRefusesADirWithAnOpenFileDescriptor is the case argv evidence alone
// cannot see, and the reason the deacon patrol's shell block reached for lsof
// in the first place: a dolt sql-server holds .dolt/noms/LOCK open and names
// the fixture directory nowhere in its command line.
//
// The fixture is back-dated past the quiet period BEFORE the handle is opened
// — reading a file does not change its mtime — so age offers no protection
// here and the descriptor is the only thing standing between the directory and
// rm -rf. This runs the real procfs scanner, not a stub.
func TestSweepRefusesADirWithAnOpenFileDescriptor(t *testing.T) {
	root := t.TempDir()
	held, lock := mkBeadsFixture(t, root, "beads-test-dolt-busy", 9*time.Hour)

	fd, err := os.Open(lock)
	if err != nil {
		t.Fatalf("opening %s: %v", lock, err)
	}
	defer fd.Close()

	res, err := Sweep(Options{Dir: root, MinAge: time.Hour})
	if errors.Is(err, ErrNoProcEvidence) {
		t.Skipf("no process table on this platform: %v", err)
	}
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Inconclusive {
		t.Skipf("liveness evidence unavailable on this host: %v", res.Errors)
	}
	c := byPath(t, res, held)
	if c.Removed {
		t.Fatalf("swept a directory holding an open file descriptor: %s", held)
	}
	if c.Status != StatusLive {
		t.Errorf("status = %q (%s), want %q", c.Status, c.Reason, StatusLive)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("the held file was deleted: %v", err)
	}
}

// TestSweepControlRemovesTheSameDirOnceReleased is the control for the test
// above. Same fixture, same age, same code path, with the descriptor closed:
// if this did not remove it, the refusal above would prove nothing — it would
// be indistinguishable from a sweep that never removes beads fixtures at all.
func TestSweepControlRemovesTheSameDirOnceReleased(t *testing.T) {
	root := t.TempDir()
	released, lock := mkBeadsFixture(t, root, "beads-test-dolt-busy", 9*time.Hour)

	fd, err := os.Open(lock)
	if err != nil {
		t.Fatalf("opening %s: %v", lock, err)
	}
	if err := fd.Close(); err != nil {
		t.Fatalf("closing %s: %v", lock, err)
	}

	res, err := Sweep(Options{Dir: root, MinAge: time.Hour})
	if errors.Is(err, ErrNoProcEvidence) {
		t.Skipf("no process table on this platform: %v", err)
	}
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Inconclusive {
		t.Skipf("liveness evidence unavailable on this host: %v", res.Errors)
	}
	if res.Removed != 1 {
		t.Fatalf("removed %d directories, want 1 (%+v)", res.Removed, res.Candidates)
	}
	if _, err := os.Stat(released); !os.IsNotExist(err) {
		t.Errorf("directory survived a conclusive sweep (err=%v)", err)
	}
}

// TestLiveReferencesSeesAWorkingDirectory covers the third kind of evidence: a
// harness that chdirs into the fixture it created names it neither in argv nor
// in any descriptor, and only /proc/<pid>/cwd reports it.
func TestLiveReferencesSeesAWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	inhabited, _ := mkBeadsFixture(t, root, "beads-bd-tests-inhabited", time.Minute)
	empty, _ := mkBeadsFixture(t, root, "beads-bd-tests-empty", time.Minute)

	// `sh -c "sleep 60"` names no path at all in its command line, so argv
	// evidence cannot see it. cwd is the only trace it leaves.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %v", err)
	}
	cmd := exec.Command(sh, "-c", "sleep 60")
	cmd.Dir = inhabited
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting fixture process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	ev, err := liveReferences([]string{inhabited, empty})
	if errors.Is(err, ErrNoProcEvidence) {
		t.Skipf("no process table on this platform: %v", err)
	}
	if err != nil {
		t.Fatalf("liveReferences: %v", err)
	}
	if !ev.Refs[inhabited] {
		t.Errorf("liveReferences missed a process whose working directory is %s", inhabited)
	}
	// The control: an identical directory with nobody in it must come back
	// clean, or "sees a cwd" would be indistinguishable from "reports
	// everything live".
	if ev.Refs[empty] {
		t.Errorf("liveReferences reported %s live with no process using it", empty)
	}
}

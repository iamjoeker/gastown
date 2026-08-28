// Package tmpgc reclaims orphaned build and test scratch directories from a
// temporary directory.
//
// Three families strand directories under TMPDIR on a gastown host:
//
//   - Go toolchain work directories. The toolchain creates one ($WORK) for
//     every build, test, and vet invocation and removes it on normal exit. A
//     KILLED build — OOM killer, test timeout, session recycle — leaves it
//     behind, at 100-300MB a time.
//   - beads test fixture directories. The beads suite creates a Dolt data
//     directory per run; a suite killed mid-flight leaves it, LOCK file and
//     all.
//   - beads hermetic test environment roots. The beads suite creates one per
//     wrapper invocation to hold a private $HOME, $XDG_CONFIG_HOME and Dolt
//     root, and — because the run also builds itself a private copy of the bd
//     binary — each one costs about 201MB.
//
// Where TMPDIR is a RAM-backed tmpfs, a few dozen strandings exhaust it, and
// the first symptom is unrelated to either: gastown's own disk-space guard
// starts refusing polecat creation with "insufficient disk space" while `df /`
// still reports terabytes free, because /tmp is a different filesystem
// (gt-yb33).
//
// # Removal fails closed
//
// Sweeping is an rm -rf path. Every check that cannot produce a positive
// "nothing is using this" answer REFUSES to remove, and absence of a signal is
// never read as a clean bill of health. A directory is removed only when all
// of the following hold:
//
//   - Its name matches one of the known families exactly and it is a direct
//     child of the swept directory.
//   - It is a real directory, not a symlink.
//   - It is owned by the current user.
//   - Every file in it can be walked; a single unreadable subtree refuses the
//     whole candidate, because what cannot be inspected cannot be cleared.
//   - No live process references the path in its argv, has it as its working
//     directory, or holds a file descriptor inside it. See liveReferences for
//     why all three are needed and why /proc rather than lsof answers them.
//   - Nothing anywhere in the tree has been modified within MinAge. A live
//     build writes constantly, so a quiet tree is the secondary liveness
//     signal, covering the startup window in which a build has created $WORK
//     but named it to nothing yet.
//
// If the process table cannot be read at all, the sweep is inconclusive and
// removes NOTHING, however old the directories look.
//
// # Liveness is consulted before age
//
// The two liveness checks are ordered, and the order is deliberate: process
// evidence is asked FIRST and age only decides what the process table left
// alone. Both answers refuse removal, so the order changes no outcome today —
// it fixes what a candidate is REPORTED as, and it fixes precedence for any
// future check that treats "not young" as "eligible".
//
// The case that forces the order is a directory being written to right now.
// The sweep snapshots its clock before it walks, so a tree the Go driver is
// still compiling into can carry an mtime at or after that snapshot; its age
// then comes out zero or negative and it reads as merely young, when the
// stronger and more specific fact — a running process is holding it — was
// available the whole time (gt-5q6u).
package tmpgc

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// DefaultMinAge is how long a candidate's whole tree must have been untouched
// before it is treated as orphaned. The Go driver writes into $WORK
// continuously while a build runs, and a Dolt server writes to its data
// directory, so an hour of total silence is well past any live invocation —
// including the multi-minute test binaries in this repo.
const DefaultMinAge = time.Hour

// MinSafeMinAge is the shortest quiet period a destructive sweep will accept.
//
// The two pieces of evidence are complementary, and neither is sufficient
// alone. Argv evidence has a startup window: the Go driver creates $WORK before
// it spawns the first compile, so for the first seconds of a build the work
// directory exists, is empty, and is named by no process. Measured on this host
// on 2026-08-19, a `go build ./internal/...` started three seconds earlier
// showed up as "0 B, idle 0s" with nothing in the process table referencing it
// — a sweep with no quiet period would have deleted a running build's scratch
// space. The quiet period is what covers that window, so a destructive sweep
// refuses to run without a meaningful one. Dry runs accept any value: they
// classify, they do not delete.
const MinSafeMinAge = 5 * time.Minute

// ErrNoProcEvidence reports that live-process evidence is unavailable on this
// platform or host. It is a refusal, not a failure: callers report it and
// remove nothing.
var ErrNoProcEvidence = errors.New("no live-process evidence available")

// Evidence is what the process table could tell us about a set of paths.
type Evidence struct {
	// Refs holds the paths some running process is using.
	Refs map[string]bool
	// OpaqueProcesses counts processes owned by this user whose working
	// directory and open descriptors the kernel would not publish, because
	// they have marked themselves non-dumpable. Their argv was still read.
	// This is a hole in the coverage, so it is carried out to the caller and
	// reported rather than rounded to zero. See liveReferences.
	OpaqueProcesses int
}

// workDirPattern matches the Go toolchain's work directory names exactly:
// "go-build2674671939" for build/test/vet, "go-link-1315979694" for the
// external linker. Anchored on both ends so that no directory a human or
// another tool created under TMPDIR can be mistaken for one.
var workDirPattern = regexp.MustCompile(`^go-(build|link)-?[0-9]+$`)

// beadsTestDirPattern matches the beads suite's Dolt fixture directories:
// "beads-test-dolt-<suffix>" and "beads-bd-tests-<suffix>". Anchored the same
// way, and requiring a non-empty suffix so the bare prefixes — which a human
// might create by hand — are not candidates.
var beadsTestDirPattern = regexp.MustCompile(`^beads-(test-dolt|bd-tests)-.+$`)

// beadsTestEnvDirPattern matches the beads suite's hermetic environment roots:
// "beads-test-env-<suffix>", created by
//
//	mktemp -d "${TMPDIR:-/tmp}/beads-test-env-XXXXXX"
//
// in the beads repo's scripts/ci/lib/test-env.sh. The suffix is restricted to
// mktemp's own substitution alphabet rather than the ".+" the families above
// use, because these roots are the ones an operator is most likely to keep a
// copy of while debugging a failed run: every real name is exactly six
// alphanumerics, so "beads-test-env-zX8yTM.bak" and "beads-test-env-zX8yTM-keep"
// are outside the family and a copy someone parked under TMPDIR is not
// swept. Measured on this host on 2026-08-19, all 63 stranded roots matched
// `[A-Za-z0-9]{6}` exactly; the length itself is not pinned, so a wrapper that
// widens XXXXXX keeps working.
var beadsTestEnvDirPattern = regexp.MustCompile(`^beads-test-env-[A-Za-z0-9]+$`)

// A Family is a class of temporary directory the sweep knows how to identify
// by name. Membership is the FIRST safety check and the narrowest one: a
// directory whose name is not exactly a member of some family is never
// considered, whatever its age or size.
type Family struct {
	// Name identifies the family in reports and JSON.
	Name string
	// Pattern matches a directory's base name, anchored at both ends.
	Pattern *regexp.Regexp
	// Summary is a one-line description for help text.
	Summary string
}

// FamilyGoWork is the Go toolchain's per-invocation work directory.
var FamilyGoWork = Family{
	Name:    "go-work",
	Pattern: workDirPattern,
	Summary: "Go toolchain work directories (go-build<n>, go-link-<n>)",
}

// FamilyBeadsTest is the beads suite's per-run Dolt fixture directory.
//
// Before gt-1gdh this family was swept by an inline shell block in the deacon
// patrol formula, which decided liveness from `lsof +D`. That block failed
// OPEN for years because lsof answers through an exit status that is non-zero
// for every failure mode as well as for success-with-nothing-found; the
// repaired version worked only by carefully not trusting that status (gt-32z).
// Here there is no status to misread.
var FamilyBeadsTest = Family{
	Name:    "beads-test",
	Pattern: beadsTestDirPattern,
	Summary: "beads test fixture directories (beads-test-dolt-*, beads-bd-tests-*)",
}

// FamilyBeadsTestEnv is the beads suite's per-invocation hermetic environment
// root.
//
// beads_test_env_enter() in the beads repo's scripts/ci/lib/test-env.sh points
// $HOME, $XDG_CONFIG_HOME, $DOLT_ROOT_PATH and $GIT_CONFIG_GLOBAL inside a
// fresh mktemp directory, and scripts/test.sh then go-builds a private copy of
// cmd/bd into $BEADS_TEST_ENV_ROOT/prebuilt-bd/bd. That binary is why each root
// costs about 201MB. Nothing reuses one: every invocation mktemps its own and
// rebuilds, so a stranded root is dead weight and not a warm cache. The
// wrapper removes it from a bash `trap ... EXIT`, which does not run when the
// run is SIGKILLed — the usual end of a long suite on a loaded host.
//
// # Only an open descriptor proves this family is live
//
// A run in flight is invisible to the other two kinds of evidence. Measured on
// this host on 2026-08-19 with a beads suite running, a full sweep of /proc
// found:
//
//   - 0 processes naming any beads-test-env path in argv. The wrapper exports
//     the root through the ENVIRONMENT, so the command line is a plain
//     `go test -p 4 -parallel 4 -timeout 25m ./cmd/bd`.
//   - 0 processes with one as their working directory. The suite chdirs to the
//     repo, not into its environment root.
//   - 1 process holding a descriptor inside one — /proc/2698443/fd/4, on
//     xdg-config/go/telemetry/local/go@....v1.count, a counter file the Go
//     toolchain keeps mapped for the life of the command because
//     $XDG_CONFIG_HOME points into the root.
//
// So for this family the descriptor scan is not one signal among three, it is
// the only one that fires, and a sweep that judged liveness from argv or cwd
// would have deleted a running suite's $HOME. That is the whole argument for
// adding the family here rather than to any check that cannot read /proc.
var FamilyBeadsTestEnv = Family{
	Name:    "beads-test-env",
	Pattern: beadsTestEnvDirPattern,
	Summary: "beads hermetic test environment roots (beads-test-env-*, ~201 MB each)",
}

// DefaultFamilies is what a sweep considers when Options.Families is empty.
func DefaultFamilies() []Family {
	return []Family{FamilyGoWork, FamilyBeadsTest, FamilyBeadsTestEnv}
}

// matchFamily returns the family a base name belongs to, and whether it
// belongs to any of them.
func matchFamily(families []Family, name string) (Family, bool) {
	for _, f := range families {
		if f.Pattern.MatchString(name) {
			return f, true
		}
	}
	return Family{}, false
}

// Status is the disposition of a single candidate directory.
type Status string

const (
	// StatusReclaimable means every safety check passed: the directory is an
	// orphan and may be removed.
	StatusReclaimable Status = "reclaimable"
	// StatusLive means a running process is using this directory: it names it
	// in its argv, has it as its working directory, or holds a descriptor
	// inside it.
	StatusLive Status = "live"
	// StatusYoung means something in the tree was modified within MinAge.
	StatusYoung Status = "young"
	// StatusRefused means a safety check could not be satisfied. The reason
	// says which one.
	StatusRefused Status = "refused"
)

// Candidate is one directory considered by a scan.
type Candidate struct {
	Path      string        `json:"path"`
	Family    string        `json:"family"`
	SizeBytes uint64        `json:"size_bytes"`
	Newest    time.Time     `json:"newest_mtime"`
	Age       time.Duration `json:"age"`
	Status    Status        `json:"status"`
	Reason    string        `json:"reason,omitempty"`
	Removed   bool          `json:"removed"`
}

// Result is the outcome of a scan or sweep.
type Result struct {
	Dir string `json:"dir"`
	// Families names the classes of directory this run considered, so a
	// report cannot be mistaken for a broader sweep than it was.
	Families         []string      `json:"families"`
	MinAge           time.Duration `json:"min_age"`
	DryRun           bool          `json:"dry_run"`
	Candidates       []Candidate   `json:"candidates"`
	Removed          int           `json:"removed"`
	RemovedBytes     uint64        `json:"removed_bytes"`
	ReclaimableBytes uint64        `json:"reclaimable_bytes"`
	// Inconclusive means liveness evidence could not be gathered, so nothing
	// was eligible for removal regardless of age.
	Inconclusive bool `json:"inconclusive"`
	// OpaqueProcesses counts running processes this sweep could not fully
	// inspect. See Evidence.OpaqueProcesses. It is not a failure, but it is a
	// gap in the coverage, and a report that hid it would read as complete.
	OpaqueProcesses int      `json:"opaque_processes,omitempty"`
	Errors          []string `json:"errors,omitempty"`
}

// Options configures a scan or sweep.
type Options struct {
	// Dir is the directory to sweep. Empty means os.TempDir().
	Dir string
	// MinAge is the quiet period a tree must observe before it is eligible.
	// Zero means DefaultMinAge. Negative is rejected.
	MinAge time.Duration
	// DryRun classifies candidates without removing anything.
	DryRun bool
	// Families restricts which classes of directory are considered. Empty
	// means DefaultFamilies.
	Families []Family

	// now overrides the clock. Tests only.
	now func() time.Time
	// liveRefs overrides live-process evidence. Tests only.
	liveRefs func(paths []string) (Evidence, error)
}

func (o *Options) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}

func (o *Options) references(paths []string) (Evidence, error) {
	if o.liveRefs != nil {
		return o.liveRefs(paths)
	}
	return liveReferences(paths)
}

func (o *Options) dir() string {
	if o.Dir != "" {
		return o.Dir
	}
	return os.TempDir()
}

func (o *Options) minAge() time.Duration {
	if o.MinAge == 0 {
		return DefaultMinAge
	}
	return o.MinAge
}

func (o *Options) families() []Family {
	if len(o.Families) > 0 {
		return o.Families
	}
	return DefaultFamilies()
}

// Scan classifies the sweepable temp directories under opts.Dir without
// removing anything.
func Scan(opts Options) (*Result, error) {
	opts.DryRun = true
	return sweep(opts, false)
}

// Sweep classifies the sweepable temp directories under opts.Dir and removes
// the reclaimable ones. With opts.DryRun set it is equivalent to Scan.
func Sweep(opts Options) (*Result, error) {
	return sweep(opts, !opts.DryRun)
}

func sweep(opts Options, remove bool) (*Result, error) {
	if opts.MinAge < 0 {
		return nil, fmt.Errorf("min age must not be negative: %s", opts.MinAge)
	}
	if remove && opts.minAge() < MinSafeMinAge {
		return nil, fmt.Errorf("refusing to remove with a min age below %s (got %s): "+
			"a build that has just created its work directory names it nowhere yet",
			MinSafeMinAge, opts.minAge())
	}
	dir := opts.dir()
	if err := validateSweepDir(dir); err != nil {
		return nil, err
	}

	families := opts.families()
	res := &Result{
		Dir:    dir,
		MinAge: opts.minAge(),
		DryRun: !remove,
	}
	for _, f := range families {
		res.Families = append(res.Families, f.Name)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	now := opts.clock()
	for _, entry := range entries {
		// Only directories are candidates, and symlinks are candidates only so
		// that they are visibly REFUSED rather than silently skipped. Plain
		// files are never considered: the PID files that sit beside these
		// directories share their prefix, and they belong to a different
		// cleanup with a different proof of death.
		if !entry.IsDir() && entry.Type()&fs.ModeSymlink == 0 {
			continue
		}
		family, ok := matchFamily(families, entry.Name())
		if !ok {
			continue
		}
		c := inspect(filepath.Join(dir, entry.Name()), now)
		c.Family = family.Name
		res.Candidates = append(res.Candidates, c)
	}
	if len(res.Candidates) == 0 {
		return res, nil
	}

	// Liveness evidence is gathered once, for every candidate that survived the
	// cheap checks — INCLUDING the ones the age check is about to call young,
	// because a directory being written to right now is the case where age is
	// least able to speak and process evidence is most specific. Asking about
	// them is close to free: liveReferences walks /proc once whatever the size
	// of the path set. A candidate already refused stays refused: asking the
	// process table about it cannot un-refuse an unreadable tree.
	var ask []string
	for _, c := range res.Candidates {
		if c.Status == StatusReclaimable {
			ask = append(ask, c.Path)
		}
	}
	if len(ask) > 0 {
		ev, err := opts.references(ask)
		res.OpaqueProcesses = ev.OpaqueProcesses
		if err != nil {
			// Could not look. That is not permission to delete.
			res.Inconclusive = true
			res.Errors = append(res.Errors, fmt.Sprintf("live-process evidence unavailable: %v", err))
		}
		for i := range res.Candidates {
			c := &res.Candidates[i]
			if c.Status != StatusReclaimable {
				continue
			}
			switch {
			case err == nil && ev.Refs[c.Path]:
				c.Status = StatusLive
				c.Reason = "referenced by a running process"
			case c.Age < res.MinAge:
				c.Status = StatusYoung
				c.Reason = fmt.Sprintf("modified %s ago (min age %s)", c.Age.Round(time.Second), res.MinAge)
			case err != nil:
				// Old, and unheld as far as anyone can tell — but nobody could
				// look, so there is no "as far as anyone can tell".
				c.Status = StatusRefused
				c.Reason = "live-process evidence unavailable"
			}
		}
	}

	for i := range res.Candidates {
		c := &res.Candidates[i]
		if c.Status != StatusReclaimable {
			continue
		}
		res.ReclaimableBytes += c.SizeBytes
		if !remove {
			continue
		}
		if err := removeWorkDir(families, c.Path); err != nil {
			c.Status = StatusRefused
			c.Reason = err.Error()
			res.ReclaimableBytes -= c.SizeBytes
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", c.Path, err))
			continue
		}
		c.Removed = true
		res.Removed++
		res.RemovedBytes += c.SizeBytes
		res.ReclaimableBytes -= c.SizeBytes
	}

	sort.Slice(res.Candidates, func(i, j int) bool {
		return res.Candidates[i].SizeBytes > res.Candidates[j].SizeBytes
	})
	return res, nil
}

// validateSweepDir rejects sweep roots that could turn a targeted cleanup into
// a broad one.
func validateSweepDir(dir string) error {
	if dir == "" {
		return errors.New("sweep directory must not be empty")
	}
	clean := filepath.Clean(dir)
	if clean == string(filepath.Separator) || clean == "." {
		return fmt.Errorf("refusing to sweep %q", dir)
	}
	fi, err := os.Lstat(clean)
	if err != nil {
		return fmt.Errorf("stat %s: %w", clean, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("not a directory: %s", clean)
	}
	return nil
}

// inspect applies every check that can be answered from the filesystem alone.
//
// It measures age but does not judge it. StatusReclaimable here means only
// "nothing on disk refuses this"; the caller consults the process table first
// and applies MinAge to whatever that left alone. See the package comment.
func inspect(path string, now time.Time) Candidate {
	c := Candidate{Path: path}

	fi, err := os.Lstat(path)
	if err != nil {
		c.Status = StatusRefused
		c.Reason = fmt.Sprintf("cannot stat: %v", err)
		return c
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		c.Status = StatusRefused
		c.Reason = "symlink"
		return c
	}
	if !fi.IsDir() {
		c.Status = StatusRefused
		c.Reason = "not a directory"
		return c
	}
	owned, err := ownedByCurrentUser(fi)
	if err != nil {
		c.Status = StatusRefused
		c.Reason = fmt.Sprintf("cannot determine owner: %v", err)
		return c
	}
	if !owned {
		c.Status = StatusRefused
		c.Reason = "owned by another user"
		return c
	}

	size, newest, err := walkTree(path, fi.ModTime())
	if err != nil {
		// A tree we cannot fully read is a tree whose liveness we cannot judge.
		c.Status = StatusRefused
		c.Reason = fmt.Sprintf("cannot fully inspect: %v", err)
		return c
	}
	c.SizeBytes = size
	c.Newest = newest
	// now was snapshotted before the walk, so a tree still being written to can
	// carry an mtime AFTER it and measure as negative age. Clamp to zero: a
	// directory cannot have been quiet for less than no time, and a negative
	// duration in a report reads as a clock fault rather than as what it is —
	// something writing here while we looked.
	if c.Age = now.Sub(newest); c.Age < 0 {
		c.Age = 0
	}

	c.Status = StatusReclaimable
	return c
}

// walkTree returns the total size of the tree and the newest modification time
// anywhere in it. The root's own mtime seeds the comparison: a build that has
// only just created $WORK has an empty but freshly stamped directory.
func walkTree(root string, rootMod time.Time) (uint64, time.Time, error) {
	var total uint64
	newest := rootMod

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			// The Go driver deletes its own scratch files constantly; a file
			// that vanished mid-walk belongs to something, and something that
			// is still writing here is exactly what we refuse to touch.
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("tree changed during inspection: %s", path)
			}
			return err
		}
		if info.Mode().IsRegular() {
			total += uint64(info.Size())
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	if err != nil {
		return 0, time.Time{}, err
	}
	return total, newest, nil
}

// removeWorkDir re-verifies the identity of a path immediately before deleting
// it, so that a directory swapped between inspection and removal is not
// removed on the strength of the old evidence.
func removeWorkDir(families []Family, path string) error {
	if _, ok := matchFamily(families, filepath.Base(path)); !ok {
		return fmt.Errorf("not a sweepable temp directory: %s", path)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat before removal: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("became a symlink before removal")
	}
	if !fi.IsDir() {
		return errors.New("no longer a directory")
	}
	owned, err := ownedByCurrentUser(fi)
	if err != nil {
		return fmt.Errorf("owner check before removal: %w", err)
	}
	if !owned {
		return errors.New("owned by another user")
	}
	return os.RemoveAll(path)
}

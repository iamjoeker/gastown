// Package tmpgc reclaims orphaned Go toolchain work directories from a
// temporary directory.
//
// The Go toolchain creates a work directory ($WORK) under TMPDIR for every
// build, test, and vet invocation and removes it on normal exit. A KILLED
// build — OOM killer, test timeout, session recycle — leaves it behind. Where
// TMPDIR is a RAM-backed tmpfs, a few dozen strandings exhaust it, and the
// first symptom is unrelated to Go: gastown's own disk-space guard starts
// refusing polecat creation with "insufficient disk space" while `df /` still
// reports terabytes free, because /tmp is a different filesystem (gt-yb33).
//
// # Removal fails closed
//
// Sweeping is an rm -rf path. Every check that cannot produce a positive
// "nothing is using this" answer REFUSES to remove, and absence of a signal is
// never read as a clean bill of health. A directory is removed only when all
// of the following hold:
//
//   - Its name matches a Go work directory exactly (go-build<digits>,
//     go-link-<digits>) and it is a direct child of the swept directory.
//   - It is a real directory, not a symlink.
//   - It is owned by the current user.
//   - Every file in it can be walked; a single unreadable subtree refuses the
//     whole candidate, because what cannot be inspected cannot be cleared.
//   - Nothing anywhere in the tree has been modified within MinAge. A live
//     build writes constantly, so a quiet tree is the primary liveness signal.
//   - No live process references the path in its argv. The Go driver passes
//     $WORK paths to every compile, link, and vet subprocess it spawns, so a
//     build in progress names its own work directory in the process table.
//
// If the process table cannot be read at all, the sweep is inconclusive and
// removes NOTHING, however old the directories look.
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

// DefaultMinAge is how long a work directory's whole tree must have been
// untouched before it is treated as orphaned. The Go driver writes into $WORK
// continuously while a build runs, so an hour of total silence is well past
// any live invocation — including the multi-minute test binaries in this repo.
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

// workDirPattern matches the Go toolchain's work directory names exactly:
// "go-build2674671939" for build/test/vet, "go-link-1315979694" for the
// external linker. Anchored on both ends so that no directory a human or
// another tool created under TMPDIR can be mistaken for one.
var workDirPattern = regexp.MustCompile(`^go-(build|link)-?[0-9]+$`)

// Status is the disposition of a single candidate directory.
type Status string

const (
	// StatusReclaimable means every safety check passed: the directory is an
	// orphan and may be removed.
	StatusReclaimable Status = "reclaimable"
	// StatusLive means a running process names this directory in its argv.
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
	SizeBytes uint64        `json:"size_bytes"`
	Newest    time.Time     `json:"newest_mtime"`
	Age       time.Duration `json:"age"`
	Status    Status        `json:"status"`
	Reason    string        `json:"reason,omitempty"`
	Removed   bool          `json:"removed"`
}

// Result is the outcome of a scan or sweep.
type Result struct {
	Dir              string        `json:"dir"`
	MinAge           time.Duration `json:"min_age"`
	DryRun           bool          `json:"dry_run"`
	Candidates       []Candidate   `json:"candidates"`
	Removed          int           `json:"removed"`
	RemovedBytes     uint64        `json:"removed_bytes"`
	ReclaimableBytes uint64        `json:"reclaimable_bytes"`
	// Inconclusive means liveness evidence could not be gathered, so nothing
	// was eligible for removal regardless of age.
	Inconclusive bool     `json:"inconclusive"`
	Errors       []string `json:"errors,omitempty"`
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

	// now overrides the clock. Tests only.
	now func() time.Time
	// liveRefs overrides live-process evidence. Tests only.
	liveRefs func(paths []string) (map[string]bool, error)
}

func (o *Options) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}

func (o *Options) references(paths []string) (map[string]bool, error) {
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

// Scan classifies the Go work directories under opts.Dir without removing
// anything.
func Scan(opts Options) (*Result, error) {
	opts.DryRun = true
	return sweep(opts, false)
}

// Sweep classifies the Go work directories under opts.Dir and removes the
// reclaimable ones. With opts.DryRun set it is equivalent to Scan.
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

	res := &Result{
		Dir:    dir,
		MinAge: opts.minAge(),
		DryRun: !remove,
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	now := opts.clock()
	for _, entry := range entries {
		if !workDirPattern.MatchString(entry.Name()) {
			continue
		}
		res.Candidates = append(res.Candidates, inspect(filepath.Join(dir, entry.Name()), now, res.MinAge))
	}
	if len(res.Candidates) == 0 {
		return res, nil
	}

	// Liveness evidence is gathered once, for every candidate that survived the
	// cheap checks. A candidate already refused stays refused: asking the
	// process table about it cannot un-refuse an unreadable tree.
	var ask []string
	for _, c := range res.Candidates {
		if c.Status == StatusReclaimable {
			ask = append(ask, c.Path)
		}
	}
	if len(ask) > 0 {
		refs, err := opts.references(ask)
		if err != nil {
			// Could not look. That is not permission to delete.
			res.Inconclusive = true
			res.Errors = append(res.Errors, fmt.Sprintf("live-process evidence unavailable: %v", err))
			for i := range res.Candidates {
				if res.Candidates[i].Status == StatusReclaimable {
					res.Candidates[i].Status = StatusRefused
					res.Candidates[i].Reason = "live-process evidence unavailable"
				}
			}
		} else {
			for i := range res.Candidates {
				if res.Candidates[i].Status == StatusReclaimable && refs[res.Candidates[i].Path] {
					res.Candidates[i].Status = StatusLive
					res.Candidates[i].Reason = "referenced by a running process"
				}
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
		if err := removeWorkDir(c.Path); err != nil {
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
func inspect(path string, now time.Time, minAge time.Duration) Candidate {
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
	c.Age = now.Sub(newest)

	if c.Age < minAge {
		c.Status = StatusYoung
		c.Reason = fmt.Sprintf("modified %s ago (min age %s)", c.Age.Round(time.Second), minAge)
		return c
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
func removeWorkDir(path string) error {
	if !workDirPattern.MatchString(filepath.Base(path)) {
		return fmt.Errorf("not a Go work directory: %s", path)
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

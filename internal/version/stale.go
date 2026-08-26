// Package version provides version information and staleness checking for gt.
package version

import (
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/util"
)

// These variables are set at build time via ldflags in cmd package.
// We provide fallback methods to read from build info.
var (
	// Commit can be set from cmd package or read from build info
	Commit = ""
)

// StaleBinaryInfo contains information about binary staleness.
type StaleBinaryInfo struct {
	IsStale       bool   // True if binary commit is behind the build-branch ref
	IsForward     bool   // True if the compare commit is a descendant of binary commit (safe to rebuild)
	OnMainBranch  bool   // True if the resolved source worktree is on a build branch
	BinaryCommit  string // Commit hash the binary was built from
	RepoCommit    string // Commit of the ref the binary was compared against (CompareRef)
	CompareRef    string // The ref staleness was computed against (e.g. "main", "origin/main")
	CommitsBehind int    // Number of commits binary is behind (0 if unknown)
	Refreshed     bool   // True if RepoCommit was read from the remote during this check
	RefreshError  string // Why the remote read did not happen or did not succeed
	Skipped       bool   // True if staleness could not be determined safely
	SkipReason    string // Human-readable reason the check was skipped
	Error         error  // Any error encountered during check
}

// StaleOptions tunes how the compare ref is resolved.
type StaleOptions struct {
	// RefreshRemote asks the check to read the build branch's tip from the
	// remote rather than trusting a local ref. It costs one bounded fetch, so
	// callers on a hot path (the per-command startup warning) leave it off and
	// accept that they can only ever under-report staleness.
	RefreshRemote bool

	// Remote overrides the remote to read from. Empty means "whatever the
	// build branch tracks", falling back to origin.
	Remote string
}

// defaultRemote is the remote a build branch is read from when it tracks
// nothing.
const defaultRemote = "origin"

type buildBranchRef struct {
	ref     string
	display string
	commit  string
	remote  string // remote to re-read this branch from ("" = whatever it tracks)
	branch  string // branch name as it exists on that remote
}

// resolveCommitHash gets the commit hash from build info or the Commit variable.
//
// vcs.revision is NOT trustworthy on its own: Go stamps it from whichever git
// repository encloses the build directory, which inside a Gas Town tree is
// often the town repo rather than gastown. CheckStaleBinary is safe against
// that only because it verifies the hash resolves in the source repo before
// comparing anything (see the resolveGitCommit call below, which turns a
// foreign sha into Skipped rather than a wrong verdict). Do not remove that
// check, and do not surface this value to users as the binary's build commit —
// see internal/cmd.resolveCommitHash for the provenance distinction. (gt-5mvj)
func resolveCommitHash() string {
	if Commit != "" {
		return Commit
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value
			}
		}
	}

	return ""
}

// Describe returns a one-line, human-readable staleness summary for a stale
// binary, using subject as the leading noun so callers can vary it
// ("Binary" for gt doctor, "gt binary" for the startup warning):
//
//	"Binary is 3 commits behind main (built from abc123…, main at def456…)"
//	"gt binary is stale (built from abc123…, origin/main at def456…)"
//
// It is only meaningful when i.IsStale; callers gate on that. A zero
// CommitsBehind (count unknown) falls back to the "is stale" wording.
func (i *StaleBinaryInfo) Describe(subject string) string {
	if i.CommitsBehind > 0 {
		return fmt.Sprintf("%s is %d commits behind %s (built from %s, %s at %s)",
			subject, i.CommitsBehind, i.CompareRef,
			ShortCommit(i.BinaryCommit), i.CompareRef, ShortCommit(i.RepoCommit))
	}
	return fmt.Sprintf("%s is stale (built from %s, %s at %s)",
		subject, ShortCommit(i.BinaryCommit), i.CompareRef, ShortCommit(i.RepoCommit))
}

// ShortCommit returns first 12 characters of a hash.
func ShortCommit(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// commitsMatch compares two commit hashes, handling different lengths.
// Returns true if one is a prefix of the other (minimum 7 chars to avoid false positives).
func commitsMatch(a, b string) bool {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	// Need at least 7 chars for a reasonable comparison
	if minLen < 7 {
		return false
	}
	return strings.HasPrefix(a, b[:minLen]) || strings.HasPrefix(b, a[:minLen])
}

// CheckStaleBinary compares the binary's embedded commit with a build-branch
// ref resolved entirely from the local repository. It never touches the
// network, which also means it can only ever UNDER-report staleness: see
// CheckStaleBinaryWithOptions.
func CheckStaleBinary(repoDir string) *StaleBinaryInfo {
	return CheckStaleBinaryWithOptions(repoDir, StaleOptions{})
}

// CheckStaleBinaryWithOptions compares the binary's embedded commit with a
// build-branch ref. It returns staleness info including whether the binary
// needs rebuilding. This check is designed to be fast and non-blocking -
// errors are captured but don't interrupt normal operation.
//
// # Why opts.RefreshRemote exists
//
// Every ref this check can reach locally is a CACHE WITH NO INVALIDATION.
// GetRepoRoot resolves to $GT_ROOT/gastown/mayor/rig, a working clone; its
// `main` only moves when something pulls, and its `refs/remotes/origin/main`
// only moves when something fetches. In a Gas Town nothing does either on a
// schedule. So when a commit lands on the real main, the compare ref does not
// move, the binary is measured against the commit it was already built from,
// and a binary that IS out of date reports stale:false — which is the one
// answer that makes the rebuild loop do nothing. Measured on gastown: twice in
// one evening, the second time within twenty minutes of a hand-rebuild
// (gt-ympl / hq-cak50).
//
// RefreshRemote reads the branch tip from the remote instead, for one bounded
// fetch. When that read fails the check does NOT quietly fall back to
// reporting freshness: a local ref can only lag the remote, so "behind the
// local ref" still proves stale, while "level with the local ref" proves
// nothing and is reported as Skipped.
func CheckStaleBinaryWithOptions(repoDir string, opts StaleOptions) *StaleBinaryInfo {
	info := &StaleBinaryInfo{}

	// Get binary commit
	info.BinaryCommit = resolveCommitHash()
	if info.BinaryCommit == "" {
		info.Error = fmt.Errorf("cannot determine binary commit (dev build?)")
		return info
	}
	if !isGitRepo(repoDir) {
		info.Error = fmt.Errorf("source repo %q is not a git worktree", repoDir)
		return info
	}
	binaryCommit, err := resolveGitCommit(repoDir, info.BinaryCommit)
	if err != nil {
		info.Skipped = true
		info.SkipReason = "binary commit not found in source repo; cannot compare staleness"
		return info
	}

	// Check which branch the resolved source worktree is on.
	// Accept main/master (upstream) and carry/* (fork operational branches).
	var branch string
	branchCmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	branchCmd.Dir = repoDir
	util.SetDetachedProcessGroup(branchCmd)
	if branchOutput, err := branchCmd.Output(); err == nil {
		branch = strings.TrimSpace(string(branchOutput))
	}
	info.OnMainBranch = isBuildBranch(branch)

	// Decide which ref to compare the binary against.
	//
	// GetRepoRoot resolves to $GT_ROOT/gastown/mayor/rig, a worktree that
	// normally sits on a feature branch (that's where the Mayor does git work).
	// Diffing the binary against that worktree's HEAD compares it to unmerged
	// feature work and produces a false "N commits behind" warning advising a
	// rebuild from the feature branch (GH#4034). Staleness is only meaningful
	// relative to a *build branch*.
	var compareCommit string
	var compareRef buildBranchRef
	if info.OnMainBranch {
		// Already on a build branch — its HEAD is the build branch.
		compareCommit, err = resolveGitCommit(repoDir, "HEAD")
		if err != nil {
			info.Error = fmt.Errorf("cannot resolve build branch HEAD: %w", err)
			return info
		}
		compareRef = buildBranchRef{display: branch, commit: compareCommit, branch: branch}
	} else {
		// Resolve a real build-branch ref instead of the feature HEAD.
		ref, ok := resolveBuildBranchRef(repoDir, binaryCommit)
		if !ok {
			info.Skipped = true
			info.SkipReason = "source worktree not on a build branch and no build-branch ref found to compare against"
			return info
		}
		compareRef = ref
		compareCommit = ref.commit
	}
	info.CompareRef = compareRef.display

	// Replace the local (never-invalidated) compare commit with the remote's,
	// when the caller is willing to pay for the round trip.
	if opts.RefreshRemote {
		remote := opts.Remote
		if remote == "" {
			remote = compareRef.remote
		}
		if remote == "" {
			remote = branchRemote(repoDir, compareRef.branch)
		}
		remoteCommit, cleanup, err := fetchBranchTip(repoDir, remote, compareRef.branch)
		if cleanup != nil {
			// Held until every ancestry/count/diff question below has been
			// asked: the private ref is what keeps the fetched objects
			// reachable.
			defer cleanup()
		}
		switch {
		case err != nil:
			info.RefreshError = err.Error()
		case remoteCommit == "":
			info.RefreshError = fmt.Sprintf("%s/%s resolved to no commit", remote, compareRef.branch)
		default:
			info.Refreshed = true
			compareCommit = remoteCommit
			info.CompareRef = remote + "/" + compareRef.branch
		}
	}
	info.RepoCommit = compareCommit

	// Compare commits using prefix matching (handles short vs full hash)
	// Use the shorter of the two commit lengths for comparison
	if !commitsMatch(info.BinaryCommit, info.RepoCommit) {
		// Check if all commits between binary and the build ref only touch
		// .beads/ files (e.g., bd backup commits). These don't affect the
		// binary and should not trigger a stale warning. (GH#2596)
		if onlyBeadsChanges(repoDir, binaryCommit, compareCommit) {
			// Build ref advanced but only via beads-only commits — not stale
			markUnprovenFreshness(info, opts)
			return info
		}

		info.IsStale = true

		// Check if this is a forward-only update (binary commit is ancestor of
		// the build ref). This prevents rebuilding to an older or diverged
		// commit, which caused a crash loop when a worktree's HEAD was behind
		// the binary's commit.
		info.IsForward = isAncestor(repoDir, binaryCommit, compareCommit)

		// Try to count commits between binary and the build ref
		countCmd := exec.Command("git", "rev-list", "--count", binaryCommit+".."+compareCommit)
		countCmd.Dir = repoDir
		util.SetDetachedProcessGroup(countCmd)
		if countOutput, err := countCmd.Output(); err == nil {
			if count, parseErr := fmt.Sscanf(strings.TrimSpace(string(countOutput)), "%d", &info.CommitsBehind); parseErr != nil || count != 1 {
				info.CommitsBehind = 0
			}
		}
	}

	markUnprovenFreshness(info, opts)
	return info
}

// markUnprovenFreshness downgrades an unverified "fresh" verdict to Skipped.
//
// Staleness detection is one-sided. A local ref can only LAG the remote, never
// lead it, so a binary measured as behind that ref is behind the remote too —
// the positive verdict survives a failed refresh. The negative one does not:
// "level with a ref that nothing updates" is not evidence of anything, and it
// is precisely the shape that let a stale binary read as fresh for two hours
// (gt-ympl). A caller that asked for the remote and did not get it is told the
// check could not measure, rather than being handed the wrong answer.
func markUnprovenFreshness(info *StaleBinaryInfo, opts StaleOptions) {
	if !opts.RefreshRemote || info.Refreshed || info.IsStale {
		return
	}
	info.Skipped = true
	info.SkipReason = fmt.Sprintf("could not read %s from the remote, and a local ref that nothing updates cannot prove the binary is fresh: %s",
		info.CompareRef, info.RefreshError)
}

// resolveBuildBranchRef finds a build-branch ref to compare the binary against
// when the resolved source worktree is parked on a non-build branch (the normal
// state for $GT_ROOT/gastown/mayor/rig). Without this, staleness would be
// computed against unmerged feature work (GH#4034).
//
// Candidate refs are fully qualified to avoid branch/tag shadowing. Among refs
// that contain the binary commit, choose the freshest descendant; only use the
// candidate order below to break truly diverged ties.
func resolveBuildBranchRef(repoDir, binaryCommit string) (buildBranchRef, bool) {
	var usable []buildBranchRef
	for _, candidate := range buildBranchCandidates(repoDir) {
		commit, err := resolveGitCommit(repoDir, candidate.ref)
		if err != nil || !isAncestor(repoDir, binaryCommit, commit) {
			continue
		}
		candidate.commit = commit
		usable = append(usable, candidate)
	}
	if len(usable) == 0 {
		return buildBranchRef{}, false
	}

	frontier := make([]buildBranchRef, 0, len(usable))
	for i, candidate := range usable {
		older := false
		for j, other := range usable {
			if i == j || candidate.commit == other.commit {
				continue
			}
			if isAncestor(repoDir, candidate.commit, other.commit) {
				older = true
				break
			}
		}
		if !older {
			frontier = append(frontier, candidate)
		}
	}
	return frontier[0], true
}

func buildBranchCandidates(repoDir string) []buildBranchRef {
	candidates := make([]buildBranchRef, 0, 10)
	for _, pattern := range []string{
		"refs/heads/carry/",
		"refs/remotes/upstream/carry/",
		"refs/remotes/origin/carry/",
	} {
		if ref, ok := singleBranchRef(repoDir, pattern); ok {
			candidates = append(candidates, ref)
		}
	}
	candidates = append(candidates,
		buildBranchRef{ref: "refs/remotes/upstream/main", display: "upstream/main", remote: "upstream", branch: "main"},
		buildBranchRef{ref: "refs/remotes/upstream/master", display: "upstream/master", remote: "upstream", branch: "master"},
		buildBranchRef{ref: "refs/remotes/origin/main", display: "origin/main", remote: "origin", branch: "main"},
		buildBranchRef{ref: "refs/remotes/origin/master", display: "origin/master", remote: "origin", branch: "master"},
		buildBranchRef{ref: "refs/heads/main", display: "main", branch: "main"},
		buildBranchRef{ref: "refs/heads/master", display: "master", branch: "master"},
	)
	return candidates
}

// branchRemote returns the remote a local branch tracks, defaulting to origin.
//
// Not merely a convenience: gastown clones carry an `upstream` that is ~99
// commits behind the fork they actually build from, so guessing the wrong
// remote here would reintroduce the same wrong-reference bug this refresh
// exists to close.
func branchRemote(repoDir, branch string) string {
	if branch == "" {
		return defaultRemote
	}
	cmd := exec.Command("git", "config", "--get", "branch."+branch+".remote")
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return defaultRemote
	}
	if remote := strings.TrimSpace(string(out)); remote != "" {
		return remote
	}
	return defaultRemote
}

// fetchBranchTip reads branch's tip from remote and brings the commit into the
// local object store so the ancestry and count queries below can answer about
// it. The caller must invoke the returned cleanup.
func fetchBranchTip(repoDir, remote, branch string) (commit string, cleanup func(), err error) {
	if branch == "" {
		return "", nil, fmt.Errorf("no branch name to read from %s", remote)
	}
	return git.NewGit(repoDir).FetchPushRemoteBranchTip(remote, branch)
}

func resolveGitCommit(repoDir, rev string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// isAncestor reports whether ancestor is an ancestor of ref (a commit is its
// own ancestor) in repoDir.
func isAncestor(repoDir, ancestor, ref string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, ref)
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	return cmd.Run() == nil
}

// singleBranchRef returns the sole matching branch/ref, if exactly one exists.
// Multiple matches are ambiguous and yield false.
func singleBranchRef(repoDir, pattern string) (buildBranchRef, bool) {
	cmd := exec.Command("git", "for-each-ref", "--format=%(refname)", pattern)
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return buildBranchRef{}, false
	}
	refs := strings.Fields(strings.TrimSpace(string(out)))
	if len(refs) != 1 {
		return buildBranchRef{}, false
	}
	display := strings.TrimPrefix(refs[0], "refs/heads/")
	display = strings.TrimPrefix(display, "refs/remotes/")

	// Split the display back into (remote, branch) so the ref can be re-read
	// from the remote. A local ref has no remote in its name; a remote-tracking
	// one carries it as the first segment, and the rest is the branch as the
	// remote knows it (carry/operational, not origin/carry/operational).
	var remote, branch string
	if strings.HasPrefix(refs[0], "refs/remotes/") {
		remote, branch, _ = strings.Cut(display, "/")
	} else {
		branch = display
	}
	return buildBranchRef{ref: refs[0], display: display, remote: remote, branch: branch}, true
}

// GetRepoRoot returns the git repository root for the gt source code.
// The canonical source is the gastown repo itself ($GT_ROOT/gastown).
// Crew rigs also contain cmd/gt/main.go but have different HEADs,
// so we prefer the gastown repo over CWD-based git toplevel detection.
func GetRepoRoot() (string, error) {
	// Check if GT_ROOT environment variable is set (agents always have this)
	if gtRoot := os.Getenv("GT_ROOT"); gtRoot != "" {
		candidates := []string{
			gtRoot + "/gastown",
			gtRoot + "/gastown/mayor/rig",
		}
		for _, candidate := range candidates {
			if hasGtSource(candidate) {
				return candidate, nil
			}
		}
	}

	// Try common development paths relative to home
	home := os.Getenv("HOME")
	if home != "" {
		candidates := []string{
			home + "/gt/gastown",
			home + "/gt/gastown/mayor/rig",
			home + "/gastown",
			home + "/gastown/mayor/rig",
			home + "/src/gastown",
			home + "/src/gastown/mayor/rig",
		}
		for _, candidate := range candidates {
			if hasGtSource(candidate) {
				return candidate, nil
			}
		}
	}

	// Fall back to current directory's git repo (may be a crew rig)
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	util.SetDetachedProcessGroup(cmd)
	if output, err := cmd.Output(); err == nil {
		root := strings.TrimSpace(string(output))
		if hasGtSource(root) {
			return root, nil
		}
	}

	return "", fmt.Errorf("cannot locate gt source repository")
}

// isGitRepo checks if a directory is a git repository.
func isGitRepo(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}

// hasGtSource checks if a directory contains the gt source code.
// We look for cmd/gt/main.go as the definitive marker.
func hasGtSource(dir string) bool {
	_, err := os.Stat(dir + "/cmd/gt/main.go")
	return err == nil
}

// onlyBeadsChanges checks whether all commits between binaryCommit and
// compareRef exclusively modify files under .beads/. Returns true if the diff
// contains no changes outside .beads/, meaning the binary is functionally
// up-to-date. Used to suppress false-positive stale warnings from bd backup
// commits. (GH#2596)
func onlyBeadsChanges(repoDir, binaryCommit, compareRef string) bool {
	// Get files changed between binary commit and the build ref, excluding
	// .beads/. If this produces no output, all changes are within .beads/
	cmd := exec.Command("git", "diff", "--name-only", binaryCommit+".."+compareRef, "--", ".", ":!.beads")
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	if err != nil {
		// Can't determine — be conservative, assume stale
		return false
	}
	return strings.TrimSpace(string(output)) == ""
}

// isBuildBranch returns true if the given branch is safe for automated rebuilds.
// Accepted branches:
//   - main, master: upstream default branches
//   - carry/*: fork operational branches (e.g., carry/operational)
//
// This prevents automated rebuilds from random feature, fix, or polecat branches
// which could cause downgrades or crash loops.
func isBuildBranch(branch string) bool {
	switch branch {
	case "main", "master":
		return true
	}
	return strings.HasPrefix(branch, "carry/")
}

// SetCommit allows the cmd package to pass in the build-time commit.
func SetCommit(commit string) {
	Commit = commit
}

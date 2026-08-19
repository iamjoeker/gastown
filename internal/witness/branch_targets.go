package witness

import (
	"slices"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/git"
)

// Target selection for containment questions — "is this branch's content in the
// trunk?" — shared by the branch sweep (gt-by1e) and the witness's landed-check
// (gt-e7dd), because both are wrong in the same expensive way if they pick one
// trunk on a rig that has two.

// BranchSweepRefResolver is the slice of git that target selection needs.
type BranchSweepRefResolver interface {
	RemoteDefaultBranch() string
	CleanBaseRef(remote, defaultBranch, target string) string
	RefExists(ref string) (bool, error)
	IsAncestor(ancestor, descendant string) (bool, error)
}

// ResolveComparisonTargets picks the refs a branch must be absent from before
// its work counts as unlanded.
//
// One trunk is the normal case and two is the fork case, and picking the wrong
// one of two is not a rounding error: on gastown, origin/main is 289 commits
// ahead of upstream/main, and comparing against upstream alone put six branches
// on the short list of which three had demonstrably landed. So when both refs
// exist, both are checked, and containment in either counts.
//
// A fully qualified explicit target is honoured exactly — a caller naming
// upstream/main is asking about upstream/main. A bare one names a BRANCH, so it
// expands the same way the default does.
//
// Candidates are ordered most-advanced first, so the ref quoted back to a reader
// is the one work actually lands on rather than whichever the fork-detection
// heuristic happens to prefer.
func ResolveComparisonTargets(g BranchSweepRefResolver, remote, explicit string) []string {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" && (strings.HasPrefix(explicit, "refs/") || git.RemoteForRef(explicit) != "") {
		return []string{explicit}
	}

	branch := strings.TrimSpace(g.RemoteDefaultBranch())
	if explicit != "" {
		branch = explicit
	}
	if branch == "" {
		branch = "main"
	}

	var candidates []string
	for _, candidate := range []string{remote + "/" + branch, "upstream/" + branch} {
		if slices.Contains(candidates, candidate) {
			continue
		}
		if ok, err := g.RefExists(candidate); err == nil && ok {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		// Nothing resolved. Fall back to the repo's own notion of a base rather
		// than inventing one, and let the caller report against it.
		if fallback := strings.TrimSpace(g.CleanBaseRef(remote, branch, "")); fallback != "" {
			return []string{fallback}
		}
		return nil
	}
	return orderTargetsByReach(g, candidates)
}

// orderTargetsByReach sorts candidates so that a ref containing another comes
// first. The count of other candidates a ref contains is a stable sort key, so
// this stays well-defined however many trunks a rig grows.
func orderTargetsByReach(g BranchSweepRefResolver, candidates []string) []string {
	if len(candidates) < 2 {
		return candidates
	}
	reach := make(map[string]int, len(candidates))
	for _, a := range candidates {
		for _, b := range candidates {
			if a == b {
				continue
			}
			if contained, err := g.IsAncestor(b, a); err == nil && contained {
				reach[a]++
			}
		}
	}
	ordered := append([]string(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return reach[ordered[i]] > reach[ordered[j]]
	})
	return ordered
}

// TargetRemotes lists the remotes that must be refreshed for these targets.
func TargetRemotes(targets []string, fallback string) []string {
	var remotes []string
	for _, target := range targets {
		name := git.RemoteForRef(target)
		if name == "" {
			name = fallback
		}
		if name != "" && !slices.Contains(remotes, name) {
			remotes = append(remotes, name)
		}
	}
	return remotes
}

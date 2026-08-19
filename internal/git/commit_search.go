package git

import (
	"fmt"
	"regexp"
	"strings"
)

// CommitRef is one commit found by a message search.
type CommitRef struct {
	SHA     string `json:"sha"`
	Date    string `json:"date"` // committer date, RFC3339
	Subject string `json:"subject"`
}

// commitTokenPattern restricts search tokens to the shape of a bead ID. The
// token is interpolated into a regular expression handed to `git log --grep`,
// so anything that could carry regex or option syntax is refused outright
// rather than escaped and hoped for.
var commitTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// SupportedCommitToken reports whether token can be searched for. Callers that
// sweep many IDs use it to separate an unsearchable ID — which is one bead's
// problem — from a git failure, which invalidates the whole sweep.
func SupportedCommitToken(token string) bool {
	return commitTokenPattern.MatchString(strings.TrimSpace(token))
}

// tokenBoundaryPattern builds the standalone-word matcher for token.
//
// Standalone matters: bead IDs are prefixes of one another (gt-60 is a prefix
// of gt-602), so a substring search reports the longer bead's commits as the
// shorter bead's work. The bound on each side is any character that cannot
// appear inside an ID, which still admits the two forms that carry them in
// practice — "fix(x): thing (gt-602)" and the merge subject
// "Merge polecat/settler/gt-602+mszje5tl into main".
func tokenBoundaryPattern(token string) string {
	return fmt.Sprintf(`(^|[^A-Za-z0-9._-])%s([^A-Za-z0-9._-]|$)`, regexp.QuoteMeta(token))
}

// CommitsWithSubjectToken returns commits reachable from ref whose *subject*
// names token as a standalone word, newest first.
//
// Subject-only is the point. Gas Town's convention puts the bead ID in the
// subject of the commit that does the work ("fix(x): thing (gt-602)") and in
// the merge subject of the branch that lands it, while the body is where
// cross-references live: follow-ups filed, related beads, prior art. Measured
// on gastown 2026-08-18, a whole-message search for 47 open beads returned 10
// hits of which 9 were body-only cross-references and exactly 1 was the bead's
// own landed work — so counting body mentions as attribution is wrong nine
// times in ten.
//
// Callers that intend to draw a conclusion from an empty result must fetch
// first. A commit that exists on the remote but not in this clone is invisible
// here, and reads exactly like work that was never done.
func (g *Git) CommitsWithSubjectToken(ref, token string, limit int) ([]CommitRef, error) {
	ref = strings.TrimSpace(ref)
	token = strings.TrimSpace(token)
	if ref == "" {
		return nil, fmt.Errorf("commit search: empty ref")
	}
	if !commitTokenPattern.MatchString(token) {
		return nil, fmt.Errorf("commit search: unsupported token %q", token)
	}

	pattern := tokenBoundaryPattern(token)

	// git --grep matches the whole message; it is used here only as a cheap
	// prefilter, and the subject test below is what decides. --max-count is
	// deliberately not passed: it would cut the list before the subject filter
	// runs, so a bead with many body mentions could lose its real commit.
	out, err := g.run(
		"log",
		"--extended-regexp",
		"--grep="+pattern,
		"--format=%H%x1f%cI%x1f%s",
		ref,
		"--",
	)
	if err != nil {
		return nil, fmt.Errorf("commit search for %s in %s: %w", token, ref, err)
	}

	subjectRE, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("commit search for %s: %w", token, err)
	}

	var commits []CommitRef
	for _, commit := range parseCommitRefs(out) {
		if !subjectRE.MatchString(commit.Subject) {
			continue
		}
		commits = append(commits, commit)
		if limit > 0 && len(commits) == limit {
			break
		}
	}
	return commits, nil
}

func parseCommitRefs(out string) []CommitRef {
	var commits []CommitRef
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		commits = append(commits, CommitRef{SHA: parts[0], Date: parts[1], Subject: parts[2]})
	}
	return commits
}

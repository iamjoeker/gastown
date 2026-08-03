package cmd

// Ledger integrity guards for `gt done` (gt-r5p).
//
// The Capability Ledger is the trust mechanism for Gas Town: it is meant to
// record what an agent did, not what it claimed. Two gaps let it record the
// opposite, with a plausible-looking commit SHA attached:
//
//  1. A polecat slung a code bead could close it with zero commits, and
//  2. --skip-verify recorded whatever HEAD pointed at as proof of work. In a
//     fresh polecat sandbox HEAD is origin/main — an unrelated contributor's
//     commit, often predating the bead by days.
//
// The rules below close both. They are pure functions so the refusal logic is
// testable without a git repo or a live beads database.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
)

// noMRCloseContext describes a `gt done` completion that closes the source
// bead without creating a merge request.
type noMRCloseContext struct {
	IssueID string
	// IsPolecat is true when running as a polecat (GT_POLECAT set). Polecats
	// are the agents whose own work the ledger is verifying.
	IsPolecat bool
	// IsNonCodeTask is true when the bead carries no_merge or review_only —
	// audits, reviews, research. Zero commits is expected for these.
	IsNonCodeTask bool
	// BranchPushedWithWork is true when real commits exist on the pushed
	// feature branch. The branch can be zero commits ahead of base while still
	// carrying work if base advanced after the push (GH#wd7).
	BranchPushedWithWork bool
	// SkipVerify is true when --skip-verify was passed.
	SkipVerify bool
}

// noMRCloseRefusal returns the reason a no-MR close must be refused, or "" when
// the close is legitimate.
//
// Two rules, both of which would have caught the gt-y20 false completion:
//
//   - --skip-verify is documented as an audit-only escape hatch for non-code
//     closes. Enforce that: on a code bead it is refused, not merely annotated.
//   - A polecat closing a code bead must have work somewhere. With no commits
//     ahead of base and nothing on the pushed branch, there is definitionally
//     none, so there is nothing to record and nothing to close.
func noMRCloseRefusal(c noMRCloseContext) string {
	if c.IsNonCodeTask {
		return ""
	}
	if c.SkipVerify {
		return fmt.Sprintf("--skip-verify is an audit-only escape hatch for non-code closes; %s is a code bead "+
			"(no no_merge/review_only flag), so its completion must be verified against a real pushed commit", c.IssueID)
	}
	if c.IsPolecat && !c.BranchPushedWithWork {
		return fmt.Sprintf("cannot close code bead %s with no work: no commits ahead of base and no commits on the pushed branch. "+
			"HEAD is just the base ref, so there is no commit that proves this work was done", c.IssueID)
	}
	return ""
}

// ledgerProofRejection returns the reason a commit must not be recorded on a
// bead as proof of work, or "" when recording it is safe.
//
// A commit only proves this agent's work if this agent produced it and it
// exists because of the sling. Either check alone would have rejected the
// upstream merge commit recorded against gt-y20.
func ledgerProofRejection(info *git.CommitIdentity, slungAt time.Time, identities []string) string {
	if info == nil || strings.TrimSpace(info.SHA) == "" {
		return "no commit metadata available"
	}
	if !slungAt.IsZero() && info.CommittedAt.Before(slungAt) {
		return fmt.Sprintf("commit %s dates from %s, before the bead was slung at %s",
			shortSHA(info.SHA), info.CommittedAt.UTC().Format(time.RFC3339), slungAt.UTC().Format(time.RFC3339))
	}
	if len(identities) > 0 && !commitAuthoredBy(info, identities) {
		return fmt.Sprintf("commit %s was authored by %q, not by the closing agent (%s)",
			shortSHA(info.SHA), info.AuthorName, strings.Join(identities, ", "))
	}
	return ""
}

// commitAuthoredBy reports whether any of the given agent identities appears in
// the commit's author or committer fields. Agents commit under a bare name
// ("guzzle") or a full role path ("gastown/polecats/guzzle") depending on how
// the sandbox was configured, so both forms are accepted.
func commitAuthoredBy(info *git.CommitIdentity, identities []string) bool {
	fields := []string{info.AuthorName, info.AuthorEmail, info.CommitterName, info.CommitterEmail}
	for _, identity := range identities {
		for _, alias := range identityAliases(identity) {
			for _, field := range fields {
				field = strings.ToLower(strings.TrimSpace(field))
				if field == "" {
					continue
				}
				if field == alias || strings.Contains(field, alias) {
					return true
				}
			}
		}
	}
	return false
}

// identityAliases expands an agent identity into the lowercased forms a commit
// may carry: the full value plus its final path segment. Segments shorter than
// three characters are dropped — they match too much to be evidence.
func identityAliases(identity string) []string {
	identity = strings.ToLower(strings.TrimSpace(identity))
	if len(identity) < 3 {
		return nil
	}
	aliases := []string{identity}
	if idx := strings.LastIndex(identity, "/"); idx >= 0 && idx < len(identity)-1 {
		if segment := identity[idx+1:]; len(segment) >= 3 {
			aliases = append(aliases, segment)
		}
	}
	return aliases
}

// closingAgentIdentities returns the names under which the agent running
// `gt done` may legitimately have authored a commit. Empty means identity
// cannot be established, in which case the author check is skipped rather than
// failing every close in an unconfigured environment.
func closingAgentIdentities() []string {
	seen := map[string]bool{}
	var identities []string
	for _, key := range []string{"GIT_AUTHOR_NAME", "BD_ACTOR", "GT_POLECAT", "GT_CREW", "GT_ROLE"} {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" || seen[strings.ToLower(value)] {
			continue
		}
		seen[strings.ToLower(value)] = true
		identities = append(identities, value)
	}
	return identities
}

// issueSlungAt returns the time the issue was attached to an agent's hook, or
// the zero time when the bead carries no attachment timestamp.
func issueSlungAt(issue *beads.Issue) time.Time {
	fields := beads.ParseAttachmentFields(issue)
	if fields == nil {
		return time.Time{}
	}
	stamp := strings.TrimSpace(fields.AttachedAt)
	if stamp == "" {
		return time.Time{}
	}
	slungAt, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return time.Time{}
	}
	return slungAt
}

func shortSHA(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

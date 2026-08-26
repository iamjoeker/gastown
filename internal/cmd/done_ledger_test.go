package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/git"
)

// stubLedgerWrites replaces the ledger write seams for the duration of a test
// and returns the annotations that reached them.
func stubLedgerWrites(t *testing.T, commentErr, updateErr error) *[]string {
	t.Helper()
	origComment, origUpdate := ledgerAddComment, ledgerUpdateIssue
	t.Cleanup(func() {
		ledgerAddComment, ledgerUpdateIssue = origComment, origUpdate
	})

	var written []string
	ledgerAddComment = func(_ *beads.Beads, _, msg string) error {
		written = append(written, msg)
		return commentErr
	}
	ledgerUpdateIssue = func(_ *beads.Beads, _ string, _ beads.UpdateOptions) error {
		return updateErr
	}
	return &written
}

// The gt-290c hazard: noteVerifiedPushSkipped writes the only durable record
// that a completion bypassed verified-push checks (the DONE_SKIP_VERIFY
// witness mail is a wisp and gets reaped). Dropping that write silently left
// the close with no audit trail at all.
func TestNoteVerifiedPushSkipped_LostAnnotationIsVisibleAndFails(t *testing.T) {
	writeErr := errors.New("dolt: connection refused")
	stubLedgerWrites(t, writeErr, nil)

	var err error
	stderr := captureStderr(t, func() {
		// commit "" and a nil git context keep this away from a real repo: the
		// annotation still has to be written, which is the point.
		err = noteVerifiedPushSkipped(nil, &beads.Beads{}, nil, t.TempDir(), "gt-290c", "main", "", "--skip-verify on no-MR non-code close")
	})

	if err == nil {
		t.Fatal("expected a lost skip-verify annotation to return an error, got nil")
	}
	if !errors.Is(err, writeErr) {
		t.Errorf("returned error should wrap the write failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "gt-290c") {
		t.Errorf("returned error should name the bead, got: %v", err)
	}
	for _, want := range []string{"gt-290c", "LEDGER ANNOTATION LOST", "verified_push_skipped:", "bd comments add"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr should contain %q so the lost record is recoverable, got:\n%s", want, stderr)
		}
	}
}

func TestNoteVerifiedPushSkipped_SuccessfulAnnotationIsSilent(t *testing.T) {
	written := stubLedgerWrites(t, nil, nil)

	var err error
	stderr := captureStderr(t, func() {
		err = noteVerifiedPushSkipped(nil, &beads.Beads{}, nil, t.TempDir(), "gt-290c", "main", "", "--skip-verify on branch push")
	})

	if err != nil {
		t.Fatalf("expected a successful annotation to return nil, got: %v", err)
	}
	if len(*written) != 1 || !strings.Contains((*written)[0], "verified_push_skipped:") {
		t.Errorf("expected one verified_push_skipped annotation, got: %v", *written)
	}
	if strings.Contains(stderr, "LEDGER ANNOTATION LOST") {
		t.Errorf("a successful write should not report a lost annotation, got:\n%s", stderr)
	}
}

func TestNoteVerifiedPushSkipped_NoIssueWritesNothing(t *testing.T) {
	written := stubLedgerWrites(t, errors.New("must not be called"), nil)

	if err := noteVerifiedPushSkipped(nil, &beads.Beads{}, nil, t.TempDir(), "", "main", "", "reason"); err != nil {
		t.Fatalf("expected no error with an empty issue ID, got: %v", err)
	}
	if len(*written) != 0 {
		t.Errorf("expected no annotation write with an empty issue ID, got: %v", *written)
	}
}

func TestNoteVerifiedPushFailure_LostWritesAreReported(t *testing.T) {
	tests := []struct {
		name       string
		commentErr error
		updateErr  error
		wantErr    bool
		wantIn     []string
	}{
		{
			name:    "both writes land",
			wantErr: false,
		},
		{
			name:       "lost comment is reported",
			commentErr: errors.New("dolt: connection refused"),
			wantErr:    true,
			wantIn:     []string{"comment write failed"},
		},
		{
			name:      "lost status update is reported even when the comment lands",
			updateErr: errors.New("schema gate closed"),
			wantErr:   true,
			wantIn:    []string{"in_progress"},
		},
		{
			name:       "both losses are reported together",
			commentErr: errors.New("dolt: connection refused"),
			updateErr:  errors.New("schema gate closed"),
			wantErr:    true,
			wantIn:     []string{"comment write failed", "in_progress"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubLedgerWrites(t, tt.commentErr, tt.updateErr)

			var err error
			stderr := captureStderr(t, func() {
				err = noteVerifiedPushFailure(&beads.Beads{}, t.TempDir(), "gt-290c", "main", "abc123", errors.New("commit not on origin"))
			})

			if tt.wantErr && err == nil {
				t.Fatal("expected a lost ledger write to return an error, got nil")
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("expected nil when both writes land, got: %v", err)
				}
				if strings.Contains(stderr, "LEDGER ANNOTATION LOST") {
					t.Errorf("successful writes should stay quiet, got:\n%s", stderr)
				}
				return
			}
			if !strings.Contains(stderr, "verified_push_failed:") {
				t.Errorf("stderr should echo the lost annotation, got:\n%s", stderr)
			}
			for _, want := range tt.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error should mention %q, got: %v", want, err)
				}
			}
		})
	}
}

func TestReportLostLedgerAnnotation_NoWriteErrorIsSilent(t *testing.T) {
	var err error
	stderr := captureStderr(t, func() {
		err = reportLostLedgerAnnotation("gt-290c", "verified_push_skipped: ...", nil)
	})
	if err != nil {
		t.Errorf("expected nil for a successful write, got: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected no output for a successful write, got:\n%s", stderr)
	}
}

// The gt-y20 incident: a polecat in a fresh sandbox (HEAD == origin/main, zero
// commits, no branch) closed a P1 code bead with --skip-verify, and the ledger
// recorded an upstream contributor's merge commit from ten days earlier as
// proof of work.
func TestNoMRCloseRefusal_SkipVerifyOnCodeBeadIsRefused(t *testing.T) {
	refusal := noMRCloseRefusal(noMRCloseContext{
		IssueID:              "gt-y20",
		IsPolecat:            true,
		IsNonCodeTask:        false,
		BranchPushedWithWork: false,
		SkipVerify:           true,
	})
	if refusal == "" {
		t.Fatal("expected --skip-verify on a code bead with no work to be refused")
	}
	if !strings.Contains(refusal, "gt-y20") {
		t.Errorf("refusal should name the bead, got: %s", refusal)
	}
}

func TestNoMRCloseRefusal(t *testing.T) {
	tests := []struct {
		name    string
		ctx     noMRCloseContext
		refused bool
	}{
		{
			name:    "polecat code bead with no work is refused",
			ctx:     noMRCloseContext{IssueID: "gt-1", IsPolecat: true},
			refused: true,
		},
		{
			name:    "skip-verify on a code bead is refused even when work was pushed",
			ctx:     noMRCloseContext{IssueID: "gt-1", IsPolecat: true, BranchPushedWithWork: true, SkipVerify: true},
			refused: true,
		},
		{
			name:    "skip-verify on a code bead is refused for non-polecats too",
			ctx:     noMRCloseContext{IssueID: "gt-1", SkipVerify: true},
			refused: true,
		},
		{
			name:    "non-code bead closes with skip-verify",
			ctx:     noMRCloseContext{IssueID: "gt-1", IsPolecat: true, IsNonCodeTask: true, SkipVerify: true},
			refused: false,
		},
		{
			name:    "non-code bead closes without skip-verify",
			ctx:     noMRCloseContext{IssueID: "gt-1", IsPolecat: true, IsNonCodeTask: true},
			refused: false,
		},
		{
			name:    "polecat code bead with work on the pushed branch closes",
			ctx:     noMRCloseContext{IssueID: "gt-1", IsPolecat: true, BranchPushedWithWork: true},
			refused: false,
		},
		{
			name:    "non-polecat code bead is not blocked by the zero-work rule",
			ctx:     noMRCloseContext{IssueID: "gt-1"},
			refused: false,
		},
		{
			// gt-7k3q: a sibling landed the work first, so the polecat has no
			// commits by design. The ledger points at the landing commit on the
			// target, which is better evidence than this branch could offer.
			name:    "polecat code bead superseded by work on the target closes",
			ctx:     noMRCloseContext{IssueID: "gt-1", IsPolecat: true, WorkLandedOnTarget: true},
			refused: false,
		},
		{
			// The landed evidence answers the no-work rule, not the
			// --skip-verify one. --skip-verify stays an audit-only escape hatch
			// for non-code closes, and a superseded code bead does not need it.
			name:    "skip-verify on a superseded code bead is still refused",
			ctx:     noMRCloseContext{IssueID: "gt-1", IsPolecat: true, WorkLandedOnTarget: true, SkipVerify: true},
			refused: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refusal := noMRCloseRefusal(tt.ctx)
			if tt.refused && refusal == "" {
				t.Error("expected refusal, got none")
			}
			if !tt.refused && refusal != "" {
				t.Errorf("expected close to be allowed, got refusal: %s", refusal)
			}
		})
	}
}

func TestLedgerProofRejection(t *testing.T) {
	slungAt := time.Date(2026, 8, 2, 23, 22, 9, 0, time.UTC)

	tests := []struct {
		name       string
		info       *git.CommitIdentity
		identities []string
		rejected   bool
	}{
		{
			name:       "nil commit metadata is rejected",
			info:       nil,
			identities: []string{"guzzle"},
			rejected:   true,
		},
		{
			name:       "empty SHA is rejected",
			info:       &git.CommitIdentity{SHA: "  "},
			identities: []string{"guzzle"},
			rejected:   true,
		},
		{
			// The gt-y20 commit: an upstream merge dated ten days before the sling.
			name: "commit predating the sling is rejected",
			info: &git.CommitIdentity{
				SHA:           "649b832b7672bc7a2dbef26f5983aba6198b819b",
				AuthorName:    "Bella-Giraffety",
				CommitterName: "Bella-Giraffety",
				CommittedAt:   time.Date(2026, 7, 23, 9, 3, 2, 0, time.UTC),
			},
			identities: []string{"guzzle"},
			rejected:   true,
		},
		{
			name: "commit by another author is rejected",
			info: &git.CommitIdentity{
				SHA:           "abc123def456",
				AuthorName:    "Bella-Giraffety",
				CommitterName: "Bella-Giraffety",
				CommittedAt:   slungAt.Add(time.Hour),
			},
			identities: []string{"guzzle", "gastown/polecats/guzzle"},
			rejected:   true,
		},
		{
			name: "commit by the closing agent after the sling is accepted",
			info: &git.CommitIdentity{
				SHA:           "abc123def456",
				AuthorName:    "guzzle",
				CommitterName: "guzzle",
				CommittedAt:   slungAt.Add(time.Hour),
			},
			identities: []string{"guzzle"},
			rejected:   false,
		},
		{
			name: "full role path in the author field is accepted",
			info: &git.CommitIdentity{
				SHA:           "abc123def456",
				AuthorName:    "gastown/polecats/guzzle",
				CommitterName: "gastown/polecats/guzzle",
				CommittedAt:   slungAt.Add(time.Hour),
			},
			identities: []string{"gastown/polecats/guzzle"},
			rejected:   false,
		},
		{
			name: "bare agent name matches a full role path identity",
			info: &git.CommitIdentity{
				SHA:           "abc123def456",
				AuthorName:    "guzzle",
				CommitterName: "guzzle",
				CommittedAt:   slungAt.Add(time.Hour),
			},
			identities: []string{"gastown/polecats/guzzle"},
			rejected:   false,
		},
		{
			name: "unknown identity skips the author check rather than failing",
			info: &git.CommitIdentity{
				SHA:           "abc123def456",
				AuthorName:    "Someone Else",
				CommitterName: "Someone Else",
				CommittedAt:   slungAt.Add(time.Hour),
			},
			identities: nil,
			rejected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rejection := ledgerProofRejection(tt.info, slungAt, tt.identities)
			if tt.rejected && rejection == "" {
				t.Error("expected the commit to be rejected as proof of work, got none")
			}
			if !tt.rejected && rejection != "" {
				t.Errorf("expected the commit to be accepted, got rejection: %s", rejection)
			}
		})
	}
}

func TestLedgerProofRejection_NoSlingTimeSkipsDateCheck(t *testing.T) {
	info := &git.CommitIdentity{
		SHA:           "abc123def456",
		AuthorName:    "guzzle",
		CommitterName: "guzzle",
		CommittedAt:   time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if rejection := ledgerProofRejection(info, time.Time{}, []string{"guzzle"}); rejection != "" {
		t.Errorf("expected no rejection when the bead has no sling timestamp, got: %s", rejection)
	}
}

func TestIssueSlungAt(t *testing.T) {
	tests := []struct {
		name  string
		issue *beads.Issue
		want  time.Time
	}{
		{
			name:  "nil issue has no sling time",
			issue: nil,
		},
		{
			name:  "issue without attachment fields has no sling time",
			issue: &beads.Issue{Description: "just a description"},
		},
		{
			name:  "unparseable timestamp has no sling time",
			issue: &beads.Issue{Description: "attached_at: yesterday\nattached_molecule: gt-wisp-1"},
		},
		{
			name:  "RFC3339Nano timestamp is parsed",
			issue: &beads.Issue{Description: "attached_at: 2026-08-02T23:22:09.429807506Z\nattached_molecule: gt-wisp-tn0"},
			want:  time.Date(2026, 8, 2, 23, 22, 9, 429807506, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := issueSlungAt(tt.issue)
			if !got.Equal(tt.want) {
				t.Errorf("issueSlungAt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIdentityAliases(t *testing.T) {
	tests := []struct {
		identity string
		want     []string
	}{
		{"gastown/polecats/guzzle", []string{"gastown/polecats/guzzle", "guzzle"}},
		{"guzzle", []string{"guzzle"}},
		{"GUZZLE", []string{"guzzle"}},
		// Too short to be evidence — matches far too much.
		{"ab", nil},
		{"rig/ab", []string{"rig/ab"}},
		{"", nil},
	}

	for _, tt := range tests {
		t.Run(tt.identity, func(t *testing.T) {
			got := identityAliases(tt.identity)
			if len(got) != len(tt.want) {
				t.Fatalf("identityAliases(%q) = %v, want %v", tt.identity, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("identityAliases(%q)[%d] = %q, want %q", tt.identity, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestClosingAgentIdentities(t *testing.T) {
	for _, key := range []string{"GIT_AUTHOR_NAME", "BD_ACTOR", "GT_POLECAT", "GT_CREW", "GT_ROLE"} {
		t.Setenv(key, "")
	}
	t.Setenv("GIT_AUTHOR_NAME", "guzzle")
	t.Setenv("BD_ACTOR", "gastown/polecats/guzzle")
	t.Setenv("GT_POLECAT", "guzzle") // duplicate of GIT_AUTHOR_NAME, deduped

	got := closingAgentIdentities()
	want := []string{"guzzle", "gastown/polecats/guzzle"}
	if len(got) != len(want) {
		t.Fatalf("closingAgentIdentities() = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("closingAgentIdentities()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

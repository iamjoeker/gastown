package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

type fakeSupersededProbe struct {
	commits  []git.CommitRef
	err      error
	gotRef   string
	gotToken string
	calls    int
}

func (f *fakeSupersededProbe) CommitsWithSubjectToken(ref, token string, limit int) ([]git.CommitRef, error) {
	f.calls++
	f.gotRef = ref
	f.gotToken = token
	if f.err != nil {
		return nil, f.err
	}
	if limit > 0 && len(f.commits) > limit {
		return f.commits[:limit], nil
	}
	return f.commits, nil
}

func TestAssessSupersededWork(t *testing.T) {
	landing := git.CommitRef{SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Subject: "fix(testenv): guard production dolt (bd-4xn)"}
	revert := git.CommitRef{SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Subject: "Revert \"fix(testenv): guard production dolt (bd-4xn)\""}

	tests := []struct {
		name       string
		probe      *fakeSupersededProbe
		nilProbe   bool
		req        supersededRequest
		wantLanded bool
		wantSHA    string
		reasonHas  string
	}{
		{
			name:       "landing commit on target proves the work landed",
			probe:      &fakeSupersededProbe{commits: []git.CommitRef{landing}},
			req:        supersededRequest{IssueID: "bd-4xn", BaseRef: "origin/main"},
			wantLanded: true,
			wantSHA:    landing.SHA,
		},
		{
			name:       "a revert on top is skipped for the commit beneath it",
			probe:      &fakeSupersededProbe{commits: []git.CommitRef{revert, landing}},
			req:        supersededRequest{IssueID: "bd-4xn", BaseRef: "origin/main"},
			wantLanded: true,
			wantSHA:    landing.SHA,
		},
		{
			name:      "a revert alone does not prove landing",
			probe:     &fakeSupersededProbe{commits: []git.CommitRef{revert}},
			req:       supersededRequest{IssueID: "bd-4xn", BaseRef: "origin/main"},
			reasonHas: "revert",
		},
		{
			name:      "no commit names the bead",
			probe:     &fakeSupersededProbe{},
			req:       supersededRequest{IssueID: "bd-4xn", BaseRef: "origin/main"},
			reasonHas: "no evidence the work landed",
		},
		{
			name:      "a probe error refuses rather than concluding absence",
			probe:     &fakeSupersededProbe{err: errors.New("bad revision")},
			req:       supersededRequest{IssueID: "bd-4xn", BaseRef: "origin/main"},
			reasonHas: "could not search",
		},
		{
			name:      "empty commit sha is not evidence",
			probe:     &fakeSupersededProbe{commits: []git.CommitRef{{Subject: "fix (bd-4xn)"}}},
			req:       supersededRequest{IssueID: "bd-4xn", BaseRef: "origin/main"},
			reasonHas: "no evidence the work landed",
		},
		{
			name:      "missing issue id",
			probe:     &fakeSupersededProbe{commits: []git.CommitRef{landing}},
			req:       supersededRequest{BaseRef: "origin/main"},
			reasonHas: "no source issue",
		},
		{
			name:      "missing base ref",
			probe:     &fakeSupersededProbe{commits: []git.CommitRef{landing}},
			req:       supersededRequest{IssueID: "bd-4xn"},
			reasonHas: "no target ref",
		},
		{
			name:      "nil probe",
			nilProbe:  true,
			req:       supersededRequest{IssueID: "bd-4xn", BaseRef: "origin/main"},
			reasonHas: "no git client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got supersededVerdict
			if tt.nilProbe {
				got = assessSupersededWork(nil, tt.req)
			} else {
				got = assessSupersededWork(tt.probe, tt.req)
			}
			if got.Landed != tt.wantLanded {
				t.Fatalf("Landed = %v, want %v (reason: %s)", got.Landed, tt.wantLanded, got.Reason)
			}
			if tt.wantLanded {
				if got.Commit.SHA != tt.wantSHA {
					t.Errorf("Commit.SHA = %q, want %q", got.Commit.SHA, tt.wantSHA)
				}
				if got.Reason != "" {
					t.Errorf("Reason = %q, want empty on a landed verdict", got.Reason)
				}
				return
			}
			if got.Reason == "" {
				t.Fatal("Reason is empty on a refusal; the polecat is left with no way out again")
			}
			if !strings.Contains(got.Reason, tt.reasonHas) {
				t.Errorf("Reason = %q, want it to contain %q", got.Reason, tt.reasonHas)
			}
		})
	}
}

// The probe must be aimed at the target ref, not at HEAD. HEAD in a
// zero-commits-ahead sandbox is the base ref's own tip, so searching it would
// find the sibling's commit even when the polecat's branch has diverged from
// the target — and would answer a question nobody asked.
func TestAssessSupersededWorkSearchesTheTargetRef(t *testing.T) {
	probe := &fakeSupersededProbe{}
	assessSupersededWork(probe, supersededRequest{IssueID: "gt-7k3q", BaseRef: "upstream/main"})
	if probe.gotRef != "upstream/main" {
		t.Errorf("searched ref %q, want %q", probe.gotRef, "upstream/main")
	}
	if probe.gotToken != "gt-7k3q" {
		t.Errorf("searched token %q, want %q", probe.gotToken, "gt-7k3q")
	}
}

func TestIsRevertSubject(t *testing.T) {
	tests := map[string]bool{
		`Revert "fix(x): thing (gt-1)"`: true,
		"revert: fix(x): thing (gt-1)":  true,
		"revert(done): thing (gt-1)":    true,
		"REVERT \"thing (gt-1)\"":       true,
		"fix(x): thing (gt-1)":          false,
		"feat: add revert support":      false,
		"":                              false,
	}
	for subject, want := range tests {
		if got := isRevertSubject(subject); got != want {
			t.Errorf("isRevertSubject(%q) = %v, want %v", subject, got, want)
		}
	}
}

// The close reason is the only durable record that this bead was closed over
// somebody else's commit. It has to name that commit.
func TestSupersededCloseReasonRecordsTheEvidence(t *testing.T) {
	req := supersededRequest{IssueID: "bd-4xn", BaseRef: "origin/main"}
	v := supersededVerdict{
		Landed: true,
		Commit: git.CommitRef{SHA: "cafebabe00000000000000000000000000000000", Subject: "fix(testenv): guard production dolt (bd-4xn)"},
	}
	got := supersededCloseReason(req, v)
	for _, want := range []string{
		"superseded: true",
		"commit_sha: cafebabe00000000000000000000000000000000",
		"target_branch: origin/main",
		"fix(testenv): guard production dolt (bd-4xn)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("close reason %q is missing %q", got, want)
		}
	}
}

// Every refusal must name a next action. gt-7k3q's polecat closed a bead by
// hand precisely because the errors described the state and not the way out.
func TestSupersededRefusalHintNamesTheWayOut(t *testing.T) {
	hint := supersededRefusalHint(supersededVerdict{Reason: "no commit reachable from origin/main has bd-4xn in its subject"})
	for _, want := range []string{"no commit reachable", "Do NOT close the bead by hand", "gt done --status ESCALATED"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint %q is missing %q", hint, want)
		}
	}
}

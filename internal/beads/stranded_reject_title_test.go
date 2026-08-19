package beads

import "testing"

func TestStrandedRejectTitleIsDeterministic(t *testing.T) {
	a := StrandedRejectTitle("gt-wisp-0an54", "gt-aqk")
	b := StrandedRejectTitle("gt-wisp-0an54", "gt-aqk")
	if a != b {
		t.Fatalf("title is not deterministic: %q vs %q", a, b)
	}
	if StrandedRejectTitle("gt-wisp-0an54", "gt-aqk") == StrandedRejectTitle("gt-wisp-0an54", "gt-other") {
		t.Fatalf("titles for different source issues collide")
	}
}

// The sweep recognises reports the refinery filed. If the constructor and the
// recogniser ever disagree, an already-reported stranding gets reported twice.
func TestIsStrandedRejectTitleForMatchesWhatWeWrite(t *testing.T) {
	title := StrandedRejectTitle("gt-wisp-mr1", "gt-1jrl")
	if !IsStrandedRejectTitleFor(title, "gt-1jrl") {
		t.Fatalf("%q not recognised as a report for gt-1jrl", title)
	}
}

func TestIsStrandedRejectTitleForRejectsNearMisses(t *testing.T) {
	cases := []struct {
		name  string
		title string
		issue string
		want  bool
	}{
		{
			name:  "different source issue",
			title: StrandedRejectTitle("gt-wisp-mr1", "gt-other"),
			issue: "gt-1jrl",
		},
		{
			// gt-abc must not be silenced by a report about gt-abcdef.
			name:  "issue id is a prefix of the reported one",
			title: StrandedRejectTitle("gt-wisp-mr1", "gt-abcdef"),
			issue: "gt-abc",
		},
		{
			name:  "unrelated bead that happens to name the issue",
			title: "Flaky test in gt-1jrl needs a rerun",
			issue: "gt-1jrl",
		},
		{
			name:  "empty title",
			title: "",
			issue: "gt-1jrl",
		},
		{
			name:  "empty issue",
			title: StrandedRejectTitle("gt-wisp-mr1", "gt-1jrl"),
			issue: "",
		},
		{
			name:  "match",
			title: StrandedRejectTitle("gt-wisp-mr1", "gt-1jrl"),
			issue: "gt-1jrl",
			want:  true,
		},
		{
			name:  "match tolerates surrounding whitespace",
			title: "  " + StrandedRejectTitle("gt-wisp-mr1", "gt-1jrl") + "  ",
			issue: " gt-1jrl ",
			want:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsStrandedRejectTitleFor(tc.title, tc.issue); got != tc.want {
				t.Fatalf("IsStrandedRejectTitleFor(%q, %q) = %v, want %v", tc.title, tc.issue, got, tc.want)
			}
		})
	}
}

// The prefix is what a human greps for, so it must stay on the front of the
// title even if the rest of the sentence is reworded.
func TestStrandedRejectTitleCarriesThePrefix(t *testing.T) {
	title := StrandedRejectTitle("gt-wisp-mr1", "gt-1jrl")
	if len(title) < len(StrandedRejectTitlePrefix) || title[:len(StrandedRejectTitlePrefix)] != StrandedRejectTitlePrefix {
		t.Fatalf("title %q does not start with %q", title, StrandedRejectTitlePrefix)
	}
}

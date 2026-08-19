package scratchpad

import (
	"testing"
	"time"
)

var testNow = time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

// deadSession is a scratchpad that every rule agrees is dead: born long ago,
// quiet for a day, in a project no live process touches. Each test below
// perturbs exactly one fact so a failure names the rule that broke.
func deadSession() Session {
	return Session{
		ProjectSlug: "-home-u-src-rig-polecats-dust",
		ID:          "360031bc-da3a-42ab-af49-8f5027879679",
		Path:        "/tmp/claude-1000/-home-u-src-rig-polecats-dust/360031bc-da3a-42ab-af49-8f5027879679",
		Birth:       testNow.Add(-48 * time.Hour),
		BirthKnown:  true,
		LastWrite:   testNow.Add(-24 * time.Hour),
		Bytes:       1024,
	}
}

func classifyOneForTest(t *testing.T, s Session, procs []Process, transcripts map[string]time.Time) Decision {
	t.Helper()
	got := Classify([]Session{s}, procs, transcripts, DefaultPolicy(), testNow)
	if len(got) != 1 {
		t.Fatalf("Classify returned %d decisions, want 1", len(got))
	}
	return got[0]
}

func TestClassify(t *testing.T) {
	sameSlug := "-home-u-src-rig-polecats-dust"

	tests := []struct {
		name        string
		session     func(s Session) Session
		procs       []Process
		transcripts map[string]time.Time
		want        Verdict
	}{
		{
			name:    "dead session with nothing running",
			session: func(s Session) Session { return s },
			want:    VerdictSweep,
		},
		{
			name: "birth time unavailable",
			session: func(s Session) Session {
				s.BirthKnown = false
				return s
			},
			want: VerdictKeep,
		},
		{
			name:    "live process in the same project started before the session was created",
			session: func(s Session) Session { return s },
			procs: []Process{{
				PID:   100,
				Start: testNow.Add(-72 * time.Hour),
				Cwd:   "/home/u/src/rig/polecats/dust",
				Slugs: []string{sameSlug},
			}},
			want: VerdictKeep,
		},
		{
			name:    "live process in the same project started after the session was created",
			session: func(s Session) Session { return s },
			procs: []Process{{
				PID:   100,
				Start: testNow.Add(-1 * time.Hour),
				Cwd:   "/home/u/src/rig/polecats/dust",
				Slugs: []string{sameSlug},
			}},
			want: VerdictSweep,
		},
		{
			name:    "live process in another project cannot protect it",
			session: func(s Session) Session { return s },
			procs: []Process{{
				PID:   100,
				Start: testNow.Add(-72 * time.Hour),
				Cwd:   "/home/u/src/rig/polecats/chrome",
				Slugs: []string{"-home-u-src-rig-polecats-chrome"},
			}},
			want: VerdictSweep,
		},
		{
			name:    "live process working inside the scratchpad",
			session: func(s Session) Session { return s },
			procs: []Process{{
				PID:   100,
				Start: testNow.Add(-1 * time.Minute),
				Cwd:   "/tmp/claude-1000/-home-u-src-rig-polecats-dust/360031bc-da3a-42ab-af49-8f5027879679/scratchpad",
				Slugs: []string{"-tmp-claude-1000--home-u-src-rig-polecats-dust-360031bc-da3a-42ab-af49-8f5027879679-scratchpad"},
			}},
			want: VerdictKeep,
		},
		{
			name:    "process with an unreadable working directory shadows every project",
			session: func(s Session) Session { return s },
			procs: []Process{{
				PID:   100,
				Start: testNow.Add(-72 * time.Hour),
			}},
			want: VerdictKeep,
		},
		{
			name:    "process with an unreadable working directory started after the session",
			session: func(s Session) Session { return s },
			procs: []Process{{
				PID:   100,
				Start: testNow.Add(-1 * time.Hour),
			}},
			want: VerdictSweep,
		},
		{
			name: "younger than the forensic floor",
			session: func(s Session) Session {
				s.Birth = testNow.Add(-30 * time.Minute)
				s.LastWrite = testNow.Add(-25 * time.Minute)
				return s
			},
			want: VerdictKeep,
		},
		{
			name: "written inside the idle window",
			session: func(s Session) Session {
				s.LastWrite = testNow.Add(-30 * time.Minute)
				return s
			},
			want: VerdictKeep,
		},
		{
			name:        "resumed session whose transcript is still moving",
			session:     func(s Session) Session { return s },
			transcripts: map[string]time.Time{deadSession().ID: testNow.Add(-5 * time.Minute)},
			want:        VerdictKeep,
		},
		{
			name:        "transcript quiet as long as the scratchpad",
			session:     func(s Session) Session { return s },
			transcripts: map[string]time.Time{deadSession().ID: testNow.Add(-24 * time.Hour)},
			want:        VerdictSweep,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyOneForTest(t, tc.session(deadSession()), tc.procs, tc.transcripts)
			if got.Verdict != tc.want {
				t.Fatalf("verdict = %q (%s), want %q", got.Verdict, got.Reason, tc.want)
			}
		})
	}
}

// TestClassifyResumedSession covers `claude --resume`: the process adopts a
// session id created long before it started, so the birth-to-start rule sees an
// orphan. What gives it away is the transcript moving after the process began.
func TestClassifyResumedSession(t *testing.T) {
	s := deadSession() // born 48h ago, scratchpad quiet for 24h
	procStart := testNow.Add(-6 * time.Hour)
	procs := []Process{{
		PID:   100,
		Start: procStart,
		Cwd:   "/home/u/src/rig/polecats/dust",
		Slugs: []string{s.ProjectSlug},
	}}

	// The resumed session has been thinking for hours without touching its
	// scratchpad, so only the transcript shows it is alive — and it is stale
	// enough that the plain idle check in rule 6 would not save it either.
	resumed := map[string]time.Time{s.ID: procStart.Add(3 * time.Hour)}
	if got := classifyOneForTest(t, s, procs, resumed); got.Verdict != VerdictKeep {
		t.Errorf("resumed session verdict = %q (%s), want keep", got.Verdict, got.Reason)
	}

	// Control: the same transcript, last written before that process existed,
	// belongs to the session that has already exited.
	stale := map[string]time.Time{s.ID: procStart.Add(-1 * time.Hour)}
	if got := classifyOneForTest(t, s, procs, stale); got.Verdict != VerdictSweep {
		t.Errorf("exited session verdict = %q (%s), want sweep — a transcript older than every live process proves nothing is appending to it",
			got.Verdict, got.Reason)
	}
}

// TestClassifyStartSlack covers the jitter in the process start time: it comes
// from second-resolution elapsed time, so a directory born a moment before its
// own process appears to start must still be treated as that process's.
func TestClassifyStartSlack(t *testing.T) {
	s := deadSession()
	s.Birth = testNow.Add(-3 * time.Hour)
	procs := []Process{{
		PID:   100,
		Start: s.Birth.Add(2 * time.Minute), // within the 5m slack
		Slugs: []string{s.ProjectSlug},
	}}
	if got := classifyOneForTest(t, s, procs, nil); got.Verdict != VerdictKeep {
		t.Fatalf("verdict = %q (%s), want keep — a directory born inside the start slack belongs to that process",
			got.Verdict, got.Reason)
	}
}

// TestClassifyOneLiveProcessDoesNotProtectOthers is the control for the
// protection rules: with a live process present, the sessions it cannot own
// must still be swept. Without this, a rule that protected everything whenever
// any process was running would pass every other test in this file.
func TestClassifyOneLiveProcessDoesNotProtectOthers(t *testing.T) {
	live := deadSession()
	live.Birth = testNow.Add(-30 * time.Minute)
	live.LastWrite = testNow.Add(-1 * time.Minute)
	live.ID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	old := deadSession()
	procs := []Process{{
		PID:   100,
		Start: testNow.Add(-35 * time.Minute),
		Cwd:   "/home/u/src/rig/polecats/dust",
		Slugs: []string{live.ProjectSlug},
	}}

	got := Classify([]Session{live, old}, procs, nil, DefaultPolicy(), testNow)
	if got[0].Verdict != VerdictKeep {
		t.Errorf("live session verdict = %q (%s), want keep", got[0].Verdict, got[0].Reason)
	}
	if got[1].Verdict != VerdictSweep {
		t.Errorf("earlier session in the same project verdict = %q (%s), want sweep", got[1].Verdict, got[1].Reason)
	}
}

func sweepable(id string, bytes int64, lastWrite time.Time) Decision {
	return Decision{
		Session: Session{ID: id, Path: "/tmp/claude-1000/p/" + id, Bytes: bytes, LastWrite: lastWrite},
		Verdict: VerdictSweep,
	}
}

func TestSelectHoldsEverythingBelowHighWater(t *testing.T) {
	decisions := []Decision{
		sweepable("a", 5<<30, testNow.Add(-10*time.Hour)),
		sweepable("b", 3<<30, testNow.Add(-5*time.Hour)),
	}
	// 50% used, high-water 80%.
	got := Select(decisions, 100<<30, 50<<30, DefaultPolicy(), false)
	if got.Triggered {
		t.Error("Triggered = true at 50% usage, want false")
	}
	if len(got.Selected) != 0 {
		t.Errorf("selected %d scratchpads below the high-water mark, want 0", len(got.Selected))
	}
	if got.Held != 2 || got.HeldBytes != 8<<30 {
		t.Errorf("held = %d (%d bytes), want 2 (%d bytes)", got.Held, got.HeldBytes, int64(8<<30))
	}
}

func TestSelectStopsAtTarget(t *testing.T) {
	// 85 GB of 100 GB used: above the 80% high-water, target 60% means 25 GB
	// must go. Oldest first, and the third scratchpad is not needed.
	decisions := []Decision{
		sweepable("newest", 20<<30, testNow.Add(-1*time.Hour)),
		sweepable("oldest", 20<<30, testNow.Add(-10*time.Hour)),
		sweepable("middle", 20<<30, testNow.Add(-5*time.Hour)),
	}
	got := Select(decisions, 100<<30, 85<<30, DefaultPolicy(), false)
	if !got.Triggered {
		t.Fatal("Triggered = false at 85% usage with an 80% high-water mark, want true")
	}
	if len(got.Selected) != 2 {
		t.Fatalf("selected %d scratchpads, want 2 (85 GB - 40 GB = 45 GB, under the 60 GB target)", len(got.Selected))
	}
	if got.Selected[0].Session.ID != "oldest" || got.Selected[1].Session.ID != "middle" {
		t.Errorf("selected %s then %s, want oldest then middle", got.Selected[0].Session.ID, got.Selected[1].Session.ID)
	}
	if got.Held != 1 {
		t.Errorf("held = %d, want 1", got.Held)
	}
}

func TestSelectAllBypassesThePressureGate(t *testing.T) {
	decisions := []Decision{
		sweepable("a", 1<<30, testNow.Add(-10*time.Hour)),
		{Session: Session{ID: "kept"}, Verdict: VerdictKeep},
	}
	got := Select(decisions, 100<<30, 10<<30, DefaultPolicy(), true)
	if len(got.Selected) != 1 || got.Selected[0].Session.ID != "a" {
		t.Fatalf("selected %d scratchpads, want only the sweepable one", len(got.Selected))
	}
	if got.Bytes != 1<<30 {
		t.Errorf("bytes = %d, want %d", got.Bytes, int64(1<<30))
	}
}

func TestSelectNeverTakesKeptSessions(t *testing.T) {
	decisions := []Decision{
		{Session: Session{ID: "live", Bytes: 50 << 30}, Verdict: VerdictKeep},
	}
	got := Select(decisions, 100<<30, 99<<30, DefaultPolicy(), false)
	if len(got.Selected) != 0 {
		t.Fatalf("selected %d kept scratchpads under maximum pressure, want 0", len(got.Selected))
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		dir  string
		want string
	}{
		{"/home/jkerby/src/gt/gastown/polecats/dust/gastown", "-home-jkerby-src-gt-gastown-polecats-dust-gastown"},
		// Underscores collapse to "-" exactly like separators, which is why
		// duly_noted appears as duly-noted in the scratchpad tree.
		{"/home/jkerby/src/gt/duly_noted/witness", "-home-jkerby-src-gt-duly-noted-witness"},
		{"/home/jkerby", "-home-jkerby"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := Slugify(tc.dir); got != tc.want {
			t.Errorf("Slugify(%q) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

func TestParseEtime(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "01:23", want: 83 * time.Second},
		{in: "01:02:03", want: time.Hour + 2*time.Minute + 3*time.Second},
		{in: "2-01:02:03", want: 48*time.Hour + time.Hour + 2*time.Minute + 3*time.Second},
		{in: "", wantErr: true},
		{in: "garbage", wantErr: true},
	}
	for _, tc := range tests {
		got, err := parseEtime(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseEtime(%q) = %v, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseEtime(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseEtime(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsAgentProcess(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "claude binary", args: []string{"/home/u/.local/bin/claude", "--dangerously-skip-permissions"}, want: true},
		{name: "node entrypoint", args: []string{"node", "/opt/claude/cli.js"}, want: true},
		// A shell an agent spawned quotes "claude" constantly — snapshot paths,
		// mail bodies. Counting those as agents would attribute liveness to
		// whatever text a command happened to contain.
		{name: "shell that merely mentions claude", args: []string{"/usr/bin/zsh", "-c", "source /home/u/.claude-accounts/snapshot.sh"}, want: false},
		{name: "unrelated process", args: []string{"/usr/bin/dolt", "sql-server"}, want: false},
		{name: "empty", args: nil, want: false},
	}
	for _, tc := range tests {
		if got := isAgentProcess(tc.args); got != tc.want {
			t.Errorf("%s: isAgentProcess(%v) = %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

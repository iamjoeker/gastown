package doltserver

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestParseProcStatStartTicks(t *testing.T) {
	tests := []struct {
		name string
		stat string
		want uint64
		ok   bool
	}{
		{
			name: "real dolt sql-server line",
			stat: "187419 (dolt) S 1 187419 187419 0 -1 4194304 21532 0 0 0 3841 431 0 0 20 0 39 0 8855545 4308062208 42117 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 5 0 0 0 0 0 0 0 0 0 0 0 0 0\n",
			want: 8855545,
			ok:   true,
		},
		{
			// comm is arbitrary bytes inside parens — spaces and nested
			// parens included — which is why parsing starts after the LAST ')'.
			name: "comm containing spaces and parens",
			stat: "42 (we ird) proc) S 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 777 0 0 0 0 0 0 0 0",
			want: 777,
			ok:   true,
		},
		{
			name: "truncated line",
			stat: "42 (dolt) S 1 42 42 0 -1",
		},
		{
			name: "no comm parens",
			stat: "42 dolt S 1 42",
		},
		{
			name: "non-numeric starttime",
			stat: "42 (dolt) S 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 later 0 0 0 0 0 0 0 0",
		},
		{
			name: "empty",
			stat: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseProcStatStartTicks(tt.stat)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("ticks = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseProcBootTime(t *testing.T) {
	procStat := `cpu  1 2 3 4
cpu0 1 2 3 4
intr 0
ctxt 12345
btime 1786929210
processes 999
`
	got, ok := parseProcBootTime(procStat)
	if !ok {
		t.Fatal("btime line not found")
	}
	if got.Unix() != 1786929210 {
		t.Fatalf("boot time = %d, want 1786929210", got.Unix())
	}

	if _, ok := parseProcBootTime("cpu 1 2 3 4\nprocesses 9\n"); ok {
		t.Fatal("expected no boot time when btime is absent")
	}
	if _, ok := parseProcBootTime("btime notanumber\n"); ok {
		t.Fatal("expected no boot time for unparseable btime")
	}
}

func TestParsePsLstart(t *testing.T) {
	// ps space-pads the day of month, so both widths must parse.
	for _, in := range []string{
		"Mon Aug 17 20:49:25 2026\n",
		"Sun Aug  3 04:05:06 2026\n",
	} {
		got, ok := parsePsLstart(in)
		if !ok {
			t.Fatalf("parsePsLstart(%q) failed", in)
		}
		if got.Year() != 2026 || got.Month() != time.August {
			t.Fatalf("parsePsLstart(%q) = %v", in, got)
		}
	}

	for _, in := range []string{"", "\n  \n", "not a timestamp"} {
		if _, ok := parsePsLstart(in); ok {
			t.Fatalf("parsePsLstart(%q) unexpectedly succeeded", in)
		}
	}
}

// TestProcessStartTimeSelf checks the OS lookup against a process whose start
// time is known to be in the past but not absurdly so: this test binary.
func TestProcessStartTimeSelf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process start time is not implemented on Windows")
	}

	started, ok := ProcessStartTime(os.Getpid())
	if !ok {
		t.Fatal("ProcessStartTime(self) failed")
	}
	age := time.Since(started)
	if age < 0 || age > time.Hour {
		t.Fatalf("self start time = %v (age %v), want a recent past time", started, age)
	}
}

func TestProcessStartTimeInvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if _, ok := ProcessStartTime(pid); ok {
			t.Fatalf("ProcessStartTime(%d) should not succeed", pid)
		}
	}
}

func TestResolveStartedAtPrefersLiveProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process start time is not implemented on Windows")
	}

	// The bug this guards (gt-pdd): a recorded start time from before the
	// running process existed must not be what gets reported.
	stale := time.Now().Add(-72 * time.Hour)
	got, live := ResolveStartedAt(os.Getpid(), stale)
	if !live {
		t.Fatal("expected the live process to supply the start time")
	}
	if got.Equal(stale) {
		t.Fatal("stale recorded time was returned instead of the live one")
	}
	if time.Since(got) > time.Hour {
		t.Fatalf("resolved start time = %v, want the test binary's start time", got)
	}
}

func TestResolveStartedAtFallsBackToRecorded(t *testing.T) {
	recorded := time.Now().Add(-2 * time.Hour)
	// PID 0 is never a readable process, so the recorded value is all there is.
	got, live := ResolveStartedAt(0, recorded)
	if live {
		t.Fatal("no live process available, but the result claims otherwise")
	}
	if !got.Equal(recorded) {
		t.Fatalf("start time = %v, want recorded %v", got, recorded)
	}
}

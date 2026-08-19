package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// formatCall matches a time layout passed to .Format(...).
var formatCall = regexp.MustCompile(`\.Format\("([^"]*)"\)`)

// zoneTokens are the layout elements that render a zone. A wall-clock layout
// that carries any of them is self-describing; one that carries none is not.
var zoneTokens = []string{"MST", "Z07:00", "Z0700", "-07:00", "-0700"}

// TestMailTimestampsCarryZoneLabel asserts that every wall-clock timestamp
// `gt mail` renders names its zone.
//
// gt renders local time and bd renders UTC. Correlating the two is routine
// here, and an unlabelled local timestamp compared against a bead timestamp
// silently carries the offset as an error: a prediction stated in UTC, read
// against a local timestamp, came within one step of being recorded as a
// failed test when it was in fact eleven minutes away (hq-rmi).
//
// The reason this is a source scan rather than an output assertion is that the
// defect's real shape is a MISSED CALL SITE, not a wrong format. The first fix
// labelled `gt audit` and shipped believing `gt feed` was covered; the feed
// renders through a different file and stayed unlabelled until someone ran the
// rebuilt binary. Six more mail surfaces were still unlabelled after that. An
// output test on one command cannot see the site it does not call, so the
// invariant is asserted over the sources instead: every layout in this package's
// mail commands that renders a clock time must name its zone.
func TestMailTimestampsCarryZoneLabel(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}

	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "mail") || !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		scanned++

		for _, line := range strings.Split(string(src), "\n") {
			for _, match := range formatCall.FindAllStringSubmatch(line, -1) {
				layout := match[1]
				if !strings.Contains(layout, "15:04") {
					continue // Not a clock time — a bare date carries no offset hazard.
				}
				if hasZoneToken(layout) {
					continue
				}
				t.Errorf("%s renders a clock time with no zone: .Format(%q)\n"+
					"  gt shows local time, bd shows UTC. Add MST (or render UTC) so the\n"+
					"  value cannot be compared against a bead timestamp as if it were UTC.",
					name, layout)
			}
		}
	}

	// A scan that silently matched nothing would pass forever. Assert it read
	// the files it claims to police.
	if scanned == 0 {
		t.Fatal("scanned no mail sources — the file pattern no longer matches anything")
	}
}

func hasZoneToken(layout string) bool {
	for _, token := range zoneTokens {
		if strings.Contains(layout, token) {
			return true
		}
	}
	return false
}

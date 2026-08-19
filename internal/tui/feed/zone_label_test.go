package feed

import (
	"strings"
	"testing"
	"time"
)

// TestHeaderNamesTheZone asserts the feed header states which zone its event
// times are in.
//
// The rows render "15:04" with no zone, and gt's local time is routinely
// compared against bd's UTC — unlabelled, that comparison silently carries the
// offset (hq-rmi). The label belongs in the header rather than on every row
// because all rows share one zone, so the header is where its absence is a
// defect.
func TestHeaderNamesTheZone(t *testing.T) {
	m := &Model{width: 120}

	header := m.renderHeader()

	zone := time.Now().Format("MST")
	if !strings.Contains(header, zone) {
		t.Errorf("header does not name the zone %q, so its 15:04 times are unlabelled:\n%s", zone, header)
	}
}

// TestProblemsHeaderNamesTheZone covers the other header branch: the problems
// view renders through the same function and must not drop the label.
func TestProblemsHeaderNamesTheZone(t *testing.T) {
	m := &Model{width: 120, viewMode: ViewProblems}

	header := m.renderHeader()

	zone := time.Now().Format("MST")
	if !strings.Contains(header, zone) {
		t.Errorf("problems-view header does not name the zone %q:\n%s", zone, header)
	}
}

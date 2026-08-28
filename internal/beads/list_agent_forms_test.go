package beads

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Tests for the fan-out every assignee-filtered listing now goes through
// (gt-gbv4).
//
// Eight readers in this tree filtered on an agent's assignee with a single
// form. Each was a silent false-empty in the direction that costs the most —
// "this agent holds nothing" for an agent that holds something — and the caller
// acted on the absence: gt prime dropped an agent with attached work into
// interactive mode, gt done reported nothing assigned, and the handoff cleanup
// left the beads it exists to close.

// newAssigneeStubBeads installs a bd that only ever answers for one assignee
// form, which is precisely how the real store behaves for a bead written under
// the other convention.
func newAssigneeStubBeads(t *testing.T, answersFor, issueID string) *Beads {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    --assignee=%s)
      printf '[{"id":"%s","title":"work","status":"hooked","assignee":"%s"}]\n'
      exit 0
      ;;
  esac
done
echo '[]'
`, answersFor, issueID, answersFor)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return New(workDir)
}

// TestListAcrossAgentAddressFormsFindsTheOtherForm is the fix: a bead written
// bare is found by a caller holding the canonical form, and vice versa.
func TestListAcrossAgentAddressFormsFindsTheOtherForm(t *testing.T) {
	cases := []struct{ stored, asked string }{
		{"deacon", "deacon/"},
		{"deacon/", "deacon"},
		{"mayor", "mayor/"},
		{"mayor/", "mayor"},
	}
	for _, tc := range cases {
		t.Run(tc.stored+"->"+tc.asked, func(t *testing.T) {
			b := newAssigneeStubBeads(t, tc.stored, "hq-1")

			// Control first: the plain listing must MISS it, or this test would
			// pass against the single-form code it exists to catch.
			plain, err := b.List(ListOptions{Status: StatusHooked, Assignee: tc.asked, Priority: -1})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(plain) != 0 {
				t.Fatalf("single-form List found %d bead(s) for %q against a store holding %q — the stub does not discriminate, so this test proves nothing",
					len(plain), tc.asked, tc.stored)
			}

			found, err := b.ListAcrossAgentAddressForms(ListOptions{Status: StatusHooked, Assignee: tc.asked, Priority: -1})
			if err != nil {
				t.Fatalf("ListAcrossAgentAddressForms: %v", err)
			}
			if len(found) != 1 || found[0].ID != "hq-1" {
				t.Fatalf("stored %q, asked %q: got %d bead(s), want the one that is there", tc.stored, tc.asked, len(found))
			}
		})
	}
}

// TestListAcrossAgentAddressFormsDeduplicates covers the case both queries hit.
// A store that has genuinely converged returns the same bead under both forms,
// and a caller that sees it twice reports two pieces of work where there is one.
func TestListAcrossAgentAddressFormsDeduplicates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	// Answers for every form.
	script := "#!/bin/sh\nprintf '[{\"id\":\"hq-1\",\"title\":\"work\",\"status\":\"hooked\",\"assignee\":\"deacon/\"}]\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	found, err := New(workDir).ListAcrossAgentAddressForms(ListOptions{
		Status: StatusHooked, Assignee: "deacon", Priority: -1,
	})
	if err != nil {
		t.Fatalf("ListAcrossAgentAddressForms: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("got %d beads, want 1 — the same bead matched under both forms and was not deduplicated", len(found))
	}
}

// TestListAcrossAgentAddressFormsCostsNothingForSingleFormAgents is the
// no-regression control. Rig agents have exactly one form, and a fan-out that
// queried twice for them would double the bd subprocesses on the hook path for
// no benefit at all.
func TestListAcrossAgentAddressFormsCostsNothingForSingleFormAgents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	callLog := filepath.Join(workDir, "calls.log")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\necho '[]'\n", callLog)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, agent := range []string{"gastown/witness", "gastown/polecats/toast", "deacon/dogs/alpha"} {
		if err := os.Remove(callLog); err != nil && !os.IsNotExist(err) {
			t.Fatalf("reset call log: %v", err)
		}
		if _, err := New(workDir).ListAcrossAgentAddressForms(ListOptions{
			Status: StatusHooked, Assignee: agent, Priority: -1,
		}); err != nil {
			t.Fatalf("ListAcrossAgentAddressForms(%s): %v", agent, err)
		}
		data, err := os.ReadFile(callLog)
		if err != nil {
			t.Fatalf("read call log: %v", err)
		}
		// Count listings only. b.run also fires a one-off `bd version` probe,
		// which is not a query and does not multiply with the forms.
		if n := countListCalls(string(data)); n != 1 {
			t.Errorf("%s: %d bd list calls, want 1 — single-form agents must not pay for the fan-out\nlog:\n%s", agent, n, string(data))
		}
	}
}

func countListCalls(log string) int {
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, " list ") || strings.Contains(line, " query ") {
			n++
		}
	}
	return n
}

package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/polecat"
)

func TestEffectivePolecatDirCap(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		want       int
	}{
		{"negative uses floor", -1, minPolecatDirsPerRig},
		{"zero uses floor", 0, minPolecatDirsPerRig},
		{"default below floor uses floor", 10, minPolecatDirsPerRig},
		{"one below floor uses floor", minPolecatDirsPerRig - 1, minPolecatDirsPerRig},
		{"floor remains floor", minPolecatDirsPerRig, minPolecatDirsPerRig},
		{"above floor is honored", 45, 45},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectivePolecatDirCap(tt.configured); got != tt.want {
				t.Errorf("effectivePolecatDirCap(%d) = %d, want %d", tt.configured, got, tt.want)
			}
		})
	}
}

// TestSelfResolvingReuseRefusalReasonsExcludesGenuineStuckStates covers
// gt-818eo: the gt-83m4 consecutive-refusal escalation must skip reasons that
// clear on their own once the merge queue drains (an open or stale MR left by
// StateHandedOff), while still escalating reasons that mean an agent actually
// needs a human — including the generic "state-not-eligible" a truly stuck
// state now falls back to (see stateNotEligibleReason in internal/polecat).
func TestSelfResolvingReuseRefusalReasonsExcludesGenuineStuckStates(t *testing.T) {
	selfResolving := []string{polecat.WorkstateReasonActiveMROpen, polecat.WorkstateReasonActiveMRStale}
	for _, reason := range selfResolving {
		if !selfResolvingReuseRefusalReasons[reason] {
			t.Errorf("selfResolvingReuseRefusalReasons[%q] = false, want true (self-resolving, must not escalate)", reason)
		}
	}

	genuinelyStuck := []string{"state-not-eligible", "agent-state-paused", "git-dirty", "mq-not-submitted", "unknown"}
	for _, reason := range genuinelyStuck {
		if selfResolvingReuseRefusalReasons[reason] {
			t.Errorf("selfResolvingReuseRefusalReasons[%q] = true, want false (genuinely stuck, must still escalate)", reason)
		}
	}
}

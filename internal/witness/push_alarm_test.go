package witness

import (
	"strings"
	"testing"
)

func TestPushFailureAlarmNamedBranch(t *testing.T) {
	alarm := pushFailureAlarm("furiosa", "polecat/furiosa/bd-791+abc", "bd-791")

	if alarm.Unnamed {
		t.Error("Unnamed = true for a real branch name")
	}
	if !strings.HasPrefix(alarm.MayorMessage, "PUSH_FAILED: ") {
		t.Errorf("MayorMessage = %q, want the PUSH_FAILED alarm preserved", alarm.MayorMessage)
	}
	if !strings.Contains(alarm.MayorMessage, "branch=polecat/furiosa/bd-791+abc") {
		t.Errorf("MayorMessage = %q, want the branch an operator can push", alarm.MayorMessage)
	}
	for name, action := range map[string]string{"Action": alarm.Action, "DiscoveryAction": alarm.DiscoveryAction} {
		if !strings.Contains(action, "push-failed-recovery-needed") {
			t.Errorf("%s = %q, want the push-failure action", name, action)
		}
	}
}

// branch="HEAD" is the detached-worktree placeholder, not a branch. Reporting it
// as PUSH_FAILED tells an operator to push a ref that cannot exist (gt-e45).
func TestPushFailureAlarmDetachedBranchIsItsOwnCondition(t *testing.T) {
	for _, branch := range []string{"HEAD", "", "  "} {
		t.Run("branch="+branch, func(t *testing.T) {
			alarm := pushFailureAlarm("furiosa", branch, "bd-791")

			if !alarm.Unnamed {
				t.Error("Unnamed = false, want true for an unnameable branch")
			}
			if strings.Contains(alarm.MayorMessage, "PUSH_FAILED") {
				t.Errorf("MayorMessage = %q, want a distinct alarm — no push result was measured", alarm.MayorMessage)
			}
			if !strings.HasPrefix(alarm.MayorMessage, "POLECAT_DETACHED: ") {
				t.Errorf("MayorMessage = %q, want the POLECAT_DETACHED alarm", alarm.MayorMessage)
			}
			if strings.Contains(alarm.MayorMessage, "branch=HEAD") {
				t.Errorf("MayorMessage = %q, want no unpushable branch name offered to the operator", alarm.MayorMessage)
			}
			if !strings.Contains(alarm.MayorMessage, "bd-791") {
				t.Errorf("MayorMessage = %q, want the issue so the work can be traced", alarm.MayorMessage)
			}
			for name, action := range map[string]string{"Action": alarm.Action, "DiscoveryAction": alarm.DiscoveryAction} {
				if strings.Contains(action, "push-failed") {
					t.Errorf("%s = %q, want it kept apart from a push failure", name, action)
				}
				if !strings.Contains(action, "branch-unresolvable") {
					t.Errorf("%s = %q, want the unresolvable-branch action", name, action)
				}
			}
		})
	}
}

func TestIsUnresolvableBranchName(t *testing.T) {
	tests := map[string]bool{
		"":                          true,
		"   ":                       true,
		"HEAD":                      true,
		" HEAD ":                    true,
		"head":                      false, // a real branch may be named "head"
		"polecat/furiosa/bd-791+ab": false,
		"main":                      false,
	}
	for branch, want := range tests {
		if got := isUnresolvableBranchName(branch); got != want {
			t.Errorf("isUnresolvableBranchName(%q) = %v, want %v", branch, got, want)
		}
	}
}

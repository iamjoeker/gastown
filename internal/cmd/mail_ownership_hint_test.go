package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// gt-gbv4 asks for one assertion about error text specifically:
//
//	"any error text naming a flag only does so when the invoked command
//	 exposes it."
//
// bd's ownership refusal ends "reclaim or use --force to override". `gt mail
// archive` grew a --force so that advice became followable. `gt mail delete`
// routes through the same close and can surface the same refusal, and it has
// never had a --force — so repeating bd's wording there sends the reader after
// a flag that does not exist on the command they ran.

var ownershipRefusal = errors.New(`cannot close hq-abc: assignee is "gastown/toast", actor is "gastown/polecats/toast"; reclaim or use --force to override`)

// TestOwnershipHintNamesACommandThatHasTheFlag covers the command with no
// --force of its own.
func TestOwnershipHintNamesACommandThatHasTheFlag(t *testing.T) {
	out := captureStdout(t, func() {
		printOwnershipRefusalHint(ownershipRefusal, "hq-abc", "gastown/toast", "delete")
	})

	if !strings.Contains(out, "mail archive hq-abc --force") {
		t.Errorf("delete hint does not name the command that exposes --force:\n%s", out)
	}
	if !strings.Contains(out, "has no --force") {
		t.Errorf("delete hint does not say the invoked command lacks the flag:\n%s", out)
	}
}

// TestOwnershipHintOmitsRedirectWhenTheCommandHasTheFlag is the control. If the
// redirect printed unconditionally it would tell someone already running
// `gt mail archive --force` to go run `gt mail archive --force`.
func TestOwnershipHintOmitsRedirectWhenTheCommandHasTheFlag(t *testing.T) {
	out := captureStdout(t, func() {
		printOwnershipRefusalHint(ownershipRefusal, "hq-abc", "gastown/toast", "archive")
	})

	if strings.Contains(out, "has no --force") {
		t.Errorf("archive exposes --force and must not be told it does not:\n%s", out)
	}
	if !strings.Contains(out, "only its assignee can close it") {
		t.Errorf("archive lost the explanation it already had:\n%s", out)
	}
}

// TestOwnershipHintSilentOnUnrelatedErrors keeps the hint from firing on every
// failure. A network error is not an ownership problem and --force will not fix
// it.
func TestOwnershipHintSilentOnUnrelatedErrors(t *testing.T) {
	out := captureStdout(t, func() {
		printOwnershipRefusalHint(errors.New("dial tcp 127.0.0.1:3307: connection refused"), "hq-abc", "gastown/toast", "delete")
		printOwnershipRefusalHint(nil, "hq-abc", "gastown/toast", "delete")
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("hint fired on a non-ownership error:\n%s", out)
	}
}

// TestMailDeleteStillHasNoForceFlag pins the premise the hint is written
// against. If `gt mail delete` ever grows a --force, the redirect above becomes
// wrong and this test says so instead of letting it rot into bad advice.
func TestMailDeleteStillHasNoForceFlag(t *testing.T) {
	if mailDeleteCmd.Flags().Lookup("force") != nil {
		t.Fatal("gt mail delete now has --force; printOwnershipRefusalHint must stop redirecting to gt mail archive")
	}
	// And the command the hint redirects to must actually have it.
	if mailArchiveCmd.Flags().Lookup("force") == nil {
		t.Fatal("gt mail archive lost --force; the hint now names an unreachable remedy — the exact defect gt-gbv4 reported")
	}
	var _ *cobra.Command = mailDeleteCmd
}

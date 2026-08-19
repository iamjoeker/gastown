package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/mail"
)

// TestMailSendDefaultsToDurable pins the gt-jbn regression.
//
// `--wisp` defaulted to true, so every `gt mail send` created an ephemeral wisp
// bead. Wisps are age-GC reclaimable (`bd mol wisp gc --age`), and unread mail
// ages fastest — nothing touches its updated_at — so the mail most likely to be
// deleted was the mail nobody had read yet. CLAUDE.md meanwhile told agents
// mail creates "a permanent bead + Dolt commit" and to prefer it over nudge for
// anything that must survive session death.
//
// Ephemeral storage is now opt-in. Protocol/lifecycle traffic is still stored
// as wisps, but by subject auto-detection in Router.shouldBeWisp, not by a
// default that catches every message.
func TestMailSendDefaultsToDurable(t *testing.T) {
	wisp := mailSendCmd.Flags().Lookup("wisp")
	if wisp == nil {
		t.Fatal("--wisp flag not registered on mail send")
	}
	if wisp.DefValue != "false" {
		t.Errorf("--wisp default = %q, want %q — mail is durable unless explicitly opted out",
			wisp.DefValue, "false")
	}

	permanent := mailSendCmd.Flags().Lookup("permanent")
	if permanent == nil {
		t.Fatal("--permanent flag not registered on mail send")
	}
	if permanent.DefValue != "false" {
		t.Errorf("--permanent default = %q, want %q", permanent.DefValue, "false")
	}
}

// TestRoutingFlagsReachClassifier pins the gt-rhxb regression.
//
// --permanent only cancelled --wisp; it never reached the subject
// auto-detection in the router. A merge receipt ("MERGED crater ...") was
// therefore stored as an age-GC reclaimable wisp no matter what the sender
// passed, and --permanent was documented as overriding exactly that.
func TestRoutingFlagsReachClassifier(t *testing.T) {
	tests := []struct {
		name          string
		subject       string
		wisp          bool
		permanent     bool
		wantEphemeral bool
	}{
		{"plain subject, no flags", "Please review this", false, false, false},
		{"plain subject, --wisp", "Please review this", true, false, true},
		{"protocol subject, no flags", "MERGED crater", false, false, true},
		{"protocol subject, --permanent", "MERGED crater", false, true, false},
		{"protocol subject, both flags", "MERGED crater", true, true, false},
		{"prose that starts with a protocol word", "Merged crater by hand", false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := mail.NewMessage("a/", "b/", tt.subject, "body")
			applyRoutingFlags(msg, tt.wisp, tt.permanent)
			if got := mail.WillBeEphemeral(msg); got != tt.wantEphemeral {
				t.Errorf("WillBeEphemeral(subject=%q, wisp=%v, permanent=%v) = %v, want %v",
					tt.subject, tt.wisp, tt.permanent, got, tt.wantEphemeral)
			}
		})
	}
}

package cmd

import "testing"

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

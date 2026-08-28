package cmd

import (
	"testing"
)

// `gt mail send` has offered --cc all along. `gt mail reply` did not, so an
// operator who reached for it got a flag listing, sent NOTHING, and exited 1 —
// then archived the original on the next line, which succeeded. The inbox went
// clean and the reply never left (gt-1t0v #5, carried to gt-khq8).
//
// Nothing in the reply path made the flag hard to support: the message it builds
// has a CC field and the router already delivers it.
func TestMailReplyAcceptsCC(t *testing.T) {
	if mailReplyCmd.Flags().Lookup("cc") == nil {
		t.Fatal("gt mail reply has no --cc; reaching for it sends nothing")
	}

	origReply, origSend := mailReplyCC, mailCC
	t.Cleanup(func() { mailReplyCC, mailCC = origReply, origSend })

	mailReplyCC, mailCC = nil, nil
	if err := mailReplyCmd.Flags().Parse([]string{"--cc", "gastown/witness", "--cc", "mayor/"}); err != nil {
		t.Fatalf("parsing --cc on reply: %v", err)
	}
	if len(mailReplyCC) != 2 || mailReplyCC[0] != "gastown/witness" || mailReplyCC[1] != "mayor/" {
		t.Fatalf("mailReplyCC = %v, want both recipients", mailReplyCC)
	}

	// Separate variables on purpose. Sharing one with `gt mail send` would let a
	// send earlier in the same process decide who a later reply copies in.
	if len(mailCC) != 0 {
		t.Fatalf("reply --cc wrote through to the send command's variable: %v", mailCC)
	}
}

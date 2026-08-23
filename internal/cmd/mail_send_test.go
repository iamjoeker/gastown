package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/mail"
)

func TestHasReplyPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"Re: Status", true},
		{"re: status", true},
		{"RE:status", true},
		{"  Re: leading ws", true},
		{"Reply: not a Re prefix", false},
		{"Status", false},
		{"", false},
		{"Re", false},
		{":empty", false},
	}
	for _, c := range cases {
		if got := hasReplyPrefix(c.in); got != c.want {
			t.Errorf("hasReplyPrefix(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNormalizeReplySubject(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Re: Hello", "hello"},
		{"re:hello", "hello"},
		{"Re: Re: Re: nested", "nested"},
		{"  Re:  spaced  ", "spaced"},
		{"plain subject", "plain subject"},
		{"", ""},
		{"Re: ", ""},
	}
	for _, c := range cases {
		if got := normalizeReplySubject(c.in); got != c.want {
			t.Errorf("normalizeReplySubject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeAddress(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"mayor/", "mayor"},
		{"Mayor", "mayor"},
		{"  gastown/Toast/  ", "gastown/toast"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeAddress(c.in); got != c.want {
			t.Errorf("normalizeAddress(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPickReplyTo(t *testing.T) {
	msg := func(id, from, subj string) *mail.Message {
		return &mail.Message{ID: id, From: from, Subject: subj}
	}

	t.Run("exact match returns id", func(t *testing.T) {
		msgs := []*mail.Message{
			msg("bd-1", "deacon/", "Build broken"),
			msg("bd-2", "witness/", "Build broken"),
		}
		if got := pickReplyTo(msgs, "deacon/", "Re: Build broken"); got != "bd-1" {
			t.Errorf("got %q, want bd-1", got)
		}
	})

	t.Run("address normalization", func(t *testing.T) {
		msgs := []*mail.Message{msg("bd-3", "Mayor", "[HIGH] alert")}
		if got := pickReplyTo(msgs, "mayor/", "Re: [HIGH] alert"); got != "bd-3" {
			t.Errorf("got %q, want bd-3", got)
		}
	})

	t.Run("nested Re prefixes normalize", func(t *testing.T) {
		msgs := []*mail.Message{msg("bd-4", "deacon/", "Re: original")}
		if got := pickReplyTo(msgs, "deacon/", "Re: Re: original"); got != "bd-4" {
			t.Errorf("got %q, want bd-4", got)
		}
	})

	t.Run("ambiguous match returns empty", func(t *testing.T) {
		msgs := []*mail.Message{
			msg("bd-5", "deacon/", "stuck"),
			msg("bd-6", "deacon/", "stuck"),
		}
		if got := pickReplyTo(msgs, "deacon/", "Re: stuck"); got != "" {
			t.Errorf("got %q, want empty (ambiguous)", got)
		}
	})

	t.Run("wrong sender returns empty", func(t *testing.T) {
		msgs := []*mail.Message{msg("bd-7", "witness/", "important")}
		if got := pickReplyTo(msgs, "deacon/", "Re: important"); got != "" {
			t.Errorf("got %q, want empty (wrong sender)", got)
		}
	})

	t.Run("wrong subject returns empty", func(t *testing.T) {
		msgs := []*mail.Message{msg("bd-8", "deacon/", "alpha")}
		if got := pickReplyTo(msgs, "deacon/", "Re: beta"); got != "" {
			t.Errorf("got %q, want empty (wrong subject)", got)
		}
	})

	t.Run("empty subject after strip returns empty", func(t *testing.T) {
		msgs := []*mail.Message{msg("bd-9", "deacon/", "")}
		if got := pickReplyTo(msgs, "deacon/", "Re: "); got != "" {
			t.Errorf("got %q, want empty (degenerate)", got)
		}
	})

	t.Run("empty message list returns empty", func(t *testing.T) {
		if got := pickReplyTo(nil, "deacon/", "Re: anything"); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// setSendFlags parses args against the real mail-send flag set and restores the
// flags' prior state afterward. Testing against mailSendCmd rather than a
// stand-in matters here: mailBodySource has to distinguish --message from its
// --body alias, and both write the same variable, so only the real
// registration exercises the distinction.
func setSendFlags(t *testing.T, args ...string) {
	t.Helper()
	prevBody, prevStdin, prevAllowEmpty := mailBody, mailStdin, mailAllowEmpty
	changed := map[string]bool{}
	for _, name := range []string{"message", "body", "stdin", "allow-empty"} {
		if f := mailSendCmd.Flags().Lookup(name); f != nil {
			changed[name] = f.Changed
		}
	}
	t.Cleanup(func() {
		mailBody, mailStdin, mailAllowEmpty = prevBody, prevStdin, prevAllowEmpty
		for name, was := range changed {
			if f := mailSendCmd.Flags().Lookup(name); f != nil {
				f.Changed = was
			}
		}
	})
	if err := mailSendCmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%v): %v", args, err)
	}
}

func TestCheckMailBody(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		source     string
		allowEmpty bool
		wantErr    bool
		wantSub    string
	}{
		{name: "non-empty body passes", body: "hello", source: "--message/-m"},
		{name: "body of only punctuation passes", body: ".", source: "--message/-m"},
		{name: "empty stdin refused", source: "--stdin", wantErr: true, wantSub: "--stdin produced an empty body"},
		{name: "whitespace-only stdin refused", body: "  \n\t ", source: "--stdin", wantErr: true, wantSub: "--stdin produced an empty body"},
		{name: "empty -m refused", source: "--message/-m", wantErr: true, wantSub: "--message/-m produced an empty body"},
		{name: "empty --body refused", source: "--body", wantErr: true, wantSub: "--body produced an empty body"},
		{name: "no body flag at all refused", wantErr: true, wantSub: "no message body"},
		{name: "allow-empty overrides refusal", source: "--stdin", allowEmpty: true},
		{name: "allow-empty with no source at all", allowEmpty: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkMailBody(c.body, c.source, c.allowEmpty)
			if !c.wantErr {
				if err != nil {
					t.Fatalf("checkMailBody(%q, %q, %v) = %v, want nil", c.body, c.source, c.allowEmpty, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkMailBody(%q, %q, %v) = nil, want error", c.body, c.source, c.allowEmpty)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantSub)
			}
			// Every refusal must name the escape hatch, or the sender is stuck.
			if !strings.Contains(err.Error(), "--allow-empty") {
				t.Errorf("error %q does not mention --allow-empty", err.Error())
			}
		})
	}
}

func TestMailBodySource(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "no body flag", args: []string{"-s", "x"}, want: ""},
		{name: "message", args: []string{"-s", "x", "-m", "hi"}, want: "--message/-m"},
		{name: "empty message still counts as a source", args: []string{"-s", "x", "-m", ""}, want: "--message/-m"},
		{name: "body alias", args: []string{"-s", "x", "--body", "hi"}, want: "--body"},
		{name: "stdin", args: []string{"-s", "x", "--stdin"}, want: "--stdin"},
		{name: "stdin wins over message", args: []string{"-s", "x", "--stdin", "-m", "hi"}, want: "--stdin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setSendFlags(t, c.args...)
			if got := mailBodySource(mailSendCmd); got != c.want {
				t.Errorf("mailBodySource(%v) = %q, want %q", c.args, got, c.want)
			}
		})
	}

	t.Run("nil command with stdin", func(t *testing.T) {
		setSendFlags(t, "-s", "x", "--stdin")
		if got := mailBodySource(nil); got != "--stdin" {
			t.Errorf("got %q, want %q", got, "--stdin")
		}
	})
	t.Run("nil command without stdin", func(t *testing.T) {
		setSendFlags(t, "-s", "x", "-m", "hi")
		if got := mailBodySource(nil); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// detachFromTown points mail's workspace lookup at an empty directory, so a
// send that gets past the body guard dies at findMailWorkDir instead of
// delivering real mail. Chdir alone is not enough: findMailWorkDir prefers
// GT_TOWN_ROOT/GT_ROOT over cwd, and those are set in every agent session — a
// run from /tmp with the guard removed still reached the router.
//
// It also turns each assertion below into a real control. Unguarded, this path
// returns "not in a Gas Town workspace"; seeing the refusal instead proves the
// guard exists AND runs ahead of recipient and workspace resolution, which is
// where a harness with no stdin attached needs it to fire.
func detachFromTown(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GT_TOWN_ROOT", dir)
	t.Setenv("GT_ROOT", dir)
}

// TestRunMailSendRefusesEmptyBody drives the real send path, reproducing
// canary D from gt-gxxm: --stdin reading from /dev/null used to deliver a
// subject with an empty body and exit 0.
func TestRunMailSendRefusesEmptyBody(t *testing.T) {
	prevSubject := mailSubject
	t.Cleanup(func() { mailSubject = prevSubject })

	t.Run("empty stdin", func(t *testing.T) {
		detachFromTown(t)

		devNull, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatalf("opening %s: %v", os.DevNull, err)
		}
		defer devNull.Close()
		prevStdin := os.Stdin
		os.Stdin = devNull
		t.Cleanup(func() { os.Stdin = prevStdin })

		setSendFlags(t, "-s", "canary", "--stdin")

		err = runMailSend(mailSendCmd, []string{"mayor/"})
		if err == nil {
			t.Fatal("runMailSend with empty stdin returned nil, want refusal")
		}
		if !strings.Contains(err.Error(), "refusing to send") || !strings.Contains(err.Error(), "--stdin") {
			t.Errorf("error %q is not the empty-stdin refusal", err.Error())
		}
	})

	t.Run("no body flag at all", func(t *testing.T) {
		detachFromTown(t)
		setSendFlags(t, "-s", "canary")

		err := runMailSend(mailSendCmd, []string{"mayor/"})
		if err == nil {
			t.Fatal("runMailSend with no body returned nil, want refusal")
		}
		if !strings.Contains(err.Error(), "refusing to send: no message body") {
			t.Errorf("error %q is not the missing-body refusal", err.Error())
		}
	})

	t.Run("whitespace-only -m", func(t *testing.T) {
		detachFromTown(t)
		setSendFlags(t, "-s", "canary", "-m", "   \n\t")

		err := runMailSend(mailSendCmd, []string{"mayor/"})
		if err == nil {
			t.Fatal("runMailSend with whitespace-only body returned nil, want refusal")
		}
		if !strings.Contains(err.Error(), "refusing to send") || !strings.Contains(err.Error(), "--message/-m") {
			t.Errorf("error %q is not the empty--m refusal", err.Error())
		}
	})
}

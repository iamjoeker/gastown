package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/mail"
)

// TestSearchJSONEmptyResultIsAnArrayCarryingItsScope guards the machine-readable
// half of the same property the scope line carries for humans.
//
// A bare `[]` cannot distinguish "no such message exists" from "the store
// holding it was never read", and `null` adds a third shape for a reader to
// disambiguate for nothing. The envelope always carries the scope, and the
// messages are always an array.
func TestSearchJSONEmptyResultIsAnArrayCarryingItsScope(t *testing.T) {
	out, err := json.Marshal(newSearchJSON(&mail.SearchResult{
		Scope: mail.SearchScope{Inbox: true},
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := string(out)
	if !strings.Contains(got, `"messages":[]`) {
		t.Errorf("empty result marshalled as %s, want an empty array", got)
	}
	if !strings.Contains(got, `"scope":["inbox"]`) {
		t.Errorf("empty result marshalled as %s, want it to name the scope it read", got)
	}
}

// TestSearchJSONNamesUnreadableStores: a store that could not be read has to
// reach a machine reader too, or the JSON zero is exactly the uninterpretable
// one this change removed from the text output.
func TestSearchJSONNamesUnreadableStores(t *testing.T) {
	out, err := json.Marshal(newSearchJSON(&mail.SearchResult{
		Scope: mail.SearchScope{Inbox: true, Unavailable: []string{"sent: no record"}},
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"unavailable":["sent: no record"]`) {
		t.Errorf("marshalled as %s, want the unreadable store named", out)
	}
}

// TestSearchScopeLineNamesWhatWasExcluded pins the half that makes a zero
// interpretable.
//
// "0 message(s)" from an inbox-only search and "0 message(s)" from a search
// that also read the archive are the same string and different findings. A
// reader told only what WAS covered still has to know the full set of stores to
// work out what is missing, and the reader who most needs telling is exactly
// the one who does not — so the excluded stores are named, with the flag that
// would include them (gt-7gvk).
func TestSearchScopeLineNamesWhatWasExcluded(t *testing.T) {
	tests := []struct {
		name        string
		scope       mail.SearchScope
		wantScope   []string
		wantMissing []string
		wantAbsent  []string
	}{
		{
			name:        "inbox only names both exclusions",
			scope:       mail.SearchScope{Inbox: true},
			wantScope:   []string{"inbox"},
			wantMissing: []string{"archived (--archive)", "sent (--sent)"},
		},
		{
			name:        "archived included still names sent",
			scope:       mail.SearchScope{Inbox: true, Archived: true},
			wantScope:   []string{"inbox", "archived"},
			wantMissing: []string{"sent (--sent)"},
			wantAbsent:  []string{"archived (--archive)"},
		},
		{
			name:       "everything searched excludes nothing",
			scope:      mail.SearchScope{Inbox: true, Archived: true, Sent: true},
			wantScope:  []string{"inbox", "archived", "sent"},
			wantAbsent: []string{"not searched"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scopeNames(tt.scope)
			if strings.Join(got, "+") != strings.Join(tt.wantScope, "+") {
				t.Errorf("scopeNames = %v, want %v", got, tt.wantScope)
			}

			suffix := excludedSuffix(tt.scope)
			for _, want := range tt.wantMissing {
				if !strings.Contains(suffix, want) {
					t.Errorf("excludedSuffix = %q, want it to name %q", suffix, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(suffix, absent) {
					t.Errorf("excludedSuffix = %q, should not name %q", suffix, absent)
				}
			}
		})
	}
}

// TestSearchScopeSuffixIsEmptyOnlyWhenNothingIsExcluded guards against the
// suffix quietly going blank — a blank suffix asserts full coverage, so it must
// be reachable only from full coverage.
func TestSearchScopeSuffixIsEmptyOnlyWhenNothingIsExcluded(t *testing.T) {
	for _, scope := range []mail.SearchScope{
		{Inbox: true},
		{Inbox: true, Archived: true},
		{Inbox: true, Sent: true},
	} {
		if excludedSuffix(scope) == "" {
			t.Errorf("scope %+v excludes a store but reports nothing excluded", scope)
		}
	}
	if got := excludedSuffix(mail.SearchScope{Inbox: true, Archived: true, Sent: true}); got != "" {
		t.Errorf("full scope reported an exclusion: %q", got)
	}
}

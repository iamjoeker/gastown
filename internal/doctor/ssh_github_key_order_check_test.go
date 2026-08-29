package doctor

import (
	"strings"
	"testing"
)

func TestNewSSHGitHubKeyOrderCheck(t *testing.T) {
	check := NewSSHGitHubKeyOrderCheck()

	if check.Name() != "ssh-github-key-order" {
		t.Errorf("expected name 'ssh-github-key-order', got %q", check.Name())
	}
	if check.Description() == "" {
		t.Error("expected non-empty description")
	}
	if check.CanFix() {
		t.Error("expected CanFix() to return false — this check must never modify ~/.ssh/config")
	}
}

func TestCountAgentIdentities(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{"empty", "", 0},
		{"whitespace only", "\n\n  \n", 0},
		{"one key", "256 SHA256:abc... comment (ED25519)\n", 1},
		{"six keys", strings.Repeat("256 SHA256:abc... comment (ED25519)\n", 6), 6},
		{"trailing blank line", "256 SHA256:abc... comment (ED25519)\n256 SHA256:def... comment (RSA)\n\n", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countAgentIdentities(tt.output); got != tt.want {
				t.Errorf("countAgentIdentities(%q) = %d, want %d", tt.output, got, tt.want)
			}
		})
	}
}

func TestGithubIdentityIsPinned(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"identitiesonly yes", "identitiesonly yes\nidentityfile ~/.ssh/github_ed25519.pub\n", true},
		{"identitiesonly no", "identitiesonly no\nidentityfile ~/.ssh/id_rsa\n", false},
		{"identitiesonly absent", "identityfile ~/.ssh/id_rsa\n", false},
		{"empty output", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := githubIdentityIsPinned(tt.output); got != tt.want {
				t.Errorf("githubIdentityIsPinned(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestSSHGitHubKeyOrderCheck_Run(t *testing.T) {
	// Integration-style: exercises the real ssh-add/ssh binaries on this
	// machine. It must never fail the test regardless of the environment's
	// actual key count — only that Run() returns a well-formed result and
	// never attempts to fix (CanFix() is false, so gt doctor --fix will
	// never touch ~/.ssh/config for this check).
	check := NewSSHGitHubKeyOrderCheck()
	result := check.Run(&CheckContext{})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Name != check.Name() {
		t.Errorf("expected result name %q, got %q", check.Name(), result.Name)
	}
	if result.Message == "" {
		t.Error("expected non-empty message")
	}
}

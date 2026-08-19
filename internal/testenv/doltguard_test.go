package testenv

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// testenv guards itself the same way every other package does; the call is
	// unqualified only because this is the package under test.
	GuardProductionDolt()
	os.Exit(m.Run())
}

func TestNeedsGuarding(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"unset falls through to the 3307 default", "", true},
		{"whitespace is as good as unset", "   ", true},
		{"the production port itself", "3307", true},
		{"the production port with stray whitespace", " 3307 ", true},
		{"unparseable is skipped by the resolvers, so also falls through", "not-a-port", true},
		{"a container port a helper already chose", "49173", false},
		{"a developer's own scratch server", "4407", false},
		{"the guarded port, so a second call is a no-op", "63307", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsGuarding(tc.value); got != tc.want {
				t.Errorf("needsGuarding(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestGuardProductionDoltRedirectsEveryPortVariable(t *testing.T) {
	// Every variable starts out pointing at production — the state a test
	// process inherits from an agent or developer shell.
	for _, name := range doltPortEnvVars {
		t.Setenv(name, strconv.Itoa(ProductionDoltPort))
	}
	t.Setenv(AllowProductionDoltEnv, "")

	GuardProductionDolt()

	for _, name := range doltPortEnvVars {
		if got := os.Getenv(name); got != strconv.Itoa(GuardedDoltPort) {
			t.Errorf("%s = %q, want %q", name, got, strconv.Itoa(GuardedDoltPort))
		}
	}
}

func TestGuardProductionDoltFillsUnsetVariables(t *testing.T) {
	// An unset variable is the more common case and the more dangerous one:
	// it is exactly how resolution reaches doltserver.DefaultPort.
	for _, name := range doltPortEnvVars {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unsetenv %s: %v", name, err)
		}
	}
	t.Setenv(AllowProductionDoltEnv, "")

	GuardProductionDolt()

	for _, name := range doltPortEnvVars {
		if got := os.Getenv(name); got != strconv.Itoa(GuardedDoltPort) {
			t.Errorf("%s = %q, want %q", name, got, strconv.Itoa(GuardedDoltPort))
		}
	}
}

func TestGuardProductionDoltKeepsDeliberatePorts(t *testing.T) {
	// A helper that started a container for this run has already written its
	// mapped port here. Overwriting it would send the suite at a dead port and
	// break tests that were correctly isolated.
	const containerPort = "49173"
	for _, name := range doltPortEnvVars {
		t.Setenv(name, containerPort)
	}
	t.Setenv(AllowProductionDoltEnv, "")

	GuardProductionDolt()

	for _, name := range doltPortEnvVars {
		if got := os.Getenv(name); got != containerPort {
			t.Errorf("%s = %q, want the container port %q left alone", name, got, containerPort)
		}
	}
}

func TestGuardProductionDoltRespectsOptIn(t *testing.T) {
	for _, name := range doltPortEnvVars {
		t.Setenv(name, strconv.Itoa(ProductionDoltPort))
	}
	// Naming the port is what authorizes; see testguard.AllowDoltEnv.
	t.Setenv(AllowProductionDoltEnv, strconv.Itoa(ProductionDoltPort))

	GuardProductionDolt()

	for _, name := range doltPortEnvVars {
		if got := os.Getenv(name); got != strconv.Itoa(ProductionDoltPort) {
			t.Errorf("%s = %q, want the opt-in to preserve %d", name, got, ProductionDoltPort)
		}
	}
}

func TestProductionDoltAllowedRequiresNamingThePort(t *testing.T) {
	// The opt-in follows testguard's rule: the value must be the boundary being
	// crossed. A bare "1" is the shape a stray export takes, and it is exactly
	// the shape that must not disarm the guard.
	for _, value := range []string{"", "1", "true", "yes", "0", "false", "63307", "3307 ", " "} {
		t.Setenv(AllowProductionDoltEnv, value)
		if ProductionDoltAllowed() {
			t.Errorf("ProductionDoltAllowed() = true for %q, want false", value)
		}
	}

	t.Setenv(AllowProductionDoltEnv, strconv.Itoa(ProductionDoltPort))
	if !ProductionDoltAllowed() {
		t.Errorf("ProductionDoltAllowed() = false for %d, want true", ProductionDoltPort)
	}
}

// TestTestguardStaysDependencyFree protects the property that lets this package
// be called from TestMain everywhere, including from the packages testutil
// itself depends on.
//
// testenv holds no gastown imports except testguard, so testguard is the only
// way a cycle could appear here. Today it imports nothing but the standard
// library; if that changes, every TestMain in the tree is one import away from
// a cycle, and the failure surfaces as an unrelated package refusing to build.
func TestTestguardStaysDependencyFree(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/steveyegge/gastown/internal/testguard").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	const self = "github.com/steveyegge/gastown/internal/testguard"
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		// go list -deps includes the package itself.
		if dep != self && strings.HasPrefix(dep, "github.com/steveyegge/gastown/") {
			t.Errorf("testguard depends on %s; it must stay standard-library only so testenv cannot cycle", dep)
		}
	}
}

func TestGuardedPortIsNotProduction(t *testing.T) {
	if GuardedDoltPort == ProductionDoltPort {
		t.Fatalf("GuardedDoltPort == ProductionDoltPort (%d): the guard would be a no-op", GuardedDoltPort)
	}
}

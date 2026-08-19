package doctor

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// routingModeAuto is the only routing.mode value that diverts anything.
//
// beads routes on two inputs (internal/routing/routing.go, DetermineTargetRepoWithRule):
// the role detected from the process CWD, and the routing config of the opened
// store. The role-based swap to a planning repo is reached ONLY under
// `Mode == "auto"`. Every other value — "explicit", "maintainer", "contributor",
// an unrecognised string, and the empty string of an unset key — falls straight
// through to the default repo, i.e. the local one.
//
// That matters because it inverts what this check used to assume. gt-frr was
// resolved, by Overseer decision, by DELETING routing.mode outright; the
// deleted state is a safe state, not a degraded one, and a check that nags to
// put the key back is nagging to undo a decision.
const routingModeAuto = "auto"

// routingLegacyAutoRoute is the pre-routing.mode key. beads still honours it:
// when routing.mode resolves empty, `contributor.auto_route=true` promotes the
// mode to "auto" (cmd/bd/routing_read.go). A store carrying it is auto-routing
// with routing.mode showing as unset, so reading routing.mode alone is not
// enough to clear a namespace.
const routingLegacyAutoRoute = "contributor.auto_route"

// RoutingModeCheck detects when beads routing.mode resolves to "auto", which
// routes issues and mail to a contributor planning store (~/.beads-planning)
// that no other agent reads. Diversion needs BOTH a contributor role and
// mode=auto, so this check covers the half that is configuration.
//
// See: https://github.com/steveyegge/beads/issues/1165
type RoutingModeCheck struct {
	FixableCheck
}

// NewRoutingModeCheck creates a new routing mode check.
func NewRoutingModeCheck() *RoutingModeCheck {
	return &RoutingModeCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "routing-mode",
				CheckDescription: "Check beads routing.mode is not 'auto' (prevents .beads-planning routing)",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// routingLayer names where an acting value was read from, for messages.
const (
	routingLayerYAML   = ".beads/config.yaml"
	routingLayerDolt   = "the Dolt config table"
	routingLayerLegacy = "the Dolt config table (contributor.auto_route=true)"
)

// routingState is how one beads namespace resolves routing.mode.
//
// beads reads the key from config.yaml first and falls back to the Dolt config
// row when the yaml value is empty (cmd/bd/routing_read.go,
// resolveRoutingConfigValue). Both layers have to be read: gt-frr's auto value
// lived in the Dolt table with yaml unset, and `bd config get routing.mode` is
// a yaml-only key path (cmd/bd/config.go calls GetYamlConfig for it) that
// reports "not set" for exactly that case. gastown measured the same
// precedence independently in gt-il30 — see internal/configreg/registry.go.
type routingState struct {
	yamlMode  string // routing.mode in .beads/config.yaml, "" when unset
	doltMode  string // routing.mode in the Dolt config table, "" when absent
	autoRoute bool   // legacy contributor.auto_route=true
}

// mode returns the value beads acts on, and the layer it came from.
func (s routingState) mode() (mode, layer string) {
	if s.yamlMode != "" {
		return s.yamlMode, routingLayerYAML
	}
	if s.doltMode != "" {
		return s.doltMode, routingLayerDolt
	}
	if s.autoRoute {
		return routingModeAuto, routingLayerLegacy
	}
	return "", ""
}

// isAuto reports whether this namespace auto-routes.
func (s routingState) isAuto() bool {
	mode, _ := s.mode()
	return mode == routingModeAuto
}

// Run checks that routing.mode does not resolve to "auto".
func (c *RoutingModeCheck) Run(ctx *CheckContext) *CheckResult {
	// Check town-level beads config
	townBeadsDir := filepath.Join(ctx.TownRoot, ".beads")
	result := c.checkRoutingMode(townBeadsDir, "town")
	if result.Status != StatusOK {
		return result
	}

	// Also check rig-level beads if specified
	if ctx.RigName != "" {
		rigBeadsDir := filepath.Join(ctx.RigPath(), ".beads")
		rigResult := c.checkRoutingMode(rigBeadsDir, fmt.Sprintf("rig '%s'", ctx.RigName))
		if rigResult.Status != StatusOK {
			return rigResult
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: "Beads routing.mode does not auto-route",
	}
}

// checkRoutingMode checks the routing mode in a specific beads directory.
func (c *RoutingModeCheck) checkRoutingMode(beadsDir, location string) *CheckResult {
	state, err := readRoutingState(beadsDir)
	if err != nil {
		// Report the gap rather than clearing the namespace: with routing.mode
		// unset in yaml the Dolt row is what beads acts on, so an unreadable
		// store means the mode is genuinely unknown.
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("Could not determine routing.mode at %s: %v", location, err),
		}
	}

	mode, layer := state.mode()
	if mode == routingModeAuto {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: fmt.Sprintf("routing.mode is 'auto' at %s, set in %s", location, layer),
			Details: []string{
				"Auto routing mode uses git remote URL to detect user role",
				"Non-SSH URLs (HTTPS or file paths) trigger routing to ~/.beads-planning",
				"This causes mail and issues to be stored in the wrong location",
				"See: https://github.com/steveyegge/beads/issues/1165",
			},
			FixHint: "Run 'gt doctor --fix' or 'bd config set routing.mode explicit'",
		}
	}

	if mode == "" {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("routing.mode is unset at %s (no auto-routing)", location),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("routing.mode is '%s' at %s (no auto-routing)", mode, location),
	}
}

// Fix clears auto-routing where it is actually configured.
//
// It does not touch a namespace that is already safe. Writing routing.mode into
// a store that has none would re-add the key gt-frr deliberately deleted, and
// unset does not auto-route, so there is nothing there to fix.
func (c *RoutingModeCheck) Fix(ctx *CheckContext) error {
	type routingLocation struct {
		beadsDir string
		label    string
	}

	locations := []routingLocation{
		{filepath.Join(ctx.TownRoot, ".beads"), "town"},
	}
	if ctx.RigName != "" {
		locations = append(locations, routingLocation{
			beadsDir: filepath.Join(ctx.RigPath(), ".beads"),
			label:    fmt.Sprintf("rig %s", ctx.RigName),
		})
	}

	for _, loc := range locations {
		state, err := readRoutingState(loc.beadsDir)
		if err != nil {
			return fmt.Errorf("reading routing.mode at %s: %w", loc.label, err)
		}
		if !state.isAuto() {
			continue
		}
		// `bd config set routing.mode` writes config.yaml, which is the layer
		// beads reads first — that clears a yaml "auto" and also defeats the
		// legacy contributor.auto_route promotion, which only applies while
		// routing.mode is empty everywhere.
		if state.doltMode == routingModeAuto {
			// The acting value is a database row. Shadowing it from yaml would
			// report a fix whose effect depends on which layer wins, and that
			// ambiguity is exactly what cost hours in gt-il30. Name the row
			// instead of guessing.
			return fmt.Errorf("routing.mode is 'auto' in the Dolt config table at %s; "+
				"bd config set writes config.yaml and cannot remove that row. Remove it with: "+
				"bd sql \"DELETE FROM config WHERE `key` = 'routing.mode'\"", loc.label)
		}
		if err := c.setRoutingMode(loc.beadsDir); err != nil {
			return fmt.Errorf("fixing %s beads: %w", loc.label, err)
		}
	}

	return nil
}

// setRoutingMode sets routing.mode to "explicit" in the specified beads directory.
func (c *RoutingModeCheck) setRoutingMode(beadsDir string) error {
	cmd := exec.Command("bd", "config", "set", "routing.mode", "explicit")
	cmd.Dir = filepath.Dir(beadsDir)
	cmd.Env = append(cmd.Environ(), "BEADS_DIR="+beadsDir)

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd config set failed: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

// readRoutingState resolves routing.mode for one beads namespace across the
// layers beads itself reads.
func readRoutingState(beadsDir string) (routingState, error) {
	yamlMode, err := readYAMLRoutingMode(beadsDir)
	if err != nil {
		return routingState{}, err
	}
	if yamlMode != "" {
		// yaml wins outright, so the database layer cannot change the answer.
		// Skipping it keeps the check answerable while Dolt is down.
		return routingState{yamlMode: yamlMode}, nil
	}

	rows, err := readDoltRoutingConfig(beadsDir)
	if err != nil {
		return routingState{}, err
	}
	return routingState{
		doltMode:  rows["routing.mode"],
		autoRoute: strings.EqualFold(rows[routingLegacyAutoRoute], "true"),
	}, nil
}

// bdConfigGetJSON is the envelope `bd config get --json` emits.
type bdConfigGetJSON struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// readYAMLRoutingMode reads routing.mode from .beads/config.yaml, returning ""
// when the key is unset.
//
// `bd config get` EXITS ZERO for an unset key and prints the human sentence
// "routing.mode (not set in config.yaml)" on stdout. Branching on the exit
// status therefore never sees the unset case and hands that whole sentence on
// as if it were the value. --json is the unambiguous form; the text form is
// still parsed as a fallback because gastown supports bd back to 0.57.0.
func readYAMLRoutingMode(beadsDir string) (string, error) {
	stdout, jsonErr := runBDConfigGet(beadsDir, "--json")
	if jsonErr == nil {
		var decoded bdConfigGetJSON
		if err := json.Unmarshal(bytes.TrimSpace(stdout), &decoded); err == nil {
			return strings.TrimSpace(decoded.Value), nil
		}
	}

	stdout, err := runBDConfigGet(beadsDir)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(stdout))
	if strings.HasPrefix(value, "routing.mode (not set") {
		return "", nil
	}
	return value, nil
}

// runBDConfigGet runs `bd config get routing.mode` in a beads namespace.
func runBDConfigGet(beadsDir string, extraArgs ...string) ([]byte, error) {
	args := append([]string{"config", "get", "routing.mode"}, extraArgs...)
	cmd := exec.Command("bd", args...)
	cmd.Dir = filepath.Dir(beadsDir)
	cmd.Env = append(cmd.Environ(), "BEADS_DIR="+beadsDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("bd config get: %s", msg)
		}
		return nil, fmt.Errorf("bd config get: %w", err)
	}
	return stdout.Bytes(), nil
}

// routingDoltConfigQuery reads the routing keys that live in the database. Both
// are fetched together so the acting mode and the legacy promotion cost one
// query.
const routingDoltConfigQuery = "SELECT `key`, value FROM config WHERE `key` IN ('routing.mode', 'contributor.auto_route')"

// readDoltRoutingConfig reads the routing keys from a namespace's Dolt config
// table. A namespace with no such rows yields an empty map, not an error.
func readDoltRoutingConfig(beadsDir string) (map[string]string, error) {
	cmd := exec.Command("bd", "sql", "--csv", routingDoltConfigQuery) //nolint:gosec // G204: query is a constant
	cmd.Dir = filepath.Dir(beadsDir)
	cmd.Env = append(cmd.Environ(), "BEADS_DIR="+beadsDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("bd sql: %s", msg)
		}
		return nil, fmt.Errorf("bd sql: %w", err)
	}

	records, err := csv.NewReader(bytes.NewReader(stdout.Bytes())).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing routing config: %w", err)
	}

	// Matching on the keys that were asked for drops the CSV header without
	// assuming a header is present, and without assuming its position.
	values := make(map[string]string, 2)
	for _, record := range records {
		if len(record) < 2 {
			continue
		}
		switch key := strings.TrimSpace(record[0]); key {
		case "routing.mode", routingLegacyAutoRoute:
			values[key] = strings.TrimSpace(record[1])
		}
	}
	return values, nil
}

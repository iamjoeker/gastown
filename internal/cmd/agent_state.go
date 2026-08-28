package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

var (
	agentStateSet  []string
	agentStateIncr string
	agentStateDel  []string
	agentStateJSON bool
)

var agentStateCmd = &cobra.Command{
	Use:   "state <agent-bead>",
	Short: "Get or set operational state on agent beads",
	Long: `Get or set label-based operational state on agent beads.

Agent beads store operational state (like idle cycle counts) as labels.
This command provides a convenient interface for reading and modifying
these labels without affecting other bead properties.

LABEL FORMAT:
Labels are stored as key:value pairs (e.g., idle:3, backoff:2m).

OPERATIONS:
  Get all labels (default):
    gt agents state <agent-bead>

  Set a label:
    gt agents state <agent-bead> --set idle=0
    gt agents state <agent-bead> --set idle=0 --set backoff=30s

  Increment a numeric label:
    gt agents state <agent-bead> --incr idle
    (Creates label with value 1 if not present)

  Delete a label:
    gt agents state <agent-bead> --del idle

A modification that would leave the labels exactly as they already are is
skipped rather than written: every bd update is a permanent Dolt commit, and
setting a label to the value it already holds buys nothing. Output says which
happened.

COMMON LABELS:
  idle:<n>           - Consecutive idle patrol cycles
  backoff:<duration> - Current backoff interval
  last_activity:<ts> - Last activity timestamp

EXAMPLES:
  # Check current idle count
  gt agents state gt-gastown-witness

  # Reset idle counter after finding work
  gt agents state gt-gastown-witness --set idle=0

  # Increment idle counter on timeout
  gt agents state gt-gastown-witness --incr idle

  # Get state as JSON
  gt agents state gt-gastown-witness --json`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentState,
}

func init() {
	agentStateCmd.Flags().StringArrayVar(&agentStateSet, "set", nil,
		"Set label value (format: key=value, repeatable)")
	agentStateCmd.Flags().StringVar(&agentStateIncr, "incr", "",
		"Increment numeric label (creates with value 1 if missing)")
	agentStateCmd.Flags().StringArrayVar(&agentStateDel, "del", nil,
		"Delete label (repeatable)")
	agentStateCmd.Flags().BoolVar(&agentStateJSON, "json", false,
		"Output as JSON")

	// Add as subcommand of agents
	agentsCmd.AddCommand(agentStateCmd)
}

// agentStateResult holds the state query result.
type agentStateResult struct {
	AgentBead string            `json:"agent_bead"`
	Labels    map[string]string `json:"labels"`
}

func runAgentState(cmd *cobra.Command, args []string) error {
	agentBead := args[0]

	beadsDir, err := resolveAgentStateBeadsDir(agentBead)
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	// Determine operation mode
	hasSet := len(agentStateSet) > 0
	hasIncr := agentStateIncr != ""
	hasDel := len(agentStateDel) > 0

	if hasSet || hasIncr || hasDel {
		// Modification mode
		return modifyAgentState(agentBead, beadsDir, hasIncr)
	}

	// Query mode
	return queryAgentState(agentBead, beadsDir)
}

// queryAgentState retrieves and displays labels from an agent bead.
func queryAgentState(agentBead, beadsDir string) error {
	labels, err := getAgentLabels(agentBead, beadsDir)
	if err != nil {
		return err
	}

	result := &agentStateResult{
		AgentBead: agentBead,
		Labels:    labels,
	}

	if agentStateJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	// Human-readable output
	fmt.Printf("%s Agent: %s\n\n", style.Bold.Render("📊"), agentBead)

	if len(labels) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no operational state labels)"))
		return nil
	}

	for key, value := range labels {
		fmt.Printf("  %s: %s\n", key, value)
	}

	return nil
}

// modifyAgentState modifies labels on an agent bead.
// Uses read-modify-write pattern: read current labels, apply changes, write back all.
func modifyAgentState(agentBead, beadsDir string, hasIncr bool) error {
	// Read current labels
	labels, err := getAgentLabels(agentBead, beadsDir)
	if err != nil {
		return err
	}

	// Also get non-state labels (ones without : separator) to preserve them
	allLabels, err := getAllAgentLabels(agentBead, beadsDir)
	if err != nil {
		return err
	}

	// Apply increment operation
	if hasIncr {
		currentValue := 0
		if valStr, ok := labels[agentStateIncr]; ok {
			if v, err := strconv.Atoi(valStr); err == nil {
				currentValue = v
			}
		}
		labels[agentStateIncr] = strconv.Itoa(currentValue + 1)
	}

	// Apply set operations
	for _, setOp := range agentStateSet {
		parts := strings.SplitN(setOp, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid set format: %s (expected key=value)", setOp)
		}
		labels[parts[0]] = parts[1]
	}

	// Apply delete operations
	for _, delKey := range agentStateDel {
		delete(labels, delKey)
	}

	// Build final label list: non-state labels + state labels (key:value format)
	var finalLabels []string

	// First, keep non-state labels (those without : separator)
	for _, label := range allLabels {
		if !strings.Contains(label, ":") {
			finalLabels = append(finalLabels, label)
		}
	}

	// Add state labels from modified map
	for key, value := range labels {
		finalLabels = append(finalLabels, key+":"+value)
	}

	// Build update command with --set-labels to replace all
	args := []string{"update", agentBead}
	for _, label := range finalLabels {
		args = append(args, "--set-labels="+label)
	}

	// If no labels, clear all
	if len(finalLabels) == 0 {
		args = append(args, "--set-labels=")
	}

	// Skip the write when it would not change anything. bd update is a Dolt
	// commit that lives forever, and the commonest caller by far is the
	// "reset idle" step in the witness, refinery and deacon patrol formulas —
	// which runs on every signal wake against a counter await-signal has
	// already reset in-process (gt-609). That step therefore spends a
	// permanent commit per wake, per patrol agent, to write the value that is
	// already there. await-signal's own setAgentIdleCycles is guarded the same
	// way; this closes the same hole for the caller-side path (gt-i7g9).
	if labelSetsEqual(allLabels, finalLabels) {
		fmt.Printf("%s Agent state for %s already matches — no write\n",
			style.Dim.Render("="), agentBead)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), bdCallTimeout)
	defer cancel()

	cmd := beads.CommandContext(ctx, filepath.Dir(beadsDir), beadsDir, beads.MutationPinned, args...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		return fmt.Errorf("updating agent state: %w", err)
	}

	fmt.Printf("%s Updated agent state for %s\n", style.Bold.Render("✓"), agentBead)

	return nil
}

// labelSetsEqual reports whether two label lists describe the same set of
// labels, ignoring order. bd stores labels unordered and modifyAgentState
// rebuilds the list from a map, so order carries no meaning and comparing it
// would report a difference on every call.
//
// Duplicates are significant: a bead carrying the same label twice differs
// from one carrying it once, and the rebuild collapses that, so the write must
// still go through to repair it. Hence a count-per-label comparison rather
// than a set membership test.
func labelSetsEqual(current, next []string) bool {
	if len(current) != len(next) {
		return false
	}
	counts := make(map[string]int, len(current))
	for _, l := range current {
		counts[l]++
	}
	for _, l := range next {
		counts[l]--
		if counts[l] < 0 {
			return false
		}
	}
	return true
}

// getAgentLabels retrieves state labels from an agent bead.
// Returns only labels in key:value format, parsed into a map.
// State labels are those with a : separator (e.g., idle:3, backoff:2m).
func getAgentLabels(agentBead, beadsDir string) (map[string]string, error) {
	allLabels, err := getAllAgentLabels(agentBead, beadsDir)
	if err != nil {
		return nil, err
	}

	// Parse state labels (those with : separator) into key:value map
	labels := make(map[string]string)
	for _, label := range allLabels {
		parts := strings.SplitN(label, ":", 2)
		if len(parts) == 2 {
			labels[parts[0]] = parts[1]
		}
	}

	return labels, nil
}

// bdCallTimeout is the per-call timeout for bd subprocess invocations in agent-bead
// helpers. bd commands should be fast against a local Dolt server, but can hang
// indefinitely if Dolt is unresponsive (e.g., connection pool exhausted). A 30s
// ceiling prevents await-event/await-signal from stalling past the patrol timeout.
const bdCallTimeout = 30 * time.Second

// getAllAgentLabels retrieves all labels (including non-state) from an agent bead.
func getAllAgentLabels(agentBead, beadsDir string) ([]string, error) {
	args := []string{"show", agentBead, "--json"}

	ctx, cancel := context.WithTimeout(context.Background(), bdCallTimeout)
	defer cancel()

	cmd := beads.CommandContext(ctx, filepath.Dir(beadsDir), beadsDir, beads.ReadOnlyPinned, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if strings.Contains(errMsg, "not found") {
			return nil, fmt.Errorf("agent bead not found: %s", agentBead)
		}
		if errMsg != "" {
			return nil, fmt.Errorf("%s", errMsg)
		}
		return nil, fmt.Errorf("querying agent bead: %w", err)
	}

	return parseAgentBeadLabels(stdout.Bytes(), stderr.Bytes(), agentBead)
}

// parseAgentBeadLabels parses the JSON output from bd show --json and extracts labels.
// This is separated from getAllAgentLabels to enable unit testing.
func parseAgentBeadLabels(stdout, stderr []byte, agentBead string) ([]string, error) {
	// Check for empty stdout before parsing - can happen with daemon mismatch
	// or other errors that don't set exit code
	if len(stdout) == 0 {
		errMsg := strings.TrimSpace(string(stderr))
		if errMsg != "" {
			return nil, fmt.Errorf("%s", errMsg)
		}
		return nil, fmt.Errorf("agent bead query returned no output: %s", agentBead)
	}

	// Parse JSON output - bd show --json returns an array
	var issues []struct {
		Labels []string `json:"labels"`
	}

	if err := json.Unmarshal(stdout, &issues); err != nil {
		return nil, fmt.Errorf("parsing agent bead response: %w", err)
	}

	if len(issues) == 0 {
		return nil, fmt.Errorf("agent bead not found: %s", agentBead)
	}

	return issues[0].Labels, nil
}

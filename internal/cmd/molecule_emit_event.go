package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/channelevents"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	emitEventChannel string
	emitEventType    string
	emitEventPayload []string
	emitEventRig     string
)

var moleculeEmitEventCmd = &cobra.Command{
	Use:   "emit-event",
	Short: "Emit a file-based event on a named channel",
	Long: `Emit an event file for subscribers to pick up.

This is the Go counterpart to emit-event.sh. Events are JSON files consumed
by await-event subscribers (e.g., the refinery watching for MERGE_READY events).

CHANNEL SCOPING:
Most channels name a per-rig agent, so they are scoped by rig and live at
~/gt/events/<rig>/<channel>/. The rig comes from --rig, else GT_RIG, else the
current directory. Channels consumed by a town-level singleton (mayor, deacon)
are not rig-scoped and live at ~/gt/events/<channel>/.

Emit a rig-scoped event to the rig whose agent should act on it. That is not
always your own rig — the witness of rig A emits to rig A's refinery.

EVENT FORMAT:
Creates a JSON file named <timestamp>.event in the channel directory:
  {"type": "...", "channel": "...", "rig": "...", "timestamp": "...", "payload": {...}}

EXAMPLES:
  # Emit a MERGE_READY event for this rig's refinery
  # (rig taken from GT_RIG or the current directory)
  gt mol step emit-event --channel refinery --type MERGE_READY \
    --payload polecat=nux --payload branch=polecat/nux/gt-iw7m

  # Emit a PATROL_WAKE event to a named rig's refinery
  gt mol step emit-event --channel refinery --rig gastown --type PATROL_WAKE \
    --payload source=witness --payload queue_depth=3

  # Emit an MQ_SUBMIT event
  gt mol step emit-event --channel refinery --type MQ_SUBMIT \
    --payload branch=feat/new-feature --payload mr_id=bd-42`,
	RunE: runMoleculeEmitEvent,
}

// EmitEventResult is returned when an event is emitted.
type EmitEventResult struct {
	Path    string `json:"path"`
	Channel string `json:"channel"`
	Rig     string `json:"rig,omitempty"`
	Type    string `json:"type"`
}

func init() {
	moleculeEmitEventCmd.Flags().StringVar(&emitEventChannel, "channel", "",
		"Event channel name (required, e.g., 'refinery')")
	moleculeEmitEventCmd.Flags().StringVar(&emitEventType, "type", "",
		"Event type (required, e.g., 'MERGE_READY')")
	moleculeEmitEventCmd.Flags().StringArrayVar(&emitEventPayload, "payload", nil,
		"Payload key=value pairs (repeatable)")
	moleculeEmitEventCmd.Flags().StringVar(&emitEventRig, "rig", "",
		"Rig whose agent should receive the event (default: GT_RIG, else inferred from cwd)")
	moleculeEmitEventCmd.Flags().BoolVar(&moleculeJSON, "json", false,
		"Output as JSON")
	_ = moleculeEmitEventCmd.MarkFlagRequired("channel")
	_ = moleculeEmitEventCmd.MarkFlagRequired("type")

	moleculeStepCmd.AddCommand(moleculeEmitEventCmd)
}

func runMoleculeEmitEvent(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		townRoot = ""
	}
	rigName := resolveChannelRig(emitEventRig, townRoot)

	path, err := channelevents.EmitFromCwd(rigName, emitEventChannel, emitEventType, emitEventPayload)
	if err != nil {
		return err
	}

	if moleculeJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		res := EmitEventResult{
			Path:    path,
			Channel: emitEventChannel,
			Type:    emitEventType,
		}
		if !channelevents.IsTownScoped(emitEventChannel) {
			res.Rig = rigName
		}
		return enc.Encode(res)
	}

	fmt.Println(path)
	return nil
}

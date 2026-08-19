package polecat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/testguard"
	"github.com/steveyegge/gastown/internal/townlog"
)

// needs_mq_submit is computed on read, from state that keeps moving. That is the
// right shape for the predicate — a stored copy would drift — but it left the
// condition it exists to surface unauditable: the flag was true while a polecat's
// pushed branch sat outside the queue and false again the moment the work landed,
// and nothing in between wrote it down. So minutes later every surface reads
// false, which is also exactly what "the check never fired at all" looks like.
// Correct operation and total regression were observationally identical after the
// fact (gt-7i07), and the fix this predicate belongs to (gt-46rk) exists because
// gt done's refusal left no trace.
//
// This journal converts the STATE into an EVENT. The state stays computed; the
// transitions get a durable line in the town log, one per episode, so that "did
// needs_mq_submit fire for ace?" is a question the log can answer at any later
// time.

// mqSubmitStateFile is the per-town record of which polecats are currently
// carrying the flag. It exists only to find the edges — it is a de-duplication
// ledger, never a source of truth about a polecat, and deleting it costs nothing
// beyond one repeated log line per still-flagged polecat.
const mqSubmitStateFile = "needs-mq-submit.json"

// mqSubmitEntryTTL prunes entries for polecats that were flagged and then
// vanished (nuked, renamed, rig removed) so the ledger cannot grow forever.
// Long enough that no live episode is forgotten; the log line already written
// for such an entry is unaffected.
const mqSubmitEntryTTL = 30 * 24 * time.Hour

// MQSubmitObservation identifies the polecat a disposition was computed for, and
// the surface that computed it.
type MQSubmitObservation struct {
	Rig     string
	Polecat string
	Issue   string
	Branch  string
	// Source names the command that observed the condition ("polecat-list",
	// "check-recovery"). A reader reconstructing an episode needs to know which
	// surface saw it, because the surfaces do not gather the same facts.
	Source string
}

func (o MQSubmitObservation) key() string {
	return o.Rig + "/" + o.Polecat
}

func (o MQSubmitObservation) agent() string {
	return fmt.Sprintf("%s/polecats/%s", o.Rig, o.Polecat)
}

type mqSubmitEntry struct {
	Reason    string    `json:"reason,omitempty"`
	MQStatus  string    `json:"mq_status,omitempty"`
	Issue     string    `json:"issue,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Source    string    `json:"source,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
}

type mqSubmitState struct {
	Polecats map[string]mqSubmitEntry `json:"polecats"`
}

// MQSubmitStatePath returns the ledger path for a town.
func MQSubmitStatePath(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", mqSubmitStateFile)
}

// RecordNeedsMQSubmit turns one surface's computed disposition into a durable
// record of the needs_mq_submit EDGES, and reports whether it wrote a log line.
//
// Rising edge: the polecat was not flagged and now is. Logged once per episode,
// not once per observation — the witness reads these surfaces on a loop, and a
// line per read would bury the transition it exists to mark.
//
// Falling edge: logged ONLY when the disposition carries proof that the
// merge-queue question was asked and answered — mq_status submitted or
// not_required. A surface that never ran the check reports needs_mq_submit=false
// because it has no facts, not because the condition cleared (the inventory
// surface deliberately gathers no git state at all), and a "resolved" line
// written from that silence would be the same lie in the opposite direction as
// the one this bead is about. An unproven false leaves the entry standing, so
// the next surface that does look still finds the episode open.
func RecordNeedsMQSubmit(townRoot string, obs MQSubmitObservation, d WorkstateDisposition) (bool, error) {
	if townRoot == "" || obs.Rig == "" || obs.Polecat == "" {
		return false, nil
	}
	if handled, err := guardTestMQSubmitJournal(townRoot); handled {
		return false, err
	}

	state, err := loadMQSubmitState(townRoot)
	if err != nil {
		return false, err
	}
	key := obs.key()
	prev, flagged := state.Polecats[key]

	if d.NeedsMQSubmit {
		if flagged {
			return false, nil
		}
		now := time.Now()
		state.Polecats[key] = mqSubmitEntry{
			Reason:    d.Reason,
			MQStatus:  d.MQStatus,
			Issue:     obs.Issue,
			Branch:    obs.Branch,
			Source:    obs.Source,
			FirstSeen: now,
		}
		if err := saveMQSubmitState(townRoot, state, now); err != nil {
			return false, err
		}
		// The log line goes out after the ledger is committed. The other order
		// risks a line the ledger has no memory of, which would re-log the same
		// rising edge on every subsequent read.
		if err := logMQSubmitEvent(townRoot, townlog.EventNeedsMQSubmit, obs, mqSubmitRaisedContext(obs, d)); err != nil {
			return false, err
		}
		return true, nil
	}

	if !flagged || !mqSubmitResolutionProven(d) {
		return false, nil
	}
	now := time.Now()
	delete(state.Polecats, key)
	if err := saveMQSubmitState(townRoot, state, now); err != nil {
		return false, err
	}
	if err := logMQSubmitEvent(townRoot, townlog.EventNeedsMQSubmitCleared, obs, mqSubmitClearedContext(obs, d, prev, now)); err != nil {
		return false, err
	}
	return true, nil
}

// mqSubmitResolutionProven reports whether a needs_mq_submit=false disposition
// is evidence that the condition cleared, rather than evidence that nobody
// looked. Only the two merge-queue answers DecideWorkstate reaches by actually
// consulting the queue count; every other route to false — blocked on git,
// blocked on a hook, lookup failed, or a surface that never set MQCheckRequired
// — leaves mq_status empty or unknown and proves nothing.
func mqSubmitResolutionProven(d WorkstateDisposition) bool {
	return d.MQStatus == "submitted" || d.MQStatus == "not_required"
}

func mqSubmitRaisedContext(obs MQSubmitObservation, d WorkstateDisposition) string {
	fields := []string{
		"reason=" + orUnknown(d.Reason),
		"mq_status=" + orUnknown(d.MQStatus),
		"verdict=" + orUnknown(d.Verdict),
	}
	fields = appendMQSubmitIdentity(fields, obs)
	if len(d.Blockers) > 0 {
		fields = append(fields, "blockers="+strings.Join(d.Blockers, "; "))
	}
	return strings.Join(fields, " ")
}

func mqSubmitClearedContext(obs MQSubmitObservation, d WorkstateDisposition, prev mqSubmitEntry, now time.Time) string {
	fields := []string{
		"mq_status=" + orUnknown(d.MQStatus),
		"was=" + orUnknown(prev.Reason),
	}
	fields = appendMQSubmitIdentity(fields, obs)
	if !prev.FirstSeen.IsZero() {
		fields = append(fields,
			"flagged_for="+now.Sub(prev.FirstSeen).Round(time.Second).String(),
			"first_seen="+prev.FirstSeen.Format(time.RFC3339),
		)
	}
	return strings.Join(fields, " ")
}

func appendMQSubmitIdentity(fields []string, obs MQSubmitObservation) []string {
	if obs.Issue != "" {
		fields = append(fields, "bead="+obs.Issue)
	}
	if obs.Branch != "" {
		fields = append(fields, "branch="+obs.Branch)
	}
	if obs.Source != "" {
		fields = append(fields, "source="+obs.Source)
	}
	return fields
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func logMQSubmitEvent(townRoot string, eventType townlog.EventType, obs MQSubmitObservation, context string) error {
	return townlog.NewLogger(townRoot).Log(eventType, obs.agent(), context)
}

func loadMQSubmitState(townRoot string) (mqSubmitState, error) {
	state := mqSubmitState{Polecats: map[string]mqSubmitEntry{}}
	data, err := os.ReadFile(MQSubmitStatePath(townRoot)) //nolint:gosec // G304: path is derived from the town root
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, fmt.Errorf("reading needs_mq_submit ledger: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		// A corrupt ledger must not stop the surface that was only reporting on a
		// polecat. Start over: the cost is at most one repeated rising-edge line.
		return mqSubmitState{Polecats: map[string]mqSubmitEntry{}}, nil
	}
	if state.Polecats == nil {
		state.Polecats = map[string]mqSubmitEntry{}
	}
	return state, nil
}

func saveMQSubmitState(townRoot string, state mqSubmitState, now time.Time) error {
	for key, entry := range state.Polecats {
		if !entry.FirstSeen.IsZero() && now.Sub(entry.FirstSeen) > mqSubmitEntryTTL {
			delete(state.Polecats, key)
		}
	}

	path := MQSubmitStatePath(townRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating needs_mq_submit ledger directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding needs_mq_submit ledger: %w", err)
	}
	data = append(data, '\n')

	// Written through a temp file and renamed: several surfaces compute this
	// verdict, they run concurrently, and a torn ledger read as corrupt would
	// re-log every open episode.
	tmp, err := os.CreateTemp(filepath.Dir(path), mqSubmitStateFile+".*")
	if err != nil {
		return fmt.Errorf("creating needs_mq_submit ledger temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing needs_mq_submit ledger: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("closing needs_mq_submit ledger: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replacing needs_mq_submit ledger: %w", err)
	}
	return nil
}

// guardTestMQSubmitJournal keeps a unit test from stamping episodes into a live
// town's ledger. townlog guards its own file on the same boundary; the ledger
// needs the guard too, because an entry written there suppresses the real rising
// edge line for a production polecat — a test could silence the record instead
// of merely adding to it.
func guardTestMQSubmitJournal(townRoot string) (handled bool, err error) {
	if !testing.Testing() {
		return false, nil
	}
	if testguard.Disposable(townRoot) || testguard.Authorized(townRoot) {
		return false, nil
	}
	return true, testguard.Refusal("record needs_mq_submit", MQSubmitStatePath(townRoot), "town root", townRoot)
}

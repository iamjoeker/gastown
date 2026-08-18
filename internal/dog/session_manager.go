// Package dog provides dog session management for Deacon's helper workers.
package dog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// Session errors
var (
	ErrSessionRunning  = errors.New("session already running")
	ErrSessionNotFound = errors.New("session not found")
)

// SessionManager handles dog session lifecycle.
type SessionManager struct {
	tmux     *tmux.Tmux
	mgr      *Manager
	townRoot string
}

// NewSessionManager creates a new dog session manager.
// The Manager parameter is used to sync persistent dog state (idle/working)
// when sessions start and stop.
func NewSessionManager(t *tmux.Tmux, townRoot string, mgr *Manager) *SessionManager {
	return &SessionManager{
		tmux:     t,
		mgr:      mgr,
		townRoot: townRoot,
	}
}

// SessionStartOptions configures dog session startup.
type SessionStartOptions struct {
	// WorkDesc is the work description (formula or bead ID) for the startup prompt.
	WorkDesc string

	// AgentOverride specifies an alternate agent (e.g., "gemini", "claude-haiku").
	AgentOverride string
}

// SessionInfo contains information about a running dog session.
type SessionInfo struct {
	// DogName is the dog name.
	DogName string `json:"dog_name"`

	// SessionID is the tmux session identifier.
	SessionID string `json:"session_id"`

	// Running indicates if the session is currently active.
	Running bool `json:"running"`

	// Attached indicates if someone is attached to the session.
	Attached bool `json:"attached,omitempty"`

	// Created is when the session was created.
	Created time.Time `json:"created,omitempty"`
}

// SessionName generates the tmux session name for a dog.
// Pattern: hq-dog-{name}
// Dogs are town-level (managed by deacon), so they use the hq- prefix.
// We use "hq-dog-" instead of "hq-deacon-" to avoid tmux prefix-matching
// collisions with the "hq-deacon" session.
func (m *SessionManager) SessionName(dogName string) string {
	return fmt.Sprintf("hq-dog-%s", dogName)
}

// kennelPath returns the path to the dog's kennel directory.
func (m *SessionManager) kennelPath(dogName string) string {
	return filepath.Join(m.townRoot, "deacon", "dogs", dogName)
}

// Start creates and starts a new session for a dog.
// Dogs run agent sessions that check mail for work and execute formulas.
func (m *SessionManager) Start(dogName string, opts SessionStartOptions) error {
	kennelDir := m.kennelPath(dogName)
	if _, err := os.Stat(kennelDir); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrDogNotFound, dogName)
	}

	sessionID := m.SessionName(dogName)

	// Kill any existing zombie session (tmux alive but agent dead).
	_, err := session.KillExistingSession(m.tmux, sessionID, true)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSessionRunning, sessionID)
	}

	// Build instructions for the dog.
	// For plugin work, explicitly direct the dog to read mail for the full
	// plugin instructions rather than trying to locate the plugin locally.
	// This prevents dogs from scanning their worktree's plugins/ directory
	// and escalating "plugin not found" when the plugin is town-level.
	workInfo := ""
	if opts.WorkDesc != "" {
		if strings.HasPrefix(opts.WorkDesc, "plugin:") {
			pluginName := strings.TrimPrefix(opts.WorkDesc, "plugin:")
			workInfo = fmt.Sprintf(" Plugin %s dispatched — full instructions are in your mail. Do NOT look for the plugin locally; read mail instead.", pluginName)
		} else {
			workInfo = fmt.Sprintf(" Work assigned: %s.", opts.WorkDesc)
		}
	}
	instructions := fmt.Sprintf("I am Dog %s.%s IMPORTANT: If your hook is empty and you have no mail, WAIT — the dispatcher is still setting up your assignment. Do NOT search for work, scan directories, or take autonomous action. Check hook (`"+cli.Name()+" hook`) and mail (`"+cli.Name()+" mail inbox`). If neither has work, wait 10 seconds and re-check. Execute only assigned work. When done, run `"+cli.Name()+" dog done` — this clears your work and auto-terminates the session.", dogName, workInfo)

	// Use unified session lifecycle.
	theme := tmux.DogTheme()
	_, err = session.StartSession(m.tmux, session.SessionConfig{
		SessionID: sessionID,
		WorkDir:   kennelDir,
		Role:      "dog",
		TownRoot:  m.townRoot,
		AgentName: dogName,
		Beacon: session.BeaconConfig{
			Recipient: session.BeaconRecipient("dog", dogName, ""),
			Sender:    "deacon",
			Topic:     "assigned",
		},
		Instructions:   instructions,
		AgentOverride:  opts.AgentOverride,
		Theme:          &theme,
		WaitForAgent:   true,
		WaitFatal:      true,
		AcceptBypass:   true,
		ReadyDelay:     true,
		VerifySurvived: true,
		TrackPID:       true,
	})
	if err != nil {
		return err
	}

	// Update persistent state to working
	if m.mgr != nil {
		if err := m.mgr.SetState(dogName, StateWorking); err != nil {
			// Log but don't fail - session is running, state sync is best-effort
			m.warn(dogName, "failed to set dog %s state to working: %v", dogName, err)
		}
	}

	m.record(dogName, "session start: %s (work=%q)", sessionID, opts.WorkDesc)

	return nil
}

// warn reports a session-lifecycle problem to stderr and to the dog's durable
// session log. stderr alone reaches only whoever is watching at this instant
// (gt-wlco); the log is what anyone reads afterwards.
func (m *SessionManager) warn(dogName, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
	m.record(dogName, "%s", "WARN "+msg)
}

// record appends to the dog's durable session log, best-effort.
func (m *SessionManager) record(dogName, format string, args ...any) {
	if m.mgr == nil {
		return
	}
	if err := m.mgr.LogEvent(dogName, format, args...); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not record to %s session log: %v\n", dogName, err)
	}
}

// Stop terminates a dog session.
func (m *SessionManager) Stop(dogName string, force bool) error {
	sessionID := m.SessionName(dogName)

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return ErrSessionNotFound
	}

	// Try graceful shutdown first
	if !force {
		_ = m.tmux.SendKeysRaw(sessionID, "C-c")
		session.WaitForSessionExit(m.tmux, sessionID, constants.GracefulShutdownTimeout)
	}

	if err := m.tmux.KillSessionWithProcesses(sessionID); err != nil {
		// record, not warn: the error is returned, so the caller owns the visible
		// half of the report. The log entry is the half that outlives the pane.
		m.record(dogName, "WARN killing session %s: %v", sessionID, err)
		return fmt.Errorf("killing session: %w", err)
	}

	// Update persistent state to idle so dog is available for reassignment
	if m.mgr != nil {
		if err := m.mgr.SetState(dogName, StateIdle); err != nil {
			m.warn(dogName, "failed to set dog %s state to idle: %v", dogName, err)
		}
	}

	m.record(dogName, "session stop: %s terminated (force=%v)", sessionID, force)

	return nil
}

// IsRunning checks if a dog session is active.
//
// This answers "does the tmux session exist", which is NOT the same question as
// "can anything in it do work" — a session whose agent process has died still
// answers yes. Dispatch decisions must use HasLiveAgent instead.
func (m *SessionManager) IsRunning(dogName string) (bool, error) {
	sessionID := m.SessionName(dogName)
	return m.tmux.HasSession(sessionID)
}

// HasLiveAgent reports whether the dog's session exists AND an agent process is
// alive inside it.
//
// This is the verdict dispatch must gate on, not IsRunning. A dog session
// outlives its agent: the pane is still there, tmux still lists the session, and
// has-session still says yes, but nothing in it can read mail or run a plugin.
// Delivering into that session destroys the dispatch silently (gt-p2e7) — the
// nudge is typed at a corpse, the mail stays open, the dog stays in
// StateWorking, and the work product never appears. Dogs are the one agent
// whose output is a file on disk rather than a branch or a bead, so nothing
// downstream notices the miss; six dolt backups went missing in a single day
// before anyone tallied artifacts against dispatches.
//
// An error means the liveness probe itself failed. It is returned rather than
// folded into the bool so callers can distinguish "confirmed dead" from
// "unknown" — those two warrant opposite actions, and only one of them is safe
// to kill a session over.
func (m *SessionManager) HasLiveAgent(dogName string) (bool, error) {
	sessionID := m.SessionName(dogName)

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return false, fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return false, nil
	}

	alive, err := m.tmux.IsAgentAliveChecked(sessionID)
	if err != nil {
		return false, fmt.Errorf("checking agent liveness in %s: %w", sessionID, err)
	}
	return alive, nil
}

// Status returns detailed status for a dog session.
func (m *SessionManager) Status(dogName string) (*SessionInfo, error) {
	sessionID := m.SessionName(dogName)

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("checking session: %w", err)
	}

	info := &SessionInfo{
		DogName:   dogName,
		SessionID: sessionID,
		Running:   running,
	}

	if !running {
		return info, nil
	}

	tmuxInfo, err := m.tmux.GetSessionInfo(sessionID)
	if err != nil {
		return info, nil
	}

	info.Attached = tmuxInfo.Attached

	return info, nil
}

// GetPane returns the pane ID for a dog session.
func (m *SessionManager) GetPane(dogName string) (string, error) {
	sessionID := m.SessionName(dogName)

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return "", ErrSessionNotFound
	}

	// Get pane ID from session
	pane, err := m.tmux.GetPaneID(sessionID)
	if err != nil {
		return "", fmt.Errorf("getting pane: %w", err)
	}

	return pane, nil
}

// EnsureRunning ensures a dog session has a live agent in it, starting one if
// needed. Returns the pane ID.
//
// The gate is HasLiveAgent, not IsRunning: a session that exists but has lost
// its agent must be replaced, not reused, or the dispatch about to be delivered
// into it is destroyed (gt-p2e7). Start already handles the replacement
// correctly — KillExistingSession(checkAlive=true) tears down a session whose
// agent is dead and refuses one whose agent is alive — it was simply never
// reached, because mere existence looked like health.
func (m *SessionManager) EnsureRunning(dogName string, opts SessionStartOptions) (string, error) {
	live, probeErr := m.HasLiveAgent(dogName)
	if probeErr != nil {
		// Liveness unknown. Starting here would mean tearing down a session on
		// the strength of a probe that just failed, so leave it alone and let
		// GetPane report whether there is anything to deliver into. Callers that
		// plan a delivery path treat an unknown probe as "up" for the same
		// reason: a redundant nudge at a dead session is a no-op, a suppressed
		// one at a live session strands the dispatch.
		return m.GetPane(dogName)
	}

	if !live {
		if startErr := m.Start(dogName, opts); startErr != nil {
			// Start refuses to displace a session whose agent is alive, and it
			// reports that refusal as ErrSessionRunning — but it maps EVERY
			// KillExistingSession failure to that same error, so "a live agent
			// beat us here" and "the kill failed" are indistinguishable from the
			// error alone. Only the first is safe to continue on, so re-probe
			// instead of trusting the label.
			live, probeErr = m.HasLiveAgent(dogName)
			if probeErr != nil || !live {
				return "", startErr
			}
		}
	}

	return m.GetPane(dogName)
}

// Package dog manages Dogs - Deacon's helper workers for infrastructure tasks.
// Dogs are reusable workers with multi-rig worktrees, managed by the Deacon.
// Unlike polecats (single-rig, ephemeral sessions), dogs handle cross-rig infrastructure work.
package dog

import (
	"time"
)

// State represents a dog's operational state.
type State string

const (
	// StateIdle means the dog is available for work.
	StateIdle State = "idle"
	// StateWorking means the dog is executing a task.
	StateWorking State = "working"
)

// ExecState is a dog's observed execution state.
//
// State (above) is pool *intent* — what .dog.json says the dispatcher meant to
// happen. ExecState is what is actually true, derived by joining that intent
// with tmux session liveness and open dispatch mail. The two disagree exactly
// when something has gone wrong, which is why intent alone must never be read
// as health.
type ExecState string

const (
	// ExecIdle: pool idle, no session, no pending dispatch. Genuinely available.
	ExecIdle ExecState = "idle"
	// ExecWorking: pool working and the session is alive. Normal execution.
	ExecWorking ExecState = "working"
	// ExecStalled: pool working but the session is gone. The work is not
	// happening and nothing will make it happen.
	ExecStalled ExecState = "stalled"
	// ExecPending: pool idle with dispatch mail still open and no session to
	// read it. The dispatch has an assignee that cannot execute it.
	ExecPending ExecState = "pending"
	// ExecOrphan: pool idle but a session is still alive. The session outlived
	// its assignment.
	ExecOrphan ExecState = "orphan"
)

// Healthy reports whether an execution state is a normal steady state.
func (e ExecState) Healthy() bool {
	return e == ExecIdle || e == ExecWorking
}

// DeriveExecState joins pool intent with observed session liveness and open
// dispatch count to produce the dog's real execution state.
//
// Precedence matters: a working dog with no session is stalled regardless of
// its mail, and an idle dog with a live session is an orphan regardless of its
// mail — in both cases the session/intent mismatch is the more actionable fact.
func DeriveExecState(poolState State, sessionRunning bool, openDispatches int) ExecState {
	if poolState == StateWorking {
		if sessionRunning {
			return ExecWorking
		}
		return ExecStalled
	}
	if sessionRunning {
		return ExecOrphan
	}
	if openDispatches > 0 {
		return ExecPending
	}
	return ExecIdle
}

// Dog represents a Deacon helper worker.
type Dog struct {
	Name          string            // Dog name (e.g., "alpha")
	State         State             // Current state
	Path          string            // Path to kennel dir (~/gt/deacon/dogs/<name>)
	Worktrees     map[string]string // Rig name -> worktree path
	LastActive    time.Time         // Last activity timestamp
	Work          string            // Current work assignment (bead ID or molecule)
	WorkStartedAt time.Time         // When current work was assigned
	CreatedAt     time.Time         // When dog was added to kennel
}

// DogState is the persistent state stored in .dog.json.
type DogState struct {
	Name          string            `json:"name"`
	State         State             `json:"state"`
	LastActive    time.Time         `json:"last_active"`
	Work          string            `json:"work,omitempty"`            // Current work assignment
	WorkStartedAt time.Time         `json:"work_started_at,omitempty"` // When work was assigned
	Worktrees     map[string]string `json:"worktrees,omitempty"`       // Rig -> path (for verification)
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

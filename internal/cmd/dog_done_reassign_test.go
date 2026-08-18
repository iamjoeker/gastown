package cmd

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/dog"
)

// dogStateFunc adapts a function to dogStateReader.
type dogStateFunc func(name string) (*dog.Dog, error)

func (f dogStateFunc) Get(name string) (*dog.Dog, error) { return f(name) }

// idleDog and workingDog are the two states that decide whether `gt dog done`
// may kill the session it is sitting in.
func idleDog() *dog.Dog { return &dog.Dog{Name: "alpha", State: dog.StateIdle} }
func workingDog() *dog.Dog {
	return &dog.Dog{Name: "alpha", State: dog.StateWorking, Work: "plugin:dolt-backup"}
}

// =============================================================================
// gt-p2e7: the gt dog done termination window
//
// runDogDone clears the dog's work and then kills its tmux session. Between
// those two acts the dog is IDLE with a LIVE SESSION, which is exactly the state
// gt dog dispatch selects for: GetIdleDog picks it, AssignWorkIfIdle succeeds,
// planDispatchDelivery sees a live agent so the mail notification becomes the
// sole delivery, and EnsureRunning starts nothing because a session is already
// there. The kill then lands on the agent that was about to run the work.
//
// Nothing errors. The dispatch mail stays open, the dog stays in StateWorking,
// and the dolt backup — a file on disk, checked by nobody — never appears. Six
// went missing on dog alpha in one day, against a 7-for-7 control group on the
// other dogs, before anyone tallied artifacts against dispatches.
//
// waitForDogReassignment is the guard: spend the termination delay watching the
// dog's state instead of ignoring it, and decline to kill a session that has
// been handed new work.
// =============================================================================

// The regression itself. A dispatch landing mid-window must be seen, and must be
// seen promptly rather than at the end of the wait — the nudge is already in
// flight at the pane we are deciding whether to destroy.
func TestWaitForDogReassignment_DetectsDispatchInsideTheWindow(t *testing.T) {
	reads := 0
	reader := dogStateFunc(func(string) (*dog.Dog, error) {
		reads++
		if reads >= 3 {
			return workingDog(), nil
		}
		return idleDog(), nil
	})

	start := time.Now()
	reassigned, err := waitForDogReassignment(reader, "alpha", time.Second, 5*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("waitForDogReassignment returned error %v, want nil", err)
	}
	if !reassigned {
		t.Fatal("a dispatch that landed during the termination delay was not seen. " +
			"gt dog done would now kill the session holding it, and the dispatch " +
			"would be lost with no error anywhere (gt-p2e7)")
	}
	if elapsed >= time.Second {
		t.Errorf("waited %s for a reassignment visible after ~10ms; the watch must "+
			"return as soon as work appears, not at the deadline", elapsed)
	}
}

// The ordinary case must still terminate. A dog that stays idle for the whole
// delay is safe to kill, and refusing to would leak an agent per dispatch.
func TestWaitForDogReassignment_IdleThroughoutAuthorizesTermination(t *testing.T) {
	reader := dogStateFunc(func(string) (*dog.Dog, error) { return idleDog(), nil })

	start := time.Now()
	reassigned, err := waitForDogReassignment(reader, "alpha", 40*time.Millisecond, 5*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("waitForDogReassignment returned error %v, want nil", err)
	}
	if reassigned {
		t.Fatal("an idle dog was reported as reassigned; its session would never be " +
			"killed and every dispatch would leak an agent")
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("returned after %s, want at least the full 40ms delay: the delay is "+
			"what lets the agent read its own success output", elapsed)
	}
}

// Fail direction. An unreadable state file must not read as "idle, safe to
// kill". The two mistakes are not symmetric: a session left up is an idle agent
// that the health check reports as an orphan and reaps, while a session killed
// over a fresh dispatch destroys the work outright and silently.
func TestWaitForDogReassignment_UnreadableStateDoesNotAuthorizeTermination(t *testing.T) {
	readErr := errors.New("dolt: connection refused")
	reader := dogStateFunc(func(string) (*dog.Dog, error) { return nil, readErr })

	reassigned, err := waitForDogReassignment(reader, "alpha", 40*time.Millisecond, 5*time.Millisecond)

	if !reassigned {
		t.Fatal("unreadable dog state authorized a session kill. Unknown is not idle: " +
			"killing on a failed read is how a dispatch disappears without a trace")
	}
	if !errors.Is(err, readErr) {
		t.Errorf("err = %v, want the read failure surfaced so the operator learns "+
			"the session was spared for lack of information, not because of new work", err)
	}
}

// A torn or half-applied state write can leave State idle with Work still set.
// Either signal alone means the dog is holding something.
func TestWaitForDogReassignment_WorkFieldAloneCountsAsHeld(t *testing.T) {
	reader := dogStateFunc(func(string) (*dog.Dog, error) {
		return &dog.Dog{Name: "alpha", State: dog.StateIdle, Work: "plugin:dolt-backup"}, nil
	})

	reassigned, err := waitForDogReassignment(reader, "alpha", 0, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForDogReassignment returned error %v, want nil", err)
	}
	if !reassigned {
		t.Fatal("a dog carrying work was treated as idle because its State field said " +
			"idle; both fields describe the assignment and either one being set means " +
			"the session is in use")
	}
}

// With no delay to spend there is still a decision to make, so there must still
// be a read. A zero delay must not degrade to the old unconditional kill.
func TestWaitForDogReassignment_ZeroDelayStillChecksState(t *testing.T) {
	reads := 0
	reader := dogStateFunc(func(string) (*dog.Dog, error) {
		reads++
		return workingDog(), nil
	})

	reassigned, err := waitForDogReassignment(reader, "alpha", 0, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForDogReassignment returned error %v, want nil", err)
	}
	if reads == 0 {
		t.Fatal("zero delay skipped the state read entirely, restoring the " +
			"unconditional kill this guard replaces")
	}
	if !reassigned {
		t.Fatal("a working dog was reported as safe to terminate")
	}
}

// dogHoldsWork is the predicate the whole guard rests on; pin its truth table
// directly so a future edit cannot quietly narrow it.
func TestDogHoldsWork(t *testing.T) {
	tests := []struct {
		name     string
		state    *dog.Dog
		getErr   error
		wantHeld bool
	}{
		{name: "idle with no work", state: idleDog(), wantHeld: false},
		{name: "working", state: workingDog(), wantHeld: true},
		{
			name:     "idle state but work still recorded",
			state:    &dog.Dog{Name: "alpha", State: dog.StateIdle, Work: "plugin:dolt-backup"},
			wantHeld: true,
		},
		{name: "state unreadable", getErr: errors.New("boom"), wantHeld: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := dogStateFunc(func(string) (*dog.Dog, error) {
				if tt.getErr != nil {
					return nil, tt.getErr
				}
				return tt.state, nil
			})
			held, err := dogHoldsWork(reader, "alpha")
			if held != tt.wantHeld {
				t.Errorf("dogHoldsWork() = %v, want %v", held, tt.wantHeld)
			}
			if (err != nil) != (tt.getErr != nil) {
				t.Errorf("dogHoldsWork() err = %v, want error presence %v", err, tt.getErr != nil)
			}
		})
	}
}

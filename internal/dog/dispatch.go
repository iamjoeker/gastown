package dog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/mail"
)

// DispatchSubjectPrefix is the subject prefix the Deacon uses for plugin
// dispatch mail. Dispatch mail is the durable half of `gt dog dispatch`:
// the bead survives even when the session that was supposed to execute it
// does not.
const DispatchSubjectPrefix = "Plugin: "

// DefaultStaleDispatchAfter is how long a dispatch may stay open before it is
// treated as an alarm condition. Dispatches are minutes of work; anything
// still open after this either lost its session or never received its
// instruction.
const DefaultStaleDispatchAfter = 30 * time.Minute

// DefaultDispatchAlarmCooldown throttles repeat escalations for the same dog.
// Health checks run every patrol cycle; without a cooldown a single stranded
// dispatch would escalate on every cycle.
const DefaultDispatchAlarmCooldown = 6 * time.Hour

// DispatchMailbox is the subset of *mail.Mailbox needed to inspect and reclaim
// a dog's dispatch mail.
type DispatchMailbox interface {
	List() ([]*mail.Message, error)
	Archive(id string) error
}

// DispatchScan summarizes the open dispatch mail sitting in a dog's inbox.
type DispatchScan struct {
	// Open is the number of open dispatch messages.
	Open int `json:"open"`

	// OldestAge is the age of the oldest open dispatch. Zero when Open is 0.
	OldestAge time.Duration `json:"oldest_age,omitempty"`

	// IDs are the message IDs of the open dispatches, oldest first.
	IDs []string `json:"ids,omitempty"`
}

// DogAddress returns the mail address for a dog.
func DogAddress(dogName string) string {
	return fmt.Sprintf("deacon/dogs/%s", dogName)
}

// IsDispatchMail reports whether msg is a Deacon plugin dispatch addressed
// directly to dogAddress.
//
// The sender check keeps reclamation scoped to Deacon dispatches so a CC or a
// human message with a similar subject is never archived out from under the
// dog. The recipient check keeps CC'd copies from being treated as the
// primary dispatch.
func IsDispatchMail(msg *mail.Message, dogAddress string) bool {
	if msg == nil {
		return false
	}
	if !strings.HasPrefix(msg.Subject, DispatchSubjectPrefix) {
		return false
	}
	if mail.AddressToIdentity(msg.To) != mail.AddressToIdentity(dogAddress) {
		return false
	}
	switch mail.AddressToIdentity(msg.From) {
	case "deacon/", "daemon":
		return true
	default:
		return false
	}
}

// ScanDispatchMail reports the open dispatch mail in a dog's inbox.
//
// Ages are measured against now so callers can test deterministically. A
// message with a zero timestamp contributes to Open but not to OldestAge —
// an unknown age must not be reported as "brand new".
func ScanDispatchMail(mb DispatchMailbox, dogAddress string, now time.Time) (DispatchScan, error) {
	var scan DispatchScan
	if mb == nil {
		return scan, nil
	}

	messages, err := mb.List()
	if err != nil {
		return scan, fmt.Errorf("listing mailbox for %s: %w", dogAddress, err)
	}

	oldest := time.Time{}
	for _, msg := range messages {
		if !IsDispatchMail(msg, dogAddress) {
			continue
		}
		scan.Open++
		scan.IDs = append(scan.IDs, msg.ID)
		if msg.Timestamp.IsZero() {
			continue
		}
		if oldest.IsZero() || msg.Timestamp.Before(oldest) {
			oldest = msg.Timestamp
		}
	}

	if !oldest.IsZero() {
		if age := now.Sub(oldest); age > 0 {
			scan.OldestAge = age
		}
	}
	return scan, nil
}

// ReclaimDispatchMail archives every open dispatch in a dog's inbox and
// returns how many were archived.
//
// This is what makes session death fail a dispatch instead of orphaning it:
// once the assignee's session is gone the dispatch can never be executed by
// that dog, so leaving it open only hides the failure.
func ReclaimDispatchMail(mb DispatchMailbox, dogAddress string) (int, error) {
	if mb == nil {
		return 0, nil
	}

	messages, err := mb.List()
	if err != nil {
		return 0, fmt.Errorf("listing mailbox for %s: %w", dogAddress, err)
	}

	archived := 0
	var firstErr error
	for _, msg := range messages {
		if !IsDispatchMail(msg, dogAddress) {
			continue
		}
		if err := mb.Archive(msg.ID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("archiving %s: %w", msg.ID, err)
			}
			continue
		}
		archived++
	}
	return archived, firstErr
}

// dispatchAlarmState is the on-disk record of the last alarm raised for a dog.
type dispatchAlarmState struct {
	LastAlarmAt time.Time `json:"last_alarm_at"`
}

// alarmStatePath returns the path of a dog's dispatch-alarm marker.
func (m *Manager) alarmStatePath(name string) string {
	return filepath.Join(m.dogDir(name), ".dispatch-alarm.json")
}

// ShouldAlarmDispatch reports whether a dispatch alarm for this dog is due,
// and records the alarm when it is.
//
// Returns true at most once per cooldown window. The marker lives in the dog's
// kennel directory, so removing the dog also forgets its alarm history. A
// marker that cannot be read is treated as "never alarmed" — failing toward
// raising the alarm is the safe direction for a condition whose whole defect
// was going unnoticed for twelve days.
func (m *Manager) ShouldAlarmDispatch(name string, cooldown time.Duration, now time.Time) bool {
	if err := validateDogName(name); err != nil {
		return false
	}

	path := m.alarmStatePath(name)
	if data, err := os.ReadFile(path); err == nil {
		var state dispatchAlarmState
		if jsonErr := json.Unmarshal(data, &state); jsonErr == nil {
			if !state.LastAlarmAt.IsZero() && now.Sub(state.LastAlarmAt) < cooldown {
				return false
			}
		}
	}

	// Best-effort record: if the write fails the alarm still fires, it just
	// may fire again next cycle. A lost alarm is worse than a repeated one.
	_ = atomicfile.WriteJSON(path, dispatchAlarmState{LastAlarmAt: now})
	return true
}

// ClearDispatchAlarm forgets a dog's alarm history so a fresh problem alarms
// immediately rather than waiting out a cooldown from a resolved one.
func (m *Manager) ClearDispatchAlarm(name string) {
	if err := validateDogName(name); err != nil {
		return
	}
	_ = os.Remove(m.alarmStatePath(name))
}

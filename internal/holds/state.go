// Package holds provides a durable, queryable registry for standing
// operational "do not X" directives (mayor holds): scope, threshold,
// reason, release condition, and who set them.
//
// Holds previously existed only as prose in mail threads, which is
// invisible to a cold-started agent — mail is not enumerable in one
// command, and (unlike this registry) is not guaranteed to survive
// session death. This registry fixes that: any agent can run
// `gt hold list` before taking a spending or dispatch action and see
// every active hold, regardless of which session set it or when.
package holds

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Hold is a single standing operational directive.
type Hold struct {
	ID               string `json:"id"`
	Scope            string `json:"scope"`                       // what is restricted, e.g. "session restart", "polecat dispatch"
	Threshold        string `json:"threshold,omitempty"`          // free-text condition, e.g. "cost > $50"
	Reason           string `json:"reason"`
	ReleaseCondition string `json:"release_condition,omitempty"`
	SetBy            string `json:"set_by"`
	SetAt            string `json:"set_at"`
	Released         bool   `json:"released"`
	ReleasedBy       string `json:"released_by,omitempty"`
	ReleasedAt       string `json:"released_at,omitempty"`
	ReleaseReason    string `json:"release_reason,omitempty"`
}

// Registry is the durable set of holds, stored at
// <townRoot>/.runtime/holds.json. Follows the pattern of
// scheduler/capacity.SchedulerState — a plain JSON file under .runtime,
// not a wisp, so it is not subject to TTL compaction or Dolt GC.
type Registry struct {
	NextID int    `json:"next_id"`
	Holds  []Hold `json:"holds"`
}

func registryFile(townRoot string) string {
	return filepath.Join(townRoot, ".runtime", "holds.json")
}

// Load reads the holds registry, returning an empty registry if the file
// doesn't exist. Absence means "no holds have ever been set" — the same
// convention SchedulerState uses for its zero value.
func Load(townRoot string) (*Registry, error) {
	path := registryFile(townRoot)
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed internally
	if err != nil {
		if os.IsNotExist(err) {
			return &Registry{}, nil
		}
		return nil, err
	}

	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

// Save writes the holds registry atomically (write-to-temp + rename), to
// avoid corruption from concurrent writers.
func Save(townRoot string, reg *Registry) error {
	path := registryFile(townRoot)
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".holds-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// Add appends a new active hold to the registry and returns it.
func (r *Registry) Add(scope, threshold, reason, releaseCondition, by string) Hold {
	r.NextID++
	h := Hold{
		ID:               fmt.Sprintf("hold-%d", r.NextID),
		Scope:            scope,
		Threshold:        threshold,
		Reason:           reason,
		ReleaseCondition: releaseCondition,
		SetBy:            by,
		SetAt:            time.Now().UTC().Format(time.RFC3339),
	}
	r.Holds = append(r.Holds, h)
	return h
}

// Find returns the hold with the given ID, or false if none matches.
func (r *Registry) Find(id string) (*Hold, bool) {
	for i := range r.Holds {
		if r.Holds[i].ID == id {
			return &r.Holds[i], true
		}
	}
	return nil, false
}

// Release marks a hold as released. Returns an error if the hold is
// unknown or already released.
func (r *Registry) Release(id, by, reason string) error {
	h, ok := r.Find(id)
	if !ok {
		return fmt.Errorf("no such hold: %s", id)
	}
	if h.Released {
		return fmt.Errorf("hold %s is already released (by %s at %s)", id, h.ReleasedBy, h.ReleasedAt)
	}
	h.Released = true
	h.ReleasedBy = by
	h.ReleasedAt = time.Now().UTC().Format(time.RFC3339)
	h.ReleaseReason = reason
	return nil
}

// Active returns all holds that have not been released, in the order
// they were set.
func (r *Registry) Active() []Hold {
	active := make([]Hold, 0, len(r.Holds))
	for _, h := range r.Holds {
		if !h.Released {
			active = append(active, h)
		}
	}
	return active
}

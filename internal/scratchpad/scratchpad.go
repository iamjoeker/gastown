// Package scratchpad implements the retention policy for per-session agent
// scratchpad directories.
//
// Every Claude Code session is handed a private working directory at
// $TMPDIR/claude-<uid>/<project-slug>/<session-id>/ (holding "scratchpad" and
// "tasks"). Nothing removes them when the session ends, so on a busy Gas Town
// box they accumulate until the /tmp tmpfs fills and unrelated work starts
// failing on "insufficient disk space" (gt-yb33).
//
// The hard part is not deleting — it is proving a session is dead. A scratchpad
// has no in-band liveness signal: the mtime can be hours old while the session
// is alive and idle, so an age-only sweep deletes a live agent's working files
// mid-task. That failure is unrecoverable and invisible until the agent trips
// over a file it wrote itself.
//
// Two facts, measured on the box that motivated this (2026-08-19, 13.7 GB
// across 2654 session directories), shape the policy:
//
//   - Age alone is not just unsafe, it is ineffective. Nothing in the tree was
//     older than 72h and ~9 GB of the 13.7 GB had been written in the previous
//     six hours. A 24h retention would have reclaimed 2 GB of 14 GB. Reclaim
//     has to come from dead sessions of any age, not from old sessions.
//   - A session directory's filesystem birth time lands within a couple of
//     seconds of its claude process's start time. That is the only usable link
//     between a pid and a session id: the id appears in neither the process
//     cmdline nor its environment, and the transcript is not held open.
//
// So death is proved by conjunction, never by a single signal. Classify only
// reports a directory sweepable when every one of these holds:
//
//  1. its birth time is known (an unknown birth is treated as possibly live);
//  2. no live claude process could own it — for every live process whose
//     project slug matches, the directory predates that process's start;
//  3. no live process is working inside it;
//  4. it is older than the forensic floor (MinAge) — agents cite these paths
//     in beads and handoffs, so same-day deletion loses evidence;
//  5. nothing in the directory has been written for Idle;
//  6. the session's transcript is absent or has been quiet for Idle, and no
//     live process has been running since before that transcript was last
//     written. These are the two guards for `claude --resume`, which adopts an
//     old session id and so inherits an old directory birth that rule 2 would
//     otherwise misread as dead.
//
// Selection is then pressure-driven rather than age-driven: below the
// high-water mark nothing is deleted at all, and above it the oldest dead
// scratchpads go first, stopping the moment the filesystem is projected back
// under the target. Forensics are kept for as long as there is room to keep
// them.
package scratchpad

import (
	"fmt"
	"sort"
	"time"
)

// Session is one <project-slug>/<session-id> scratchpad directory.
type Session struct {
	// ProjectSlug is the directory name Claude Code derives from the session's
	// working directory (every non-alphanumeric character becomes "-").
	ProjectSlug string

	// ID is the session UUID, which is also the directory name.
	ID string

	// Path is the absolute path of the session directory.
	Path string

	// Birth is the filesystem creation time of the session directory, which
	// tracks the owning process's start time. BirthKnown is false when the
	// platform or filesystem does not record it — callers must treat that as
	// "unknown", never as "old".
	Birth      time.Time
	BirthKnown bool

	// LastWrite is the newest modification time anywhere in the subtree.
	LastWrite time.Time

	// Bytes is the total apparent size of the subtree.
	Bytes int64
}

// Process is a live claude process that might own a scratchpad.
type Process struct {
	// PID and Start identify the process. Start is derived from elapsed time,
	// so it carries about a second of jitter — see Policy.StartSlack.
	PID   int
	Start time.Time

	// Cwd is the process working directory, empty when it could not be read.
	Cwd string

	// Slugs are the project slugs this process could own sessions under,
	// derived from its working directory and environment. Empty means the
	// process could not be attributed to any project, which makes it a
	// wildcard: it is then assumed capable of owning any session started after
	// it began.
	Slugs []string
}

// Attributable reports whether the process could be tied to specific projects.
func (p Process) Attributable() bool { return len(p.Slugs) > 0 }

// Policy holds the tunable thresholds of the retention rule.
type Policy struct {
	// Idle is how long a session must have written nothing — to its scratchpad
	// and to its transcript — before it counts as quiet.
	Idle time.Duration

	// MinAge is the forensic floor: a scratchpad younger than this is never
	// swept, however dead it looks.
	MinAge time.Duration

	// StartSlack widens the window matching a directory birth to a process
	// start, absorbing the second-resolution jitter of the process start time.
	StartSlack time.Duration

	// HighWater is the filesystem usage fraction (0-1) below which nothing is
	// swept at all.
	HighWater float64

	// Target is the filesystem usage fraction (0-1) the sweep stops at.
	Target float64
}

// DefaultPolicy returns the recommended thresholds.
//
// Idle and MinAge are deliberately short. The durable forensic record of a
// session is its transcript under ~/.claude*/projects/, which this package
// never touches; a scratchpad holds working files. The real protection against
// premature deletion is the liveness conjunction and the high-water gate, not a
// long retention — and on the measured box a long retention reclaimed almost
// nothing anyway.
func DefaultPolicy() Policy {
	return Policy{
		Idle:       2 * time.Hour,
		MinAge:     2 * time.Hour,
		StartSlack: 5 * time.Minute,
		HighWater:  0.80,
		Target:     0.60,
	}
}

// Verdict is the classification of one scratchpad.
type Verdict string

const (
	// VerdictSweep means every death check passed: no live session can own it.
	VerdictSweep Verdict = "sweep"

	// VerdictKeep means at least one check could not rule out a live owner.
	VerdictKeep Verdict = "keep"
)

// Decision pairs a session with its verdict and the reason for it.
type Decision struct {
	Session Session
	Verdict Verdict
	Reason  string
}

// Classify applies the death conjunction to each session.
//
// transcripts maps a session id to the newest modification time of its
// transcript across all account roots; a missing entry means no transcript was
// found, which is evidence of death rather than life (the transcript outlives
// the session, so its absence means the session was never recorded or has
// already been cleaned up).
//
// Every rule fails closed: anything Classify cannot establish yields
// VerdictKeep.
func Classify(sessions []Session, procs []Process, transcripts map[string]time.Time, p Policy, now time.Time) []Decision {
	decisions := make([]Decision, 0, len(sessions))
	for _, s := range sessions {
		decisions = append(decisions, classifyOne(s, procs, transcripts, p, now))
	}
	return decisions
}

func classifyOne(s Session, procs []Process, transcripts map[string]time.Time, p Policy, now time.Time) Decision {
	keep := func(format string, args ...any) Decision {
		return Decision{Session: s, Verdict: VerdictKeep, Reason: fmt.Sprintf(format, args...)}
	}

	// 1. Birth unknown — the pid-to-session link is unavailable, so nothing
	// below can prove no live process owns this directory.
	if !s.BirthKnown {
		return keep("birth time unavailable — cannot prove no live session owns it")
	}

	// 2 and 3. A live process could own it, either because its start time
	// precedes the directory's birth or because it is working inside it.
	transcript, hasTranscript := transcripts[s.ID]
	for _, proc := range procs {
		if isUnder(proc.Cwd, s.Path) {
			return keep("live claude pid %d is working inside it", proc.PID)
		}
		if proc.Attributable() && !ownsSlug(proc, s.ProjectSlug) {
			continue
		}
		// An unattributable process is a wildcard over every slug: with no
		// working directory to place it, it could be running any project.
		unknownProject := !proc.Attributable()

		if !s.Birth.Before(proc.Start.Add(-p.StartSlack)) {
			if unknownProject {
				return keep("live claude pid %d has an unreadable working directory and started before this session", proc.PID)
			}
			return keep("live claude pid %d in the same project started before this session", proc.PID)
		}
		// The directory predates this process, so the process did not create
		// it — but `claude --resume` adopts an old session id, and hence an old
		// directory, without changing its birth. A transcript written after
		// this process started is that adoption happening: some live process is
		// appending to this session right now.
		if hasTranscript && !transcript.Before(proc.Start) {
			return keep("transcript written after live claude pid %d started — session was resumed", proc.PID)
		}
	}

	// 4. Forensic floor.
	if age := now.Sub(s.Birth); age < p.MinAge {
		return keep("younger than the %s forensic floor (age %s)", short(p.MinAge), short(age))
	}

	// 5. Scratchpad quiet.
	if idle := now.Sub(s.LastWrite); idle < p.Idle {
		return keep("written %s ago, less than the %s idle window", short(idle), short(p.Idle))
	}

	// 6. Transcript quiet. A resumed session inherits an old directory birth,
	// so rule 2 cannot see it; its transcript is what still moves.
	if hasTranscript {
		if idle := now.Sub(transcript); idle < p.Idle {
			return keep("transcript written %s ago — session may have been resumed", short(idle))
		}
	}

	return Decision{
		Session: s,
		Verdict: VerdictSweep,
		Reason:  fmt.Sprintf("no live claude process can own it; quiet for %s", short(now.Sub(s.LastWrite))),
	}
}

func ownsSlug(p Process, slug string) bool {
	for _, s := range p.Slugs {
		if s == slug {
			return true
		}
	}
	return false
}

// Selection is the outcome of applying filesystem pressure to the decisions.
type Selection struct {
	// Selected are the scratchpads to delete, oldest first.
	Selected []Decision

	// Bytes is the total size of Selected.
	Bytes int64

	// Held counts sweepable scratchpads deliberately left in place — either
	// the filesystem is below the high-water mark or the target was already
	// reached. They are kept for forensics until the space is actually needed.
	Held int

	// HeldBytes is the total size of the held scratchpads.
	HeldBytes int64

	// UsedPercent is the filesystem usage the selection was computed against.
	UsedPercent float64

	// Triggered reports whether the high-water mark was reached.
	Triggered bool
}

// Select decides which sweepable scratchpads to actually delete.
//
// Below the high-water mark nothing is selected: a dead scratchpad is cheap to
// keep and expensive to lose, so it stays until the space is needed. Above it,
// the oldest dead scratchpads are taken first and only until the filesystem is
// projected back under the target.
//
// When all is true the pressure gate is bypassed and every sweepable
// scratchpad is selected — for an operator reclaiming deliberately.
func Select(decisions []Decision, totalBytes, usedBytes uint64, p Policy, all bool) Selection {
	sel := Selection{}
	if totalBytes > 0 {
		sel.UsedPercent = float64(usedBytes) / float64(totalBytes) * 100
	}

	sweepable := make([]Decision, 0, len(decisions))
	for _, d := range decisions {
		if d.Verdict == VerdictSweep {
			sweepable = append(sweepable, d)
		}
	}
	// Oldest first, so the freshest forensics survive longest.
	sort.SliceStable(sweepable, func(i, j int) bool {
		return sweepable[i].Session.LastWrite.Before(sweepable[j].Session.LastWrite)
	})

	sel.Triggered = totalBytes > 0 && float64(usedBytes) >= p.HighWater*float64(totalBytes)
	if all {
		sel.Selected = sweepable
		for _, d := range sweepable {
			sel.Bytes += d.Session.Bytes
		}
		return sel
	}
	if !sel.Triggered {
		sel.Held = len(sweepable)
		for _, d := range sweepable {
			sel.HeldBytes += d.Session.Bytes
		}
		return sel
	}

	targetBytes := p.Target * float64(totalBytes)
	remaining := float64(usedBytes)
	for _, d := range sweepable {
		if remaining <= targetBytes {
			sel.Held++
			sel.HeldBytes += d.Session.Bytes
			continue
		}
		sel.Selected = append(sel.Selected, d)
		sel.Bytes += d.Session.Bytes
		remaining -= float64(d.Session.Bytes)
	}
	return sel
}

// short renders a duration the way an operator reads it, without the
// sub-second noise time.Duration prints by default.
func short(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	default:
		return fmt.Sprintf("%.1fd", d.Hours()/24)
	}
}

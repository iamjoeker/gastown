# Scratchpad Retention

How Gas Town decides that an agent's `/tmp` scratchpad is safe to delete, and
why the obvious policy — delete anything older than N — is both dangerous and
useless here.

Command: `gt deacon sweep-scratchpads` (dry run unless `--apply`).

## The problem

Every Claude Code session is handed a private working directory:

```
$TMPDIR/claude-<uid>/<project-slug>/<session-id>/{scratchpad,tasks}
```

Nothing removes it when the session ends. On a box running a full town this
accumulates fast enough to exhaust the `/tmp` tmpfs, at which point unrelated
work starts failing with errors that name disk space rather than the cause —
`internal/polecat`'s disk guard blocked 16 tests this way in gt-yb33, while
`df /` showed 1.3T free because only the separate 31G `/tmp` tmpfs was full.

A scratchpad has no in-band liveness signal. Its mtime can be hours old while
the session is alive and idle, so deleting by age deletes live agents' working
files. That failure is unrecoverable and invisible until the agent trips over a
file it wrote itself.

## What the measurement showed

`/tmp/claude-1000` on the reference box, 2026-08-19: **13.7 GB across 2654
session directories**, plus 136 loose files (353 MB) that agents wrote directly
into the root, outside the convention.

Cumulative size by age of the newest write anywhere in each session directory:

| older than | directories | size |
|---|---|---|
| 1h | 2597 | 11.07 GB |
| 6h | 2402 | 4.51 GB |
| 12h | 2236 | 2.04 GB |
| 24h | 1979 | 2.03 GB |
| 48h | 479 | 0.01 GB |
| 72h | 0 | 0.00 GB |

Two conclusions:

1. **Age-based retention does not reclaim the space.** Nothing was older than
   72h and roughly 9 GB of the 13.7 GB had been written in the previous six
   hours. A 24h retention would have freed 2 GB of 14 GB. The reclaim has to
   come from *dead sessions of any age*, not from old sessions.
2. **Age-based retention deletes live work.** At that same moment, 2 of the 15
   sessions with a live process attached had been quiet for more than two hours
   — one for 4.7h, one holding 214 MB. A 2h age-only sweep would have destroyed
   both.

So the policy is liveness-driven, with age used only as a floor.

## Proving a session dead

The session id appears in neither the process command line nor its environment,
and the transcript is not held open, so there is no direct pid-to-session link.
What does exist: **a session directory's filesystem birth time lands within a
couple of seconds of its process's start time** (measured across 15 live
sessions: +1s or +2s, every one). Birth time is available on tmpfs via
`statx(STATX_BTIME)`.

A scratchpad is swept only when *all* of these hold. Any check that cannot be
evaluated keeps the directory:

| # | Check | What it rules out |
|---|---|---|
| 1 | Birth time is known | A platform without birth time cannot link any process to any session, so nothing below can conclude anything |
| 2 | No live claude process attributed to the same project started before it was born | The process that created it, and any process that could have created it via `/clear` |
| 3 | No live process is working inside it | An agent that `cd`'d into its own scratchpad |
| 4 | Older than `--min-age` (default 2h) | Forensic floor — agents cite these paths in beads and handoffs |
| 5 | Nothing written anywhere in the subtree for `--idle` (default 2h) | An agent actively writing files |
| 6 | Transcript absent or quiet for `--idle`, **and** not written after any live process started | `claude --resume`, which adopts an old session id and inherits its old directory birth |

Rule 6's second half is what makes resume safe: a resumed session's directory
birth predates its process, so rule 2 sees an orphan, but the transcript under
`~/.claude*/projects/` keeps moving while the process runs.

Rule 2 also fails closed on attribution. A live process whose working directory
cannot be read (hardened kernel, `lsof` absent) is treated as a wildcard that
could own any project, protecting every session younger than it.

Directories under a project that are not session UUIDs are never touched, and
neither are the loose files in the root — nothing can prove those dead, so the
sweep only counts and reports them.

## How much gets deleted

Selection is driven by filesystem pressure, not by age:

- Below `--high-water` (default 80% used) **nothing is deleted**. A dead
  scratchpad is cheap to keep and expensive to lose, so it stays until the
  space is actually needed.
- Above it, the oldest dead scratchpads go first and only until usage is
  projected back under `--target` (default 60%).
- `--all` bypasses both marks, for an operator reclaiming deliberately.

Each directory is re-checked immediately before deletion; one that was written
to since it was classified is skipped rather than removed.

On the reference box at 83% full this selected 2460 of 2562 dead scratchpads
(7.2 GB), holding 2.9 GB back for forensics, and kept all 15 live sessions.

## Where the sweep belongs

`gt deacon sweep-scratchpads` is a deacon subcommand and a **dry run by
default**: it prints what it would reclaim and exits.

Recommended operation:

1. The deacon patrol runs it in report mode (`--json`) and escalates when the
   filesystem is above the high-water mark. Reporting is always safe.
2. Applying stays an explicit call — the deacon or an operator running
   `--apply` — until a real report has been reviewed on the box in question.
   The liveness proof is verified, but deleting other agents' working files is
   an irreversible action taken on shared state, and the high-water gate means
   there is time to look first.

It is deliberately a separate command from the stranded Go build-directory
sweep. Those two consumers have nothing in common: a `go-build` directory is
provably dead once its owning process exits, while a scratchpad requires the
whole conjunction above. Composing them under one patrol step is fine;
collapsing them into one rule is not.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--root` | `$TMPDIR/claude-<uid>` | Scratchpad root |
| `--idle` | 2h | How long a session must be quiet before it counts as dead |
| `--min-age` | 2h | Forensic floor; never sweep anything younger |
| `--high-water` | 80 | Usage percent that triggers a sweep |
| `--target` | 60 | Usage percent to sweep down to |
| `--all` | off | Ignore the water marks and take every dead scratchpad |
| `--apply` | off | Actually delete |
| `--json` | off | Machine-readable report |
| `--verbose` | off | Every scratchpad with its verdict and reason |

## Related

- gt-yb33 — the tmpfs exhaustion that surfaced this, and the go-build sweep
- `docs/CLEANUP.md` — the full cleanup command catalog

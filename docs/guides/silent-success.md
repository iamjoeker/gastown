# Silent Success

**Rule: exit 0 must mean "the requested effect occurred", never "the command
ran".**

A crash is self-reporting. A command that exits 0, prints an affirmative
message, and changes no state is not — the operator receives positive
confirmation that the thing they asked for happened, and it did not.

This guide is the convention; apply it at code review. It exists because five
members of this class were found in a single night (gt-dr6t).

---

## The members (2026-08-17/18)

| Command | What it reported | What it did |
|---|---|---|
| `gt dog done` | nothing, exit 0 | archived zero dispatch mail; 230+ accumulated at ~1 per 3 min across four dogs |
| `gt escalate close` | `✓ Escalation closed` | closed the wisp, left the issue open, so the Mayor's queue kept carrying it as `[open] [HIGH]` |
| `bd close --reason` on a closed bead | full success banner echoing the new reason | wrote nothing; `close_reason` stayed at its old value (verified twice by SQL) |
| 13 `gt` parent commands | the parent's help, exit 0 | nothing — `gt dog whatevr` succeeded |
| `gt nudge` (observed) | exit 0 | the tmux send failed and the exit status did not reflect it |

Found by four agents independently across four subsystems, which is what makes
it a class rather than five unrelated bugs.

## Why it survives review

**It defeats the natural verification habit.** Every agent in this town verified
by reading the command's own output — which is exactly the surface that lies.
The only checks that held were the ones that measured state *afterwards*: SQL
row counts, `/proc`, inode comparisons, differential before/after.

**It is invisible at the moment of failure.** Detection requires someone to
measure the effect later and to have a reason to. Three of the five above were
found only because an unrelated investigation happened to count the population:

- `gt dog done` — the mayor watched a full session lifecycle and the dispatch
  count did not move (5 samples, 53 → 53).
- `bd close --reason` — ghoul tried to correct a wrong close reason and verified
  by SQL that the correction had not landed.
- `gt escalate close` — the witness noticed the escalation queue carrying items
  that had been retracted.

## The three rules

1. **A command that mutates state MUST verify the mutation before reporting
   success**, or must report "attempted" rather than "done".

2. **A cleanup or best-effort path MUST return an error.** Callers may choose
   not to abort, but MUST surface it. This is the cheapest and highest-yield of
   the three, because the compiler enforces it: a caller cannot discard an
   `error` return without visibly ignoring a value.

   `closePluginMails` is the worked example — changed from `func(string)` to
   `func(string) error` for exactly this reason (89a02d0b).

3. **Exit 0 must mean the requested effect occurred.** If the command decided
   not to act (target has DND, agent not running), say so on stdout *and* pick
   the exit status deliberately — do not let "we chose to skip" and "we did it"
   share a code by accident.

## Reviewer checklist

- Does any success path return `nil` without having confirmed the effect?
- Does any helper that mutates state return no error at all? (`func(x) `, not
  `func(x) error`)
- Is there an `_ =` or a bare call discarding an error on a mutation path?
- Does the command print `✓` before, or instead of, checking?
- For a parent command: does it have a `Run`/`RunE`? Cobra answers an unknown
  subcommand of a parent without one by printing help to **stdout** and exiting
  **0**.

## Verifying a fix

Measure state, not output. The command's own report is the thing under test, so
it cannot also be the evidence.

```bash
# Wrong: the success banner is the surface that lies.
gt escalate close hq-wisp-xxxx && echo "closed"

# Right: read the row back.
gt escalate close hq-wisp-xxxx
dolt ... -q "SELECT status FROM hq.issues WHERE id = 'hq-go1yz'"
```

Validate the control too: confirm the check you are relying on is *able* to
fail. A guard that cannot fail reports success for the same reason the bug does.

## Enforcement in code

The parent-command member is enforced by a test rather than a convention —
`TestEveryParentCommandIsRunnable` in `internal/cmd/require_subcommand_test.go`
walks the whole command tree and fails on any parent lacking a `Run`/`RunE`, so
a new one cannot regress silently:

```go
var parentCmd = &cobra.Command{
	Use:   "parent",
	Short: "...",
	RunE:  requireSubcommand,   // ← without this, `gt parent typo` exits 0
}
```

Prefer this shape wherever the class can be pinned by a test: a convention
catches what a reviewer remembers to look for, a test catches the rest.

## See also

- [Verification Sweeps](verification-sweeps.md) — the same failure shape in
  search tooling: a recursive `grep` that returns a perfect all-clear over the
  subtrees most likely to be wrong.

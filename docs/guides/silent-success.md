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

## The read-path variant: zero vs unknown (gt-edty)

The same shape appears on reads, where the lie is a value rather than an exit
status: a query that fails returns an empty result, and "I could not look" is
served as "there is nothing there".

```go
// Before (internal/web/fetcher.go): FetchEscalations
stdout, err := f.runBdCmd(f.townRoot, "list", "--label=gt:escalation", ...)
if err != nil {
    return nil, nil // No escalations or bd not available
}
```

The comment states the bug — those are two different facts sharing one return
value. A Dolt outage, a `bd` crash, a timeout, and a genuinely quiet town all
rendered as the same empty Escalation panel with no error indicator.

**It is worst on exactly the panels that matter most.** The escalation panel
exists to surface trouble, and bd is most likely to be failing when the town is
in trouble — so the sicker the town, the calmer the dashboard looked.

Four rules for read paths:

4. **Zero must mean zero; unknown must look different from zero.** A failed
   query returns an error, and the display renders an explicit unavailable
   state — a `?` rather than a `0`, and never the "nothing here" empty state.

5. **A partial answer must name what is missing.** Where a hard failure is worse
   than a short answer (a union over several stores), carry the failed sources
   BY NAME and render them, as `MergeQueueResult.FailedRigs` and
   `StoreResult.FailedStores` do. A count that is a floor must not be displayed
   as a total.

6. **The discriminator is "did the source answer", not "did the call return an
   error".** Not every error branch is a swallowed failure. A source that
   answered "there is nothing" reports a real zero even though the call failed:

   | Situation | Call | Meaning |
   |---|---|---|
   | Kennel directory does not exist | `ENOENT` | zero — no dog was ever created |
   | Kennel exists, cannot be read | `EACCES` | unknown |
   | `.events.jsonl` does not exist | `ENOENT` | zero — nothing was ever logged |
   | tmux: `no server running` | exit 1 | zero — nothing is running |
   | tmux: timeout, or missing from `PATH` | exit ≠ 0 | unknown |
   | Circuit breaker open | no call made | unknown — the query never ran |

   Getting this backwards costs both ways. A caveat that appears on every quiet
   or shut-down town trains operators to ignore it, which removes the warning
   just as effectively as never showing one.

7. **An error with no fact in it cannot be branched on, or read.** Rule 6 needs
   the error to say WHICH failure it was, and the operator needs the same words.
   `runCmd`/`runBdCmd` fold the first line of the command's stderr into the
   error for both reasons: `connection refused` and `unknown label` send someone
   to different places, and `exit status 1` sends them nowhere. A fix that edits
   only the `if err != nil` branch is not complete while the error it returns
   carries nothing to distinguish.

Aggregates inherit the defect: a summary that counts an unreadable panel as 0
will happily report "✓ All clear". Not being able to see is itself an alert.

**The error has to survive every layer, not just the one that produced it.** A
fetcher can return a perfectly good error and still change nothing: the handler
above it logs the error and builds the panel from the zero value, so the same
"could not look" arrives as "nothing there" one layer up. Four panels stayed
blind this way for a full release after their fetchers were fixed (gt-xw1t).
`var err error` next to `log.Printf` in a fan-out is the signature — the result
variable is kept and the error is not.

**A value that renders only when it is known hides instead of lying, which is
harder to notice.** `{{if .Health}}` around the heartbeat stat meant a failed
read did not show a wrong heartbeat — it removed the liveness indicator from the
banner, and a warning light that is absent reads exactly like one that is off.
A panel with no empty state to lie with is not therefore safe; check what the
page looks like when the value is missing, not only when it is wrong.

## Reviewer checklist

- Does any success path return `nil` without having confirmed the effect?
- On a read path: can a failed query and an empty result return the same value?
  (`return nil, nil` on an error branch is the signature of this bug.)
- Does the error reach the reader, or only `log.Printf`? A logged error and a
  discarded one render identically to whoever is looking at the page. Check
  every layer between the query and the template, not just the query.
- Does the display element render conditionally on the value being present? Then
  its failure mode is a missing indicator, not a wrong one.
- Does the error carry the fact the caller has to branch on, and the operator
  has to read? (`exit status 1` carries neither.)
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

Rule 2 — *a best-effort path MUST return an error* — is enforced the same way by
`TestDeliveryReportersReturnTheirFailure` in
`internal/cmd/success_reporting_policy_test.go`. It walks the delivery packages
and fails on any function named for delivery (`send`, `notify`, `nudge`,
`escalate`, `dispatch`, …) that swallows a failure into a log and returns
nothing, held against a baseline that can only shrink.

Rule 2 is enforceable where the others are not, and the reason is worth stating:
**the defect is visible in the signature.** The three worst instances found for
gt-9tpw were all invisible at the call site —

- gt-32gf `watchAndDeliver` logged the tmux failure and returned nothing, so
  `runNudge` printed `✓ Nudged` one line below the error, exit 0.
- gt-lae6 `nudgeWitness` returns nothing, so `gt done`'s durable checkpoint
  hardcodes `"ok"` — written *before* the attempt it describes.
- gt-9tpw `Daemon.escalate` returned nothing, so a destructive reaper fallback
  reported itself escalated when the escalation had failed.

— and in each the caller was blameless: it had nothing to test. A rule about
print statements cannot see any of them, because the print is correct code
sitting downstream of a function that lied by omission.

### An alarm that shares a failure mode with the fault

`Daemon.escalate` is the worked example of a trap specific to this class. The
reaper escalates when Dog dispatch fails; both the dispatch and the escalation
run `gt`, so the single most likely cause — a broken `gt` binary, which is what
happened on 2026-08-24 — takes out the alarm along with the safe path.

Escalating and assuming it landed would be this very defect committed inside its
own fix. So `escalateE` returns whether the escalation was delivered, and the
caller says plainly in the daemon log when a destructive path ran unreported.

**When you add a report, ask what it shares with the thing it reports on.** A
channel that fails whenever its subject fails is not a channel.

## See also

- [Verification Sweeps](verification-sweeps.md) — the same failure shape in
  search tooling: a recursive `grep` that returns a perfect all-clear over the
  subtrees most likely to be wrong.
- [False-Zero Queries](false-zero-queries.md) — the read-side twin: a listing
  that names neither the store nor the scope it queried, so a wrong-store zero
  and a real one are the same characters on screen.

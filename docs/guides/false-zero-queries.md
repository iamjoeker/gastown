# False-Zero Queries

**Rule: a listing must name the store and the scope it queried.** An empty
result that names neither is unfalsifiable — a wrong-store zero, a wrong-status
zero, and a real zero are the same characters on screen.

This guide is the convention; apply it at code review, and apply it before you
escalate a data-loss report. It exists because four surfaces answered "nothing"
about merge requests that demonstrably existed, and the chain nearly produced a
confident false data-loss escalation (gt-kb63). **No data was lost. The
instruments were wrong.**

---

## The measurement (2026-08-19)

Two merge requests, `gt-wisp-ydmw` and `gt-wisp-44ep`, merged and verified
present in `gastown.wisps`, absent from `hq.wisps`. Four ways to look for them
(measured against the live server for gt-kb63):

| Probe | Result |
|---|---|
| `bd list --label gt:merge-request --status all` | `No issues found.` |
| `bd mol wisp list` (town root) | 990 rows, neither MR |
| `bd mol wisp list --all` (town root) | 5007 rows, neither MR |
| `bd mol wisp list --all` (gastown rig) | 634 rows, **both MRs** |
| `gt mq list gastown --status closed` | 53 rows, **both MRs** |

Three of the four surfaces say "destroyed". Row counts drift as wisps churn —
what does not drift is which surfaces can see the records at all.

Re-measured independently a few hours later from a gastown worktree, the status
half alone hides most of the table:

```
$ bd mol wisp list        | wc -l      # open only
87
$ bd mol wisp list --all  | wc -l      # open + closed
644
```

557 wisps invisible, with no indication on screen that anything was excluded.

## The two filters, either alone fatal

**1. Store scope.** `bd mol wisp list` reads the store of the **cwd**. From the
town root that is `hq.wisps`; MR wisps live in `<rig>.wisps`. Run from the town
root it cannot see rig wisps at all. This is the dominant factor and it is
invisible in the output — the command names no store and reports a large,
plausible row count either way.

**2. Default status.** Bare `bd mol wisp list` excludes closed. Every MERGED MR
is closed by definition, so the default scope excludes exactly the population
anyone auditing merges cares about.

Get the store right but forget `--all`: zero. Remember `--all` but run from the
town root: zero. Both wrong: zero.

The help text makes the second easy to misread. Its summary line reads "List all
wisps", and it prints

```
  - Status: Current status (open, in_progress, closed)
```

directly above the `--all` flag. That line describes the **field's** possible
values, not the listing's scope — but read quickly it promises coverage the
default does not provide.

## The structurally-impossible zero

`bd list --label gt:merge-request` is a different fault. MR records are
**wisps**; `bd list` queries the `issues` table. They are separate tables, so
**no filter on `bd list` can ever return an MR**. That zero is not a small
result set — it is a query that cannot succeed, rendered as an ordinary empty
result.

A zero that is structurally impossible to be nonzero must never render the same
as a zero that merely found nothing.

## Searching the listing is contaminated by construction

Mail is stored in the same `wisps` table, and mail subjects quote the ids they
are about. A content search for a bare id therefore returns **false positives**:

```
hq-wisp-qhucy9  closed  P2  task  MERGE_READY: gt-wisp-ydmw (gt-xahy) — c...
```

That is an hq mail wisp *about* the MR, not the MR. It "found" a record from the
wrong store entirely and nearly refuted a correct report. Anchor to the id
column (`grep -E "^<id> "`) or query the table directly.

## What to use

**To audit merge requests, use `gt mq list <rig> --status closed`.** It was the
only correct surface of the four: it takes the rig explicitly, so it cannot be
pointed at the wrong store by accident, and it prints the store and status scope
it queried in its header:

```
📋 Merge queue for 'gastown':
  store: /home/jkerby/src/gt/gastown   scope: status=open (default — closed MRs not shown)
```

`--status closed` for merged and rejected MRs, `--status all` for every MR. When
a scope that hides closed comes back empty, the command says so rather than
leaving "(empty)" to be read as "none exist".

## The four rules

1. **Name the store.** A listing that queries one database among several must
   print which one. Otherwise a wrong-store zero is indistinguishable from a
   real one, and the command is unfalsifiable by construction.
2. **Name the scope when it is a default.** An operator who did not choose the
   filter is the one most likely to mistake its empty result for "everything".
3. **A structurally-impossible zero is an error, not an empty result.** If the
   filter can never match the table being queried, say so and name the surface
   that can.
4. **Two absences are not one.** Absent-because-closed and
   absent-because-nonexistent have different remedies (reopen vs create).
   Collapsing them reports the alarming one.

Rule 4 had already bitten `gt doctor`: `AgentBeadsCheck` classified agent beads
against the open-only wisp listing, so a **closed** agent bead was reported as
**missing** — while the same check's `Fix` knew better and reopened it. Check
and fix disagreed on the same input. The check now loads both scopes and reports
closed beads as closed.

## Why it matters more here than elsewhere

Wisps are unversioned and unbacked, with no restore path (hq-rkpw). That makes
"were these records destroyed?" a question agents will keep asking, urgently,
and the instruments answered it wrongly in the alarming direction. A false
data-loss escalation costs a night. **Acting** on one — restoring, force-cleaning,
rebuilding a store — could cost real data.

## Prior members of this class

- `gt-lf1n` — status panels querying only `hq`, so rig state read as absent.
- `bd-99f` — `bd ready` leaking foreign rows across the same boundary.

Same shape each time: a query silently scoped to one store, rendering its
partial view as a complete answer.

## See also

- [Silent Success](silent-success.md) — the write-side twin: exit 0 that means
  "the command ran", not "the effect occurred".
- [Verification Sweeps](verification-sweeps.md) — the same failure shape in
  search tooling: a recursive `grep` that returns a perfect all-clear over the
  subtrees most likely to be wrong.

# Durable records: `gt:record`

Some beads exist to be **read**, not implemented: incident write-ups, merge
ledgers, session archives. Label them `gt:record` and the dispatch path will
leave them alone.

```bash
bd label add <bead-id> gt:record
```

`gt:ledger` and `gt:incident` are accepted synonyms — use whichever names the
artifact honestly. All three behave identically.

## Why the label exists

Two mechanisms collide without it.

**Durability.** After the incident in which seven closed MR wisps were
destroyed, the standing defence is to write incident and merge-ledger state onto
a **normal bead** rather than a wisp: wisps are what the GC purges, ordinary
beads are not. That advice is correct and stays correct.

**Dispatch.** The dispatch path treats any open, non-wisp bead of a work type as
implementable work. It attaches `mol-polecat-work` and slings a polecat at it.

So the very act of protecting a record made it look like a work item. Two
measured cases (`gt-8uc`, a beads-rig merge ledger, and `gt-6dp`, an incident
report) each consumed a full polecat session that ended in "there is nothing
here to build". Worse, the loop cannot self-limit: a ledger is never *done* in
the implementer sense, so unless the polecat explicitly closes the bead, the
zombie patrol resets it to open and dispatches it again. The record's durability
is exactly what makes it recur.

`gt:record` breaks the loop without touching durability. The bead stays an
ordinary open row in `issues`, out of reach of every wisp GC path; only the
dispatch surfaces change behaviour.

## What the label changes

| Surface | Behaviour |
|---|---|
| `gt sling <bead>` | Refuses, naming the label and the override |
| `executeSling` (batch sling, queue/scheduler dispatch) | Refuses before spawning anything |
| Scheduler dispatch planning | Skips with `dispatch_skip reason=record_label`, so the record does not burn the sling context's dispatch-failure quota |
| `gt ready` | Omits the record from ready work |

Nothing else changes. In particular the label does **not**:

- make the bead ephemeral or a wisp,
- exempt it from `bd close`,
- protect it from auto-close — a closed bead is just as durable, so closing a
  record is a fine disposition and always was.

## Overriding

If a record really does contain work:

```bash
bd label remove <bead-id> gt:record   # preferred — it was mislabelled
gt sling <bead-id> <rig> --force      # one-off override, label stays
```

`--force` is deliberately the same escape hatch used by the deferred-bead and
cross-rig guards.

## A caveat on `gt ready`

`bd ready --json` does not return a `labels` field. Filtering the ready rows on
their own labels would therefore match nothing and silently pass every record
through — a false zero of exactly the kind
[`docs/guides/false-zero-queries.md`](false-zero-queries.md) describes. `gt
ready` instead builds the exclusion set from a separate labelled `bd list`
query, the same way it excludes wisps. If `bd ready` ever gains labels, the
extra query can be dropped; `TestFilterRecordBeads_LabelFreeRowsNeedTheIDSet`
holds a control that will notice.

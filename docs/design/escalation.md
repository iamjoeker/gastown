# Gas Town Escalation Protocol

> Reference for the unified escalation system in Gas Town.

## Overview

Gas Town agents escalate issues when automated resolution is not possible.
Escalations are severity-routed, tracked as beads, and support stale detection
with automatic re-escalation.

## Severity Levels

| Level | Priority | Description | Default Route |
|-------|----------|-------------|---------------|
| **CRITICAL** | P0 (urgent) | System-threatening, immediate attention | bead + mail + email + SMS |
| **HIGH** | P1 (high) | Important blocker, needs human soon | bead + mail + email |
| **MEDIUM** | P2 (normal) | Standard escalation, human at convenience | bead + mail mayor |

## Tiered Escalation Flow

```
Agent -> gt escalate -s <SEVERITY> "description"
           |
           v
     [Deacon receives]
           |
           +-- resolves --> updates issue, re-slings work
           +-- cannot  --> forwards to Mayor
                              +-- resolves --> updates issue, re-slings
                              +-- cannot  --> forwards to Overseer --> resolves
```

Each tier can resolve OR forward. The chain is tracked via bead comments.

## Configuration

Config file: `$GT_ROOT/settings/escalation.json` — the town root `gt` resolves from
your cwd. It is **not** `~/gt`, which is not a town root; that path exists on some
hosts holding unrelated files, so looking there yields a missing config rather
than an error.

### Default Configuration

```json
{
  "type": "escalation",
  "version": 1,
  "routes": {
    "low": ["bead"],
    "medium": ["bead", "mail:mayor"],
    "high": ["bead", "mail:mayor", "email:human"],
    "critical": ["bead", "mail:mayor", "email:human", "sms:human"]
  },
  "contacts": {
    "human_email": "",
    "human_sms": "",
    "slack_webhook": "",
    "smtp_host": "",
    "smtp_port": "587",
    "smtp_from": "",
    "smtp_user": "",
    "smtp_pass": "",
    "sms_webhook": ""
  },
  "stale_threshold": "4h",
  "max_reescalations": 2
}
```

### Action Types

| Action | Format | Behavior |
|--------|--------|----------|
| `bead` | `bead` | Record the escalation as a durable bead |
| `mail:<target>` | `mail:mayor` | Send gt mail to target |
| `email:human` | `email:human` | Send email to `contacts.human_email` |
| `sms:human` | `sms:human` | Send SMS to `contacts.human_sms` |
| `slack` | `slack` | Post to `contacts.slack_webhook` |
| `log` | `log` | Write to escalation log file |

## Escalation Beads

Escalation beads use `type: escalation` with structured labels for tracking.

### Anatomy: one escalation, two kinds of bead

Every escalation produces **two** kinds of bead, and confusing them is the
classic way to convince yourself an escalation is resolved when it is not:

| | Record | Delivered copy |
|---|---|---|
| Where | `wisps` table (ephemeral, has a TTL) | `issues` table (durable) |
| ID shape | `hq-wisp-<x>` | `hq-<x>` |
| How many | exactly one | one per mail target, or one from the `bead` action |
| Carries | the structured `severity:`/`reason:`/`closed_by:` fields | the mail body, or the structured fields for a `bead`-action copy |
| Links | — | `escalation:<record-id>`, `thread:<record-id>` |
| ID printed in | the escalation mail body ("To close: …") | `gt escalate list`, the Mayor's queue |

Both carry `gt:escalation`. **The queue renders the copies, not the record**, so
resolving an escalation means resolving both halves. `gt escalate close` and
`gt escalate ack` do that themselves and accept **either** ID — the record ID
from the mail body or the copy ID from `gt escalate list`. `gt escalate list`
additionally hides copies whose record is already closed, which covers
escalations closed before this reconciliation existed (gt-4xl).

Closing a copy directly with `bd close` does not touch the record, and closing
the record with `bd close` does not touch the copies. Use `gt escalate close`.

### Severity reaches the priority column

`-s <severity>` sets the bead **priority** as well as the `severity:<level>`
label, on both halves: critical→P0, high→P1, medium→P2, low→P3. An unrecognised
severity sets no priority at all rather than defaulting to P0.

It is not cosmetic. Priority is what generic readers render and what the
reaper's staleness sweep keys its P0/P1 exemption on, so a severity that never
reached the column also forfeited that protection. Until gt-nhp only the
delivered copy carried it (gt-3i4e); the record did not, so every escalation
record read P2 whatever was filed — `hq-wisp-yro9` went in at HIGH about a live
nuke hazard and rendered as an ordinary P2. A re-escalation moves the priority
with the severity for the same reason: the bump exists to make an ignored
escalation louder.

### Escalations are not garbage

Escalation beads are protected from every reaper path gt owns, which is a
departure from how wisps normally work and is deliberate (gt-nhp):

| Path | Guard |
|---|---|
| reaper age-close (`Reap`) | `reaper.ReapProtectedWispLabels` |
| reaper delete (`Purge`) | `reaper.ProtectedWispLabels` |
| `gt compact` | `reaper.ProtectedWispLabels` (shared variable) |
| staleness auto-close (`AutoClose`) | `reaper.AutoCloseExemptLabels` |

The reason is that age-eligibility inverts the selection for this one type.
Every path above keys on age or last-update, and an escalation nobody touches is
never updated — so an IGNORED escalation ages into the delete window BEFORE one
being worked, and the escalations most likely to be destroyed are precisely the
ones nobody acted on. Closing is protected as well as deleting, because
`gt escalate list` hides copies whose record is closed: reaping the record alone
removes a live escalation from the only surface an operator reads.

**The cost was real and deliberate**: escalation rows accumulated without bound,
on the grounds that the growth is merely expensive while the deletion is final.
Wisps are unversioned, unbacked and `dolt_ignore`'d, so there is no Dolt history
to restore one from.

**Retention now pays that bill without reopening the hole** (gt-6xwt). `Purge`
exports a closed escalation record — description, close reason, labels,
comments, dependencies — to the durable archive under `~/.gt/wisp-archive/`, and
only then deletes the row; `gt reaper archive --grep <id>` reads it back. The
export is fsynced before the DELETE, so a failure leaves the row in Dolt rather
than the record nowhere, and `--no-archive` restores the keep-forever behaviour.
Note what this does NOT change: `Reap` still never closes an open escalation
record, because the hazard there is disappearing from `gt escalate list` while
still needing attention, and no archive fixes that. A PINNED record is never
exported or released either — the pin is a responder's instruction about that
one row.

**Residual gap — bd's own GC is NOT covered.** `bd purge` and
`bd mol wisp gc --age` protect by label too, but from bd's `gc.protected_labels`
config, whose default is merge-request and message records only. That key is
unset in this town, so a manual `bd mol wisp gc` still deletes open escalation
records — this is the path that reported "would clean 734 abandoned wisp(s)" in
gt-nhp's evidence. Setting it needs the exact default value the deployed bd
uses, so that adding `gt:escalation` extends the list instead of replacing it
and silently dropping the two protections already there.

### The `bead` action, and why a route must deliver something

The record is a wisp: unversioned, unbacked and `dolt_ignore`'d, with no restore
path if it is ever deleted. gt's own reaper no longer age-GCs it (see
"Escalations are not garbage" above), but bd's GC still can, and a wisp is the
wrong substrate for a durable record either way. **The durable half of an
escalation is the copy**, so a route that produces no copy stakes the whole
escalation on the wisp surviving.

That is what the `bead` action is for, and for a long time it did not exist. It
was configured on all four routes, reported as `"created": true` on every
escalation, and dispatched nowhere — the copy was only ever a side effect of the
`mail:` actions. `critical`, `high` and `medium` all carry `mail:mayor`, so they
looked fine; `low` is `["bead"]` alone, so it delivered nothing, appeared on no
surface, and was destroyed unread. Severity was the revealer, not the cause
(gt-3i4e).

Now:

- `bead` creates the durable copy — labelled `gt:escalation`, `severity:<sev>`
  and `escalation:<record-id>`, carrying the record's structured fields so they
  outlive the wisp. When a `mail:` action already produced a linked copy, that
  copy satisfies the action; escalations are never duplicated in the queue.
- Every delivery status is set from a real result. Nothing is seeded successful.
- **A route that delivers nothing exits non-zero** and reports
  `"status": "undelivered"`. A no-op can no longer print a success banner.

### Label Schema

| Label | Values | Purpose |
|-------|--------|---------|
| `severity:<level>` | MEDIUM, HIGH, CRITICAL | Current severity |
| `source:<type>:<name>` | plugin:rebuild-gt, patrol:deacon | What triggered it |
| `acknowledged:<bool>` | true, false | Has human acknowledged |
| `reescalated:<bool>` | true, false | Has been re-escalated |
| `reescalation_count:<n>` | 0, 1, 2, ... | Times re-escalated |
| `original_severity:<level>` | MEDIUM, HIGH | Initial severity |

## Category Routing (future)

Categories provide structured routing based on the nature of the escalation.
Not yet implemented as CLI flags; currently use `--to` for explicit routing.

| Category | Description | Default Route |
|----------|-------------|---------------|
| `decision` | Multiple valid paths, need choice | Deacon -> Mayor |
| `help` | Need guidance or expertise | Deacon -> Mayor |
| `blocked` | Waiting on unresolvable dependency | Mayor |
| `failed` | Unexpected error, can't proceed | Deacon |
| `emergency` | Security or data integrity issue | Overseer (direct) |
| `gate_timeout` | Gate didn't resolve in time | Deacon |
| `lifecycle` | Worker stuck or needs recycle | Witness |

## Commands

### gt escalate

Create a new escalation.

```bash
gt escalate -s <MEDIUM|HIGH|CRITICAL> "Short description" \
  [-m "Detailed explanation"] [--source="plugin:rebuild-gt"]
```

Flags: `-s` severity (required), `-m` body, `--source` origin identifier,
`--to` route to tier (deacon/mayor/overseer), `--dry-run`, `--json`.

For Dolt outages or GT behavior mismatches that involve Dolt-backed state, add
the RCA capture checklist from `docs/dolt-health-guide.md` to the escalation
body or the follow-up bead before restarting services.

### gt escalate ack

Acknowledge an escalation (prevents re-escalation).

```bash
gt escalate ack <bead-id> [--note="Investigating"]
```

### gt escalate list

```bash
gt escalate list [--severity=...] [--stale] [--unacked] [--all] [--json]
```

### gt escalate stale

Re-escalate stale (unacked past `stale_threshold`) escalations. Bumps severity
(MEDIUM->HIGH->CRITICAL), re-executes route, respects `max_reescalations`.

```bash
gt escalate stale [--dry-run]
```

### gt escalate close

```bash
gt escalate close <bead-id> [--reason="Fixed in commit abc123"]
```

## Integration Points

### Plugin System

Plugins use escalation for failure notification:

```bash
gt escalate -s MEDIUM "Plugin FAILED: rebuild-gt" \
  -m "$ERROR" --source="plugin:rebuild-gt"
```

### Deacon Patrol

Deacon uses escalation for health issues:

```bash
if [ $unresponsive_cycles -ge 5 ]; then
  gt escalate -s HIGH "Witness unresponsive: gastown" \
    -m "Witness has been unresponsive for $unresponsive_cycles cycles" \
    --source="patrol:deacon:health-scan"
fi
```

Deacon patrol also runs `gt escalate stale` periodically to catch unacked
escalations and re-escalate them.

## When to Escalate

### Agents SHOULD escalate when:

- **System errors**: Database corruption, disk full, network failures
- **Security issues**: Unauthorized access attempts, credential exposure
- **Unresolvable conflicts**: Merge conflicts that cannot be auto-resolved
- **Ambiguous requirements**: Spec is unclear, multiple valid interpretations
- **Design decisions**: Architectural choices that need human judgment
- **Stuck loops**: Agent is stuck and cannot make progress
- **Gate timeouts**: Async conditions did not resolve in expected time

### Agents should NOT escalate for:

- **Normal workflow**: Regular work that can proceed without human input
- **Recoverable errors**: Transient failures that will auto-retry
- **Information queries**: Questions that can be answered from context

## Mayor Startup Check

On `gt prime`, Mayor displays pending escalations grouped by severity.
Action: review with `bd list --tag=escalation`, close with `bd close <id> --reason "..."`.


## Viewing Escalations

```bash
# List all open escalations
bd list --status=open --tag=escalation

# Filter by category
bd list --tag=escalation --tag=decision

# View specific escalation
bd show <escalation-id>

# Close resolved escalation
bd close <id> --reason "Resolved by fixing X"
```

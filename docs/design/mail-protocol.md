# Gas Town Mail Protocol

> Reference for inter-agent mail communication in Gas Town

## Overview

Gas Town agents coordinate via mail messages routed through the beads system.
Mail uses `type=message` beads with routing handled by `gt mail`.

## Message Types

### POLECAT_DONE

**Route**: Polecat → Witness

**Purpose**: Signal work completion, trigger cleanup flow.

**Subject format**: `POLECAT_DONE <polecat-name>`

**Body format**:
```
Exit: MERGED|ESCALATED|DEFERRED
Issue: <issue-id>
MR: <mr-id>          # if exit=MERGED
Branch: <branch>
```

**Trigger**: `gt done` command generates this automatically.

**Handler**: Witness creates a cleanup wisp for the polecat.

### MERGE_READY

**Route**: Witness → Refinery

**Purpose**: Signal a branch is ready for merge queue processing.

**Subject format**: `MERGE_READY <polecat-name>`

**Body format**:
```
Branch: <branch>
Issue: <issue-id>
Polecat: <polecat-name>
Verified: clean git state, issue closed
```

**Trigger**: Witness sends after verifying polecat work is complete.

**Handler**: Refinery adds to merge queue, processes when ready.

### MERGED

**Route**: Refinery → Witness

**Purpose**: Confirm branch was merged successfully, safe to nuke polecat.

**Subject format**: `MERGED <polecat-name>`

**Body format**:
```
Branch: <branch>
Issue: <issue-id>
Polecat: <polecat-name>
Rig: <rig>
Target: <target-branch>
Merged-At: <timestamp>
Merge-Commit: <sha>
```

**Trigger**: Refinery sends after successful merge to main.

**Handler**: Witness completes cleanup wisp, nukes polecat worktree.

### MERGE_FAILED

**Route**: Refinery → Witness

**Purpose**: Notify that merge attempt failed (tests, build, or other non-conflict error).

**Subject format**: `MERGE_FAILED <polecat-name>`

**Body format**:
```
Branch: <branch>
Issue: <issue-id>
Polecat: <polecat-name>
Rig: <rig>
Target: <target-branch>
Failed-At: <timestamp>
Failure-Type: <tests|build|push|other>
Error: <error-message>
```

**Trigger**: Refinery sends when merge fails for non-conflict reasons.

**Handler**: Witness notifies polecat, assigns work back for rework.

### REWORK_REQUEST

**Route**: Refinery → Witness

**Purpose**: Request polecat to rebase branch due to merge conflicts.

**Subject format**: `REWORK_REQUEST <polecat-name>`

**Body format**:
```
Branch: <branch>
Issue: <issue-id>
Polecat: <polecat-name>
Rig: <rig>
Target: <target-branch>
Requested-At: <timestamp>
Conflict-Files: <file1>, <file2>, ...

Please rebase your changes onto <target-branch>:

  git fetch origin
  git rebase origin/<target-branch>
  # Resolve any conflicts
  git push -f

The Refinery will retry the merge after rebase is complete.
```

**Trigger**: Refinery sends when merge has conflicts with target branch.

**Handler**: Witness notifies polecat with rebase instructions.

### RECOVERED_BEAD

**Route**: Witness → Deacon

**Purpose**: Notify Deacon that a dead polecat's abandoned work has been recovered
and needs re-dispatch.

**Subject format**: `RECOVERED_BEAD <bead-id>`

**Body format**:
```
Recovered abandoned bead from dead polecat.

Bead: <bead-id>
Polecat: <rig>/<polecat-name>
Previous Status: <hooked|in_progress>

The bead has been reset to open with no assignee.
Please re-dispatch to an available polecat.
```

**Trigger**: Witness detects a zombie polecat with work still hooked/in_progress.
The bead is reset to open status and this mail is sent for re-dispatch.

**Handler**: Deacon runs `gt deacon redispatch <bead-id>` which:
- Rate-limits re-dispatches (5-minute cooldown per bead)
- Tracks failure count (after 3 failures, escalates to Mayor)
- Auto-detects target rig from bead prefix
- Slings the bead to an available polecat via `gt sling`

### RECOVERY_NEEDED

**Route**: Witness → Deacon

**Purpose**: Escalate a dirty polecat that has unpushed/uncommitted work needing
manual recovery before cleanup.

**Subject format**: `RECOVERY_NEEDED <rig>/<polecat-name>`

**Body format**:
```
Polecat: <rig>/<polecat-name>
Cleanup Status: <has_uncommitted|has_stash|has_unpushed>
Branch: <branch>
Issue: <issue-id>
Detected: <timestamp>
```

**Trigger**: Witness detects zombie polecat with dirty git state.

**Handler**: Deacon coordinates recovery (push branch, save work) before
authorizing cleanup. Only escalates to Mayor if Deacon cannot resolve.

### HELP

**Route**: Any → escalation target (usually Mayor)

**Purpose**: Request intervention for stuck/blocked work.

**Subject format**: `HELP: <brief-description>`

**Body format**:
```
Agent: <agent-id>
Issue: <issue-id>       # if applicable
Problem: <description>
Tried: <what was attempted>
```

**Trigger**: Agent unable to proceed, needs external help.

**Handler**: Escalation target assesses and intervenes.

### HANDOFF

**Route**: Agent → self (or successor)

**Purpose**: Session continuity across context limits/restarts.

**Subject format**: `🤝 HANDOFF: <brief-context>`

**Body format**:
```
attached_molecule: <molecule-id>   # if work in progress
attached_at: <timestamp>

## Context
<freeform notes for successor>

## Status
<where things stand>

## Next
<what successor should do>
```

**Trigger**: `gt handoff` command, or manual send before session end.

**Handler**: Next session reads handoff, continues from context.

## Format Conventions

### `--type`: does a reply come back?

Every mail bead carries a `msg-type:<value>` label. The subject prefixes above
say what a message is *about*; `msg-type` answers one narrower question:

> After the recipient reads this, do they still owe you something?

That is the only question, and it is the one that decides whether a message may
be closed on read or must stay open in the recipient's work queue. It is **not**
a record of how the mail was sent.

| `--type` | Reply owed? | Meaning |
|---|---|---|
| `notification` (default) | no | Informational. Nothing can be replied to; reading consumes it. |
| `reply` | no | An answer to someone else's question. Set automatically by `--reply-to`. |
| `handoff` | no | Session-cycling context mailed to self. Set automatically by `gt handoff`. |
| `query` | **yes** | A question. **Stays open until answered.** |
| `task` | **yes** | Work assigned to the recipient. Reading it is not doing it. |
| `scavenge` | **yes** | Optional first-come work. Still work until claimed. |
| `escalation` | **yes** | Has its own ack surface (`gt escalate ack`). Never auto-closed. |

**Pass `--type query` when you are blocked on the answer.** The default is
`notification`, so a question sent without it is indistinguishable from a
"convoy complete" — which is exactly how the mayor's inbox reached 375 unread
with a HIGH escalation sitting unactioned for 3.7 days (gt-do5c).

An unrecognised `--type` is now rejected. It used to be rewritten to
`notification` and reported as sent, so `--type query` silently produced
`msg-type:notification` and the sender was never told.

In code, do not re-derive the rule from the type name — call the predicate:

```go
msg.Type.ExpectsReply()        // is the recipient still on the hook?
msg.Type.SafeToCloseOnRead()   // may this be closed the moment it is read?
```

Both fail closed: an unset or unrecognised type is never safe to auto-close,
because an unset type is precisely what a writer that forgot to stamp one
produces, and those are indistinguishable from real questions.

**Pass a `*Message`, not a type, when deciding to auto-close.**
`mail.ConsumedByReading(msg)` is the predicate to use — `SafeToCloseOnRead` on
its own is not enough, for the reason in the next section.

### What reading a message does to it

Reading marks a message read. Whether it also **closes** it — takes it out of
the recipient's work queue — depends on whether anything is still owed:

| | outcome |
|---|---|
| automated traffic with a stamped, reply-free type | closed |
| everything else | marked read, stays open |

`gt mail read` says which happened; a bulk `gt mail mark-read` reports the split
("Marked 12 messages as read (7 closed, 5 still owed a reply or an archive)").
A message left open is cleared by answering it or by `gt mail archive`.

Closing requires **three** things, not one (`mail.ConsumedByReading` plus
`Mailbox.MarkReadConsumed`):

1. **The type was stamped.** A missing `msg-type` label is not the same as
   `msg-type:notification`, even though both read back as `notification`
   through `Message.Type` — use `Message.RawType` when the difference matters.
2. **The stamped type says a read consumes it** — `notification`, `reply` or
   `handoff`.
3. **For `notification`, the subject is automated traffic**: `Convoy complete:`,
   `SCHEDULER_OPEN`, `MERGED`, `LIFECYCLE:`. `reply` and `handoff` need no such
   corroboration.

That third condition looks redundant and is not. Until gt-do5c every message
was typed by a DEFAULT rather than by its sender, so for the beads already on
the store `notification` means *nobody said*, not *no reply possible* — and
sampling found real questions among them (gt-ac1l). The subject is a second,
independent signal that works on mail already written, which no change to the
send path can.

The reader also has to be entitled to close: a **CC copy** is a second view of
one bead (clear it with `gt mail archive`, which dismisses just your copy), and
a **hooked** bead is somebody's `gt hook` context.

**If you are blocked on an answer, send `--type query`.** A question sent under
the default type is indistinguishable from a "convoy complete", which is how
the mayor's inbox reached 375 unread with a HIGH escalation unactioned for
3.7 days.

### Subject Line

- **Type prefix**: Uppercase, identifies message type
- **Colon separator**: After type for structured info
- **Brief context**: Human-readable summary

Examples:
```
POLECAT_DONE nux
MERGE_READY greenplace/nux
HELP: Polecat stuck on test failures
🤝 HANDOFF: Schema work in progress
```

### Body Structure

- **Key-value pairs**: For structured data (one per line)
- **Blank line**: Separates structured data from freeform content
- **Markdown sections**: For freeform content (##, lists, code blocks)

### Addresses

Format: `<rig>/<role>` or `<rig>/<type>/<name>`

Examples:
```
greenplace/witness       # Witness for greenplace rig
beads/refinery           # Refinery for beads rig
greenplace/polecats/nux  # Specific polecat
mayor/                # Town-level Mayor
deacon/               # Town-level Deacon
```

### CC Copies

`gt mail send X --cc Y` assigns the message bead to X and labels it `cc:Y`. Y's
inbox picks it up through the CC label, so one bead is delivered to several
inboxes. That has two consequences worth knowing:

**A CC copy is not yours to act on.** The obligation belongs to the assignee.
`gt mail inbox` marks CC copies `(cc)` and names the addressee; `gt mail read`
prints a `CC:` header and says who the message is addressed to. Correct delivery
of a CC copy has been reported as a misroute before that rendering existed.

**A CC copy is cleared, not closed.** Only the assignee can close the bead — the
beads ownership guard refuses anyone else, correctly. Clearing a CC copy instead
adds `cc-cleared:<your-identity>` and leaves status and assignee untouched, so
the assignee's obligation is unaffected and no `--force` is needed:

```bash
gt mail archive <msg-id>   # as the cc'd party: clears your copy only
```

The label is per-recipient, so each CC'd party clears its own copy
independently. `gt mail archive --force` is not diverted: it still closes the
record, which is one agent overriding another's ownership check — rarely right.

CC copies are counted apart from addressed mail in the inbox header
(`5 messages, 2 unread, 4 cc (1 unread)`), so the headline number keeps meaning
"work addressed to me". Unread notifications still cover CC copies: reading one
is legitimate, only clearing it was broken. See gt-58s.

## Protocol Flows

### Polecat Completion Flow

```
Polecat                    Witness                    Refinery
   │                          │                          │
   │ POLECAT_DONE             │                          │
   │─────────────────────────>│                          │
   │                          │                          │
   │                    (verify clean)                   │
   │                          │                          │
   │                          │ MERGE_READY              │
   │                          │─────────────────────────>│
   │                          │                          │
   │                          │                    (merge attempt)
   │                          │                          │
   │                          │ MERGED (success)         │
   │                          │<─────────────────────────│
   │                          │                          │
   │                    (nuke polecat)                   │
   │                          │                          │
```

### Merge Failure Flow

```
                           Witness                    Refinery
                              │                          │
                              │                    (merge fails)
                              │                          │
                              │ MERGE_FAILED             │
   ┌──────────────────────────│<─────────────────────────│
   │                          │                          │
   │ (failure notification)   │                          │
   │<─────────────────────────│                          │
   │                          │                          │
Polecat (rework needed)
```

### Rebase Required Flow

```
                           Witness                    Refinery
                              │                          │
                              │                    (conflict detected)
                              │                          │
                              │ REWORK_REQUEST           │
   ┌──────────────────────────│<─────────────────────────│
   │                          │                          │
   │ (rebase instructions)    │                          │
   │<─────────────────────────│                          │
   │                          │                          │
Polecat                       │                          │
   │                          │                          │
   │ (rebases, gt done)       │                          │
   │─────────────────────────>│ MERGE_READY              │
   │                          │─────────────────────────>│
   │                          │                    (retry merge)
```

### Abandoned Work Recovery Flow

```
Dead Polecat               Witness                    Deacon
     │                        │                          │
     │ (session dies)         │                          │
     │                        │                          │
     │                  (detects zombie)                 │
     │                  (bead status=hooked)             │
     │                        │                          │
     │                  resetAbandonedBead()             │
     │                  bd update --status=open          │
     │                        │                          │
     │                        │ RECOVERED_BEAD           │
     │                        │─────────────────────────>│
     │                        │                          │
     │                        │                    gt deacon redispatch
     │                        │                    gt sling <bead> <rig>
     │                        │                          │
     │                        │                          ├──> New Polecat
     │                        │                          │    (re-dispatched)
```

### Second-Order Monitoring

```
Witness-1 ──┐
            │ (check agent bead last_activity)
Witness-2 ──┼────────────────> Deacon agent bead
            │
Witness-N ──┘
                                 │
                          (if stale >5min)
                                 │
            ─────────────────────┘
            ALERT to Mayor (mail only on failure)
```

## Communication Hygiene: Mail vs Nudge

Agents overuse mail for routine communication, generating permanent beads and
Dolt commits for messages that should be ephemeral. Every `gt mail send` creates
a durable bead in Dolt -- a permanent record with its own commit in the git-like
history. This is a critical pollution source.

### The Two Channels

**`gt nudge` (ephemeral, preferred for routine comms)**
- Sends a message directly to an agent's tmux session
- No beads created. No Dolt commits. Zero storage cost.
- Message appears as a `<system-reminder>` in the agent's context
- Suitable for: health checks, status requests, simple instructions, "wake up" signals
- Limitation: if the target session is dead, the nudge is lost

**`gt mail send` (persistent, for structured protocol messages only)**
- Creates a durable issue bead in the Dolt database
- Generates at least one Dolt commit (the write)
- Persists across session restarts -- survives agent death
- Suitable for: HANDOFF context, MERGE_READY/MERGED protocol, escalations, HELP
  requests, anything that MUST survive session death

### Durability, wisps, and GC

Mail is durable by default. Two exceptions store a message as an ephemeral
*wisp* instead, and wisps ARE reclaimable by age (`bd mol wisp gc --age`, the
reaper's reap+purge):

1. `gt mail send --wisp` -- explicit opt-in.
2. Protocol/lifecycle subjects, auto-detected by `Router.shouldBeWisp`:
   `POLECAT_STARTED`, `POLECAT_DONE`, `WORK_DONE`, `START_WORK`, `NUDGE`,
   `LIFECYCLE:`, `MERGED`, `MERGE_READY`, `MERGE_FAILED`. These are
   fire-and-forget signals with no value once acted on.

Everything else -- HANDOFF, HELP, escalations, ordinary agent-to-agent mail --
is a plain issue bead and is never age-reclaimed.

Reading a message closes its bead, so an OPEN mail bead is unread mail. Unread
mail is exempt from the reaper's staleness auto-close: closing it would stamp
`closed_at` and hand it to the mail purge a retention window later, which would
delete, silently, exactly the messages nobody had read yet. Read (closed) mail
is still purged on the normal retention window (`gt reaper purge --mail-age`,
7d by default).

`gt mail send --wisp` was the default until gt-jbn. It made every message
age-GC eligible, and unread mail aged fastest -- an unread message is never
touched, so its `updated_at` never moves and it entered the delete window before
any message being actively worked.

### The Rule

**Default to `gt nudge`. Only use `gt mail send` when the message MUST survive
the recipient's session death.**

The litmus test: "If the recipient's session dies and restarts, do they need this
message?" If yes -> mail. If no -> nudge.

### Role-Specific Guidance

| Role | Mail Budget | When to Mail | When to Nudge |
|------|-------------|-------------|---------------|
| **Polecat** | 0-1 per session | HELP/ESCALATE only (gt escalate preferred) | Everything else |
| **Witness** | Protocol msgs only | MERGE_READY, RECOVERED_BEAD, RECOVERY_NEEDED, escalations to Mayor | Polecat health checks, status pings, nudge-and-observe |
| **Refinery** | Protocol msgs only | MERGED, MERGE_FAILED, REWORK_REQUEST | Status updates to Witness |
| **Deacon** | Escalations only | Escalations to Mayor, HANDOFF to self | TIMER callbacks, HEALTH_CHECK, lifecycle pokes |
| **Dogs** | Zero | Never (results go to event beads or logs) | Report completion to Deacon via nudge |
| **Mayor** | Strategic only | Cross-rig coordination, HANDOFF to self | Instructions to Deacon/Witness |

### Why This Matters (The Commit Graph)

Dolt is git under the hood. Every mail creates a Dolt commit. Over a day of
normal operations:
- 4 agents x 15 patrol cycles x 2 mails per cycle = 120 commits just for routine chatter
- These commits live in the git history forever, even after mail rows are deleted
- Rebase can remove them, but prevention is always cheaper than cleanup

### Anti-Patterns

**DOG_DONE as mail** -- Dogs should not mail their completion status. Use
`gt nudge deacon/ "DOG_DONE: plugin-name success"` instead.

**Duplicate escalations** -- Witnesses sending 2+ mails about the same issue
minutes apart. Check inbox before sending: if you already sent about this topic,
don't send again.

**HANDOFF for routine cycles** -- Patrol agents (Witness, Deacon) doing routine
handoffs should use minimal mail. If there's nothing extraordinary, just cycle --
the next session discovers state from beads, not from mail.

**Health check responses via mail** -- When Deacon sends a health check nudge, do
NOT respond with mail. The Deacon tracks health via session status, not mail
responses.

## Implementation

### Sending Mail

```bash
# Basic send
gt mail send <addr> -s "Subject" -m "Body"

# With structured body
gt mail send greenplace/witness -s "MERGE_READY nux" -m "Branch: feature-xyz
Issue: gp-abc
Polecat: nux
Verified: clean"
```

### Receiving Mail

```bash
# Check inbox
gt mail inbox

# Read specific message
gt mail read <msg-id>

# Mark as read
gt mail ack <msg-id>
```

### In Patrol Formulas

Formulas should:
1. Check inbox at start of each cycle
2. Parse subject prefix to route handling
3. Extract structured data from body
4. Take appropriate action
5. Mark mail as read after processing

## Extensibility

New message types follow the pattern:
1. Define subject prefix (TYPE: or TYPE_SUBTYPE)
2. Document body format (key-value pairs + freeform)
3. Specify route (sender → receiver)
4. Implement handlers in relevant patrol formulas

The protocol is intentionally simple - structured enough for parsing,
flexible enough for human debugging.

## Beads-Native Messaging

Beyond direct agent-to-agent mail, the messaging system supports three bead-backed
primitives for group and broadcast communication. All use the `hq-` prefix
(town-level entities that span rigs).

### Groups (`gt:group`)

Named collections of addresses for mail distribution. Sending to a group
delivers to all members.

**Bead ID format:** `hq-group-<name>`

**Member types:** direct addresses (`gastown/crew/max`), wildcard patterns
(`*/witness`, `gastown/crew/*`), special patterns (`@town`, `@crew`,
`@witnesses`), or nested group names.

### Queues (`gt:queue`)

Work queues where each message goes to exactly one claimant (unlike groups).

**Bead ID format:** `hq-q-<name>` (town-level) or `gt-q-<name>` (rig-level)

Fields: `status` (active/paused/closed), `max_concurrency`, `processing_order`
(fifo/priority), plus count fields (available, processing, completed, failed).

### Channels (`gt:channel`)

Pub/sub broadcast streams with configurable message retention.

**Bead ID format:** `hq-channel-<name>`

Fields: `subscribers`, `status` (active/closed), `retention_count`,
`retention_hours`.

### Group and Channel CLI Commands

```bash
# Groups
gt mail group list
gt mail group show <name>
gt mail group create <name> [members...]
gt mail group add <name> <member>
gt mail group remove <name> <member>
gt mail group delete <name>

# Channels
gt mail channel list
gt mail channel show <name>
gt mail channel create <name> [--retain-count=N] [--retain-hours=N]
gt mail channel delete <name>
```

### Sending to Groups, Queues, and Channels

```bash
gt mail send my-group -s "Subject" -m "Body"           # group (expands to members)
gt mail send queue:my-queue -s "Work item" -m "Details" # queue (single claimant)
gt mail send channel:alerts -s "Alert" -m "Content"     # channel (broadcast)
```

### Address Resolution Order

When sending mail, addresses are resolved in this order:

1. **Explicit prefix** -- `group:`, `queue:`, or `channel:` uses that type directly
2. **Contains `/`** -- Treat as agent address or pattern (direct delivery)
3. **Starts with `@`** -- Special pattern (`@town`, `@crew`, etc.) or group
4. **Name lookup** -- Search group -> queue -> channel by name

If a name matches multiple types, the resolver returns an error requiring an
explicit prefix.

### Retention Policy

Channels support count-based (`--retain-count=N`) and time-based
(`--retain-hours=N`) retention. Retention is enforced on-write (after posting)
and on-patrol (Deacon runs `PruneAllChannels()` with a 10% buffer to avoid
thrashing).

## Related Documents

- `docs/agent-as-bead.md` - Agent identity and slots
- `.beads/formulas/mol-witness-patrol.formula.toml` - Witness handling
- `internal/mail/` - Mail routing implementation
- `internal/protocol/` - Protocol handlers for Witness-Refinery communication

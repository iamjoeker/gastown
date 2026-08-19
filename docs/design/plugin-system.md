# Plugin System Design

> **Status: Design proposal -- not yet implemented**
>
> Design document for the Gas Town plugin system.
> Written 2026-01-11, crew/george session.

## Problem Statement

Gas Town needs extensible, project-specific automation that runs during Deacon patrol cycles. The immediate use case is rebuilding stale binaries (gt, bd, wv), but the pattern generalizes to any periodic maintenance task.

Current state:
- Plugin infrastructure exists conceptually (patrol step mentions it)
- `~/gt/plugins/` directory exists with README
- No actual plugins in production use
- No formalized execution model

## Design Principles Applied

### Discover, Don't Track
> Reality is truth. State is derived.

Plugin state (last run, run count, results) lives on the ledger as wisps, not in shadow state files. Gate evaluation queries the ledger directly.

### ZFC: Zero Framework Cognition
> Agent decides. Go transports.

The Deacon (agent) evaluates gates and decides whether to dispatch. Go code provides transport (`gt dog dispatch`) but doesn't make decisions.

### MEOW Stack Integration

| Layer | Plugin Analog |
|-------|---------------|
| **M**olecule | `plugin.md` - work template with TOML frontmatter |
| **E**phemeral | Plugin-run wisps - high-volume, digestible |
| **O**bservable | Plugin runs appear in `bd activity` feed |
| **W**orkflow | Gate → Dispatch → Execute → Record → Digest |

---

## Architecture

### Plugin Locations

```
~/gt/
├── plugins/                      # Town-level plugins (universal)
│   └── README.md
├── gastown/
│   └── plugins/                  # Rig-level plugins
│       └── rebuild-gt/
│           └── plugin.md
├── beads/
│   └── plugins/
│       └── rebuild-bd/
│           └── plugin.md
└── wyvern/
    └── plugins/
        └── rebuild-wv/
            └── plugin.md
```

**Town-level** (`~/gt/plugins/`): Universal plugins that apply everywhere.
**Rig-level** (`<rig>/plugins/`): Project-specific plugins.

The Deacon scans both locations during patrol.

### Execution Model: Dog Dispatch

**Key insight**: Plugin execution should not block Deacon patrol.

Dogs are reusable workers designed for infrastructure tasks. Plugin execution is dispatched to dogs:

```
Deacon Patrol                    Dog Worker
─────────────────               ─────────────────
1. Scan plugins
2. Evaluate gates
3. For open gates:
   └─ gt dog dispatch plugin     ──→ 4. Execute plugin
      (non-blocking)                  5. Create result wisp
                                      6. Send DOG_DONE
4. Continue patrol
   ...
5. Process DOG_DONE              ←── (next cycle)
```

Benefits:
- Deacon stays responsive
- Multiple plugins can run concurrently (different dogs)
- Plugin failures don't stall patrol
- Consistent with Dogs' purpose (infrastructure work)

### State Tracking: Wisps on the Ledger

Each plugin run creates a wisp:

```bash
gt plugin record-run --plugin rebuild-gt --result success --rig gastown \
  --title "Plugin: rebuild-gt [success]" \
  --description "Rebuilt gt: abc123 → def456 (5 commits)"
```

**Gate evaluation** queries wisps instead of state files:

```bash
# Cooldown check: any runs in last hour?
bd list --all --label type:plugin-run --label plugin:rebuild-gt --created-after 1h -n 1
```

**Derived state** (no state.json needed):

| Query | Command |
|-------|---------|
| Last run time | `bd list --all --label=plugin:X --limit=1 --json` |
| Run count | `bd list --all --label=plugin:X --json \| jq length` |
| Last result | Parse `result:` label from latest wisp |
| Failure rate | Count `result:failure` vs total |

### Receipt Retention

A receipt is not a report, and the difference decides its TTL. Because gate
evaluation queries the receipts, a receipt is load-bearing for as long as the
longest gate that reads it — so it cannot be squashed on a fixed daily schedule,
and it cannot carry a `wisp_type`: since gt-ktvs an untyped wisp is SKIPPED by
`gt compact`, while a `gc_report` (which is what a receipt looks like) is DELETED
once it is closed and 24h old. Receipts are closed the moment they are written,
so typing them would delete `tool-updater`'s on day 1 of its 168h cooldown and
the daemon would re-dispatch a brew upgrade on every scan for the remaining six
days (gt-fqd5).

Retention is therefore derived from the gates themselves, in the plugin layer,
and `gt compact` stays out of the way:

| Plugin | Retention |
|--------|-----------|
| cooldown gate with a duration | `max(48h, duration × 2)` |
| cooldown gate whose duration cannot be parsed | the longest window in town |
| any other gate type | 48h |
| receipt whose `plugin:` label matches no discovered plugin | the longest window in town |

The 48h floor covers readers no gate declares — `gt plugin history`, and plugin
bodies that query receipts directly (quality-review reads `--created-after=-24h`
over receipts the Refinery writes). A plugin whose body needs a longer window
must declare it as a cooldown gate; nothing else is visible to the policy.

```bash
gt plugin prune --dry-run   # Show the derived policy and what would be deleted
gt plugin prune             # Delete expired receipts
```

The daemon runs the same prune hourly, capped at 500 deletions per pass, and
declines to run at all when plugin discovery yields nothing — an empty gate set
is indistinguishable from an unreadable plugins directory, and the plugin with
the longest gate is exactly the one a defaulted short window would destroy.

Never deleted, whatever their age: pinned receipts, receipts carrying a
`reaper.ProtectedWispLabels` label, receipts with a keep label, and receipts with
comments. Deletion is permanent — wisp tables are dolt-ignored, so there is no
history to read `AS OF` and no backup to restore from.

---

## Plugin Format Specification

### File Structure

```
rebuild-gt/
└── plugin.md      # Definition with TOML frontmatter
```

### plugin.md Format

```markdown
+++
name = "rebuild-gt"
description = "Rebuild stale gt binary from source"
version = 1

[gate]
type = "cooldown"
duration = "1h"

[tracking]
labels = ["plugin:rebuild-gt", "rig:gastown", "category:maintenance"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
+++

# Rebuild gt Binary

Instructions for the dog worker to execute...
```

### TOML Frontmatter Schema

```toml
# Required
name = "string"           # Unique plugin identifier
description = "string"    # Human-readable description
version = 1               # Schema version (for future evolution)

[gate]
type = "cooldown|cron|condition|event|manual"
# Type-specific fields:
duration = "1h"           # For cooldown
schedule = "0 9 * * *"    # For cron
check = "gt stale -q"     # For condition (exit 0 = run)
on = "startup"            # For event

[tracking]
labels = ["label:value", ...]  # Labels for execution wisps
digest = true|false            # Include in daily digest

[execution]
timeout = "5m"            # Max execution time
notify_on_failure = true  # Escalate on failure
severity = "low"          # Escalation severity if failed
```

### Gate Types

| Type | Config | Behavior |
|------|--------|----------|
| `cooldown` | `duration = "1h"` | Query wisps, run if none in window |
| `cron` | `schedule = "0 9 * * *"` | Run on cron schedule |
| `condition` | `check = "cmd"` | Run check command, run if exit 0 |
| `event` | `on = "startup"` | Run on Deacon startup |
| `manual` | (no gate section) | Never auto-run, dispatch explicitly |

### Instructions Section

The markdown body after the frontmatter contains agent-executable instructions. The dog worker reads and executes these steps.

Standard sections:
- **Detection**: Check if action is needed
- **Action**: The actual work
- **Record Result**: Create the execution wisp
- **Notification**: On success/failure

---

## New Commands Required

- **`gt stale`** -- Expose binary staleness check (human-readable, `--json`, `--quiet` exit code)
- **`gt dog dispatch --plugin <name>`** -- Dispatch plugin execution to an idle dog (non-blocking)
- **`gt plugin list|show|run|digest|history`** -- Plugin management and execution history

---

## Implementation Plan

### Phase 1: Foundation

1. **`gt stale` command** - Expose CheckStaleBinary() via CLI
2. **Plugin format spec** - Finalize TOML schema
3. **Plugin scanning** - Deacon scans town + rig plugin dirs

### Phase 2: Execution

4. **`gt dog dispatch --plugin`** - Formalized dog dispatch
5. **Plugin execution in dogs** - Dog reads plugin.md, executes
6. **Wisp creation** - Record results on ledger

### Phase 3: Gates & State

7. **Gate evaluation** - Cooldown via wisp query
8. **Other gate types** - Cron, condition, event
9. **Plugin digest** - Daily squash of plugin wisps

### Phase 4: Escalation

10. **`gt escalate` command** - Unified escalation API
11. **Escalation routing** - Config-driven multi-channel
12. **Stale escalation patrol** - Check unacknowledged

### Phase 5: First Plugin

13. **`rebuild-gt` plugin** - The actual gastown plugin
14. **Documentation** - So Beads/Wyvern can create theirs

---

## Open Questions

1. **Plugin discovery in multiple clones**: If gastown has crew/george, crew/max, crew/joe - which clone's plugins/ dir is canonical? Probably: scan all, dedupe by name, prefer rig-root if exists.

2. **Dog assignment**: Should specific plugins prefer specific dogs? Or any idle dog?

3. **Plugin dependencies**: Can plugins depend on other plugins? Probably not in v1.

4. **Plugin disable/enable**: How to temporarily disable a plugin without deleting it? Label on a plugin bead? `enabled = false` in frontmatter?

---

## References

- PRIMING.md - Core design principles
- mol-deacon-patrol.formula.toml - Patrol step plugin-run
- ~/gt/plugins/README.md - Current plugin stub

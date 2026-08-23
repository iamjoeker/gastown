# Gastown/Beads Cleanup Commands Reference

A comprehensive catalog of all cleanup-related commands in the gastown/beads ecosystem, organized by scope and severity.

---

## Process Cleanup

| Command | What it does |
|---------|-------------|
| `gt cleanup` | Kills orphaned Claude processes not tied to active tmux sessions |
| `gt orphans procs list` | Lists orphaned Claude processes (PPID=1) |
| `gt orphans procs kill` | Kills orphaned Claude processes (`--aggressive` for tmux-verified) |
| `gt deacon cleanup-orphans` | Kills orphaned Claude subagent processes (no controlling TTY) |
| `gt deacon zombie-scan` | Finds/kills zombie Claude processes not in active tmux sessions |

## Polecat (Agent Sandbox) Cleanup

| Command | What it does |
|---------|-------------|
| `gt polecat remove <rig>/<polecat>` | Removes polecat worktree/directory (fails if session running) |
| `gt polecat nuke <rig>/<polecat>` | Nuclear: kills session, deletes worktree, deletes branch, closes bead. **Human/Mayor only** — refuses a witness identity (restart-first policy, gt-dsgp) |
| `gt polecat nuke <rig> --all` | Nukes all polecats in a rig (same identity restriction) |
| `gt polecat gc <rig>` | GC stale polecat branches (orphaned, old timestamped) |
| `gt polecat stale <rig>` | Detects stale polecats; `--cleanup` auto-nukes them (same identity restriction) |
| `gt polecat check-recovery` | Reports whether work is at risk (SAFE_TO_NUKE vs NEEDS_RECOVERY) and what the witness may do about it (`witness_action`) |
| `gt polecat clear-state <rig>/<polecat>` | Lifts a deliberate `agent_state` pause (stuck, awaiting-gate, paused, escalated) back to idle. **Witness-runnable** — writes one agent-bead field, touches no worktree/branch/session. Refuses unless the pause is the only blocker (gt-fbgq) |
| `gt session restart <rig>/<polecat>` | How the **witness** reclaims a slot — preserves worktree and branch. Writes **no** `agent_state`, so it does not lift a pause; use `clear-state` for that |
| `gt polecat identity remove <rig> <name>` | Removes a polecat identity |
| `gt done` | Polecat self-cleaning: pushes branch, submits MR/PR path as configured, preserves handoff metadata, kills own session. MR skipped for `--status ESCALATED\|DEFERRED` or `no_merge` paths |

## Git Artifact Cleanup

| Command | What it does |
|---------|-------------|
| `gt prune-branches` | Removes stale local polecat tracking branches (`git fetch --prune` + safe delete) |
| `gt orphans` | Finds orphaned commits never merged (detection only) |
| `gt orphans kill` | Prunes orphaned commits (`git gc --prune=now`) + kills orphaned processes |

## Rig-Level Cleanup

| Command | What it does |
|---------|-------------|
| `gt rig reset` | Resets handoff content, stale mail, orphaned in_progress issues |
| `gt rig reset --handoff` | Clears handoff content only |
| `gt rig reset --mail` | Clears stale mail only |
| `gt rig reset --stale` | Resets orphaned in_progress issues |
| `gt rig remove <name>` | Unregisters rig from registry, cleans up beads routes |
| `gt rig shutdown <rig>` | Stops all agents: polecats, refinery, witness |
| `gt rig stop <rig>...` | Stop one or more rigs |
| `gt rig restart <rig>...` | Stop then start (stop phase cleans up) |

## Town-Wide Shutdown

| Command | What it does |
|---------|-------------|
| `gt down` | Stops all infrastructure (refinery, witness, mayor, boot, deacon, daemon, dolt) |
| `gt down --polecats` | Also stops all polecat sessions |
| `gt down --all` | Full shutdown with orphan cleanup and verification |
| `gt down --nuke` | Kills entire tmux server (DESTRUCTIVE - kills non-GT sessions too) |
| `gt shutdown` | "Done for the day" - stops agents AND removes polecat worktrees/branches. Flags control aggressiveness (`--graceful`, `--force`, `--nuclear`, `--polecats-only`, etc.) |

## Crew Workspace Cleanup

| Command | What it does |
|---------|-------------|
| `gt crew stop [name]` | Stops crew tmux sessions |
| `gt crew restart [name]` | Kills and restarts crew fresh ("clean slate", no handoff mail) |
| `gt crew remove <name>` | Removes workspace, closes agent bead |
| `gt crew remove <name> --purge` | Full obliteration: deletes agent bead, unassigns beads, clears mail |
| `gt crew pristine [name]` | Syncs workspaces with remote (`git pull`) |

## Ephemeral Data / Event Cleanup

| Command | What it does |
|---------|-------------|
| `gt compact` | TTL-based compaction: promotes/deletes wisps past their TTL |
| `gt krc prune` | Prunes expired events from the KRC event store |
| `gt krc config reset` | Resets KRC TTL configuration to defaults |
| `gt krc decay` | Shows forensic value decay report (pruning guidance) |

> **`gt compact report` is not a query.** It runs a real compaction to produce
> its digest, so the bare command and `--json` both delete. Only `--dry-run`
> previews — and until gt-hv3p it did not: the flag never reached the compaction
> subprocess, so `gt compact report --dry-run` deleted 454 wisps on 2026-08-22
> while suppressing the audit bead and the digest mail that would have recorded
> it. Verify a change to this path by measuring the delete-pool count either
> side of the command, never by reading its output.

> **Deletions are archived first.** Every wisp `gt compact` is about to delete is
> written and fsynced to the wisp archive — the same one the reaper writes and
> `gt reaper archive` reads — BEFORE the delete pass starts, so an interrupted
> run leaves records with no deletion and never the reverse. A run that cannot
> write its archive deletes nothing and reports the wisps as held. To enumerate
> what a run removed, take the archived ids and subtract the ids still in
> `wisps`. Relocate the archive with `GT_WISP_ARCHIVE_DIR`.

> `gt compact` acts only on wisps whose `wisp_type` is set. Rows with an empty
> `wisp_type` are counted as **Unclassified** and left untouched — no TTL policy
> can be chosen for them, and defaulting them to 24h would delete 7d escalation,
> recovery and error records (gt-ktvs). Until whatever writes wisps populates
> the column, expect `Unclassified` to be most of `Scanned`, and read a non-zero
> value there as a defect report rather than as normal.

### Where `wisp_type` comes from

`gt compact` keys its whole policy on `wisps.wisp_type` — 6h for `heartbeat`
and `ping`, 24h for `patrol` and `gc_report`, 7d for `recovery`, `error` and
`escalation`. The column is only as useful as the writers that populate it, and
until gt-fqd5 almost nothing did.

Read the two halves together before changing either. Since gt-ktvs an untyped
wisp is **skipped entirely**, not given the default TTL, so classifying a wisp
is not a cosmetic act: it moves that wisp from "compaction never touches it" to
"compaction enforces its TTL". Add a type only where you have decided what
should happen to the wisp at that age.

The vocabulary is bd's and it is closed: bd rejects any other value at create
time. There is deliberately no type for merge-request, sling-context or
work-molecule wisps, so those stay unclassified and take the 24h default.
Leave them that way rather than borrowing a bucket whose retention was reasoned
about for something else.

Two write paths exist, because bd only offers one and it does not cover
molecules:

| Wisp | Written by | How |
|------|-----------|-----|
| Escalation records | `beads.CreateEscalationBead` | `bd create --wisp-type=escalation` |
| Plugin/dog run receipts | `plugin.Recorder` | `bd create --wisp-type=gc_report` |
| Patrol cycle digests | `gt mol` digest path | `beads.CreateOptions{WispType: ...}` |
| Swarm tracking wisps | `witness.createSwarmWisp` | `bd create --wisp-type=patrol` |
| Patrol molecule wisps | `cmd.stampPatrolWispType` | post-spawn `UPDATE wisps` |
| Slung molecule wisps + steps | `cmd.stampMoleculeWispType` | post-spawn `UPDATE wisps` |
| Dog molecule wisps + steps | `daemon.dogMol.stampWispType` | post-spawn `UPDATE wisps` |

The UPDATE path exists because `bd mol wisp`, `bd mol wisp create` and
`bd mol bond` accept no `--wisp-type`, and `bd update` has none either — a
molecule wisp has no other route to the column. Retire it if beads grows the
flag.

For molecules the value comes from the formula's `[vars.wisp_type]`, resolved
rig > town > embedded. Note what that precedence means in practice: a town that
has already provisioned a formula to `.beads/formulas/` shadows the embedded
copy, so adding a declaration to an embedded formula does **not** reach it. The
dog spawner therefore carries its own `gc_report` default rather than trusting
resolution.

Verify with SQL, never by reading the writer:

```bash
bd sql --json "select coalesce(wisp_type,'') t, count(*) c from wisps group by t"
```

## Dolt Database Cleanup

| Command | What it does |
|---------|-------------|
| `gt dolt cleanup --dry-run` | Reports what cleanup would do, and any refusal it would raise. Deletes nothing. |
| `gt dolt list` | Owner or protection label for every database, not just the flagged ones |
| `gt dolt cleanup` | Removes orphaned databases from `.dolt-data/` |
| `gt dolt stop` | Stops the Dolt SQL server |
| `gt dolt rollback [backup-dir]` | Restores `.beads` from backup, resets metadata |

`gt dolt cleanup` refuses when too large a share of the town's databases is
flagged, and its refusal offers `--force`. `--force` deletes every flagged
database without the per-database check for user tables — it is the
highest-blast-radius command available against the live data plane, and it is
not the recommended next step after a refusal. Read `--dry-run` first.

Reporting surfaces — `gt dolt status`, `gt dolt init`, `gt doctor` — list
orphans but deliberately name no deleting command. They report; the operator
decides (gt-xhjb).

`gt doctor --fix` reaches the same deletion, so it evaluates the same refusal:
above the orphan ratio its fix refuses and explains instead of deleting, and
below it the fix deletes with the per-database user-tables check in force
(`--force` has no equivalent here). The balk predicate lives in
`internal/doltserver` precisely so the two commands cannot answer differently —
it used to live in `internal/cmd`, where `internal/doctor` could not reach it,
and `gt doctor --fix` force-deleted every orphan with no threshold check at all
(gt-baj6). `gt doctor` names the refusal in its report, so the fix cannot be a
surprise.

### Keeping an unreferenced database

A database with no rig `metadata.json` pointing at it is reported as an orphan
even when it is deliberate. To keep one permanently, name it in
`settings/config.json`:

```json
{
  "protected_dolt_databases": ["pc1", "pc2", "pc3"]
}
```

Orphan detection then skips it, `gt dolt list` labels it protected, and
`gt dolt cleanup` refuses to remove it **with or without `--force`** — which
also covers `gt doctor --fix` and `gt rig add`'s orphan drop, since the refusal
lives in `RemoveDatabase` rather than in any one command.
Write the decision here rather than relying on operators remembering it.

## Bead / Hook Cleanup

| Command | What it does |
|---------|-------------|
| `gt close <bead-id>` | Closes beads (lifecycle termination) |
| `gt unsling` / `gt unhook` | Removes work from agent's hook, resets bead status to "open" |
| `gt hook clear` | Alias for unsling |

## Dog (Infrastructure Worker) Cleanup

| Command | What it does |
|---------|-------------|
| `gt dog remove <name>` | Removes worktrees and dog directory |
| `gt dog remove --all` | Removes all dogs |
| `gt dog clear <name>` | Resets stuck dog to idle state |
| `gt dog done [name]` | Marks dog as done, clears work field |

## Convoy Cleanup

| Command | What it does |
|---------|-------------|
| `gt convoy close <id>` | Closes a convoy bead |
| `gt convoy land <id>` | Closes convoy, cleans up polecat worktrees, sends completion notifications |

## Mail Cleanup

| Command | What it does |
|---------|-------------|
| `gt mail delete <msg-id>` | Deletes specific messages |
| `gt mail archive <msg-id>` | Archives messages (`--stale` for stale ones) |
| `gt mail clear [target]` | Deletes all messages from an inbox (town quiescence) |

## Misc State Cleanup

| Command | What it does |
|---------|-------------|
| `gt namepool reset` | Releases all claimed polecat names |
| `gt checkpoint clear` | Removes checkpoint file |
| `gt issue clear` | Clears issue from tmux status line |
| `gt doctor --fix` | Auto-fixes: orphan sessions, wisp GC, stale redirects, worktree validity |

## Temp / Scratchpad Cleanup

| Command | What it does |
|---------|-------------|
| `gt deacon sweep-scratchpads` | Reports which agent scratchpads under `$TMPDIR/claude-<uid>` are provably dead and how much a sweep would reclaim (dry run) |
| `gt deacon sweep-scratchpads --apply` | Deletes them, oldest first, only while the filesystem is above the high-water mark |
| `gt deacon sweep-scratchpads --all --apply` | Deletes every dead scratchpad regardless of filesystem pressure |

A session scratchpad is only ever deleted when no live process can own it — see
[Scratchpad Retention](scratchpad-retention.md) for the liveness proof and why
an age-only sweep is both unsafe and ineffective here.

## System-Level Cleanup

| Command | What it does |
|---------|-------------|
| `gt disable --clean` | Disables gastown + removes shell integration |
| `gt shell remove` | Removes shell integration from RC files |
| `gt config agent remove <name>` | Removes custom agent definition |
| `gt uninstall` | Full removal: shell integration, wrapper scripts, state/config/cache dirs |
| `make clean` | Removes compiled `gt` binary |

## Scripts

| Command | What it does |
|---------|-------------|
| `scripts/migration-test/reset-vm.sh` | Restores VM to pristine v0.5.0 state (test environments) |

## Internal (Automatic / Side-Effect)

| Function | Where | What it does |
|----------|-------|-------------|
| `cleanupOrphanedProcesses()` | `polecat.go` | Auto-runs after nuke/stale cleanup |
| `retirePolecatSessionAfterDone()` | `done.go` | Self-terminates tmux session with PID exclusion after durable handoff |
| `rollbackSlingArtifacts()` | `sling.go` | Cleans up partial sling failures |
| `cleanStaleHookedBeads()` | `unsling.go` | Repairs beads stuck in "hooked" state |
| `gt signal stop` | `signal_stop.go` | Clears stop-state temp files at turn boundaries |
| `make install` | `Makefile` | Removes stale `~/go/bin/gt` and `~/bin/gt` binaries |

---

## Cleanup Layers (Low to High Severity)

| Layer | Scope | Key Commands |
|-------|-------|-------------|
| **L0** | Ephemeral data | `gt compact`, `gt krc prune` (TTL-based lifecycle), `gt deacon sweep-scratchpads` |
| **L1** | Processes | `gt cleanup`, `gt orphans procs kill`, `gt deacon cleanup-orphans` |
| **L2** | Git artifacts | `gt prune-branches`, `gt polecat gc`, `gt orphans kill` |
| **L3** | Agents/sessions | `gt polecat nuke`, `gt done`, `gt shutdown`, `gt down` |
| **L4** | Workspace | `gt rig reset`, `gt doctor --fix`, `gt dolt cleanup` |
| **L5** | System | `gt uninstall`, `gt disable --clean` |

**Total: ~63 commands/functions** across the cleanup ecosystem.

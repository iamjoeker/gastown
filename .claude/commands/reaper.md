---
description: Run the wisp reaper — scan, reap, purge, and auto-close stale beads across all Dolt databases
allowed-tools: Bash(gt reaper:*), Bash(gt escalate:*), Bash(gt dolt status:*)
argument-hint: [--dry-run]
---

# Wisp Reaper

Reap stale wisps and close stale issues across all production Dolt databases.
Runs the same cycle as `mol-dog-reaper` but directly, without Dog dispatch.

Arguments: $ARGUMENTS
If `--dry-run` is passed, report counts without making changes.

## Configuration Defaults

| Parameter | Default | Description |
|-----------|---------|-------------|
| max_age | 24h | Wisps older than this are reaped (closed) |
| purge_age | 72h | Closed wisps older than this are purged (deleted) |
| stale_issue_age | 720h | Issues stale longer than this are auto-closed (30d) |
| mail_delete_age | 72h | Closed mail older than this is purged |
| alert_threshold | 3000 | Open wisp count that triggers escalation |
| dolt_port | 3307 | Dolt server port |

`stale_issue_age` is 720h (30d) because auto-close **mutates real issues**, and 30d
is the value every other surface publishes: the `mol-dog-reaper` formula var, the
`gt reaper auto-close --help` default, and the daemon constant. This file said 168h
(7d) until gt-zjb — running it closed live issues 4.3x sooner than the town's
documented policy, silently. If that number ever needs to change, change it in
`internal/cmd/reaper.go` (the `--stale-age` flag default) and here in the same
commit; `TestReaperSkillStaleAgeMatchesCLIDefault` fails if they drift apart.

## Execution Steps

### Step 1: Verify Dolt server health

```bash
gt dolt status
```

If the server is unhealthy or unreachable, STOP and escalate:
```bash
gt escalate "Reaper blocked: Dolt server unhealthy" -s HIGH
```

### Step 2: Discover databases

```bash
gt reaper databases --json
```

This lists all production databases on the Dolt server.
Expected databases: `hq`, `beads`, `gastown` (and any rig-specific DBs).

### Step 3: Scan each database for candidates

For each database returned in Step 2:

```bash
gt reaper scan --db=<name> --port=3307 \
  --max-age=24h --purge-age=72h \
  --mail-age=72h --stale-age=720h \
  --json
```

Inspect the JSON output:
- `reap_candidates`: wisps eligible for closing
- `purge_candidates`: closed wisps eligible for deletion
- `open_wisps`: total open wisp count
- `anomalies`: array of detected problems

If `open_wisps` exceeds 3000 across all databases, note for escalation.
If no candidates found across all databases, report "nothing to reap" and stop.

### Step 4: Reap stale wisps

For each database with reap candidates:

```bash
gt reaper reap --db=<name> --port=3307 --max-age=24h [--dry-run] --json
```

**IMPORTANT**: Scan/reap count mismatch is NORMAL (witness closes wisps concurrently).
Do NOT escalate scan > reap mismatches. Only escalate actual errors.

### Step 5: Purge old closed wisps and mail

For each database with purge candidates:

```bash
gt reaper purge --db=<name> --port=3307 \
  --purge-age=72h --mail-age=72h [--dry-run] --json
```

Watch for `dolt_commit_failed` anomalies — purged data may not persist.

### Step 6: Auto-close stale issues

For each database with stale candidates:

```bash
gt reaper auto-close --db=<name> --port=3307 \
  --stale-age=720h [--dry-run] --json
```

Auto-close NEVER touches: P0/P1 issues, epics, or issues with active dependencies.
It also never touches beads labeled `gt:standing-orders`, `gt:keep`, `gt:role`,
`gt:rig`, `gt:agent`, or `gt:message` (unread mail). Scan reports candidates using
the same exclusions, so a scan count that exceeds what auto-close does is a bug,
not an expected artifact.

### Step 7: Report

Print a summary in this format:

```
## Reaper Report

**Databases scanned**: N
**Wisps reaped**: N (stale open wisps closed)
**Wisps purged**: N (old closed wisps deleted)
**Mail purged**: N (old closed mail deleted)
**Issues auto-closed**: N (stale issues past 720h)
**Open wisps remaining**: N
**Anomalies**: <list or "none">
```

If anomalies were found:
```bash
gt escalate "Reaper anomalies detected" -s MEDIUM -m "<anomaly details>"
```

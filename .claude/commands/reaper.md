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
| purge_age | 168h | Closed wisps older than this are purged — deleted permanently (7d) |
| stale_issue_age | 720h | Issues stale longer than this are auto-closed (30d) |
| mail_delete_age | 168h | Closed mail older than this is purged (7d) |
| alert_threshold | 3000 | Open wisp count that triggers escalation |
| dolt_port | 3307 | Dolt server port |

`stale_issue_age` is 720h (30d) because auto-close **mutates real issues**, and 30d
is the value every other surface publishes: the `mol-dog-reaper` formula var, the
`gt reaper auto-close --help` default, and the daemon constant. This file said 168h
(7d) until gt-zjb — running it closed live issues 4.3x sooner than the town's
documented policy, silently.

`purge_age` and `mail_delete_age` are 168h (7d) because purge **deletes
permanently**: wisps are unversioned and unbacked (hq-del4), which is what made
gt-5y7 — a purge that destroyed a closed-but-unmerged refinery rejection ~11
minutes old — unrecoverable. 7d is what every other destructive path uses: the
`--purge-age` / `--mail-age` flag defaults, the `mol-dog-reaper` formula vars,
`donePurgeMinAge` in `internal/cmd/done.go`, and `purgeMinAge` in
`internal/doltserver/sync.go`. This file said 72h until gt-tu67 — a manual
/reaper run deleted closed wisps and closed mail 2.3x sooner than the town's own
policy.

If any of these numbers ever needs to change, change it in
`internal/cmd/reaper.go` (the flag defaults) and here in the same commit; the
guards in `internal/cmd/reaper_skill_ages_test.go` fail if they drift apart.

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
  --max-age=24h --purge-age=168h \
  --mail-age=168h --stale-age=720h \
  --json
```

Inspect the JSON output:
- `reap_candidates`: wisps eligible for closing
- `purge_candidates`: closed wisps eligible for deletion
- `protected_from_purge`: closed wisps deletion is holding back by type or pin
- `archivable_from_purge`: the subset of those a purge with an archive releases
- `open_wisps`: total open wisp count
- `anomalies`: array of detected problems

If `open_wisps` exceeds 3000 across all databases, note for escalation.
If no candidates found across all databases, report "nothing to reap" and stop.

### Step 4: Reap stale wisps

For each database with reap candidates:

```bash
gt reaper reap --db=<name> --port=3307 --max-age=24h [--dry-run] --json
```

**IMPORTANT**: Scan/reap count mismatch is NORMAL in BOTH directions. Do not
escalate either one — only escalate actual errors.

- `scan > reap`: the witness closes wisps concurrently.
- `reap > scan`: one reap call runs to a fixed point (gt-r1b). Closing a molecule
  wisp releases its step-wisps, which the same call then closes; scan and
  `--dry-run` count only what is closable right now and cannot see that cascade,
  so they are a LOWER BOUND, not a prediction.

One invocation per database is enough — `passes` in the JSON reports how many
rounds it took, and the last round is always the one that closed nothing.

### Step 5: Purge old closed wisps and mail

For each database with purge candidates:

```bash
gt reaper purge --db=<name> --port=3307 \
  --purge-age=168h --mail-age=168h [--dry-run] --json
```

Watch for `dolt_commit_failed` anomalies — purged data may not persist.
Watch for `wisp_archive_failed` / `wisp_archive_stalled` anomalies — protected
wisps were NOT released and are still accumulating.

`wisps_purged`, `wisps_archived` and `wisps_protected` partition the
closed-past-`purge_age` window; they always add up and none absorbs another.

- `wisps_purged` — ordinary wisps, deleted outright.
- `wisps_archived` — merge-request and escalation wisps, exported to the durable
  archive (`~/.gt/wisp-archive/`, JSON Lines) and only then deleted. This is what
  keeps type protection from being unbounded growth (gt-6xwt). Read them back
  with `gt reaper archive --grep=<id>`.
- `wisps_protected` — still held back: pinned rows, or anything the archive
  refused. Climbing run after run means the archive is unavailable — check the
  stderr warning from `gt reaper purge`.

`--no-archive` restores the old contract (protected types are never deleted).
Never pass it to work around an archive error: the accumulation is the bill.

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
**Wisps archived**: N (protected wisps exported to the archive, then released)
**Wisps protected**: N (still held back — pinned, or archive unavailable)
**Mail purged**: N (old closed mail deleted)
**Issues auto-closed**: N (stale issues past 720h)
**Open wisps remaining**: N
**Anomalies**: <list or "none">
```

If anomalies were found:
```bash
gt escalate "Reaper anomalies detected" -s MEDIUM -m "<anomaly details>"
```

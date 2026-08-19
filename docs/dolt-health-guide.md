# Dolt Health Guide

This guide covers evidence capture for Dolt outages and Gas Town behavior
mismatches that look like Dolt trouble.

## When To Use This

Use this checklist when any of these happen:

- `bd` commands hang, time out, or return unexpected empty results.
- `gt dolt status` reports unhealthy server state, high latency, stale PIDs, or
  orphan test databases.
- A Gas Town command behaves differently from its documented or expected behavior
  and Dolt is part of the control path.

Do not restart Dolt before collecting diagnostics. A blind restart can destroy the
state needed to explain the incident.

## Immediate Diagnostics

Capture non-fatal diagnostics first:

```bash
gt dolt dump 2>&1 | tee /tmp/dolt-hang-$(date +%s).log
gt dolt status 2>&1 | tee /tmp/dolt-status-$(date +%s).log
```

Then escalate with the evidence path:

```bash
gt escalate -s HIGH "Dolt: <symptom>" -m "Evidence: /tmp/dolt-status-..."
```

## Hazard: Dolt May Have a Second Supervisor

A town can run Dolt under a systemd unit (Gas Town rigs commonly use a user unit
named `gt-dolt.service`). When it does, **gt cannot stop the server**, and the
distinction changes how you read every symptom below.

`doltserver.Stop` sends SIGTERM to the server's PID. Under `Restart=always` that
signal is not a stop — it is a restart trigger. systemd brings the server back
`RestartSec` later with a **new PID**. `gt dolt stop` and `gt down` now say so
instead of reporting success:

```
! Signalled Dolt server (PID 3580495) — but it is NOT stopped.

  This server is supervised by systemd unit gt-dolt.service (Restart=always).
  ...
  To stop the server for real:
      systemctl --user stop gt-dolt.service
```

If you see that notice, the server is coming back and re-running the stop will not
change it. That retry loop is what produced the gt-09e4 incident: three `gt down`
runs in six minutes, each reporting Dolt stopped, while the data plane never went
down at all.

`gt dolt restart` refuses outright under a reviving unit — gt would rebind the
port before systemd's restart fired, leaving systemd retrying a bind failure on a
loop. Use `systemctl --user restart gt-dolt.service`.

`systemctl --user stop gt-dolt.service` takes beads, mail, and identity offline
for every agent in the town. It is not a routine step.

Note the policy dependence: only `Restart=always` and `Restart=on-success` revive
the server. Dolt handles SIGTERM and exits 0, and systemd counts SIGTERM as a
clean exit regardless, so `on-failure`, `on-abnormal`, `on-abort` and
`on-watchdog` all leave a gt-issued stop standing.

### Starting a supervised server

`gt dolt start` starts the **unit**, not a bare process, when one owns this
town's server:

```
Dolt is supervised by systemd unit gt-dolt.service (Restart=on-failure) — starting through the supervisor
```

That is not cosmetic. A `dolt` spawned directly while the unit is stopped is a
server systemd believes does not exist: nothing restarts it when it dies, it dies
with whatever shell started it, and a later `systemctl --user start
gt-dolt.service` fails on an occupied port while `systemctl status` still reads
`inactive`. Under `Restart=on-failure` there is no second instance to paper over
it either — a graceful stop stays stopped.

gt finds the unit from a record in `daemon/dolt-state.json`, written whenever a
command sees a running supervised server (`gt dolt start`, `stop`, `status`,
`down`, `migrate`). Consequences worth knowing:

- **A town gt has never seen supervised will still spawn directly.** Run `gt dolt
  status` once against the running server to teach it the unit.
- **The record stores the unit name only.** `Restart=`, `NRestarts` and the
  start-limit properties are re-read from systemd each time, so a policy change
  (like the 2026-08-18 switch to `on-failure`) takes effect immediately.
- **A removed or masked unit is ignored**, and gt starts the server itself.
- `GT_DOLT_DIRECT_START=1 gt dolt start` forces a direct spawn — for recovering
  from a unit that points at the wrong data directory or port. It is the
  unsupervised path, deliberately.

`gt dolt migrate` uses the same record for a refusal it could not make before: it
moves database directories on disk, so if the remembered unit is not confirmed
`ActiveState=inactive` it stops before touching anything, rather than trusting a
point-in-time "no server is running" for the several minutes a migration takes.

### Reading `journalctl --user -u gt-dolt.service`

Two exit signatures, two very different causes:

| Journal lines | Means |
|---|---|
| `Stopping...` / `Stopped` | An explicit `systemctl stop`. Only systemctl produces this wording. |
| `Consumed ... CPU time`, then `Scheduled restart job` ~`RestartSec` later | The process was killed by something **outside** systemd — the `doltserver.Stop` signature (`gt dolt stop`, `gt down`, `gt doctor --fix`). |

A restart counter that resets to 1 means someone ran an explicit `systemctl start`
in between. Correlate exits against shell history (`~/.local/share/fish/fish_history`
records an epoch `when:` per command) before blaming gt — in gt-09e4 every exit
matched an operator command to the second.

### "Connection refused, then a new PID, and nobody restarted it"

This is a restart, not a mystery. `gt dolt status` prints a **Restart History**
block whenever the unit has restarted the server or cannot detect a crash loop:

```
  Supervisor: systemd unit gt-dolt.service (Restart=on-failure)

  Restart History:
    ! Restarted 2 time(s) by systemd unit gt-dolt.service since it was last started by hand.
      ...
    ! Crash-loop detection is OFF for this unit (StartLimitIntervalSec=0 or
      StartLimitBurst=0). ...
```

Read the count as a **lower bound**: systemd resets `NRestarts` on every manual
`systemctl start`/`restart`, so it counts restarts within the current manual
start, not since boot. A count above zero means the previous process died — the
uptime beside it belongs to the replacement, not to that process.

The second notice is the reason a repeatedly dying server can look healthy
everywhere. With `StartLimitIntervalSec=0` the unit never enters `failed`, so
`systemctl status` stays green, `journalctl -p err` shows nothing, and the only
journal trace of the exit is a bare `Consumed ... CPU time` line — no
`Main process exited`, no `Failed with result`. On 2026-08-18 two agents
independently escalated two such exits in 75s and neither could attribute them
(hq-njloj, hq-69g3w, gt-qiok). Arm a start limit on the unit if you want a real
crash loop to surface as a failed unit.

### Note on `dolt.auto-start`

`dolt.auto-start=false` / `BEADS_DOLT_AUTO_START=0` only stops a **bd/gt client**
from spawning its own server on connect. It has no effect on a systemd unit, and
no gt config can disable one. The bd-emitted message
`Dolt server auto-start is disabled (dolt.auto-start: false)` is true and
irrelevant at the same time whenever a unit is managing the server; it has already
sent two agents down the wrong path. Check `systemctl --user status gt-dolt.service`
before trusting it.

## RCA Capture Checklist

Attach this checklist to the escalation body, the follow-up bead, or the war-room
entry. Use `N/A` only when a field truly does not apply to a non-Dolt behavior
mismatch.

```markdown
### RCA Capture

- Trigger command:
- Concurrent GT processes:
- Dolt pid/status:
- Stale pid status:
- Orphan test server status:
- Suspected GT code path:
- Expected behavior:
- Observed behavior:
- Evidence source:
- Likely root cause:
- Smallest fix direction:
```

## Field Notes

- **Trigger command**: the exact command or agent action that exposed the issue.
- **Concurrent GT processes**: active mayor, witness, refinery, polecat, dog, or
  test processes that may share Dolt.
- **Dolt pid/status**: server PID, health, latency, and port state from
  `gt dolt status` or `gt dolt dump`.
- **Stale pid status**: whether pid files point at missing or unrelated processes.
- **Orphan test server status**: orphan database or test-server count, especially
  `testdb_*`, `beads_t*`, `beads_pt*`, or `doctest_*`.
- **Suspected GT code path**: command, package, plugin, or template that most
  likely drove the behavior.
- **Expected behavior**: what the command or workflow should have done.
- **Observed behavior**: what actually happened, including errors and timings.
- **Evidence source**: log files, command output, bead IDs, session IDs, or branch names.
- **Likely root cause**: current best explanation, clearly marked if uncertain.
- **Smallest fix direction**: the least invasive code, docs, or operations change
  that would prevent repeat incidents.

## Tests Cannot Reach the Production Port

`go test ./...` used to create real databases on the live server. Every Dolt
port resolver in the tree ends in the same fallback — `doltserver.DefaultPort`,
which is 3307 — so a test that built a fixture town under `t.TempDir()` and
then called production code inherited production. Six databases were created
in 34 seconds that way on 2026-08-18 (`forkrig`, `pc1`, `pc2`, `pc3`,
`testrig`, `testrip`).

Every test package now calls `testenv.GuardProductionDolt()` from its
`TestMain`, which points `GT_DOLT_PORT`, `BEADS_DOLT_PORT` and
`BEADS_DOLT_SERVER_PORT` at port 63307. Nothing listens there, so a suite that
reaches for Dolt without arranging its own server gets "connection refused"
instead of silently writing to the live one.

What this means in practice:

- **Running the suite is safe.** `go test ./...` from a rig, a worktree, or an
  agent sandbox no longer touches the production server. It does not need
  `gt dolt cleanup` afterwards.
- **A new package needs the TestMain.**
  `TestEveryTestPackageGuardsProductionDolt` in `internal/testenv` fails on any
  package that has tests but no guarded `TestMain`, and on any `TestMain` — a
  build-tagged variant included — that does not call the guard. Copy the file
  any package already has.
- **Order matters in a TestMain that does more.** `UnsetAmbientTownEnv` strips
  `GT_*` and `BEADS_*` wholesale, so it must run *before* the guard; container
  helpers such as `EnsureDoltContainerForTestMain` write their own mapped port,
  so they must run *after* it.
- **Testing the fallback itself.** A test whose subject is the unconfigured
  default calls `testenv.WithoutDoltPortGuard(t)`, which drops the guard for
  that test only. It is for tests that resolve a port without connecting.
- **Deliberately talking to production** — an operational smoke check run by
  hand — requires `GT_TEST_ALLOW_PRODUCTION_DOLT=1`. It is never set in CI or
  in an agent sandbox.

An orphan database appearing after a test run now means the guard was bypassed,
not that a suite forgot to clean up. Treat it as a bug and capture it with the
RCA checklist above.

## Simulated Incident Smoke Check

For documentation-only RCA work, use this smoke check to verify the checklist is
available and wired into the escalation path:

```bash
test -f docs/dolt-health-guide.md
grep -n "Trigger command" docs/dolt-health-guide.md
grep -n "RCA capture checklist" internal/templates/townroot/claude.md docs/design/escalation.md
```

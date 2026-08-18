# Turn Boundaries: Why Patrol Agents Stop, and What Restarts Them

A patrol agent — a Witness, a Refinery, the Deacon — is a conversational agent.
It executes **only inside a turn**. Its patrol loop, including the
`gt mol step await-signal` / `await-event` call that would put it back to sleep,
is a child process of that turn.

So when a turn ends, the await process is gone and nothing is left running. The
agent sits at an empty prompt indefinitely.

This is not a defect the agent can fix. Re-entering an ended turn is not a
capability it has, and any finite turn ends. **"The agent should re-enter its
loop" describes something that cannot happen.** A remedy aimed at agent
discipline has nothing to attach to: one Witness wrote the "do not narrate after
filing the report" lesson down between two stalls and stalled the same way
again.

The loop needs an owner that outlives a turn.

## Why every status surface says "running"

A stopped agent's tmux session is alive, its process tree is intact, and
`gt rig list` / `gt witness status` report `State: running` for the entire stall.
Nothing in the bead layer or the status commands shows it, because:

- The **agent-bead heartbeat label** is stamped only by `await-signal`. When the
  agent stops running, the label stops advancing — but so does the open patrol
  wisp, because both are downstream of the same missing process. Two readings of
  one cause are not two independent surfaces.
- The **open patrol wisp's age** is confounded in the other direction: it only
  rotates at `gt patrol report`, so it climbs for a healthy agent on a long
  working turn exactly as it does for a stopped one. Agents have been measured
  far over cap while mid-merge.
- The **absence of an await process** separates "not waiting" from "waiting",
  not stopped from working. A computing agent has no await process either. It is
  also easy to point at the wrong process: Witnesses run `await-signal` and
  Refineries run `await-event`, and a `ps | grep await-signal` aimed at a
  Refinery is a structural zero that can never return anything else.

The caps differ by role too — 5m for the Witness `await-signal` loop, 15m for the
Refinery `await-event` loop. Scoring one role against the other's constant
produces confident nonsense.

`gt witness status`, `gt refinery status`, and `gt rig list` now report the turn
state next to the session state, so the two are no longer conflated:

```
🦉 Witness: beads

  State: ● running
  Turn:  ○ ended — the patrol loop is stopped at an empty prompt
```

```
🟢 beads
   Witness: ● running (turn ended)  Refinery: ● running (turn ended)
```

In JSON the field is `turn` (`witness_turn` / `refinery_turn` in `gt rig list`),
one of `active`, `ended`, `stranded`, `unknown`. `running` keeps its old meaning
— the tmux session is alive — because that is a different fact, not a wrong one.

## The signal that is not confounded

The pane. It carries the turn-ended state directly:

| Pane reads | Meaning |
| --- | --- |
| Busy marker (`esc to interrupt`) on the status line | A turn is in flight — computing *or* legitimately blocked in an await. Leave it alone. |
| No busy marker, composer empty | The turn ended. The agent will not run again unprompted. |
| No busy marker, real text in the composer | A submit was stranded. A separate defect; do not type into it. |

Two traps in reading it:

- **The composer is rarely truly blank.** The TUI draws a dim placeholder ghost
  (`ESC[2m … ESC[0m`), and its text varies per agent — "keep patrolling",
  "continue patrol", and questions that read exactly like real staged input
  ("what did the dolt test come back as?"). Under `capture-pane -p` you cannot
  see dimness at all and the ghost reads as a pending message. Capture with
  `-e` and treat an all-dim span as empty.
- **Scope the busy-marker scan.** Agents that discuss pane-reading put the literal
  string `esc to interrupt` into their own transcripts, so a whole-pane grep
  matches their prose and reports a stopped agent as working. That failure was
  observed live on two agents at once, and it is silent — the check returns a
  plausible answer instead of an error. Anchor the scan on the composer line.

## What restarts them

The daemon, in `wakeStoppedPatrolAgents` (`internal/daemon/patrol_wake.go`),
running each heartbeat after the `ensure*Running` steps. Those steps cannot help
here: they see a live session, report "already running", and stop — a live
session is exactly the case this failure produces.

For each Witness, Refinery, and the Deacon it reads the pane
([`tmux.TurnState`](../../internal/tmux/turn_state.go)), and where the turn has
ended it delivers a nudge. Two agreeing samples a few seconds apart are required,
so a pane caught between a tool result and the next request is not mistaken for a
stopped one.

This terminates the "who wakes the waker?" recursion. Witnesses stop and the
Deacon wakes them; the Deacon stops too and something has to wake it. Any
re-invoker staffed by an agent inherits the same defect one level up — it is
just one more thing that needs nudging. The daemon is not a turn-taking agent, so
the recursion ends there.

Deliberately excluded:

- **The Mayor** — it sits at a prompt waiting for its operator. An ended turn is
  its resting state, not a fault.
- **Polecats** — an idle polecat is a completed one; `reapIdlePolecats` ends the
  session rather than extending it.
- **Roles whose patrol is disabled in config** — restarting a loop an operator
  turned off is worse than leaving it stopped.

Configuration (`settings/config.json`, under `operational.daemon`):

| Key | Default | Effect |
| --- | --- | --- |
| `patrol_wake_enabled` | `true` | Master switch. Off means stopped agents stay stopped. |
| `patrol_wake_cooldown` | `5m` | Minimum interval between wakes of the same session. Only bites when a wake does not take; a wake that lands makes the agent active again. |

## What this does not fix

Waking an agent restores the loop. It does not recover work the stopped turn was
the sole owner of. Two harms survive at different strengths:

- **Held knowledge** — something the agent knows and nobody else does. Fixed by
  writing it to a bead as you go, not by waking sooner.
- **In-flight work with no collector** — a background command still producing a
  result whose only reader is the stopped agent. A bead does not collect it. What
  does is sending the output to a **path** rather than into the agent's
  scrollback: once it is at a path, collection is agent-agnostic and the
  spawning agent's liveness stops mattering. One such run was recovered by a
  different agent on a different rig reading `/tmp/dolt-test-merged.log` directly
  while its owner was still stopped.

The residue after both is work that requires a **decision** on completion rather
than mere recording. Recording is agent-agnostic; judgement is not.

## See also

- [Heartbeats](heartbeats.md) — the three heartbeat stores and why none of them
  detects this.
- [The Propulsion Principle](propulsion-principle.md) — what an agent does when
  it *is* running.

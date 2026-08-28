+++
name = "rebuild-gt"
description = "Rebuild stale gt binary from gastown source"
version = 3

[gate]
type = "cooldown"
duration = "1h"

[tracking]
labels = ["plugin:rebuild-gt", "rig:gastown", "category:maintenance"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
severity = "medium"
+++

# Rebuild gt Binary

Checks if the gt binary is stale (built from older commit than HEAD) and rebuilds.

**SAFETY**: This plugin MUST only rebuild forward (binary ancestor of HEAD) and
only from the main branch. Rebuilding to an older or diverged commit caused a
crash loop where every new session's startup hook failed, the witness respawned
it, and the loop repeated every 1-2 minutes.

## Gate Check

The Deacon evaluates this before dispatch. If gate closed, skip.

## Detection

Check binary staleness:

```bash
gt stale --json
```

Read the fields **in this order**. The order is the safety property, not a
style choice:

1. `"skipped": true` → the check could not MEASURE. Record a skip wisp with
   `skip_reason` and exit. `stale` is false in this case for want of an answer,
   not for want of staleness, so any rule that reads `stale` first turns an
   unmeasurable binary into a fresh one and makes every guard below it
   unreachable. This is what left a two-hour-old binary installed (gt-ympl).
2. `"stale": false` → binary is fresh, but only believe it when
   `"compare_ref_refreshed": true`. An explicit `false` means the verdict came
   from a local ref that nothing in the town updates, which cannot prove
   freshness. The field being ABSENT means an older gt is answering; fall back
   to the legacy reading there, or the fix can never deploy itself.
3. `"safe_to_rebuild": false` → **DO NOT REBUILD**. Record a skip wisp and exit.
   The repo is on a non-main branch, or the build ref is not a descendant of
   the binary commit (would be a downgrade).
4. `"safe_to_rebuild": true` → proceed.

## Pre-flight Checks

Before building, verify the source repo is clean and on main:

```bash
cd ~/gt/gastown/mayor/rig
git status --porcelain  # Must be clean
git branch --show-current  # Must be "main"
```

If either check fails, skip the rebuild and record a wisp.

## Fast-forward the source

`gt stale` measures the binary against the **remote** build branch; `make
build` compiles whatever the checkout holds. If the checkout is behind, the
rebuild produces the commit the binary already had — the plugin then reports
success on every cycle while the binary never advances. So bring the checkout
up to its upstream first, and only ever forward:

```bash
git -C ~/gt/gastown/mayor/rig fetch --no-tags --no-write-fetch-head --force \
  origin "refs/heads/main:refs/gt/rebuild-gt/$$"
git -C ~/gt/gastown/mayor/rig merge --ff-only "$(git -C ~/gt/gastown/mayor/rig rev-parse refs/gt/rebuild-gt/$$)"
```

A private destination ref, never `FETCH_HEAD`: that file is one per repository
and `.repo.git` is shared by every worktree in the rig, so a concurrent fetch
anywhere in the town can overwrite it between the write and the read (gt-880s).

If the fetch fails or the branch cannot fast-forward, skip — do not merge.

## Action

Rebuild from source (the mayor/rig directory is the canonical source):

```bash
cd ~/gt/gastown/mayor/rig && make build && make safe-install
```

**IMPORTANT**: Use `make safe-install` (not `make install`) to avoid restarting
the daemon while sessions are active. safe-install replaces the binary but does
NOT restart the daemon — sessions will pick up the new binary on their next cycle.

## Verify the rebuild landed

Re-run `gt stale --quiet` (0=stale, 1=fresh, 2=undetermined). The binary
answering is the one just installed, so this exercises changed behaviour rather
than trusting a report. If it still says stale, the rebuild did not take:
record a failure and escalate instead of recording success. Do **not** verify
with `gt version` or the binary's mtime — on 2026-08-25 both agreed with a
rebuild that had not happened.

## Record Result

On success:
```bash
gt plugin record-run --plugin rebuild-gt --result success --rig gastown \
  --title "Plugin: rebuild-gt [success]" \
  --description "Rebuilt gt: $OLD → $NEW ($N commits)" >/dev/null 2>&1 || true
```

On failure:
```bash
gt plugin record-run --plugin rebuild-gt --result failure --rig gastown \
  --title "Plugin: rebuild-gt [failure]" \
  --description "Build failed: $ERROR" >/dev/null 2>&1 || true

gt escalate --severity=medium \
  --subject="Plugin FAILED: rebuild-gt" \
  --body="$ERROR" \
  --source="plugin:rebuild-gt"
```

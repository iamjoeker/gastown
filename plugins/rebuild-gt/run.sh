#!/usr/bin/env bash
# rebuild-gt/run.sh — Rebuild gt binary from gastown source if stale.
#
# SAFETY: Only rebuilds forward (binary is ancestor of HEAD) and only
# from main branch. A bad rebuild caused a crash loop (every session's
# startup hook failed, witness respawned, loop repeated every 1-2 min).

set -euo pipefail

log() { echo "[rebuild-gt] $*"; }

# Resolve the town root, failing loudly. `gt town root` used to be a
# nonexistent subcommand: it printed `gt town`'s help to STDOUT and exited 0,
# so `$(gt town root 2>/dev/null)` assigned help text to TOWN_ROOT and every
# derived path below was nonsense — silently (gt-cr2). The -d check keeps that
# failure loud even against a gt binary predating the fix.
TOWN_ROOT="${GT_TOWN_ROOT:-}"
if [ -z "$TOWN_ROOT" ]; then
  if ! TOWN_ROOT=$(gt town root); then
    log "FATAL: could not resolve town root; set GT_TOWN_ROOT or run inside a Gas Town workspace." >&2
    exit 1
  fi
fi
if [ ! -d "$TOWN_ROOT" ]; then
  log "FATAL: resolved town root is not a directory: '$TOWN_ROOT'" >&2
  exit 1
fi

RIG_ROOT="${TOWN_ROOT}/gastown/mayor/rig"

# --- Detection ---------------------------------------------------------------

log "Checking binary staleness..."
STALE_JSON=$(gt stale --json 2>/dev/null) || {
  log "gt stale --json failed, skipping"
  exit 0
}

IS_STALE=$(echo "$STALE_JSON" | python3 -c "import json,sys; print(json.load(sys.stdin).get('stale', False))" 2>/dev/null || echo "False")
SAFE=$(echo "$STALE_JSON" | python3 -c "import json,sys; print(json.load(sys.stdin).get('safe_to_rebuild', False))" 2>/dev/null || echo "False")

if [ "$IS_STALE" != "True" ]; then
  log "Binary is fresh. Nothing to do."
  gt plugin record-run --plugin rebuild-gt --result success --rig gastown \
    --title "rebuild-gt: binary is fresh" >/dev/null 2>&1 || true
  exit 0
fi

if [ "$SAFE" != "True" ]; then
  log "Not safe to rebuild (not on main or would be a downgrade). Skipping."
  gt plugin record-run --plugin rebuild-gt --result skipped --rig gastown \
    --title "Plugin: rebuild-gt [skipped]" \
    --description "Skipped: not safe to rebuild" >/dev/null 2>&1 || true
  exit 0
fi

# --- Pre-flight checks -------------------------------------------------------

log "Pre-flight checks..."

if [ ! -d "$RIG_ROOT" ]; then
  log "Rig root $RIG_ROOT does not exist. Skipping."
  exit 0
fi

DIRTY=$(git -C "$RIG_ROOT" status --porcelain 2>/dev/null)
if [ -n "$DIRTY" ]; then
  log "Repo is dirty, skipping rebuild."
  gt plugin record-run --plugin rebuild-gt --result skipped --rig gastown \
    --title "Plugin: rebuild-gt [skipped]" \
    --description "Skipped: repo has uncommitted changes" >/dev/null 2>&1 || true
  exit 0
fi

BRANCH=$(git -C "$RIG_ROOT" branch --show-current 2>/dev/null)
if [ "$BRANCH" != "main" ]; then
  log "Not on main branch (on $BRANCH), skipping rebuild."
  gt plugin record-run --plugin rebuild-gt --result skipped --rig gastown \
    --title "Plugin: rebuild-gt [skipped]" \
    --description "Skipped: not on main branch (on $BRANCH)" >/dev/null 2>&1 || true
  exit 0
fi

# --- Build -------------------------------------------------------------------

OLD_VER=$(gt version 2>/dev/null | head -1 || echo "unknown")
log "Rebuilding gt from $RIG_ROOT..."

if (cd "$RIG_ROOT" && make build && make safe-install) 2>&1; then
  NEW_VER=$(gt version 2>/dev/null | head -1 || echo "unknown")
  log "Rebuilt: $OLD_VER -> $NEW_VER"
  gt plugin record-run --plugin rebuild-gt --result success --rig gastown \
    --title "rebuild-gt: $OLD_VER -> $NEW_VER" >/dev/null 2>&1 || true
else
  ERROR="make build/safe-install failed"
  log "FAILED: $ERROR"
  gt plugin record-run --plugin rebuild-gt --result failure --rig gastown \
    --title "Plugin: rebuild-gt [failure]" \
    --description "Build failed: $ERROR" >/dev/null 2>&1 || true
  gt escalate "Plugin FAILED: rebuild-gt" -s medium 2>/dev/null || true
  exit 1
fi

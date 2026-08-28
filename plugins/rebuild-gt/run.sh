#!/usr/bin/env bash
# rebuild-gt/run.sh — Rebuild gt binary from gastown source if stale.
#
# SAFETY: Only rebuilds forward (binary is ancestor of the build ref) and only
# from a main checkout. A bad rebuild caused a crash loop (every session's
# startup hook failed, witness respawned, loop repeated every 1-2 min).
#
# SELF-HEALING: this plugin is the only thing that deploys a merged gt commit,
# so every silent no-op it can take is hours of a town running an old binary.
# Two of them were measured on 2026-08-25 (gt-ympl / hq-cak50) and both are
# guarded below:
#
#   1. It keyed on `stale` BEFORE `skipped`, so a check that could not MEASURE
#      presented as a binary that was FRESH — and the safe_to_rebuild guard
#      underneath it was unreachable in exactly that case.
#   2. It rebuilt the rig checkout without fast-forwarding it. Once detection
#      is fixed, a stale:true over a behind checkout compiles the same old
#      source: the binary never advances and the plugin reports success on
#      every cycle, forever.

set -euo pipefail

log() { echo "[rebuild-gt] $*"; }

record() {
  local result="$1" title="$2" description="$3"
  gt plugin record-run --plugin rebuild-gt --result "$result" --rig gastown \
    --title "$title" --description "$description" >/dev/null 2>&1 || true
}

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
  record skipped "Plugin: rebuild-gt [skipped]" "Skipped: gt stale --json failed"
  exit 0
}

# One parse, emitting shell assignments. Booleans become 1/0, and a field that
# is ABSENT becomes empty rather than 0 — the distinction matters because the
# gt binary running this check is by definition the OLD one, and an older gt
# does not emit compare_ref_refreshed at all. Reading absent as false would
# make the fix unable to deploy itself.
eval "$(printf '%s' "$STALE_JSON" | python3 -c '
import json, shlex, sys

d = json.load(sys.stdin)

def emit_bool(var, key):
    v = d.get(key)
    print("%s=%s" % (var, "1" if v is True else ("0" if v is False else "")))

def emit_str(var, key):
    print("%s=%s" % (var, shlex.quote(str(d.get(key) or ""))))

emit_bool("ST_SKIPPED", "skipped")
emit_bool("ST_STALE", "stale")
emit_bool("ST_SAFE", "safe_to_rebuild")
emit_bool("ST_REFRESHED", "compare_ref_refreshed")
emit_str("ST_SKIP_REASON", "skip_reason")
emit_str("ST_REFRESH_ERROR", "refresh_error")
emit_str("ST_COMPARE_REF", "compare_ref")
emit_str("ST_BINARY_COMMIT", "binary_commit")
emit_str("ST_REPO_COMMIT", "repo_commit")
' 2>/dev/null || echo 'ST_PARSE_FAILED=1')"

if [ -n "${ST_PARSE_FAILED:-}" ]; then
  log "Could not parse gt stale --json output, skipping"
  record skipped "Plugin: rebuild-gt [skipped]" "Skipped: gt stale --json output was unparseable"
  exit 0
fi

# 1. skipped BEFORE stale. `skipped` means the check could not measure, and
#    `stale` is false in that case for want of an answer, not for want of
#    staleness. Reading them the other way round is what made a two-hour-old
#    binary report itself fresh.
if [ "${ST_SKIPPED:-}" = "1" ]; then
  log "Staleness could not be determined: ${ST_SKIP_REASON:-no reason given}"
  record skipped "Plugin: rebuild-gt [skipped]" \
    "Skipped: staleness undetermined — ${ST_SKIP_REASON:-no reason given}"
  exit 0
fi

if [ "${ST_STALE:-}" != "1" ]; then
  # An explicit compare_ref_refreshed=false means the verdict came from a local
  # ref that nothing updates: "fresh" against it is not evidence of freshness.
  # Empty means the field is absent (older gt), where the legacy reading is all
  # there is.
  if [ "${ST_REFRESHED:-}" = "0" ]; then
    log "Binary reads fresh, but ${ST_COMPARE_REF:-the compare ref} was not read from the remote. Not trusting it."
    record skipped "Plugin: rebuild-gt [skipped]" \
      "Skipped: freshness unproven — ${ST_COMPARE_REF:-compare ref} was not read from the remote (${ST_REFRESH_ERROR:-no remote read attempted})"
    exit 0
  fi
  log "Binary is fresh (vs ${ST_COMPARE_REF:-unknown ref}). Nothing to do."
  record success "rebuild-gt: binary is fresh" "Binary ${ST_BINARY_COMMIT:0:12} is level with ${ST_COMPARE_REF:-the build ref}"
  exit 0
fi

if [ "${ST_SAFE:-}" != "1" ]; then
  log "Not safe to rebuild (not on main or would be a downgrade). Skipping."
  record skipped "Plugin: rebuild-gt [skipped]" "Skipped: not safe to rebuild"
  exit 0
fi

# --- Pre-flight checks -------------------------------------------------------

log "Pre-flight checks..."

if [ ! -d "$RIG_ROOT" ]; then
  log "Rig root $RIG_ROOT does not exist. Skipping."
  record skipped "Plugin: rebuild-gt [skipped]" "Skipped: $RIG_ROOT does not exist"
  exit 0
fi

DIRTY=$(git -C "$RIG_ROOT" status --porcelain 2>/dev/null)
if [ -n "$DIRTY" ]; then
  log "Repo is dirty, skipping rebuild."
  record skipped "Plugin: rebuild-gt [skipped]" "Skipped: repo has uncommitted changes"
  exit 0
fi

BRANCH=$(git -C "$RIG_ROOT" branch --show-current 2>/dev/null)
if [ "$BRANCH" != "main" ]; then
  log "Not on main branch (on $BRANCH), skipping rebuild."
  record skipped "Plugin: rebuild-gt [skipped]" "Skipped: not on main branch (on $BRANCH)"
  exit 0
fi

# --- Fast-forward the source ------------------------------------------------
#
# Without this the whole plugin is a treadmill: gt stale measures the binary
# against the REMOTE build branch, but `make build` compiles whatever this
# checkout holds. Behind by two commits, it rebuilds the binary to the commit
# it already had, still measures stale next cycle, and reports success every
# time. (`make safe-install` would in fact stop it at check-up-to-date — which
# turns the silent treadmill into an hourly escalation, no more useful.)
#
# --ff-only is the safety property: this may only advance the checkout along
# its upstream, never merge, never rewrite, never move it sideways.
UPSTREAM=$(git -C "$RIG_ROOT" rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null || true)
if [ -z "$UPSTREAM" ]; then
  log "main has no upstream in $RIG_ROOT; cannot fast-forward it. Skipping."
  record skipped "Plugin: rebuild-gt [skipped]" "Skipped: $RIG_ROOT main tracks no upstream, so the source cannot be brought up to date"
  exit 0
fi
FF_REMOTE=${UPSTREAM%%/*}
FF_BRANCH=${UPSTREAM#*/}

# A private destination ref, never FETCH_HEAD. FETCH_HEAD is one file per
# repository and .repo.git is shared by every worktree in the rig, so any
# concurrent fetch in the town can overwrite it between our write and our read
# (gt-880s). --no-write-fetch-head keeps this fetch from doing the same to
# everyone else.
FF_REF="refs/gt/rebuild-gt/$$"
cleanup_ff_ref() { git -C "$RIG_ROOT" update-ref -d "$FF_REF" 2>/dev/null || true; }
trap cleanup_ff_ref EXIT

log "Fetching $FF_BRANCH from $FF_REMOTE..."
if ! git -C "$RIG_ROOT" fetch --no-tags --no-write-fetch-head --force \
     "$FF_REMOTE" "refs/heads/${FF_BRANCH}:${FF_REF}" 2>&1; then
  log "Fetch from $FF_REMOTE failed. Skipping."
  record skipped "Plugin: rebuild-gt [skipped]" "Skipped: could not fetch $UPSTREAM"
  exit 0
fi

TARGET=$(git -C "$RIG_ROOT" rev-parse "$FF_REF")
CURRENT=$(git -C "$RIG_ROOT" rev-parse HEAD)
if [ "$TARGET" != "$CURRENT" ]; then
  log "Fast-forwarding $RIG_ROOT: ${CURRENT:0:12} -> ${TARGET:0:12}"
  if ! git -C "$RIG_ROOT" merge --ff-only "$TARGET" 2>&1; then
    log "Cannot fast-forward $RIG_ROOT to $UPSTREAM (diverged?). Skipping."
    record skipped "Plugin: rebuild-gt [skipped]" \
      "Skipped: $RIG_ROOT main cannot fast-forward to $UPSTREAM (${CURRENT:0:12} vs ${TARGET:0:12})"
    exit 0
  fi
fi

# --- Build -------------------------------------------------------------------

log "Rebuilding gt from $RIG_ROOT (target ${TARGET:0:12})..."

if ! (cd "$RIG_ROOT" && make build && make safe-install) 2>&1; then
  ERROR="make build/safe-install failed at ${TARGET:0:12}"
  log "FAILED: $ERROR"
  record failure "Plugin: rebuild-gt [failure]" "Build failed: $ERROR"
  gt escalate "Plugin FAILED: rebuild-gt" -s medium 2>/dev/null || true
  exit 1
fi

# --- Verify the rebuild actually landed --------------------------------------
#
# Exercising the NEW binary, not reading gt version or the file's mtime: both
# of those were checked on 2026-08-25 and both agreed with a rebuild that had
# not happened. `gt stale --quiet` exits 0=stale, 1=fresh, 2=undetermined, and
# the binary answering is the one just installed.
#
# Without this the failure mode is silent recurrence: something upstream of the
# build makes it a no-op, the plugin records success, and the town keeps
# running the old binary while the wisp log says it was rebuilt hourly.
set +e
gt stale --quiet
VERIFY=$?
set -e

case "$VERIFY" in
  1)
    log "Rebuilt and verified fresh at ${TARGET:0:12}"
    record success "rebuild-gt: ${ST_BINARY_COMMIT:0:12} -> ${TARGET:0:12}" \
      "Rebuilt from $UPSTREAM and verified fresh by re-running gt stale against the new binary"
    ;;
  0)
    ERROR="binary still reports stale after rebuilding at ${TARGET:0:12}"
    log "FAILED: $ERROR"
    record failure "Plugin: rebuild-gt [failure]" "Rebuild did not take: $ERROR"
    gt escalate "Plugin FAILED: rebuild-gt did not advance the binary" -s medium 2>/dev/null || true
    exit 1
    ;;
  *)
    log "Rebuilt at ${TARGET:0:12}; freshness could not be re-verified (gt stale exit $VERIFY)"
    record success "rebuild-gt: ${ST_BINARY_COMMIT:0:12} -> ${TARGET:0:12}" \
      "Rebuilt from $UPSTREAM; post-build gt stale could not measure (exit $VERIFY)"
    ;;
esac

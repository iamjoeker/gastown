#!/usr/bin/env bash
# gitignore-reconcile/run.sh — Auto-untrack files matched by .gitignore
#
# Scans all rig repos for tracked files that now match an active .gitignore
# rule. On clean main branches: git rm --cached + commit. On dirty branches
# or active polecat worktrees: creates a chore bead instead.

set -euo pipefail

log() { echo "[gitignore-reconcile] $*"; }

fail() {
  log "ERROR: $*"
  gt plugin record-run --plugin gitignore-reconcile --result failure \
    --title "gitignore-reconcile: FAILED" --description "$*" >/dev/null 2>&1 || true
  exit 1
}

# --- Step 1: Enumerate rig repos ---------------------------------------------
#
# `gt rig list --json` publishes a "repos" array per rig: the git clones inside
# the rig directory (mayor/rig, refinery/rig). A rig directory is not itself a
# git repo, so there is no singular path to read.
#
# An absent key is a broken contract, not an empty work list. This plugin read
# a nonexistent "repo_path" key for months and skipped every rig with exit 0
# (gt-a7a), so the two cases are now kept distinguishable.

if ! RIG_JSON=$(gt rig list --json 2>/dev/null); then
  fail "gt rig list --json failed; cannot enumerate rig repos"
fi

if ! echo "$RIG_JSON" | jq -e 'type == "array"' >/dev/null 2>&1; then
  fail "gt rig list --json did not return a JSON array; cannot enumerate rig repos"
fi

RIG_TOTAL=$(echo "$RIG_JSON" | jq 'length')
RIGS_WITH_KEY=$(echo "$RIG_JSON" | jq '[.[] | select(has("repos"))] | length')
if [ "$RIG_TOTAL" -gt 0 ] && [ "$RIGS_WITH_KEY" -eq 0 ]; then
  fail "gt rig list --json returned $RIG_TOTAL rig(s), none carrying a 'repos' key — gt is too old (rebuild it: gt plugin run rebuild-gt) or its schema changed. Refusing to report this as 'nothing to reconcile' (gt-a7a)."
fi

RIG_PATHS=$(echo "$RIG_JSON" | jq -r '.[] | (.repos // [])[]')

if [ -z "$RIG_PATHS" ]; then
  SUMMARY="no git clones found across $RIG_TOTAL rig(s)"
  log "Nothing to reconcile: $SUMMARY"
  gt plugin record-run --plugin gitignore-reconcile --result success \
    --title "gitignore-reconcile: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
  exit 0
fi

RIG_COUNT=$(echo "$RIG_PATHS" | wc -l | tr -d ' ')
log "Checking $RIG_COUNT rig repo(s) across $RIG_TOTAL rig(s) for tracked+ignored files"

# --- Step 2: Process each rig -----------------------------------------------

TOTAL_UNTRACKED=0
TOTAL_BEADS=0

while IFS= read -r REPO_PATH; do
  [ -z "$REPO_PATH" ] && continue

  if ! git -C "$REPO_PATH" rev-parse --git-dir >/dev/null 2>&1; then
    log "SKIP: $REPO_PATH — not a git repo"
    continue
  fi

  log ""
  log "=== $REPO_PATH ==="

  IGNORED_TRACKED=$(git -C "$REPO_PATH" ls-files --ignored --exclude-standard --cached 2>/dev/null || true)
  if [ -z "$IGNORED_TRACKED" ]; then
    log "  Clean"
    continue
  fi

  FILE_COUNT=$(echo "$IGNORED_TRACKED" | wc -l | tr -d ' ')
  log "  Found $FILE_COUNT tracked+ignored file(s)"

  CURRENT_BRANCH=$(git -C "$REPO_PATH" branch --show-current 2>/dev/null || true)
  IS_DIRTY=$(git -C "$REPO_PATH" status --porcelain 2>/dev/null | grep -v "^??" | head -1 || true)
  HAS_POLECATS=$(git -C "$REPO_PATH" branch 2>/dev/null | grep -E "^\+?\s+polecat/" | head -1 || true)

  if [ -n "$IS_DIRTY" ] || [ -n "$HAS_POLECATS" ] || [ "$CURRENT_BRANCH" != "main" ]; then
    REASON=""
    [ -n "$IS_DIRTY" ]       && REASON="dirty working tree"
    [ -n "$HAS_POLECATS" ]   && REASON="${REASON:+$REASON, }active polecat worktrees"
    [ "$CURRENT_BRANCH" != "main" ] && REASON="${REASON:+$REASON, }not on main ($CURRENT_BRANCH)"
    log "  SKIP: $REASON — creating chore bead"
    # Every rig clone is named "rig" (<rig>/mayor/rig), so basename alone would
    # title every bead identically. Keep the last three path components.
    REPO_NAME=$(echo "$REPO_PATH" | awk -F/ 'NF>=3 {print $(NF-2)"/"$(NF-1)"/"$NF; next} {print}')
    bd create "gitignore-reconcile: $REPO_NAME has $FILE_COUNT tracked+ignored file(s)" \
      -t chore \
      -l "plugin:gitignore-reconcile,category:git-hygiene" \
      -d "$(printf "Repo: %s\nSkipped: %s\nFiles:\n%s" "$REPO_PATH" "$REASON" "$(echo "$IGNORED_TRACKED" | head -20)")" \
      --silent 2>/dev/null || true
    TOTAL_BEADS=$((TOTAL_BEADS + 1))
    continue
  fi

  # Untrack files
  UNTRACKED_THIS=0
  while IFS= read -r FILE; do
    [ -z "$FILE" ] && continue
    log "  Untracking: $FILE"
    git -C "$REPO_PATH" rm --cached "$FILE" 2>/dev/null && UNTRACKED_THIS=$((UNTRACKED_THIS + 1)) || true
  done <<< "$IGNORED_TRACKED"

  STAGED=$(git -C "$REPO_PATH" diff --cached --name-only 2>/dev/null || true)
  if [ -n "$STAGED" ]; then
    COUNT=$(echo "$STAGED" | wc -l | tr -d ' ')
    COMMIT_MSG="chore: untrack $COUNT file(s) now matched by .gitignore

Auto-committed by gitignore-reconcile plugin."
    git -C "$REPO_PATH" commit -m "$COMMIT_MSG" \
      --author="Gas Town <gastown@local>" 2>/dev/null && \
      log "  Committed untracking of $COUNT file(s)" || \
      log "  WARN: commit failed"
    TOTAL_UNTRACKED=$((TOTAL_UNTRACKED + COUNT))
    git -C "$REPO_PATH" push origin main 2>/dev/null || log "  WARN: push failed (committed locally)"
  fi
done <<< "$RIG_PATHS"

# --- Report ------------------------------------------------------------------

log ""
log "=== Summary ==="
SUMMARY="gitignore-reconcile: $TOTAL_UNTRACKED file(s) untracked, $TOTAL_BEADS chore bead(s) created"
log "$SUMMARY"

gt plugin record-run --plugin gitignore-reconcile --result success \
  --title "$SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true

log "Done."

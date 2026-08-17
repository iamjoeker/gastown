#!/usr/bin/env bash
# submodule-commit/run.sh — Auto-commit accumulated changes in git submodules.
#
# Reads .gitmodules in opt-in rigs, commits any accumulated changes in each
# submodule on a known branch, pushes with || true (local commit is priority),
# then updates the parent repo's submodule pointer on main.
#
# Opt-in per rig: gt rig settings set <rig> plugins.submodule-commit.enabled true

set -euo pipefail

log() { echo "[submodule-commit] $*"; }

fail() {
  log "ERROR: $*"
  gt plugin record-run --plugin submodule-commit --result failure \
    --title "submodule-commit: FAILED" --description "$*" >/dev/null 2>&1 || true
  exit 1
}

# --- Step 1: Find opt-in rigs with submodules --------------------------------
#
# `gt rig list --json` publishes a "repos" array per rig: the git clones inside
# the rig directory (mayor/rig, refinery/rig). A rig directory is not itself a
# git repo, so there is no singular path to read.
#
# An absent key is a broken contract, not an empty work list. This plugin read
# a nonexistent "repo_path" key for months and skipped every rig with exit 0
# (gt-a7a), so the two cases are now kept distinguishable.
#
# Opt-in lives in rig settings, read back with `gt rig settings show <rig>`:
#   gt rig settings set <rig> plugins.submodule-commit.enabled true

if ! RIG_JSON=$(gt rig list --json 2>/dev/null); then
  fail "gt rig list --json failed; cannot enumerate rig repos"
fi

if ! echo "$RIG_JSON" | jq -e 'type == "array"' >/dev/null 2>&1; then
  fail "gt rig list --json did not return a JSON array; cannot enumerate rig repos"
fi

RIG_TOTAL=$(echo "$RIG_JSON" | jq 'length')
RIGS_WITH_KEY=$(echo "$RIG_JSON" | jq '[.[] | select(has("repos"))] | length')
if [ "$RIG_TOTAL" -gt 0 ] && [ "$RIGS_WITH_KEY" -eq 0 ]; then
  fail "gt rig list --json returned $RIG_TOTAL rig(s), none carrying a 'repos' key — gt is too old (rebuild it: gt plugin run rebuild-gt) or its schema changed. Refusing to report this as 'no opt-in rigs' (gt-a7a)."
fi

# "<rig-name><TAB><repo-path>" per clone. The rig name must come from the JSON:
# every clone is named "rig", so basename would resolve every rig to "rig" and
# every settings lookup would fail.
RIG_REPOS=$(echo "$RIG_JSON" | jq -r '.[] | . as $rig | (.repos // [])[] | "\($rig.name)\t\(.)"')

if [ -z "$RIG_REPOS" ]; then
  SUMMARY="no git clones found across $RIG_TOTAL rig(s)"
  log "Nothing to do: $SUMMARY"
  gt plugin record-run --plugin submodule-commit --result success \
    --title "submodule-commit: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
  exit 0
fi

declare -a ENABLED_RIGS=()
declare -a ENABLED_RIG_NAMES=()
WITH_SUBMODULES=0
while IFS=$'\t' read -r RIG_NAME REPO_PATH; do
  [ -z "$REPO_PATH" ] && continue
  [ ! -f "$REPO_PATH/.gitmodules" ] && continue
  WITH_SUBMODULES=$((WITH_SUBMODULES + 1))
  PLUGIN_ENABLED=$(gt rig settings show "$RIG_NAME" 2>/dev/null \
    | jq -r '.plugins["submodule-commit"].enabled // false' 2>/dev/null || echo "false")
  if [ "$PLUGIN_ENABLED" = "true" ]; then
    ENABLED_RIGS+=("$REPO_PATH")
    ENABLED_RIG_NAMES+=("$RIG_NAME")
    log "Opt-in rig: $RIG_NAME ($REPO_PATH)"
  else
    log "Not opted in: $RIG_NAME ($REPO_PATH) — set plugins.submodule-commit.enabled to enable"
  fi
done <<< "$RIG_REPOS"

if [ ${#ENABLED_RIGS[@]} -eq 0 ]; then
  SUMMARY="$WITH_SUBMODULES repo(s) with submodules across $RIG_TOTAL rig(s), none opted in"
  log "Nothing to do: $SUMMARY"
  gt plugin record-run --plugin submodule-commit --result success \
    --title "submodule-commit: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
  exit 0
fi

log "Processing ${#ENABLED_RIGS[@]} opt-in repo(s)"

# --- Step 2: Process each rig ------------------------------------------------

TOTAL_COMMITTED=0
TOTAL_PUSHED=0
TOTAL_PARENT_UPDATED=0

for i in "${!ENABLED_RIGS[@]}"; do
  REPO_PATH="${ENABLED_RIGS[$i]}"
  RIG_NAME="${ENABLED_RIG_NAMES[$i]}"

  log ""
  log "=== $RIG_NAME: $REPO_PATH ==="

  # Get plugin config
  RIG_CONFIG=$(gt rig settings show "$RIG_NAME" 2>/dev/null \
    | jq -r '.plugins["submodule-commit"] // {}' 2>/dev/null || echo "{}")
  PUSH_ENABLED=$(echo "$RIG_CONFIG" | jq -r '.push_enabled // false')
  ALLOWLIST=$(echo "$RIG_CONFIG" | jq -r '.allowlist // [] | .[]' 2>/dev/null || true)

  SUBMODULE_PATHS=$(git -C "$REPO_PATH" config --file .gitmodules --get-regexp 'submodule\..*\.path' 2>/dev/null \
    | awk '{print $2}' || true)

  if [ -z "$SUBMODULE_PATHS" ]; then
    log "  No submodules found in .gitmodules"
    continue
  fi

  PARENT_CHANGED=false

  while IFS= read -r SUB_PATH; do
    [ -z "$SUB_PATH" ] && continue

    # Apply allowlist filter
    if [ -n "$ALLOWLIST" ]; then
      MATCH=false
      while IFS= read -r ALLOWED; do
        [ "$SUB_PATH" = "$ALLOWED" ] && { MATCH=true; break; }
      done <<< "$ALLOWLIST"
      $MATCH || { log "  SKIP: $SUB_PATH — not in allowlist"; continue; }
    fi

    FULL_SUB="$REPO_PATH/$SUB_PATH"
    if [ ! -d "$FULL_SUB" ]; then
      log "  SKIP: $SUB_PATH — directory not found"
      continue
    fi
    if [ ! -d "$FULL_SUB/.git" ] && [ ! -f "$FULL_SUB/.git" ]; then
      log "  SKIP: $SUB_PATH — not initialized (run git submodule update --init)"
      continue
    fi

    SUB_DIRTY=$(git -C "$FULL_SUB" status --porcelain 2>/dev/null | head -1 || true)
    if [ -z "$SUB_DIRTY" ]; then
      log "  $SUB_PATH: clean"
      continue
    fi

    SUB_BRANCH=$(git -C "$FULL_SUB" branch --show-current 2>/dev/null || true)
    if [ -z "$SUB_BRANCH" ]; then
      log "  SKIP: $SUB_PATH — detached HEAD"
      continue
    fi

    log "  $SUB_PATH: has changes (branch=$SUB_BRANCH)"

    git -C "$FULL_SUB" add -A 2>/dev/null || true
    STAGED_COUNT=$(git -C "$FULL_SUB" diff --cached --name-only 2>/dev/null | wc -l | tr -d ' ')

    if [ "$STAGED_COUNT" -gt 0 ]; then
      git -C "$FULL_SUB" commit \
        -m "chore: accumulated changes [skip ci]

Auto-committed by submodule-commit plugin ($STAGED_COUNT file(s))." \
        --author="Gas Town <gastown@local>" 2>/dev/null && {
          log "    Committed $STAGED_COUNT file(s)"
          TOTAL_COMMITTED=$((TOTAL_COMMITTED + 1))
          PARENT_CHANGED=true
        } || { log "    WARN: commit failed"; continue; }

      if [ "$PUSH_ENABLED" = "true" ]; then
        git -C "$FULL_SUB" push origin "$SUB_BRANCH" 2>/dev/null && \
          { log "    Pushed to origin/$SUB_BRANCH"; TOTAL_PUSHED=$((TOTAL_PUSHED + 1)); } || \
          log "    WARN: push failed (local commit preserved)"
      fi
    fi
  done <<< "$SUBMODULE_PATHS"

  # Update parent pointer on main if submodules changed
  if $PARENT_CHANGED; then
    PARENT_BRANCH=$(git -C "$REPO_PATH" branch --show-current 2>/dev/null || true)
    if [ "$PARENT_BRANCH" != "main" ]; then
      log "  SKIP parent pointer update: on $PARENT_BRANCH (not main)"
      continue
    fi

    PARENT_UNSTAGED=$(git -C "$REPO_PATH" status --porcelain 2>/dev/null | grep -v "^??" | head -1 || true)
    if [ -n "$PARENT_UNSTAGED" ]; then
      log "  SKIP parent pointer update: parent has other uncommitted changes"
      continue
    fi

    # Stage only submodule pointer changes
    git -C "$REPO_PATH" add -u 2>/dev/null || true
    PARENT_STAGED=$(git -C "$REPO_PATH" diff --cached --name-only 2>/dev/null || true)
    if [ -n "$PARENT_STAGED" ]; then
      git -C "$REPO_PATH" commit \
        -m "chore: update submodule pointers [skip ci]

Auto-committed by submodule-commit plugin." \
        --author="Gas Town <gastown@local>" 2>/dev/null && {
          log "  Parent pointer updated"
          TOTAL_PARENT_UPDATED=$((TOTAL_PARENT_UPDATED + 1))
        } || log "  WARN: parent pointer commit failed"
      git -C "$REPO_PATH" push origin main 2>/dev/null || log "  WARN: parent push failed (local commit preserved)"
    fi
  fi
done

# --- Report ------------------------------------------------------------------

log ""
log "=== Summary ==="
SUMMARY="submodule-commit: $TOTAL_COMMITTED submodule(s) committed, $TOTAL_PUSHED pushed, $TOTAL_PARENT_UPDATED parent pointer(s) updated"
log "$SUMMARY"

gt plugin record-run --plugin submodule-commit --result success \
  --title "$SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true

log "Done."

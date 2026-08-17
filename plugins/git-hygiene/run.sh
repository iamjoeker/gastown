#!/usr/bin/env bash
# git-hygiene/run.sh — Clean stale branches, stashes, and loose objects.
#
# Runs across all rig repos. Covers:
# - Merged local branches
# - Orphan local branches (polecat/*, dog/*, fix/*, etc. with no remote)
# - Merged remote branches on GitHub
# - Stale stashes
# - Git garbage collection

set -euo pipefail

log() { echo "[git-hygiene] $*"; }

fail() {
  log "ERROR: $*"
  gt plugin record-run --plugin git-hygiene --result failure \
    --title "git-hygiene: FAILED" --description "$*" >/dev/null 2>&1 || true
  exit 1
}

# --- Enumerate rig repos -----------------------------------------------------
#
# `gt rig list --json` publishes a "repos" array per rig: the git clones inside
# the rig directory (mayor/rig, refinery/rig). A rig directory is not itself a
# git repo, so there is no singular path to read.
#
# An absent key is a broken contract, not an empty work list. This plugin read
# a nonexistent "repo_path" key for months and skipped every rig with exit 0
# (gt-a7a), so the two cases are now kept distinguishable: missing key fails
# loudly, genuinely-empty succeeds quietly.

if ! RIG_JSON=$(gt rig list --json 2>/dev/null); then
  fail "gt rig list --json failed; cannot enumerate rig repos"
fi

if ! echo "$RIG_JSON" | jq -e 'type == "array"' >/dev/null 2>&1; then
  fail "gt rig list --json did not return a JSON array; cannot enumerate rig repos"
fi

RIG_TOTAL=$(echo "$RIG_JSON" | jq 'length')
RIGS_WITH_KEY=$(echo "$RIG_JSON" | jq '[.[] | select(has("repos"))] | length')
if [ "$RIG_TOTAL" -gt 0 ] && [ "$RIGS_WITH_KEY" -eq 0 ]; then
  fail "gt rig list --json returned $RIG_TOTAL rig(s), none carrying a 'repos' key — gt is too old (rebuild it: gt plugin run rebuild-gt) or its schema changed. Refusing to report this as 'nothing to clean' (gt-a7a)."
fi

# One "<rig-name><TAB><repo-path>" line per clone.
RIG_REPOS=$(echo "$RIG_JSON" | jq -r '.[] | . as $rig | (.repos // [])[] | "\($rig.name)\t\(.)"')

if [ -z "$RIG_REPOS" ]; then
  SUMMARY="no git clones found across $RIG_TOTAL rig(s)"
  log "Nothing to clean: $SUMMARY"
  gt plugin record-run --plugin git-hygiene --result success \
    --title "git-hygiene: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
  exit 0
fi

RIG_COUNT=$(echo "$RIG_REPOS" | wc -l | tr -d ' ')
log "Found $RIG_COUNT rig repo(s) to clean across $RIG_TOTAL rig(s)"

# --- Process each rig repo ----------------------------------------------------

TOTAL_LOCAL_MERGED=0
TOTAL_LOCAL_ORPHAN=0
TOTAL_REMOTE=0
TOTAL_STASHES=0
TOTAL_GC=0

while IFS=$'\t' read -r RIG_NAME REPO_PATH; do
  [ -z "$REPO_PATH" ] && continue

  if ! git -C "$REPO_PATH" rev-parse --git-dir >/dev/null 2>&1; then
    log "SKIP: $REPO_PATH is not a git repo"
    continue
  fi

  log ""
  log "=== Cleaning: $REPO_PATH ==="

  # Detect default branch. A clone with no origin/HEAD is normal (fresh clone,
  # local-only repo), so these must not abort the run under `set -e -o pipefail`.
  DEFAULT_BRANCH=$(git -C "$REPO_PATH" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null \
    | sed 's|refs/remotes/origin/||' || true)
  if [ -z "$DEFAULT_BRANCH" ]; then
    DEFAULT_BRANCH="main"
  fi
  CURRENT_BRANCH=$(git -C "$REPO_PATH" branch --show-current 2>/dev/null || true)

  # Step 1: Prune remote tracking refs
  log "  Pruning remote tracking refs..."
  git -C "$REPO_PATH" fetch --prune --all 2>/dev/null || true

  # Step 2: Delete merged local branches
  log "  Deleting merged local branches..."
  MERGED_BRANCHES=$(git -C "$REPO_PATH" branch --merged "$DEFAULT_BRANCH" 2>/dev/null \
    | grep -v "^\*" \
    | grep -v "^+" \
    | grep -v -E "^\s*(main|master)$" \
    | sed 's/^[[:space:]]*//' || true)

  LOCAL_MERGED=0
  while IFS= read -r BRANCH; do
    [ -z "$BRANCH" ] && continue
    if [ "$BRANCH" = "$CURRENT_BRANCH" ] || [ "$BRANCH" = "$DEFAULT_BRANCH" ]; then
      continue
    fi
    case "$BRANCH" in
      refinery-patrol|merge/*) continue ;;
    esac
    log "    Deleting merged: $BRANCH"
    # A refused delete is routine (checked out elsewhere, protected); it must
    # not abort the run for every remaining branch and repo.
    git -C "$REPO_PATH" branch -d "$BRANCH" 2>/dev/null && LOCAL_MERGED=$((LOCAL_MERGED + 1)) || true
  done <<< "$MERGED_BRANCHES"
  TOTAL_LOCAL_MERGED=$((TOTAL_LOCAL_MERGED + LOCAL_MERGED))

  # Step 3: Delete stale unmerged orphan branches
  log "  Deleting stale orphan branches..."
  STALE_PATTERNS="polecat/|dog/|fix/|pr-|integration/|worktree-agent-"
  ALL_BRANCHES=$(git -C "$REPO_PATH" branch 2>/dev/null \
    | grep -v "^\*" \
    | grep -v "^+" \
    | sed 's/^[[:space:]]*//' || true)

  LOCAL_ORPHAN=0
  while IFS= read -r BRANCH; do
    [ -z "$BRANCH" ] && continue
    if ! echo "$BRANCH" | grep -qE "^($STALE_PATTERNS)"; then
      continue
    fi
    if [ "$BRANCH" = "$CURRENT_BRANCH" ] || [ "$BRANCH" = "$DEFAULT_BRANCH" ]; then
      continue
    fi
    case "$BRANCH" in
      main|master|refinery-patrol|merge/*) continue ;;
    esac
    if git -C "$REPO_PATH" rev-parse --verify "refs/remotes/origin/$BRANCH" >/dev/null 2>&1; then
      continue
    fi
    log "    Deleting orphan: $BRANCH"
    git -C "$REPO_PATH" branch -D "$BRANCH" 2>/dev/null && LOCAL_ORPHAN=$((LOCAL_ORPHAN + 1)) || true
  done <<< "$ALL_BRANCHES"
  TOTAL_LOCAL_ORPHAN=$((TOTAL_LOCAL_ORPHAN + LOCAL_ORPHAN))

  # Step 4: Delete merged remote branches on GitHub
  log "  Deleting merged remote branches..."
  REMOTE_DELETED=0

  # A clone with no origin is normal; it must not abort the run.
  GH_REPO=$(git -C "$REPO_PATH" remote get-url origin 2>/dev/null \
    | sed -E 's|.*github\.com[:/]||; s|\.git$||' || true)

  if [ -n "$GH_REPO" ]; then
    REMOTE_BRANCHES=$(git -C "$REPO_PATH" branch -r 2>/dev/null \
      | grep -v HEAD \
      | grep -v "origin/$DEFAULT_BRANCH" \
      | grep -v "origin/dependabot/" \
      | grep -v "origin/refinery-patrol" \
      | grep -vE "origin/merge/" \
      | sed 's|^[[:space:]]*origin/||' || true)

    REMOTE_PATTERNS="polecat/|fix/|pr-|integration/|worktree-agent-"

    while IFS= read -r RBRANCH; do
      [ -z "$RBRANCH" ] && continue
      if ! echo "$RBRANCH" | grep -qE "^($REMOTE_PATTERNS)"; then
        continue
      fi
      if git -C "$REPO_PATH" merge-base --is-ancestor "origin/$RBRANCH" "origin/$DEFAULT_BRANCH" 2>/dev/null; then
        log "    Deleting remote: origin/$RBRANCH"
        gh api "repos/$GH_REPO/git/refs/heads/$RBRANCH" -X DELETE 2>/dev/null && REMOTE_DELETED=$((REMOTE_DELETED + 1)) || true
      fi
    done <<< "$REMOTE_BRANCHES"
  fi
  TOTAL_REMOTE=$((TOTAL_REMOTE + REMOTE_DELETED))

  # Step 5: Clear stale stashes
  #
  # `git stash clear` is unrecoverable, and mayor/rig is a human's clone, so
  # this is opt-in per rig rather than on by default:
  #   gt rig settings set <rig> plugins.git-hygiene.clear_stashes true
  # Stashes are always counted and reported, so the work stays visible.
  STASH_COUNT=$(git -C "$REPO_PATH" stash list 2>/dev/null | wc -l | tr -d ' ' || echo 0)
  if [ "$STASH_COUNT" -gt 0 ]; then
    CLEAR_STASHES=$(gt rig settings show "$RIG_NAME" 2>/dev/null \
      | jq -r '.plugins["git-hygiene"].clear_stashes // false' 2>/dev/null || echo "false")
    if [ "$CLEAR_STASHES" = "true" ]; then
      log "  Clearing $STASH_COUNT stash(es)"
      git -C "$REPO_PATH" stash clear 2>/dev/null || true
      TOTAL_STASHES=$((TOTAL_STASHES + STASH_COUNT))
    else
      log "  $STASH_COUNT stash(es) present, left alone (set plugins.git-hygiene.clear_stashes on rig $RIG_NAME to clear)"
      STASH_COUNT=0
    fi
  fi

  # Step 6: Garbage collect
  log "  Running git gc..."
  git -C "$REPO_PATH" gc --prune=now --quiet 2>/dev/null && TOTAL_GC=$((TOTAL_GC + 1)) || true

  log "  Done: $LOCAL_MERGED merged, $LOCAL_ORPHAN orphan, $REMOTE_DELETED remote, $STASH_COUNT stash(es)"
done <<< "$RIG_REPOS"

# --- Report -------------------------------------------------------------------

SUMMARY="$RIG_COUNT repo(s) across $RIG_TOTAL rig(s): $TOTAL_LOCAL_MERGED merged, $TOTAL_LOCAL_ORPHAN orphan, $TOTAL_REMOTE remote, $TOTAL_STASHES stash(es), $TOTAL_GC gc"
log ""
log "=== Git Hygiene Summary ==="
log "$SUMMARY"

gt plugin record-run --plugin git-hygiene --result success \
  --title "git-hygiene: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true

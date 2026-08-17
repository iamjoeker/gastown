+++
name = "git-hygiene"
description = "Clean up stale git branches, stashes, and loose objects across all rig repos"
version = 1

[gate]
type = "cooldown"
duration = "12h"

[tracking]
labels = ["plugin:git-hygiene", "category:cleanup"]
digest = true

[execution]
timeout = "10m"
notify_on_failure = true
severity = "low"
+++

# Git Hygiene

Automated cleanup of stale git branches, stashes, and loose objects across all
rig repos. Covers local branches (merged and orphaned), remote branches on
GitHub, stale stashes, and garbage collection.

Requires: `gh` CLI installed and authenticated (`gh auth status`).

## Step 1: Enumerate rig repos

A rig directory is not a git repo — it contains up to two clones, `mayor/rig`
and `refinery/rig`. `gt rig list --json` publishes them per rig as `repos`
(plus the labelled `mayor_repo` / `refinery_repo` / `path`). There is no
singular `repo_path` key, and there never was: reading one is what made this
plugin skip every rig with exit 0 for months (gt-a7a).

An absent key is a broken contract, not an empty work list, so the two cases
must stay distinguishable — missing key fails loudly, no clones succeeds
quietly:

```bash
fail() {
  echo "ERROR: $*"
  gt plugin record-run --plugin git-hygiene --result failure \
    --title "git-hygiene: FAILED" --description "$*" >/dev/null 2>&1 || true
  exit 1
}

if ! RIG_JSON=$(gt rig list --json 2>/dev/null); then
  fail "gt rig list --json failed; cannot enumerate rig repos"
fi
if ! echo "$RIG_JSON" | jq -e 'type == "array"' >/dev/null 2>&1; then
  fail "gt rig list --json did not return a JSON array"
fi

RIG_TOTAL=$(echo "$RIG_JSON" | jq 'length')
RIGS_WITH_KEY=$(echo "$RIG_JSON" | jq '[.[] | select(has("repos"))] | length')
if [ "$RIG_TOTAL" -gt 0 ] && [ "$RIGS_WITH_KEY" -eq 0 ]; then
  fail "gt rig list --json returned $RIG_TOTAL rig(s), none carrying a 'repos' key"
fi

# One "<rig-name><TAB><repo-path>" line per clone. The rig name has to come
# from the JSON: every clone is named "rig", so basename() would resolve every
# rig to "rig".
RIG_REPOS=$(echo "$RIG_JSON" | jq -r '.[] | . as $rig | (.repos // [])[] | "\($rig.name)\t\(.)"')

if [ -z "$RIG_REPOS" ]; then
  echo "Nothing to clean: no git clones found across $RIG_TOTAL rig(s)"
  gt plugin record-run --plugin git-hygiene --result success \
    --title "git-hygiene: no clones" --description "no clones" >/dev/null 2>&1 || true
  exit 0
fi

RIG_COUNT=$(echo "$RIG_REPOS" | wc -l | tr -d ' ')
echo "Found $RIG_COUNT rig repo(s) to clean across $RIG_TOTAL rig(s)"
```

## Step 2: Process each rig repo

For each rig repo, run the full cleanup sequence. Track totals across all rigs.

```bash
TOTAL_LOCAL_MERGED=0
TOTAL_LOCAL_ORPHAN=0
TOTAL_REMOTE=0
TOTAL_STASHES=0
TOTAL_GC=0
ERRORS=()

while IFS=$'\t' read -r RIG_NAME REPO_PATH; do
  [ -z "$REPO_PATH" ] && continue

  # Verify it's a git repo
  if ! git -C "$REPO_PATH" rev-parse --git-dir >/dev/null 2>&1; then
    echo "SKIP: $REPO_PATH is not a git repo"
    continue
  fi

  echo ""
  echo "=== Cleaning: $REPO_PATH ==="

  # Detect default branch (main or master)
  DEFAULT_BRANCH=$(git -C "$REPO_PATH" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null \
    | sed 's|refs/remotes/origin/||')
  if [ -z "$DEFAULT_BRANCH" ]; then
    DEFAULT_BRANCH="main"
  fi

  CURRENT_BRANCH=$(git -C "$REPO_PATH" branch --show-current 2>/dev/null)

  ### Step 2a: Prune remote tracking refs
  echo "  Pruning remote tracking refs..."
  git -C "$REPO_PATH" fetch --prune --all 2>/dev/null || true

  ### Step 2b: Delete merged local branches
  echo "  Deleting merged local branches..."
  MERGED_BRANCHES=$(git -C "$REPO_PATH" branch --merged "$DEFAULT_BRANCH" 2>/dev/null \
    | grep -v "^\*" \
    | grep -v "^+" \
    | grep -v -E "^\s*(main|master)$" \
    | sed 's/^[[:space:]]*//')

  LOCAL_MERGED=0
  while IFS= read -r BRANCH; do
    [ -z "$BRANCH" ] && continue
    # Never delete current branch or default branch
    if [ "$BRANCH" = "$CURRENT_BRANCH" ] || [ "$BRANCH" = "$DEFAULT_BRANCH" ]; then
      continue
    fi
    # Never delete infrastructure branches
    case "$BRANCH" in
      refinery-patrol|merge/*) continue ;;
    esac
    echo "    Deleting merged: $BRANCH"
    git -C "$REPO_PATH" branch -d "$BRANCH" 2>/dev/null && LOCAL_MERGED=$((LOCAL_MERGED + 1))
  done <<< "$MERGED_BRANCHES"
  TOTAL_LOCAL_MERGED=$((TOTAL_LOCAL_MERGED + LOCAL_MERGED))

  ### Step 2c: Delete stale unmerged orphan branches
  # Only delete branches matching known agent/temp patterns that:
  # - Have no active worktree (not + prefixed)
  # - Have no corresponding remote tracking branch
  echo "  Deleting stale orphan branches..."
  STALE_PATTERNS="polecat/|dog/|fix/|pr-|integration/|worktree-agent-"
  ALL_BRANCHES=$(git -C "$REPO_PATH" branch 2>/dev/null \
    | grep -v "^\*" \
    | grep -v "^+" \
    | sed 's/^[[:space:]]*//')

  LOCAL_ORPHAN=0
  while IFS= read -r BRANCH; do
    [ -z "$BRANCH" ] && continue
    # Must match one of the stale patterns
    if ! echo "$BRANCH" | grep -qE "^($STALE_PATTERNS)"; then
      continue
    fi
    # Never delete current, default, or infrastructure branches
    if [ "$BRANCH" = "$CURRENT_BRANCH" ] || [ "$BRANCH" = "$DEFAULT_BRANCH" ]; then
      continue
    fi
    case "$BRANCH" in
      main|master|refinery-patrol|merge/*) continue ;;
    esac
    # Check if remote tracking branch exists
    if git -C "$REPO_PATH" rev-parse --verify "refs/remotes/origin/$BRANCH" >/dev/null 2>&1; then
      continue  # Remote still exists, skip
    fi
    echo "    Deleting orphan: $BRANCH"
    git -C "$REPO_PATH" branch -D "$BRANCH" 2>/dev/null && LOCAL_ORPHAN=$((LOCAL_ORPHAN + 1))
  done <<< "$ALL_BRANCHES"
  TOTAL_LOCAL_ORPHAN=$((TOTAL_LOCAL_ORPHAN + LOCAL_ORPHAN))

  ### Step 2d: Delete merged remote branches on GitHub
  echo "  Deleting merged remote branches..."
  REMOTE_DELETED=0

  # Detect GitHub repo from remote
  GH_REPO=$(git -C "$REPO_PATH" remote get-url origin 2>/dev/null \
    | sed -E 's|.*github\.com[:/]||; s|\.git$||')

  if [ -n "$GH_REPO" ]; then
    REMOTE_BRANCHES=$(git -C "$REPO_PATH" branch -r 2>/dev/null \
      | grep -v HEAD \
      | grep -v "origin/$DEFAULT_BRANCH" \
      | grep -v "origin/dependabot/" \
      | grep -v "origin/refinery-patrol" \
      | grep -vE "origin/merge/" \
      | sed 's|^[[:space:]]*origin/||')

    REMOTE_PATTERNS="polecat/|fix/|pr-|integration/|worktree-agent-"

    while IFS= read -r RBRANCH; do
      [ -z "$RBRANCH" ] && continue
      # Must match cleanup patterns
      if ! echo "$RBRANCH" | grep -qE "^($REMOTE_PATTERNS)"; then
        continue
      fi
      # Check if merged into default branch
      if git -C "$REPO_PATH" merge-base --is-ancestor "origin/$RBRANCH" "origin/$DEFAULT_BRANCH" 2>/dev/null; then
        echo "    Deleting remote: origin/$RBRANCH"
        # Use gh api because git push --delete may be blocked by pre-push hooks
        gh api "repos/$GH_REPO/git/refs/heads/$RBRANCH" -X DELETE 2>/dev/null && REMOTE_DELETED=$((REMOTE_DELETED + 1))
      fi
    done <<< "$REMOTE_BRANCHES"
  else
    echo "    SKIP: could not detect GitHub repo from remote"
  fi
  TOTAL_REMOTE=$((TOTAL_REMOTE + REMOTE_DELETED))

  ### Step 2e: Clear stale stashes
  # `git stash clear` is unrecoverable and mayor/rig is a human's clone, so
  # this is opt-in per rig. Stashes are always counted and reported.
  #   gt rig settings set <rig> plugins.git-hygiene.clear_stashes true
  STASH_COUNT=$(git -C "$REPO_PATH" stash list 2>/dev/null | wc -l | tr -d ' ' || echo 0)
  if [ "$STASH_COUNT" -gt 0 ]; then
    CLEAR_STASHES=$(gt rig settings show "$RIG_NAME" 2>/dev/null \
      | jq -r '.plugins["git-hygiene"].clear_stashes // false' 2>/dev/null || echo "false")
    if [ "$CLEAR_STASHES" = "true" ]; then
      echo "    Clearing $STASH_COUNT stash(es)"
      git -C "$REPO_PATH" stash clear 2>/dev/null || true
      TOTAL_STASHES=$((TOTAL_STASHES + STASH_COUNT))
    else
      echo "    $STASH_COUNT stash(es) present, left alone (not opted in)"
      STASH_COUNT=0
    fi
  fi

  ### Step 2f: Garbage collect
  echo "  Running git gc..."
  git -C "$REPO_PATH" gc --prune=now --quiet 2>/dev/null && TOTAL_GC=$((TOTAL_GC + 1))

  echo "  Done: $LOCAL_MERGED merged, $LOCAL_ORPHAN orphan, $REMOTE_DELETED remote, $STASH_COUNT stash(es)"
done <<< "$RIG_REPOS"
```

## Record Result

```bash
SUMMARY="$RIG_COUNT rig(s): $TOTAL_LOCAL_MERGED merged branch(es), $TOTAL_LOCAL_ORPHAN orphan branch(es), $TOTAL_REMOTE remote branch(es), $TOTAL_STASHES stash(es) cleared, $TOTAL_GC gc run(s)"
echo ""
echo "=== Git Hygiene Summary ==="
echo "$SUMMARY"
```

On success:
```bash
gt plugin record-run --plugin git-hygiene --result success \
  --title "git-hygiene: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
```

On failure:
```bash
gt plugin record-run --plugin git-hygiene --result failure \
  --title "git-hygiene: FAILED" \
  --description "Git hygiene failed: $ERROR" >/dev/null 2>&1 || true

gt escalate "Plugin FAILED: git-hygiene" \
  --severity low \
  --reason "$ERROR"
```

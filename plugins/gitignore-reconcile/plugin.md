+++
name = "gitignore-reconcile"
description = "Auto-untrack files that are tracked but match an active .gitignore rule"
version = 1

[gate]
type = "cooldown"
duration = "6h"

[tracking]
labels = ["plugin:gitignore-reconcile", "category:git-hygiene"]
digest = true

[execution]
timeout = "10m"
notify_on_failure = true
severity = "low"
+++

# Gitignore Reconcile

Scans all rig repos for files that are tracked in git but now match an active
`.gitignore` rule. On clean `main` branches, runs `git rm --cached` to untrack
them and commits. On dirty branches or active polecat worktrees, creates a
chore bead instead to avoid interference.

Root cause: `.gitignore` rules only block NEW files. Files committed before the
rule was added continue to be tracked until manually untracked.

## Step 1: Enumerate rig repos

A rig directory is not a git repo — it contains up to two clones, `mayor/rig`
and `refinery/rig`. `gt rig list --json` publishes them per rig as `repos`.
There is no singular `repo_path` key, and there never was: reading one is what
made this plugin skip every rig with exit 0 for months (gt-a7a).

An absent key is a broken contract, not an empty work list, so the two cases
must stay distinguishable:

```bash
fail() {
  echo "ERROR: $*"
  gt plugin record-run --plugin gitignore-reconcile --result failure \
    --title "gitignore-reconcile: FAILED" --description "$*" >/dev/null 2>&1 || true
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

RIG_PATHS=$(echo "$RIG_JSON" | jq -r '.[] | (.repos // [])[]')

if [ -z "$RIG_PATHS" ]; then
  echo "Nothing to reconcile: no git clones found across $RIG_TOTAL rig(s)"
  gt plugin record-run --plugin gitignore-reconcile --result success \
    --title "gitignore-reconcile: no clones" --description "no clones" >/dev/null 2>&1 || true
  exit 0
fi

RIG_COUNT=$(echo "$RIG_PATHS" | wc -l | tr -d ' ')
echo "Checking $RIG_COUNT rig repo(s) across $RIG_TOTAL rig(s) for tracked+ignored files"
```

## Step 2: For each rig repo, find and untrack gitignored files

```bash
TOTAL_UNTRACKED=0
TOTAL_BEADS=0
ERRORS=""

while IFS= read -r REPO_PATH; do
  [ -z "$REPO_PATH" ] && continue

  if ! git -C "$REPO_PATH" rev-parse --git-dir >/dev/null 2>&1; then
    continue
  fi

  echo ""
  echo "=== $REPO_PATH ==="

  # Find tracked files that match gitignore rules
  IGNORED_TRACKED=$(git -C "$REPO_PATH" ls-files --ignored --exclude-standard --cached 2>/dev/null)
  if [ -z "$IGNORED_TRACKED" ]; then
    echo "  Clean — no tracked+ignored files"
    continue
  fi

  FILE_COUNT=$(echo "$IGNORED_TRACKED" | wc -l | tr -d ' ')
  echo "  Found $FILE_COUNT tracked+ignored file(s)"

  # Check branch state
  CURRENT_BRANCH=$(git -C "$REPO_PATH" branch --show-current 2>/dev/null)
  IS_DIRTY=$(git -C "$REPO_PATH" status --porcelain 2>/dev/null | grep -v "^??" | head -1)
  HAS_POLECATS=$(git -C "$REPO_PATH" branch 2>/dev/null | grep -E "^\+?\s+polecat/" | head -1)

  if [ -n "$IS_DIRTY" ] || [ -n "$HAS_POLECATS" ] || [ "$CURRENT_BRANCH" != "main" ]; then
    # Create a chore bead instead of interfering
    REASON=""
    [ -n "$IS_DIRTY" ] && REASON="dirty working tree"
    [ -n "$HAS_POLECATS" ] && REASON="${REASON:+$REASON, }active polecat worktrees"
    [ "$CURRENT_BRANCH" != "main" ] && REASON="${REASON:+$REASON, }not on main ($CURRENT_BRANCH)"
    echo "  SKIP: $REASON — creating chore bead"
    # Every clone is named "rig", so basename alone would title every bead
    # identically. Keep the last three path components.
    REPO_NAME=$(echo "$REPO_PATH" | awk -F/ 'NF>=3 {print $(NF-2)"/"$(NF-1)"/"$NF; next} {print}')
    bd create "gitignore-reconcile: $REPO_NAME has $FILE_COUNT tracked+ignored file(s)" \
      -t chore \
      -l "plugin:gitignore-reconcile,category:git-hygiene" \
      -d "Repo: $REPO_PATH\nSkipped: $REASON\nFiles:\n$IGNORED_TRACKED" \
      --silent 2>/dev/null || true
    TOTAL_BEADS=$((TOTAL_BEADS + 1))
    continue
  fi

  # Safe to untrack: clean main branch, no active polecats
  echo "$IGNORED_TRACKED" | while IFS= read -r FILE; do
    [ -z "$FILE" ] && continue
    echo "  Untracking: $FILE"
    git -C "$REPO_PATH" rm --cached "$FILE" 2>/dev/null || true
  done

  # Commit if anything was staged
  STAGED=$(git -C "$REPO_PATH" diff --cached --name-only 2>/dev/null)
  if [ -n "$STAGED" ]; then
    COUNT=$(echo "$STAGED" | wc -l | tr -d ' ')
    git -C "$REPO_PATH" commit -m "chore: untrack $COUNT file(s) now matched by .gitignore

Auto-committed by gitignore-reconcile plugin.
Files untracked:
$(echo "$STAGED" | head -10)$([ $(echo "$STAGED" | wc -l) -gt 10 ] && echo "...and more")" \
      --author="Gas Town <gastown@local>" 2>/dev/null || true
    echo "  Committed untracking of $COUNT file(s)"
    TOTAL_UNTRACKED=$((TOTAL_UNTRACKED + COUNT))

    # Push (best effort)
    git -C "$REPO_PATH" push origin main 2>/dev/null || echo "  WARN: push failed (committed locally)"
  fi
done
```

## Record Result

```bash
SUMMARY="gitignore-reconcile: $TOTAL_UNTRACKED file(s) untracked, $TOTAL_BEADS chore bead(s) created"
echo ""
echo "=== Gitignore Reconcile Summary ==="
echo "$SUMMARY"

RESULT="success"
[ -n "$ERRORS" ] && RESULT="warning"

gt plugin record-run --plugin gitignore-reconcile --result "$RESULT" \
  --title "$SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
```

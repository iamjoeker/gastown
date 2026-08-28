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

Requires: `gh` CLI installed and authenticated (`gh api user` — **not**
`gh auth status`, see below).

## Auth failure has to be detectable (gt-zp1q)

`gh auth status` was measured on this host **exiting 0** while reporting that
the token was invalid:

```
$ gh auth status
github.com
  X Failed to log in to github.com account iamjoeker (default)
  - The token in default is invalid.
$ echo $?
0
```

So any caller shaped like `if gh auth status; then …` proceeds as though
authenticated, and one that also redirects stderr loses the diagnostic too. Step
2d below then issued a doomed `gh api … -X DELETE` per branch, swallowed every
error with `2>/dev/null`, and counted zero — a result byte-identical to "there
was nothing to delete". An expired token survived **17 consecutive merges**
that way, with a success receipt each time.

Three changes make it detectable, and each covers what the others do not:

1. **The predicate is a real authenticated API call.** `gh api user --jq .login`
   exercises the capability the deletes need rather than a status report about
   it, and exits nonzero on an invalid token. Exit 0 alone is not accepted — the
   login has to come back — because "exits 0 having done nothing" is the failure
   shape under repair. It is asked once per run, lazily, on the first branch
   that actually needs deleting, so a non-GitHub town never pays for it and
   never reports an auth problem it does not have.
2. **Remote deletes are auditable.** `gh api … -X DELETE 2>/dev/null && count++`
   is gone; stderr is captured, logged, and counted into the receipt, exactly as
   the local deletes already were.
3. **"Could not act on GitHub" is always in the summary,** and a *rejected*
   credential makes the receipt a `failure`. That is the condition this plugin is
   least able to notice on its own and the one that recurs silently. `gh` simply
   not being installed is static and stays a success — reported, not paged.

Verifying a fix here needs **both** directions, and the second is the one that
matters: point `GH_TOKEN` at a garbage value in a scoped subshell and confirm
the predicate goes false. A check that has only ever run against a working token
is precisely what shipped.

## The recovery window (gt-x6ji)

Step 2c force-deletes unmerged branches and Step 2f garbage collected in the
**same pass**. `git branch -D` drops the branch and its reflog together, so a
tip was unreachable the instant it returned, and `gc --prune=now` immediately
after collected the objects with no grace period. The exposure window was
**zero** — there was no moment in which a mistake could be noticed, let alone
undone. Three rules now hold it open, and they are deliberately redundant
because each covers what the others do not:

1. **Backup before delete.** Every force-deleted tip is first copied to
   `refs/gt-hygiene/deleted/<run-epoch>/<branch>` and read back. If the backup
   cannot be written and verified, the branch is **not** deleted. Recovery is
   `git branch <name> <backup-ref>` for `GIT_HYGIENE_BACKUP_TTL_SECONDS`
   (default 14 days), after which a later run expires the ref.
2. **`gc` does not pass `--prune=now`.** Anything else the run made unreachable
   — amended commits, dropped stashes, expired reflogs — keeps git's default
   two-week prune grace.
3. **Every delete is auditable.** Both delete steps used to end in
   `2>/dev/null && count++`, so a refusal was indistinguishable from having
   nothing to do. stderr is now captured, logged, counted, and turned into a
   `--result failure` receipt. This is the same defect that left the recycle
   formula's `git branch -D polecat/<name>` inert for months: it named a branch
   shape that never existed, failed on every invocation, and nothing said so.

## Which remotes can vouch for a branch (gt-x6ji)

Step 2c force-deletes anything "the remote" does not hold, so what counts as
the remote decides what gets destroyed. It used to resolve exactly one ref,
`refs/remotes/origin/<branch>`, which is blind in two ways:

- **Non-origin remotes.** A branch pushed anywhere else has no `origin/`
  tracking ref, so the guard never saw it.
- **Split-URL origin.** A remote may fetch one URL and push another, which is
  this town's shape. Tracking refs are built from *fetches*, so a branch pushed
  to the push URL leaves no `refs/remotes/origin/` ref and read as an orphan —
  blind to the very remote the work went to. Measured, not theoretical: three
  pushed `bd-gq7` branches all read as deletable.

So the guard consults **every** remote's tracking refs, and when a remote's
push URL differs from its fetch URL it asks that URL directly with `ls-remote`,
once per repo. If a push URL cannot be read, the orphan sweep for that repo is
**skipped entirely** — "no remote holds it" must be a fact the run established,
not a question it failed to ask. This is the only step that destroys unmerged
work, so it is the only one that fails closed.

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
TOTAL_BACKUPS_WRITTEN=0
TOTAL_BACKUPS_EXPIRED=0
TOTAL_SKIPPED_REPOS=0
DELETE_FAILURES=0
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

  # Close out backup refs from earlier runs whose recovery window has passed,
  # then learn which remotes can vouch for a branch — both before anything is
  # deleted.
  TOTAL_BACKUPS_EXPIRED=$((TOTAL_BACKUPS_EXPIRED + $(expire_backups "$REPO_PATH")))
  scan_remotes "$REPO_PATH"

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
    # Refusal is routine here (protected branch, checked out elsewhere) and must
    # not abort the run — but it is reported and counted, never swallowed.
    if delete_branch "$REPO_PATH" "$BRANCH" -d; then
      LOCAL_MERGED=$((LOCAL_MERGED + 1))
    else
      echo "    FAILED to delete merged $BRANCH: $DELETE_ERR"
      DELETE_FAILURES=$((DELETE_FAILURES + 1))
    fi
  done <<< "$MERGED_BRANCHES"
  TOTAL_LOCAL_MERGED=$((TOTAL_LOCAL_MERGED + LOCAL_MERGED))

  ### Step 2c: Delete stale unmerged orphan branches
  # Only delete branches matching known agent/temp patterns that:
  # - Have no active worktree (not + prefixed)
  # - Are held by NO remote (every remote's tracking refs, plus the heads read
  #   from a split push URL — see "Which remotes can vouch for a branch")
  # - Could be backed up first, verifiably
  #
  # Fails closed: if any remote's push URL could not be read, the sweep for this
  # repo is skipped entirely rather than run on an answer nobody has.
  LOCAL_ORPHAN=0
  if [ -n "$UNREADABLE_PUSH_REMOTES" ]; then
    echo "  SKIPPING orphan sweep: could not read push URL for remote(s):$UNREADABLE_PUSH_REMOTES"
    TOTAL_SKIPPED_REPOS=$((TOTAL_SKIPPED_REPOS + 1))
  else
    echo "  Deleting stale orphan branches..."
    STALE_PATTERNS="polecat/|dog/|fix/|pr-|integration/|worktree-agent-"
    ALL_BRANCHES=$(git -C "$REPO_PATH" branch 2>/dev/null \
      | grep -v "^\*" \
      | grep -v "^+" \
      | sed 's/^[[:space:]]*//')

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
      # Held by any remote at all? Then it is not an orphan.
      if branch_on_any_remote "$REPO_PATH" "$BRANCH"; then
        continue
      fi
      # Backup BEFORE delete, and only delete if the backup reads back.
      if ! BACKUP_REF=$(backup_branch "$REPO_PATH" "$BRANCH"); then
        echo "    SKIPPING orphan $BRANCH: could not write a recovery backup ref"
        DELETE_FAILURES=$((DELETE_FAILURES + 1))
        continue
      fi
      echo "    Deleting orphan: $BRANCH (recoverable for ${BACKUP_TTL_SECONDS}s: git -C $REPO_PATH branch $BRANCH $BACKUP_REF)"
      if delete_branch "$REPO_PATH" "$BRANCH" -D; then
        LOCAL_ORPHAN=$((LOCAL_ORPHAN + 1))
        TOTAL_BACKUPS_WRITTEN=$((TOTAL_BACKUPS_WRITTEN + 1))
      else
        echo "    FAILED to delete orphan $BRANCH: $DELETE_ERR"
        DELETE_FAILURES=$((DELETE_FAILURES + 1))
        # The branch survived, so its backup is noise.
        git -C "$REPO_PATH" update-ref -d "$BACKUP_REF" 2>/dev/null || true
      fi
    done <<< "$ALL_BRANCHES"
  fi
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
        # Ask once, here, whether we can act on GitHub at all — see "Auth
        # failure has to be detectable". Without it this loop made N doomed API
        # calls and reported the same zero as an empty work list.
        if ! gh_can_act; then
          echo "    SKIPPING remote deletes for $GH_REPO: $GH_AUTH_ERR"
          REMOTE_BLOCKED=1
          break
        fi
        echo "    Deleting remote: origin/$RBRANCH"
        # Use gh api because git push --delete may be blocked by pre-push hooks.
        # stderr is captured and counted, never sent to /dev/null.
        if REMOTE_ERR=$(gh api "repos/$GH_REPO/git/refs/heads/$RBRANCH" -X DELETE 2>&1 >/dev/null); then
          REMOTE_DELETED=$((REMOTE_DELETED + 1))
        else
          echo "    FAILED to delete remote origin/$RBRANCH: $REMOTE_ERR"
          REMOTE_DELETE_FAILURES=$((REMOTE_DELETE_FAILURES + 1))
        fi
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
  # NOT --prune=now. This runs in the same pass as the deletions above, and
  # --prune=now collected everything they had just made unreachable with no
  # grace period at all. git's default (prune objects older than two weeks) is
  # the recovery window for anything the backup refs do not cover.
  echo "  Running git gc..."
  git -C "$REPO_PATH" gc --quiet 2>/dev/null && TOTAL_GC=$((TOTAL_GC + 1))

  echo "  Done: $LOCAL_MERGED merged, $LOCAL_ORPHAN orphan, $REMOTE_DELETED remote, $STASH_COUNT stash(es)"
done <<< "$RIG_REPOS"
```

## Record Result

```bash
SUMMARY="$RIG_COUNT rig(s): $TOTAL_LOCAL_MERGED merged branch(es), $TOTAL_LOCAL_ORPHAN orphan branch(es), $TOTAL_REMOTE remote branch(es), $TOTAL_STASHES stash(es) cleared, $TOTAL_GC gc run(s)"
SUMMARY="$SUMMARY, $TOTAL_BACKUPS_WRITTEN backup(s) written, $TOTAL_BACKUPS_EXPIRED expired"
[ "$TOTAL_SKIPPED_REPOS" -gt 0 ] && SUMMARY="$SUMMARY, $TOTAL_SKIPPED_REPOS repo(s) skipped (unreadable push remote)"
[ "$DELETE_FAILURES" -gt 0 ] && SUMMARY="$SUMMARY, $DELETE_FAILURES delete failure(s)"
[ "$REMOTE_DELETE_FAILURES" -gt 0 ] && SUMMARY="$SUMMARY, $REMOTE_DELETE_FAILURES remote delete failure(s)"
[ "$TOTAL_REMOTE_BLOCKED" -gt 0 ] && SUMMARY="$SUMMARY, remote sweep BLOCKED on $TOTAL_REMOTE_BLOCKED repo(s): $GH_AUTH_ERR"
echo ""
echo "=== Git Hygiene Summary ==="
echo "$SUMMARY"
```

A delete that failed is a real result, not a footnote — reporting success while
a deletion step silently refused every branch is exactly how the recycle
formula's inert `branch -D` went unnoticed for months. So the receipt's result
follows the delete counters:

```bash
RESULT=success
[ "$DELETE_FAILURES" -gt 0 ] && RESULT=failure
[ "$REMOTE_DELETE_FAILURES" -gt 0 ] && RESULT=failure
# A rejected credential recurs silently and gets worse; gh being absent is
# static and already visible in the summary.
[ "$GH_AUTH_STATE" = denied ] && RESULT=failure

gt plugin record-run --plugin git-hygiene --result "$RESULT" \
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

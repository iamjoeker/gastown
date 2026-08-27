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

# --- Recovery window (gt-x6ji) -----------------------------------------------
#
# This plugin force-deletes unmerged branches and then garbage collects in the
# same pass. `git branch -D` drops the branch AND its reflog, so the commits are
# unreachable the instant it returns; `gc --prune=now` right after it collected
# them with no grace period at all. The exposure window was ZERO — there was no
# moment in which a mistake could be noticed, let alone undone.
#
# Two independent changes give the window back, and they are deliberately
# redundant because each covers what the other does not:
#
#   1. Every force-deleted tip is first copied to
#      refs/gt-hygiene/deleted/<run-epoch>/<branch>. A ref keeps the objects
#      reachable through `gc --prune=now`, so recovery is `git branch <name>
#      <backup-ref>` for BACKUP_TTL_SECONDS afterwards. If the backup cannot be
#      written and read back, the branch is NOT deleted.
#   2. `gc` no longer passes --prune=now, so anything else that became
#      unreachable during the run (amended commits, dropped stashes, expired
#      reflogs) keeps git's default two-week prune grace.
#
# The stamp lives in the ref name rather than in a reflog: reflogs are not
# written for refs outside the default set, and %(committerdate) is the commit's
# date, not the deletion's. A path component is readable, greppable, and makes
# expiry a plain integer comparison.
RUN_STAMP=$(date +%s)
BACKUP_TTL_SECONDS=${GIT_HYGIENE_BACKUP_TTL_SECONDS:-1209600} # 14 days
BACKUP_NS="refs/gt-hygiene/deleted"

# expire_backups drops backup refs older than the TTL. Deleting the ref is all
# that is needed: the objects then age out under gc's normal prune grace.
expire_backups() {
  local repo="$1" cutoff=$((RUN_STAMP - BACKUP_TTL_SECONDS))
  local ref stamp expired=0
  while IFS= read -r ref; do
    [ -z "$ref" ] && continue
    stamp=${ref#"$BACKUP_NS"/}
    stamp=${stamp%%/*}
    # A ref that does not carry an integer stamp was not written by this
    # plugin. Leave it alone rather than guessing at its age.
    case "$stamp" in
      '' | *[!0-9]*) continue ;;
    esac
    # age >= TTL, so a TTL of 0 means "expire on the next run" rather than
    # "expire on the run after the next one".
    if [ "$stamp" -le "$cutoff" ]; then
      git -C "$repo" update-ref -d "$ref" 2>/dev/null && expired=$((expired + 1)) || true
    fi
  done <<< "$(git -C "$repo" for-each-ref --format='%(refname)' "$BACKUP_NS" 2>/dev/null || true)"
  printf '%s\n' "$expired"
}

# backup_branch copies a branch tip into the backup namespace and reads it back.
# Prints the backup refname on success; returns nonzero if the tip could not be
# resolved, written, or verified — in which case the caller MUST NOT delete.
backup_branch() {
  local repo="$1" branch="$2"
  local sha backup_ref readback
  sha=$(git -C "$repo" rev-parse --verify --quiet "refs/heads/$branch^{commit}") || return 1
  backup_ref="$BACKUP_NS/$RUN_STAMP/$branch"
  git -C "$repo" update-ref "$backup_ref" "$sha" || return 1
  # Read back rather than trusting the exit status: a backup that is not
  # independently resolvable is not a recovery window.
  readback=$(git -C "$repo" rev-parse --verify --quiet "$backup_ref^{commit}") || return 1
  [ "$readback" = "$sha" ] || return 1
  printf '%s\n' "$backup_ref"
}

# --- Auditable deletes (gt-x6ji) ---------------------------------------------
#
# Both delete steps used to end in `2>/dev/null && count++ || true`, so a
# deletion that failed was indistinguishable from one that never had anything to
# do: no message, no count, and a success receipt either way. The recycle
# formula's own `branch -D polecat/<name>` has failed on every invocation for
# exactly this reason — the name shape is wrong and nothing has ever said so.
#
# delete_branch keeps the "one refusal must not abort the run" property that the
# `|| true` was there for, while making the refusal visible: stderr is captured
# and logged, and the caller counts it. A delete step that cannot report failure
# is unauditable, and an unauditable delete step is how a no-op survives months.
DELETE_ERR=""
delete_branch() {
  local repo="$1" branch="$2" flag="$3"
  DELETE_ERR=""
  if DELETE_ERR=$(git -C "$repo" branch "$flag" "$branch" 2>&1 >/dev/null); then
    return 0
  fi
  # Collapse to one line: this lands in a plugin receipt, not a terminal.
  DELETE_ERR=$(printf '%s' "$DELETE_ERR" | tr '\n' ' ' | sed 's/[[:space:]]\{1,\}/ /g; s/^ //; s/ $//')
  [ -n "$DELETE_ERR" ] || DELETE_ERR="git branch $flag failed with no message"
  return 1
}

# --- Can we actually act on GitHub? (gt-zp1q) ---------------------------------
#
# `gh auth status` was measured on this host EXITING 0 while printing
#
#     X Failed to log in to github.com account iamjoeker (default)
#     - The token in default is invalid.
#
# so an auth predicate shaped like `if gh auth status; then` reads
# "authenticated" throughout an outage, and one that also redirects stderr
# (`gh auth status 2>/dev/null`) throws away the only signal there was. That is
# how an expired token survived 17 consecutive merges with the remote sweep
# below deleting nothing and recording success every time.
#
# The predicate here is therefore a real authenticated API call — the capability
# this step actually needs — rather than a status report about one. `gh api user`
# returns 401 and exits nonzero on an invalid token, and it cannot drift from
# what the deletes require because it is the same credential on the same path.
#
# Two properties are deliberate:
#
#   - Exit status is not trusted alone. A `gh` that exits 0 while printing no
#     login is not proof of anything; the login has to come back.
#   - stderr is captured and reported, never discarded. "It produced nothing"
#     and "it never ran" are indistinguishable from the caller's side otherwise.
#
# Memoized and called lazily, so a town whose remotes are not GitHub — or a run
# with no branch to delete — never pays for a network round trip and never
# reports an auth problem it did not have.
GH_AUTH_STATE=unknown # unknown | ok | missing | denied
GH_AUTH_ERR=""

gh_can_act() {
  case "$GH_AUTH_STATE" in
    ok) return 0 ;;
    missing | denied) return 1 ;;
  esac

  if ! command -v gh >/dev/null 2>&1; then
    GH_AUTH_STATE=missing
    GH_AUTH_ERR="gh CLI is not installed"
    return 1
  fi

  local out
  if out=$(gh api user --jq .login 2>&1) && [ -n "$out" ]; then
    GH_AUTH_STATE=ok
    log "  gh authenticated as $out"
    return 0
  fi

  GH_AUTH_STATE=denied
  # Collapse to one line: this lands in a plugin receipt, not a terminal.
  GH_AUTH_ERR=$(printf '%s' "$out" | tr '\n' ' ' | sed 's/[[:space:]]\{1,\}/ /g; s/^ //; s/ $//')
  [ -n "$GH_AUTH_ERR" ] || GH_AUTH_ERR="gh api user exited 0 without returning a login"
  return 1
}

# --- Which remotes can vouch for a branch? (gt-x6ji) --------------------------
#
# The orphan sweep force-deletes anything "the remote" does not hold, so what
# counts as the remote decides what gets destroyed. It used to be a single ref,
# refs/remotes/origin/<branch>, which is blind in two ways.
#
# Non-origin remotes: a branch pushed anywhere else has no origin/ tracking ref,
# so the guard never sees it.
#
# Split-URL origin: a remote may fetch one URL and push another, which is this
# town's shape. Tracking refs are built from FETCHES, so a branch pushed to the
# push URL leaves no refs/remotes/origin/ ref and reads as an orphan — the guard
# is blind to the very remote the work went to. Measured, not theoretical: three
# pushed bd-gq7 branches all read as deletable.
#
# REMOTES and PUSH_HEADS are rebuilt per repo by scan_remotes below; this reads
# them as globals rather than re-deriving per branch, since ls-remote is network
# work and the answer cannot change mid-repo.
REMOTES=""
PUSH_HEADS=""
UNREADABLE_PUSH_REMOTES=""

# scan_remotes populates REMOTES, PUSH_HEADS and UNREADABLE_PUSH_REMOTES for one
# repo. A push URL that cannot be read leaves the repo's orphan sweep disabled
# (fail closed) rather than treating "cannot ask" as "not there".
scan_remotes() {
  local repo="$1"
  local remote fetch_url push_url ls
  REMOTES=$(git -C "$repo" remote 2>/dev/null || true)
  PUSH_HEADS=""
  UNREADABLE_PUSH_REMOTES=""
  while IFS= read -r remote; do
    [ -z "$remote" ] && continue
    fetch_url=$(git -C "$repo" remote get-url "$remote" 2>/dev/null || true)
    push_url=$(git -C "$repo" remote get-url --push "$remote" 2>/dev/null || true)
    [ -n "$push_url" ] || continue
    # Same URL both ways: the tracking refs already answer for this remote.
    [ "$push_url" = "$fetch_url" ] && continue
    log "  Remote '$remote' pushes to a different URL than it fetches; asking it directly"
    if ls=$(git -C "$repo" ls-remote --heads "$push_url" 2>/dev/null); then
      PUSH_HEADS="$PUSH_HEADS
$(printf '%s\n' "$ls" | sed -n 's|.*[[:space:]]refs/heads/||p')"
    else
      UNREADABLE_PUSH_REMOTES="$UNREADABLE_PUSH_REMOTES $remote"
    fi
  done <<< "$REMOTES"
}

# branch_on_any_remote answers "does any remote hold this branch?" across every
# remote's tracking refs plus the heads read from split push URLs.
branch_on_any_remote() {
  local repo="$1" branch="$2" remote
  while IFS= read -r remote; do
    [ -z "$remote" ] && continue
    if git -C "$repo" rev-parse --verify --quiet "refs/remotes/$remote/$branch" >/dev/null 2>&1; then
      return 0
    fi
  done <<< "$REMOTES"
  if [ -n "$PUSH_HEADS" ] && printf '%s\n' "$PUSH_HEADS" | grep -Fxq -- "$branch"; then
    return 0
  fi
  return 1
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
TOTAL_BACKUPS_WRITTEN=0
TOTAL_BACKUPS_EXPIRED=0
TOTAL_SKIPPED_REPOS=0
TOTAL_REMOTE_BLOCKED=0
DELETE_FAILURES=0
REMOTE_DELETE_FAILURES=0

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

  # Expire backup refs from earlier runs whose recovery window has closed.
  EXPIRED=$(expire_backups "$REPO_PATH")
  if [ "$EXPIRED" -gt 0 ]; then
    log "  Expired $EXPIRED recovery backup(s) older than ${BACKUP_TTL_SECONDS}s"
    TOTAL_BACKUPS_EXPIRED=$((TOTAL_BACKUPS_EXPIRED + EXPIRED))
  fi

  # Learn which remotes can vouch for a branch before anything is deleted.
  scan_remotes "$REPO_PATH"

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
    # A refused delete is routine (checked out elsewhere, protected) and must
    # not abort the run for every remaining branch and repo — but it is reported
    # and counted rather than swallowed.
    if delete_branch "$REPO_PATH" "$BRANCH" -d; then
      LOCAL_MERGED=$((LOCAL_MERGED + 1))
    else
      log "    FAILED to delete merged $BRANCH: $DELETE_ERR"
      DELETE_FAILURES=$((DELETE_FAILURES + 1))
    fi
  done <<< "$MERGED_BRANCHES"
  TOTAL_LOCAL_MERGED=$((TOTAL_LOCAL_MERGED + LOCAL_MERGED))

  # Step 3: Delete stale unmerged orphan branches
  #
  # This is the only step that destroys unmerged work, so it is the only one
  # that fails closed. If a remote could not be consulted, "no remote holds it"
  # is not a fact this run established — it is a question it failed to ask —
  # and the branch keeps its benefit of the doubt.
  LOCAL_ORPHAN=0
  if [ -n "$UNREADABLE_PUSH_REMOTES" ]; then
    log "  SKIPPING orphan sweep: could not read push URL for remote(s):$UNREADABLE_PUSH_REMOTES"
    log "    Branches pushed there would read as orphans; refusing to force-delete on an unread remote."
    TOTAL_SKIPPED_REPOS=$((TOTAL_SKIPPED_REPOS + 1))
  else
    log "  Deleting stale orphan branches..."
    STALE_PATTERNS="polecat/|dog/|fix/|pr-|integration/|worktree-agent-"
    ALL_BRANCHES=$(git -C "$REPO_PATH" branch 2>/dev/null \
      | grep -v "^\*" \
      | grep -v "^+" \
      | sed 's/^[[:space:]]*//' || true)

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
      if branch_on_any_remote "$REPO_PATH" "$BRANCH"; then
        continue
      fi
      # Backup BEFORE delete, and only delete if the backup reads back. This is
      # the recovery window: without it, `branch -D` drops the branch and its
      # reflog together and the tip is unreachable the instant it returns.
      if ! BACKUP_REF=$(backup_branch "$REPO_PATH" "$BRANCH"); then
        log "    SKIPPING orphan $BRANCH: could not write a recovery backup ref"
        DELETE_FAILURES=$((DELETE_FAILURES + 1))
        continue
      fi
      log "    Deleting orphan: $BRANCH (recoverable for ${BACKUP_TTL_SECONDS}s: git -C $REPO_PATH branch $BRANCH $BACKUP_REF)"
      if delete_branch "$REPO_PATH" "$BRANCH" -D; then
        LOCAL_ORPHAN=$((LOCAL_ORPHAN + 1))
        TOTAL_BACKUPS_WRITTEN=$((TOTAL_BACKUPS_WRITTEN + 1))
      else
        log "    FAILED to delete orphan $BRANCH: $DELETE_ERR"
        DELETE_FAILURES=$((DELETE_FAILURES + 1))
        # The branch survived, so its backup is noise. Dropping it keeps the
        # namespace a record of deletions rather than of attempts.
        git -C "$REPO_PATH" update-ref -d "$BACKUP_REF" 2>/dev/null || true
      fi
    done <<< "$ALL_BRANCHES"
  fi
  TOTAL_LOCAL_ORPHAN=$((TOTAL_LOCAL_ORPHAN + LOCAL_ORPHAN))

  # Step 4: Delete merged remote branches on GitHub
  log "  Deleting merged remote branches..."
  REMOTE_DELETED=0
  REMOTE_BLOCKED=0

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
        # Ask once, on the first branch that actually needs deleting, whether we
        # can act on GitHub at all. Without this the loop below issued N doomed
        # API calls, swallowed N errors, and counted zero — a result identical to
        # "there was nothing to delete" (gt-zp1q).
        if ! gh_can_act; then
          log "    SKIPPING remote deletes for $GH_REPO: $GH_AUTH_ERR"
          REMOTE_BLOCKED=1
          break
        fi
        log "    Deleting remote: origin/$RBRANCH"
        # Same rule as delete_branch above: a refusal must not abort the run, but
        # it is captured, logged and counted rather than sent to /dev/null.
        if REMOTE_ERR=$(gh api "repos/$GH_REPO/git/refs/heads/$RBRANCH" -X DELETE 2>&1 >/dev/null); then
          REMOTE_DELETED=$((REMOTE_DELETED + 1))
        else
          REMOTE_ERR=$(printf '%s' "$REMOTE_ERR" | tr '\n' ' ' | sed 's/[[:space:]]\{1,\}/ /g; s/^ //; s/ $//')
          [ -n "$REMOTE_ERR" ] || REMOTE_ERR="gh api DELETE failed with no message"
          log "    FAILED to delete remote origin/$RBRANCH: $REMOTE_ERR"
          REMOTE_DELETE_FAILURES=$((REMOTE_DELETE_FAILURES + 1))
        fi
      fi
    done <<< "$REMOTE_BRANCHES"
  fi
  TOTAL_REMOTE=$((TOTAL_REMOTE + REMOTE_DELETED))
  if [ "$REMOTE_BLOCKED" -gt 0 ]; then
    TOTAL_REMOTE_BLOCKED=$((TOTAL_REMOTE_BLOCKED + 1))
  fi

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
  #
  # NOT --prune=now. This gc runs in the same pass as the deletions above, and
  # --prune=now collected everything they had just made unreachable with no
  # grace period at all — branch tips, their reflogs, dropped stashes, amended
  # commits. git's default (prune objects older than two weeks) is the whole
  # recovery window for anything the backup refs above do not cover, and this
  # plugin's job is housekeeping, not reclaiming bytes on a deadline.
  log "  Running git gc..."
  git -C "$REPO_PATH" gc --quiet 2>/dev/null && TOTAL_GC=$((TOTAL_GC + 1)) || true

  log "  Done: $LOCAL_MERGED merged, $LOCAL_ORPHAN orphan, $REMOTE_DELETED remote, $STASH_COUNT stash(es)"
done <<< "$RIG_REPOS"

# --- Report -------------------------------------------------------------------

SUMMARY="$RIG_COUNT repo(s) across $RIG_TOTAL rig(s): $TOTAL_LOCAL_MERGED merged, $TOTAL_LOCAL_ORPHAN orphan, $TOTAL_REMOTE remote, $TOTAL_STASHES stash(es), $TOTAL_GC gc"
SUMMARY="$SUMMARY, $TOTAL_BACKUPS_WRITTEN backup(s) written, $TOTAL_BACKUPS_EXPIRED expired"
if [ "$TOTAL_SKIPPED_REPOS" -gt 0 ]; then
  SUMMARY="$SUMMARY, $TOTAL_SKIPPED_REPOS repo(s) skipped (unreadable push remote)"
fi
if [ "$DELETE_FAILURES" -gt 0 ]; then
  SUMMARY="$SUMMARY, $DELETE_FAILURES delete failure(s)"
fi
if [ "$REMOTE_DELETE_FAILURES" -gt 0 ]; then
  SUMMARY="$SUMMARY, $REMOTE_DELETE_FAILURES remote delete failure(s)"
fi
# "Could not act on GitHub" belongs in the receipt every single run. A summary
# that reads the same whether the remote sweep ran or was impossible is what
# made an expired token invisible across 17 merges (gt-zp1q).
if [ "$TOTAL_REMOTE_BLOCKED" -gt 0 ]; then
  SUMMARY="$SUMMARY, remote sweep BLOCKED on $TOTAL_REMOTE_BLOCKED repo(s): $GH_AUTH_ERR"
fi

log ""
log "=== Git Hygiene Summary ==="
log "$SUMMARY"
if [ "$TOTAL_BACKUPS_WRITTEN" -gt 0 ]; then
  log "Force-deleted tips are recoverable from $BACKUP_NS/$RUN_STAMP/<branch> for ${BACKUP_TTL_SECONDS}s"
fi

# A delete that failed is a real result, not a footnote. Reporting success while
# a deletion step silently refused every branch is exactly how this plugin's
# sibling in the recycle formula stayed inert and unnoticed.
RESULT=success
if [ "$DELETE_FAILURES" -gt 0 ] || [ "$REMOTE_DELETE_FAILURES" -gt 0 ]; then
  RESULT=failure
fi
# A rejected credential is an operator condition that recurs silently and gets
# worse: it is the failure this plugin is least able to notice on its own, so it
# fails the receipt and trips notify_on_failure. `gh` simply not being installed
# is static and visible in the summary above — worth saying, not worth paging
# about every 12h.
if [ "$GH_AUTH_STATE" = denied ]; then
  RESULT=failure
fi

gt plugin record-run --plugin git-hygiene --result "$RESULT" \
  --title "git-hygiene: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true

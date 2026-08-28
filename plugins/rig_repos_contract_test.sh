#!/usr/bin/env bash
# rig_repos_contract_test.sh — Regression tests for gt-a7a.
#
# git-hygiene, gitignore-reconcile and submodule-commit all enumerate their
# work from `gt rig list --json`. All three read a "repo_path" key that the
# command never emitted, so all three filtered every rig out and exited 0 with
# a message that read like a legitimately empty work list. They had never
# processed a repo.
#
# These tests pin the contract that replaced it:
#   1. A rig list with no "repos" key is a FAILURE, not a skip.
#   2. A rig list with "repos" is actually iterated.
#   3. Rigs with no clones is a quiet success, distinguishable from (1).
#   4. Rig names come from the JSON, not basename() of a clone path — every
#      clone is named "rig", so basename resolves every rig to "rig".

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PASS=0
FAIL=0
CLEANUP_DIRS=()

cleanup() {
  # Preserve the script's exit status: an EXIT trap's last command would
  # otherwise become the status the caller (and make) sees.
  local status=$?
  if [ "${#CLEANUP_DIRS[@]}" -gt 0 ]; then
    rm -rf "${CLEANUP_DIRS[@]}"
  fi
  exit "$status"
}
trap cleanup EXIT

record_pass() {
  PASS=$((PASS + 1))
  printf 'PASS: %s\n' "$1"
}

record_fail() {
  FAIL=$((FAIL + 1))
  printf 'FAIL: %s\n' "$1"
}

assert_contains() {
  local file="$1" needle="$2" label="$3"
  if grep -Fq -- "$needle" "$file"; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  expected %q in output:\n' "$needle"
    sed 's/^/    /' "$file"
  fi
}

assert_not_contains() {
  local file="$1" needle="$2" label="$3"
  if ! grep -Fq -- "$needle" "$file" 2>/dev/null; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  did not expect %q in output:\n' "$needle"
    sed 's/^/    /' "$file"
  fi
}

assert_exit() {
  local actual="$1" expected="$2" label="$3"
  if [ "$actual" = "$expected" ]; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  expected exit %s, got %s\n' "$expected" "$actual"
  fi
}

# --- Test environment --------------------------------------------------------

# write_fake_commands installs stubs for every external command the plugins
# call, so a test run touches nothing outside its temp directory.
write_fake_commands() {
  local bin_dir="$1"

  cat > "$bin_dir/gt" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-} ${2:-}" in
  "rig list")
    if [ -f "$TEST_STATE/rig_list.json" ]; then
      cat "$TEST_STATE/rig_list.json"
      exit 0
    fi
    exit 1
    ;;
  "rig settings")
    # gt rig settings show <rig>
    rig="${4:-}"
    if [ -f "$TEST_STATE/settings/$rig.json" ]; then
      cat "$TEST_STATE/settings/$rig.json"
      exit 0
    fi
    printf 'No settings file found\n'
    exit 0
    ;;
  "plugin record-run")
    printf '%s\n' "$*" >> "$TEST_STATE/record.log"
    exit 0
    ;;
  "escalate "*|"escalate")
    printf '%s\n' "$*" >> "$TEST_STATE/escalate.log"
    exit 0
    ;;
esac

printf 'unexpected gt call: %s\n' "$*" >&2
exit 1
SH
  chmod +x "$bin_dir/gt"

  cat > "$bin_dir/bd" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$TEST_STATE/bd.log"
exit 0
SH
  chmod +x "$bin_dir/bd"

  # Scriptable `gh` stub (gt-zp1q). The default is "the credential is rejected",
  # because that is the state the plugins have to survive and the one that was
  # never exercised. A test opts into a working credential by writing
  # $TEST_STATE/gh_login; an EMPTY gh_login is the "exits 0 having proved
  # nothing" case, which must read as unauthenticated. gh_delete_fails makes the
  # DELETE call fail while auth succeeds.
  cat > "$bin_dir/gh" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$TEST_STATE/gh.log"

if [ "${1:-}" = "api" ] && [ "${2:-}" = "user" ]; then
  if [ -f "$TEST_STATE/gh_login" ]; then
    cat "$TEST_STATE/gh_login"
    exit 0
  fi
  printf 'gh: Bad credentials (HTTP 401)\n' >&2
  exit 1
fi

if [ ! -f "$TEST_STATE/gh_login" ]; then
  printf 'gh: Bad credentials (HTTP 401)\n' >&2
  exit 1
fi
if [ -f "$TEST_STATE/gh_delete_fails" ]; then
  printf 'gh: Must have admin rights to Repository. (HTTP 403)\n' >&2
  exit 1
fi
exit 0
SH
  chmod +x "$bin_dir/gh"
}

# make_repo creates a minimal git repo with one commit on main.
make_repo() {
  local path="$1"
  mkdir -p "$path"
  git -C "$path" init -q -b main
  git -C "$path" config user.email "test@example.com"
  git -C "$path" config user.name "Test"
  printf 'seed\n' > "$path/README"
  git -C "$path" add README
  git -C "$path" commit -qm "seed"
}

# new_env builds a fresh sandbox and sets ENV_ROOT to its path. It must assign
# rather than echo: a $(new_env) subshell would lose the cleanup registration.
# Callers use $ENV_ROOT/state for fixtures and $ENV_ROOT/town for the fake town.
new_env() {
  ENV_ROOT=$(mktemp -d)
  CLEANUP_DIRS+=("$ENV_ROOT")
  mkdir -p "$ENV_ROOT/bin" "$ENV_ROOT/state/settings" "$ENV_ROOT/town"
  write_fake_commands "$ENV_ROOT/bin"
}

# run_plugin runs a plugin with the sandbox on PATH and captures output+status.
# Usage: run_plugin <env-root> <plugin-name> <out-file> [VAR=VAL ...]; echoes the
# exit status. The extra VAR=VAL words go through `env` rather than an
# assignment prefix, because assignment prefixes are recognised before "$@" is
# expanded and would be taken as the command name instead.
run_plugin() {
  local root="$1" plugin="$2" out="$3" status=0
  shift 3
  PATH="$root/bin:$PATH" TEST_STATE="$root/state" \
    env "$@" bash "$ROOT_DIR/plugins/$plugin/run.sh" > "$out" 2>&1 || status=$?
  printf '%s\n' "$status"
}

# assert_branch checks whether a local branch exists. present=yes|no.
assert_branch() {
  local repo="$1" branch="$2" present="$3" label="$4"
  if git -C "$repo" rev-parse --verify --quiet "refs/heads/$branch" >/dev/null 2>&1; then
    if [ "$present" = "yes" ]; then record_pass "$label"; else
      record_fail "$label"
      printf '  branch %q still exists\n' "$branch"
    fi
  else
    if [ "$present" = "no" ]; then record_pass "$label"; else
      record_fail "$label"
      printf '  branch %q is gone\n' "$branch"
    fi
  fi
}

# make_bare creates an empty bare repo to act as a remote.
make_bare() {
  git init -q --bare -b main "$1"
}

# commit_on makes a new branch off main with one unique commit and returns to
# main. Echoes nothing; the caller reads the tip with rev-parse if it needs it.
commit_on() {
  local repo="$1" branch="$2" file="$3"
  git -C "$repo" checkout -q -b "$branch"
  printf '%s\n' "$file" > "$repo/$file"
  git -C "$repo" add "$file"
  git -C "$repo" commit -qm "work on $branch"
  git -C "$repo" checkout -q main
}

PLUGINS=(git-hygiene gitignore-reconcile submodule-commit)

# --- Test 1: a rig list without "repos" must fail loudly ---------------------
#
# This is the gt-a7a regression itself. Before the fix each plugin logged
# "SKIP: no rigs with repo paths" and exited 0, which is indistinguishable
# from having no work — so nobody noticed for months.

for plugin in "${PLUGINS[@]}"; do
  new_env
  cat > "$ENV_ROOT/state/rig_list.json" <<'JSON'
[
  {"name":"alpha","beads_prefix":"al","status":"operational","witness":"running","refinery":"running","polecats":0,"crew":0}
]
JSON

  OUT="$ENV_ROOT/out.txt"
  STATUS=$(run_plugin "$ENV_ROOT" "$plugin" "$OUT")

  assert_exit "$STATUS" 1 "$plugin: missing repos key exits nonzero"
  assert_contains "$OUT" "ERROR" "$plugin: missing repos key logs ERROR"
  assert_contains "$OUT" "gt-a7a" "$plugin: missing repos key names the regression"
  assert_not_contains "$OUT" "SKIP: no rigs" "$plugin: missing repos key is not reported as a skip"
  assert_contains "$ENV_ROOT/state/record.log" "--result failure" \
    "$plugin: missing repos key records a failure receipt"
done

# --- Test 2: rigs with no clones is a quiet success ---------------------------
#
# The empty work list must stay distinguishable from the broken contract above:
# same outcome for the operator (nothing done), opposite outcome for the alarm.

for plugin in "${PLUGINS[@]}"; do
  new_env
  cat > "$ENV_ROOT/state/rig_list.json" <<'JSON'
[
  {"name":"alpha","beads_prefix":"al","status":"operational","path":"/nonexistent","repos":[]}
]
JSON

  OUT="$ENV_ROOT/out.txt"
  STATUS=$(run_plugin "$ENV_ROOT" "$plugin" "$OUT")

  assert_exit "$STATUS" 0 "$plugin: no clones exits 0"
  assert_contains "$OUT" "no git clones found" "$plugin: no clones says so explicitly"
  assert_contains "$ENV_ROOT/state/record.log" "--result success" \
    "$plugin: no clones records a success receipt"
done

# --- Test 3: git-hygiene actually visits every clone -------------------------

new_env
make_repo "$ENV_ROOT/town/alpha/mayor/rig"
make_repo "$ENV_ROOT/town/alpha/refinery/rig"
cat > "$ENV_ROOT/state/rig_list.json" <<JSON
[
  {"name":"alpha","beads_prefix":"al","status":"operational",
   "path":"$ENV_ROOT/town/alpha",
   "mayor_repo":"$ENV_ROOT/town/alpha/mayor/rig",
   "refinery_repo":"$ENV_ROOT/town/alpha/refinery/rig",
   "repos":["$ENV_ROOT/town/alpha/mayor/rig","$ENV_ROOT/town/alpha/refinery/rig"]}
]
JSON

OUT="$ENV_ROOT/out.txt"
STATUS=$(run_plugin "$ENV_ROOT" git-hygiene "$OUT")
assert_exit "$STATUS" 0 "git-hygiene: exits 0 with real clones"
assert_contains "$OUT" "=== Cleaning: $ENV_ROOT/town/alpha/mayor/rig ===" \
  "git-hygiene: cleans the mayor clone"
assert_contains "$OUT" "=== Cleaning: $ENV_ROOT/town/alpha/refinery/rig ===" \
  "git-hygiene: cleans the refinery clone"
assert_contains "$OUT" "2 repo(s) across 1 rig(s)" "git-hygiene: summary counts clones, not rigs"

# --- Test 4: git-hygiene leaves stashes alone unless the rig opts in ----------
#
# `git stash clear` is unrecoverable and mayor/rig is a human's clone, so the
# default must be to report rather than destroy.

new_env
make_repo "$ENV_ROOT/town/alpha/mayor/rig"
printf 'work in progress\n' >> "$ENV_ROOT/town/alpha/mayor/rig/README"
git -C "$ENV_ROOT/town/alpha/mayor/rig" stash -q
cat > "$ENV_ROOT/state/rig_list.json" <<JSON
[
  {"name":"alpha","beads_prefix":"al","status":"operational",
   "path":"$ENV_ROOT/town/alpha",
   "repos":["$ENV_ROOT/town/alpha/mayor/rig"]}
]
JSON

OUT="$ENV_ROOT/out.txt"
STATUS=$(run_plugin "$ENV_ROOT" git-hygiene "$OUT")
assert_exit "$STATUS" 0 "git-hygiene: exits 0 with a stash present"
assert_contains "$OUT" "left alone" "git-hygiene: reports the stash instead of clearing it"
STASHES=$(git -C "$ENV_ROOT/town/alpha/mayor/rig" stash list | wc -l | tr -d ' ')
if [ "$STASHES" = "1" ]; then
  record_pass "git-hygiene: stash survives when the rig has not opted in"
else
  record_fail "git-hygiene: stash survives when the rig has not opted in"
  printf '  expected 1 stash, found %s\n' "$STASHES"
fi

# Opted in, the same run clears it.
cat > "$ENV_ROOT/state/settings/alpha.json" <<'JSON'
{"type":"rig-settings","version":1,"plugins":{"git-hygiene":{"clear_stashes":true}}}
JSON
OUT="$ENV_ROOT/out2.txt"
STATUS=$(run_plugin "$ENV_ROOT" git-hygiene "$OUT")
assert_exit "$STATUS" 0 "git-hygiene: exits 0 when opted in to stash clearing"
STASHES=$(git -C "$ENV_ROOT/town/alpha/mayor/rig" stash list | wc -l | tr -d ' ')
if [ "$STASHES" = "0" ]; then
  record_pass "git-hygiene: stash cleared once the rig opts in"
else
  record_fail "git-hygiene: stash cleared once the rig opts in"
  printf '  expected 0 stashes, found %s\n' "$STASHES"
fi

# --- Test 5: gitignore-reconcile visits clones and names beads usably --------

new_env
make_repo "$ENV_ROOT/town/alpha/mayor/rig"
REPO="$ENV_ROOT/town/alpha/mayor/rig"
# A tracked file that a later .gitignore rule now matches, on a non-main branch
# so the plugin files a chore bead rather than rewriting history under us.
mkdir -p "$REPO/build"
printf 'artifact\n' > "$REPO/build/out.o"
printf 'build/\n' > "$REPO/.gitignore"
git -C "$REPO" add -A -f
git -C "$REPO" commit -qm "add tracked artifact"
git -C "$REPO" checkout -q -b dev/local
cat > "$ENV_ROOT/state/rig_list.json" <<JSON
[
  {"name":"alpha","beads_prefix":"al","status":"operational",
   "path":"$ENV_ROOT/town/alpha",
   "repos":["$REPO"]}
]
JSON

OUT="$ENV_ROOT/out.txt"
STATUS=$(run_plugin "$ENV_ROOT" gitignore-reconcile "$OUT")
assert_exit "$STATUS" 0 "gitignore-reconcile: exits 0 with real clones"
assert_contains "$OUT" "=== $REPO ===" "gitignore-reconcile: visits the clone"
assert_contains "$OUT" "tracked+ignored file(s)" "gitignore-reconcile: sees the tracked+ignored file"
assert_contains "$ENV_ROOT/state/bd.log" "alpha/mayor/rig has" \
  "gitignore-reconcile: bead title identifies which clone (not bare 'rig')"

# --- Test 6: submodule-commit resolves rig names from JSON, not basename -----
#
# Every clone path ends in "/rig", so the old basename() lookup asked for a rig
# literally named "rig" and every opt-in check answered false.

new_env
make_repo "$ENV_ROOT/town/alpha/mayor/rig"
REPO="$ENV_ROOT/town/alpha/mayor/rig"
printf '[submodule "vendor/lib"]\n\tpath = vendor/lib\n\turl = https://example.com/lib\n' > "$REPO/.gitmodules"
cat > "$ENV_ROOT/state/rig_list.json" <<JSON
[
  {"name":"alpha","beads_prefix":"al","status":"operational",
   "path":"$ENV_ROOT/town/alpha",
   "repos":["$REPO"]}
]
JSON

# Not opted in yet.
OUT="$ENV_ROOT/out.txt"
STATUS=$(run_plugin "$ENV_ROOT" submodule-commit "$OUT")
assert_exit "$STATUS" 0 "submodule-commit: exits 0 when no rig has opted in"
assert_contains "$OUT" "Not opted in: alpha" "submodule-commit: names the rig it checked"
assert_contains "$OUT" "none opted in" "submodule-commit: distinguishes 'not opted in' from 'no repos'"

# Opted in via rig settings.
cat > "$ENV_ROOT/state/settings/alpha.json" <<'JSON'
{"type":"rig-settings","version":1,"plugins":{"submodule-commit":{"enabled":true}}}
JSON
OUT="$ENV_ROOT/out2.txt"
STATUS=$(run_plugin "$ENV_ROOT" submodule-commit "$OUT")
assert_exit "$STATUS" 0 "submodule-commit: exits 0 when a rig has opted in"
assert_contains "$OUT" "Opt-in rig: alpha" "submodule-commit: honours the rig settings opt-in"

# --- Tests 7-10: the orphan sweep must be survivable (gt-x6ji) ---------------
#
# Step 2c force-deletes unmerged branches and Step 2f garbage collected in the
# SAME pass. `git branch -D` drops the branch and its reflog together, so the
# tip was unreachable the instant it returned, and `gc --prune=now` right after
# it collected the objects with no grace period: the exposure window was ZERO.
# The guard that decided what to delete resolved exactly one ref,
# refs/remotes/origin/<branch>, so a branch pushed to a non-origin remote — or
# to origin's PUSH url when it differs from its fetch url, which is this town's
# shape — read as an orphan and was deletable. Three pushed bd-gq7 branches were
# measured in that state.
#
# These tests pin the replacement: a verified backup ref before any force
# delete, gc without --prune=now, a guard that consults every remote, and a
# fail-closed skip when a remote cannot be read.

# make_hygiene_env builds a rig with one clone whose origin is a real bare repo,
# and sets REPO / ORIGIN / RIG_JSON_PATH for the caller.
make_hygiene_env() {
  new_env
  REPO="$ENV_ROOT/town/alpha/mayor/rig"
  ORIGIN="$ENV_ROOT/origin.git"
  make_repo "$REPO"
  make_bare "$ORIGIN"
  git -C "$REPO" remote add origin "$ORIGIN"
  git -C "$REPO" push -q origin main
  git -C "$REPO" fetch -q origin
  cat > "$ENV_ROOT/state/rig_list.json" <<JSON
[
  {"name":"alpha","beads_prefix":"al","status":"operational",
   "path":"$ENV_ROOT/town/alpha",
   "repos":["$REPO"]}
]
JSON
}

# --- Test 7: a force-deleted tip stays recoverable ---------------------------

make_hygiene_env
commit_on "$REPO" "polecat/gone/al-1+aaa" orphan.txt
ORPHAN_SHA=$(git -C "$REPO" rev-parse "polecat/gone/al-1+aaa")
commit_on "$REPO" "polecat/kept/al-2+bbb" pushed.txt
git -C "$REPO" push -q origin "polecat/kept/al-2+bbb"
git -C "$REPO" fetch -q origin

# An unreferenced commit: with --prune=now this is collected in the same run.
DANGLING=$(git -C "$REPO" commit-tree -m dangling "$(git -C "$REPO" rev-parse "main^{tree}")")

OUT="$ENV_ROOT/out.txt"
STATUS=$(run_plugin "$ENV_ROOT" git-hygiene "$OUT")
assert_exit "$STATUS" 0 "git-hygiene: exits 0 sweeping orphans"
assert_branch "$REPO" "polecat/gone/al-1+aaa" no "git-hygiene: deletes the orphan branch"
assert_branch "$REPO" "polecat/kept/al-2+bbb" yes "git-hygiene: keeps a branch origin still holds"

BACKUPS=$(git -C "$REPO" for-each-ref --format='%(objectname)' refs/gt-hygiene/deleted)
if printf '%s\n' "$BACKUPS" | grep -Fxq -- "$ORPHAN_SHA"; then
  record_pass "git-hygiene: the deleted tip is held by a backup ref"
else
  record_fail "git-hygiene: the deleted tip is held by a backup ref"
  printf '  expected %s among backup refs:\n%s\n' "$ORPHAN_SHA" "$BACKUPS"
fi

if git -C "$REPO" cat-file -e "$ORPHAN_SHA^{commit}" 2>/dev/null; then
  record_pass "git-hygiene: the deleted commit survives the same run's gc"
else
  record_fail "git-hygiene: the deleted commit survives the same run's gc"
fi

if git -C "$REPO" cat-file -e "$DANGLING^{commit}" 2>/dev/null; then
  record_pass "git-hygiene: gc does not prune unreachable objects with no grace period"
else
  record_fail "git-hygiene: gc does not prune unreachable objects with no grace period"
  printf '  unreferenced commit %s was collected — gc is still using --prune=now\n' "$DANGLING"
fi

assert_contains "$OUT" "recoverable for" "git-hygiene: logs how to recover a deleted branch"

# --- Test 8: backups expire once their window closes -------------------------
#
# Same repo, TTL of zero: the refs written above are now past their window and
# the next run must drop them. Expiry is what keeps the namespace from becoming
# a second, permanent copy of every branch this plugin has ever deleted.

OUT="$ENV_ROOT/out2.txt"
STATUS=$(run_plugin "$ENV_ROOT" git-hygiene "$OUT" GIT_HYGIENE_BACKUP_TTL_SECONDS=0)
assert_exit "$STATUS" 0 "git-hygiene: exits 0 expiring backups"
LEFT=$(git -C "$REPO" for-each-ref --format='%(refname)' refs/gt-hygiene/deleted | wc -l | tr -d ' ')
if [ "$LEFT" = "0" ]; then
  record_pass "git-hygiene: expired backup refs are removed"
else
  record_fail "git-hygiene: expired backup refs are removed"
  printf '  %s backup ref(s) remain\n' "$LEFT"
fi

# --- Test 9: a split fetch/push origin is not blind ---------------------------
#
# The branch below exists ONLY on the push URL. Tracking refs are built from
# fetches, so refs/remotes/origin/<branch> does not exist for it and the old
# single-ref guard read it as an orphan. This is the measured bd-gq7 shape.

make_hygiene_env
PUSH_REMOTE="$ENV_ROOT/push.git"
make_bare "$PUSH_REMOTE"
commit_on "$REPO" "polecat/split/al-3+ccc" split.txt
git -C "$REPO" push -q "$PUSH_REMOTE" "polecat/split/al-3+ccc"
git -C "$REPO" remote set-url --push origin "$PUSH_REMOTE"

OUT="$ENV_ROOT/out.txt"
STATUS=$(run_plugin "$ENV_ROOT" git-hygiene "$OUT")
assert_exit "$STATUS" 0 "git-hygiene: exits 0 with a split-URL origin"
assert_contains "$OUT" "pushes to a different URL" "git-hygiene: notices the split fetch/push URL"
assert_branch "$REPO" "polecat/split/al-3+ccc" yes \
  "git-hygiene: keeps a branch held only by the push URL"

# --- Test 10: an unreadable remote fails closed ------------------------------
#
# "No remote holds it" must be a fact the run established, not a question it
# failed to ask. If a push URL cannot be read, the whole orphan sweep for that
# repo is skipped rather than run on an answer nobody has.

make_hygiene_env
git -C "$REPO" remote set-url --push origin "$ENV_ROOT/nonexistent.git"
commit_on "$REPO" "polecat/unknown/al-4+ddd" unknown.txt

OUT="$ENV_ROOT/out.txt"
STATUS=$(run_plugin "$ENV_ROOT" git-hygiene "$OUT")
assert_exit "$STATUS" 0 "git-hygiene: exits 0 when a push remote is unreadable"
assert_contains "$OUT" "SKIPPING orphan sweep" "git-hygiene: says it skipped the sweep"
assert_branch "$REPO" "polecat/unknown/al-4+ddd" yes \
  "git-hygiene: deletes nothing when a remote could not be consulted"
assert_contains "$ENV_ROOT/state/record.log" "repo(s) skipped" \
  "git-hygiene: the skip reaches the receipt, not just the log"

# --- Tests 11-14: auth failure must be detectable (gt-zp1q) ------------------
#
# `gh auth status` exits 0 while reporting an invalid token, so a predicate
# built on its exit code reads "authenticated" throughout an outage. The remote
# sweep then issued a doomed DELETE per branch, swallowed each error with
# 2>/dev/null, and counted zero — indistinguishable from an empty work list. An
# expired token survived 17 consecutive merges that way, with a success receipt
# every time.
#
# Both directions are tested here on purpose. A check that has only ever run
# against a working credential is exactly what shipped, so the rejected-token
# case is the one that carries the regression; the accepted-token case is its
# control, proving these assertions can distinguish the two at all rather than
# failing for some unrelated reason.

# make_mergeable_remote_branch pushes a polecat branch, merges it into main, and
# pushes that — so origin/<branch> is an ancestor of origin/main and the remote
# sweep has exactly one candidate to act on.
make_mergeable_remote_branch() {
  local repo="$1" branch="$2"
  commit_on "$repo" "$branch" "${branch//\//_}.txt"
  git -C "$repo" push -q origin "$branch"
  git -C "$repo" merge -q --ff-only "$branch"
  git -C "$repo" push -q origin main
  git -C "$repo" fetch -q origin
}

# Test 11: a rejected credential blocks the sweep, says so, and fails the receipt.
make_hygiene_env
make_mergeable_remote_branch "$REPO" "polecat/landed/al-5+eee"

OUT="$ENV_ROOT/out.txt"
STATUS=$(run_plugin "$ENV_ROOT" git-hygiene "$OUT")
assert_exit "$STATUS" 0 "git-hygiene: completes the run when gh cannot authenticate"
assert_contains "$ENV_ROOT/state/gh.log" "api user" \
  "git-hygiene: probes auth with a real API call, not gh auth status"
assert_not_contains "$ENV_ROOT/state/gh.log" "auth status" \
  "git-hygiene: never uses gh auth status as the predicate"
assert_contains "$OUT" "SKIPPING remote deletes" \
  "git-hygiene: says the remote sweep did not run"
assert_not_contains "$ENV_ROOT/state/gh.log" "-X DELETE" \
  "git-hygiene: issues no doomed DELETE calls once auth is known bad"
assert_contains "$ENV_ROOT/state/record.log" "remote sweep BLOCKED" \
  "git-hygiene: the block reaches the receipt, not just the log"
assert_contains "$ENV_ROOT/state/record.log" "--result failure" \
  "git-hygiene: a rejected credential is a failure, not a quiet success"

# Test 12: the control — a working credential lets the sweep through.
make_hygiene_env
printf 'iamjoeker\n' > "$ENV_ROOT/state/gh_login"
make_mergeable_remote_branch "$REPO" "polecat/landed/al-6+fff"

OUT="$ENV_ROOT/out.txt"
STATUS=$(run_plugin "$ENV_ROOT" git-hygiene "$OUT")
assert_exit "$STATUS" 0 "git-hygiene: exits 0 with a working credential"
assert_contains "$OUT" "gh authenticated as iamjoeker" \
  "git-hygiene: reports who it authenticated as"
assert_contains "$ENV_ROOT/state/gh.log" "-X DELETE" \
  "git-hygiene: actually deletes the merged remote branch"
assert_not_contains "$OUT" "SKIPPING remote deletes" \
  "git-hygiene: does not skip the sweep when auth is good"
assert_not_contains "$ENV_ROOT/state/record.log" "BLOCKED" \
  "git-hygiene: no block reported when there was none"
assert_contains "$ENV_ROOT/state/record.log" "--result success" \
  "git-hygiene: a clean run still records success"

# Test 13: exit 0 is not proof. A gh that returns no login has demonstrated
# nothing, and accepting it would rebuild the defect from the other side.
make_hygiene_env
: > "$ENV_ROOT/state/gh_login"
make_mergeable_remote_branch "$REPO" "polecat/landed/al-7+ggg"

OUT="$ENV_ROOT/out.txt"
STATUS=$(run_plugin "$ENV_ROOT" git-hygiene "$OUT")
assert_exit "$STATUS" 0 "git-hygiene: completes when gh exits 0 with no login"
assert_contains "$OUT" "SKIPPING remote deletes" \
  "git-hygiene: exit 0 without a login does not count as authenticated"
assert_not_contains "$ENV_ROOT/state/gh.log" "-X DELETE" \
  "git-hygiene: no deletes attempted on an unproven credential"
assert_contains "$ENV_ROOT/state/record.log" "--result failure" \
  "git-hygiene: an unproven credential fails the receipt"

# Test 14: a remote delete that fails is counted and reported, not swallowed.
# This is the second half of the original defect: even with good auth, the old
# `2>/dev/null && count++` made a refusal look like nothing to do.
make_hygiene_env
printf 'iamjoeker\n' > "$ENV_ROOT/state/gh_login"
: > "$ENV_ROOT/state/gh_delete_fails"
make_mergeable_remote_branch "$REPO" "polecat/landed/al-8+hhh"

OUT="$ENV_ROOT/out.txt"
STATUS=$(run_plugin "$ENV_ROOT" git-hygiene "$OUT")
assert_exit "$STATUS" 0 "git-hygiene: a refused remote delete does not abort the run"
assert_contains "$OUT" "FAILED to delete remote" \
  "git-hygiene: names the remote branch it could not delete"
assert_contains "$OUT" "HTTP 403" \
  "git-hygiene: surfaces gh's stderr instead of discarding it"
assert_contains "$ENV_ROOT/state/record.log" "remote delete failure(s)" \
  "git-hygiene: the remote delete failure reaches the receipt"
assert_contains "$ENV_ROOT/state/record.log" "--result failure" \
  "git-hygiene: a refused remote delete is a failure, not a success"

# --- Summary -----------------------------------------------------------------

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

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

  cat > "$bin_dir/gh" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$TEST_STATE/gh.log"
exit 1
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
# Usage: run_plugin <env-root> <plugin-name> <out-file>; echoes the exit status.
run_plugin() {
  local root="$1" plugin="$2" out="$3" status=0
  PATH="$root/bin:$PATH" TEST_STATE="$root/state" \
    bash "$ROOT_DIR/plugins/$plugin/run.sh" > "$out" 2>&1 || status=$?
  printf '%s\n' "$status"
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

# --- Summary -----------------------------------------------------------------

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

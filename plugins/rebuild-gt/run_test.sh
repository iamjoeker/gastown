#!/usr/bin/env bash
# rebuild-gt/run_test.sh — the decision table, exercised.
#
# Every case here is a way the plugin can do NOTHING while looking like it
# worked. That is the failure mode worth testing: a rebuild that fails is loud
# and gets fixed within the hour; a rebuild that silently no-ops leaves the
# whole town on an old binary and reports success in the wisp log the entire
# time (gt-ympl / hq-cak50).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT_DIR/plugins/rebuild-gt/run.sh"
ORIGINAL_PATH="$PATH"
PASS=0
FAIL=0
CLEANUP_DIRS=()

cleanup() {
  for dir in ${CLEANUP_DIRS[@]+"${CLEANUP_DIRS[@]}"}; do
    rm -rf "$dir"
  done
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

assert_file_contains() {
  local file="$1" needle="$2" label="$3"
  if [ -f "$file" ] && grep -Fq -- "$needle" "$file"; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  expected %q in %s\n' "$needle" "$file"
    sed 's/^/    /' "$file" 2>/dev/null || printf '    (missing)\n'
  fi
}

assert_file_not_contains() {
  local file="$1" needle="$2" label="$3"
  if [ ! -f "$file" ] || ! grep -Fq -- "$needle" "$file"; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  did not expect %q in %s\n' "$needle" "$file"
    sed 's/^/    /' "$file" 2>/dev/null || true
  fi
}

assert_status() {
  local actual="$1" expected="$2" label="$3"
  if [ "$actual" = "$expected" ]; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  expected exit %s, got %s\n' "$expected" "$actual"
  fi
}

# write_fake_commands stubs gt and make. git is the REAL git: these cases turn
# on what git actually does with a fast-forward, and a stub could only confirm
# that the script calls the commands the stub was written to expect.
write_fake_commands() {
  local bin_dir="$1"

  cat > "$bin_dir/gt" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  town)
    if [ "${2:-}" = "root" ]; then
      cat "$TEST_STATE/town_root_value"
      exit 0
    fi
    ;;
  stale)
    printf '%s\n' "$*" >> "$TEST_STATE/stale_calls.log"
    if [ "${2:-}" = "--quiet" ]; then
      exit "$(cat "$TEST_STATE/stale_quiet_exit")"
    fi
    if [ -f "$TEST_STATE/stale_json_fail" ]; then
      exit 1
    fi
    cat "$TEST_STATE/stale.json"
    exit 0
    ;;
  plugin)
    if [ "${2:-}" = "record-run" ]; then
      printf '%s\n' "$*" >> "$TEST_STATE/record.log"
      exit 0
    fi
    ;;
  escalate)
    printf '%s\n' "$*" >> "$TEST_STATE/escalate.log"
    exit 0
    ;;
esac

printf 'unexpected gt call: %s\n' "$*" >&2
exit 1
SH
  chmod +x "$bin_dir/gt"

  # make records the source commit it was asked to compile. That recording is
  # the whole point of the fast-forward tests: "did it build" is not the
  # question, "did it build the commit that had landed" is.
  cat > "$bin_dir/make" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s@%s\n' "${1:-}" "$(git rev-parse HEAD)" >> "$TEST_STATE/make.log"
if [ -f "$TEST_STATE/make_fail" ]; then
  exit 1
fi
exit 0
SH
  chmod +x "$bin_dir/make"
}

# setup_case builds a remote and the working clone that stands in for
# $TOWN_ROOT/gastown/mayor/rig.
setup_case() {
  TEST_TMP=$(mktemp -d)
  CLEANUP_DIRS+=("$TEST_TMP")
  export TEST_STATE="$TEST_TMP/state"
  export GT_TOWN_ROOT="$TEST_TMP/town"
  local bin_dir="$TEST_TMP/bin"
  mkdir -p "$TEST_STATE" "$bin_dir" "$GT_TOWN_ROOT/gastown/mayor"
  printf '%s\n' "$GT_TOWN_ROOT" > "$TEST_STATE/town_root_value"
  printf '1\n' > "$TEST_STATE/stale_quiet_exit"

  REMOTE="$TEST_TMP/remote"
  RIG="$GT_TOWN_ROOT/gastown/mayor/rig"
  mkdir -p "$REMOTE"
  git_q -C "$REMOTE" init -q
  git_q -C "$REMOTE" config commit.gpgsign false
  printf 'v1\n' > "$REMOTE/file.txt"
  git_q -C "$REMOTE" add -A
  git_q -C "$REMOTE" commit -q --no-gpg-sign -m base
  git_q -C "$REMOTE" branch -M main
  git_q -C "$REMOTE" clone -q "$REMOTE" "$RIG"
  git_q -C "$RIG" config commit.gpgsign false

  write_fake_commands "$bin_dir"
  export PATH="$bin_dir:$ORIGINAL_PATH"
}

git_q() {
  env GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t \
      GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t \
      git "$@"
}

# land_on_remote adds a commit to the remote's main and echoes its hash. No
# local ref in the clone moves, which is the whole shape of the bug.
land_on_remote() {
  printf '%s\n' "$RANDOM" > "$REMOTE/file.txt"
  git_q -C "$REMOTE" add -A
  git_q -C "$REMOTE" commit -q --no-gpg-sign -m landed
  git_q -C "$REMOTE" rev-parse HEAD
}

write_stale_json() {
  cat > "$TEST_STATE/stale.json"
}

run_plugin() {
  local out="$TEST_TMP/run.log"
  local status=0
  bash "$SCRIPT" > "$out" 2>&1 || status=$?
  printf '%s' "$status" > "$TEST_TMP/status"
  RUN_OUT="$out"
  RUN_STATUS="$status"
}

# --- Cases -------------------------------------------------------------------

# The ordering defect. skipped:true means the check could not MEASURE, and
# stale is false in that case for want of an answer. Reading stale first turns
# an unmeasurable binary into a fresh one, and the safe_to_rebuild guard below
# is then unreachable in exactly the case it exists for.
#
# The fixture deliberately omits compare_ref_refreshed — that is an OLD gt
# answering, which is the only gt that can be installed when this plugin is
# what deploys the new one. It also isolates the guard under test: with the
# field present-and-false the unrefreshed-freshness guard would catch this case
# too, and neutering the ordering fix would still look like a pass.
test_skipped_is_read_before_stale() {
  setup_case
  write_stale_json <<'JSON'
{"stale":false,"safe_to_rebuild":false,"skipped":true,
 "skip_reason":"could not read origin/main from the remote",
 "compare_ref":"origin/main",
 "binary_commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo_commit":""}
JSON
  run_plugin

  assert_status "$RUN_STATUS" 0 "skipped: exits 0"
  assert_file_contains "$TEST_STATE/record.log" "--result skipped" \
    "skipped: recorded as skipped, not success"
  assert_file_contains "$TEST_STATE/record.log" "could not read origin/main" \
    "skipped: carries skip_reason into the wisp"
  assert_file_not_contains "$TEST_STATE/record.log" "binary is fresh" \
    "skipped: never claims the binary is fresh"
  assert_file_not_contains "$TEST_STATE/make.log" "build" \
    "skipped: does not build"
}

# An explicitly unrefreshed compare ref is not evidence of freshness.
test_unrefreshed_fresh_is_not_trusted() {
  setup_case
  write_stale_json <<'JSON'
{"stale":false,"safe_to_rebuild":false,"skipped":false,
 "compare_ref":"main","compare_ref_refreshed":false,
 "refresh_error":"remote unreachable",
 "binary_commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo_commit":"aaaaaaaa"}
JSON
  run_plugin

  assert_status "$RUN_STATUS" 0 "unrefreshed fresh: exits 0"
  assert_file_contains "$TEST_STATE/record.log" "--result skipped" \
    "unrefreshed fresh: recorded as skipped"
  assert_file_contains "$TEST_STATE/record.log" "freshness unproven" \
    "unrefreshed fresh: says why"
}

# The bootstrap case, and it is load-bearing: the gt answering `gt stale` is by
# definition the OLD binary, and an old gt emits no compare_ref_refreshed at
# all. Reading an absent field as false would make this fix unable to deploy
# itself — the plugin would skip forever waiting for a field only the
# undeployed binary can emit.
test_absent_refreshed_field_falls_back_to_legacy_reading() {
  setup_case
  write_stale_json <<'JSON'
{"stale":false,"safe_to_rebuild":false,"skipped":false,
 "binary_commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo_commit":"aaaaaaaa"}
JSON
  run_plugin

  assert_status "$RUN_STATUS" 0 "absent refreshed field: exits 0"
  assert_file_contains "$TEST_STATE/record.log" "--result success" \
    "absent refreshed field: trusts the old binary's fresh verdict"
}

test_not_safe_to_rebuild_skips() {
  setup_case
  write_stale_json <<'JSON'
{"stale":true,"safe_to_rebuild":false,"skipped":false,
 "compare_ref":"origin/main","compare_ref_refreshed":true,
 "binary_commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo_commit":"bbbbbbbb"}
JSON
  run_plugin

  assert_status "$RUN_STATUS" 0 "not safe: exits 0"
  assert_file_contains "$TEST_STATE/record.log" "not safe to rebuild" \
    "not safe: recorded as skipped"
  assert_file_not_contains "$TEST_STATE/make.log" "build" "not safe: does not build"
}

# The second defect. With detection fixed, a stale:true over a checkout nobody
# fast-forwarded compiles the commit the binary already had: the binary never
# advances, and the plugin reports success every cycle forever. Assert on WHICH
# COMMIT make was run at, not on whether make ran.
test_source_is_fast_forwarded_before_building() {
  setup_case
  local before landed
  before=$(git_q -C "$RIG" rev-parse HEAD)
  landed=$(land_on_remote)
  write_stale_json <<JSON
{"stale":true,"safe_to_rebuild":true,"skipped":false,
 "compare_ref":"origin/main","compare_ref_refreshed":true,
 "binary_commit":"$before","repo_commit":"$landed"}
JSON
  run_plugin

  assert_status "$RUN_STATUS" 0 "fast-forward: exits 0"
  assert_file_contains "$TEST_STATE/make.log" "build@$landed" \
    "fast-forward: built the commit that landed on the remote"
  assert_file_not_contains "$TEST_STATE/make.log" "build@$before" \
    "fast-forward: did not recompile the commit the binary already had"

  local after
  after=$(git_q -C "$RIG" rev-parse HEAD)
  if [ "$after" = "$landed" ]; then
    record_pass "fast-forward: rig checkout advanced to the remote tip"
  else
    record_fail "fast-forward: rig checkout advanced to the remote tip"
    printf '  HEAD is %s, want %s\n' "$after" "$landed"
  fi

  # The private ref must not outlive the run: refs/gt/ is shared with every
  # other worktree on this gitdir.
  if [ -z "$(git_q -C "$RIG" for-each-ref --format='%(refname)' 'refs/gt/rebuild-gt/')" ]; then
    record_pass "fast-forward: private fetch ref cleaned up"
  else
    record_fail "fast-forward: private fetch ref cleaned up"
    git_q -C "$RIG" for-each-ref --format='  %(refname)' 'refs/gt/rebuild-gt/'
  fi
}

# A diverged checkout must be left alone. --ff-only is the safety property; a
# merge here would put unreviewed local commits into the binary the whole town
# runs.
test_diverged_checkout_is_not_merged() {
  setup_case
  local landed
  landed=$(land_on_remote)
  printf 'local divergence\n' > "$RIG/local.txt"
  git_q -C "$RIG" add -A
  git_q -C "$RIG" commit -q --no-gpg-sign -m "local only"
  local diverged
  diverged=$(git_q -C "$RIG" rev-parse HEAD)

  write_stale_json <<JSON
{"stale":true,"safe_to_rebuild":true,"skipped":false,
 "compare_ref":"origin/main","compare_ref_refreshed":true,
 "binary_commit":"$diverged","repo_commit":"$landed"}
JSON
  run_plugin

  assert_status "$RUN_STATUS" 0 "diverged: exits 0"
  assert_file_contains "$TEST_STATE/record.log" "cannot fast-forward" \
    "diverged: recorded as skipped with the reason"
  assert_file_not_contains "$TEST_STATE/make.log" "build@" "diverged: does not build"
  if [ "$(git_q -C "$RIG" rev-parse HEAD)" = "$diverged" ]; then
    record_pass "diverged: checkout left untouched"
  else
    record_fail "diverged: checkout left untouched"
  fi
}

# A rebuild that runs and does not advance the binary is the treadmill. It must
# escalate, not record success — otherwise the wisp log reads "rebuilt hourly"
# while the town runs an old binary.
test_rebuild_that_does_not_take_escalates() {
  setup_case
  local before landed
  before=$(git_q -C "$RIG" rev-parse HEAD)
  landed=$(land_on_remote)
  printf '0\n' > "$TEST_STATE/stale_quiet_exit"   # still stale after the build
  write_stale_json <<JSON
{"stale":true,"safe_to_rebuild":true,"skipped":false,
 "compare_ref":"origin/main","compare_ref_refreshed":true,
 "binary_commit":"$before","repo_commit":"$landed"}
JSON
  run_plugin

  assert_status "$RUN_STATUS" 1 "unadvanced rebuild: exits non-zero"
  assert_file_contains "$TEST_STATE/record.log" "--result failure" \
    "unadvanced rebuild: recorded as failure"
  assert_file_contains "$TEST_STATE/escalate.log" "rebuild-gt" \
    "unadvanced rebuild: escalates"
  assert_file_not_contains "$TEST_STATE/record.log" "--result success" \
    "unadvanced rebuild: never records success"
}

test_successful_rebuild_verifies_and_records_success() {
  setup_case
  local before landed
  before=$(git_q -C "$RIG" rev-parse HEAD)
  landed=$(land_on_remote)
  printf '1\n' > "$TEST_STATE/stale_quiet_exit"   # fresh after the build
  write_stale_json <<JSON
{"stale":true,"safe_to_rebuild":true,"skipped":false,
 "compare_ref":"origin/main","compare_ref_refreshed":true,
 "binary_commit":"$before","repo_commit":"$landed"}
JSON
  run_plugin

  assert_status "$RUN_STATUS" 0 "successful rebuild: exits 0"
  assert_file_contains "$TEST_STATE/record.log" "--result success" \
    "successful rebuild: recorded as success"
  assert_file_contains "$TEST_STATE/stale_calls.log" "stale --quiet" \
    "successful rebuild: re-ran gt stale against the new binary"
  assert_file_contains "$TEST_STATE/make.log" "safe-install@$landed" \
    "successful rebuild: safe-install, not install"
}

test_dirty_checkout_skips() {
  setup_case
  local before landed
  before=$(git_q -C "$RIG" rev-parse HEAD)
  landed=$(land_on_remote)
  printf 'uncommitted\n' > "$RIG/dirty.txt"
  git_q -C "$RIG" add -A
  write_stale_json <<JSON
{"stale":true,"safe_to_rebuild":true,"skipped":false,
 "compare_ref":"origin/main","compare_ref_refreshed":true,
 "binary_commit":"$before","repo_commit":"$landed"}
JSON
  run_plugin

  assert_status "$RUN_STATUS" 0 "dirty: exits 0"
  assert_file_contains "$TEST_STATE/record.log" "uncommitted changes" "dirty: recorded as skipped"
  assert_file_not_contains "$TEST_STATE/make.log" "build@" "dirty: does not build"
}

test_stale_json_failure_records_a_skip() {
  setup_case
  touch "$TEST_STATE/stale_json_fail"
  run_plugin

  assert_status "$RUN_STATUS" 0 "gt stale fails: exits 0"
  assert_file_contains "$TEST_STATE/record.log" "--result skipped" \
    "gt stale fails: leaves a trace instead of exiting silently"
}

if ! command -v git >/dev/null 2>&1; then
  printf 'SKIP: git not available\n'
  exit 0
fi
if ! command -v python3 >/dev/null 2>&1; then
  printf 'SKIP: python3 not available\n'
  exit 0
fi

test_skipped_is_read_before_stale
test_unrefreshed_fresh_is_not_trusted
test_absent_refreshed_field_falls_back_to_legacy_reading
test_not_safe_to_rebuild_skips
test_source_is_fast_forwarded_before_building
test_diverged_checkout_is_not_merged
test_rebuild_that_does_not_take_escalates
test_successful_rebuild_verifies_and_records_success
test_dirty_checkout_skips
test_stale_json_failure_records_a_skip

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

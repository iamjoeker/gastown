#!/usr/bin/env bash
# town_root_contract_test.sh — every plugin that resolves the town root by
# shelling out to `gt town root` must fail loudly when that resolution does
# not produce a real directory.
#
# gt-cr2: `gt town root` was not a subcommand. Cobra answered it by printing
# `gt town`'s help to STDOUT and exiting 0, so
#
#   TOWN_ROOT="${GT_TOWN_ROOT:-$(gt town root 2>/dev/null)}"
#
# assigned several lines of help text to TOWN_ROOT, and every derived path
# ("$TOWN_ROOT/daemon", "$TOWN_ROOT/gastown/mayor/rig") was nonsense. Nothing
# crashed; the plugins just quietly did nothing forever.
#
# The command now exists, but plugins run against whatever gt binary is
# installed — which can predate the fix. So the guard is tested against both
# failure shapes: a non-zero `gt town root`, and one that prints help and
# exits 0.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ORIGINAL_PATH="$PATH"
PASS=0
FAIL=0
CLEANUP_DIRS=()

# Plugins whose town root resolution is covered by this contract.
PLUGINS=(rebuild-gt dolt-log-rotate stuck-agent-dog)

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

# write_stub_gt installs a `gt` that answers `town root` per MODE and refuses
# every other call, so a plugin that gets past the guard is caught here rather
# than reaching the real gt.
write_stub_gt() {
  local bin_dir="$1"

  cat > "$bin_dir/gt" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" = "town" ] && [ "${2:-}" = "root" ]; then
  case "${STUB_TOWN_ROOT_MODE:-ok}" in
    fail)
      printf 'Error: not in a Gas Town workspace\n' >&2
      exit 1
      ;;
    help)
      # Pre-gt-cr2 gt: unknown subcommand -> help on STDOUT, exit 0.
      printf 'Commands for town-level operations including session cycling.\n'
      printf '\nUsage:\n  gt town [command]\n'
      printf '\nAvailable Commands:\n  next        Switch to next town session\n'
      exit 0
      ;;
    *)
      printf '%s\n' "$STUB_TOWN_ROOT_VALUE"
      exit 0
      ;;
  esac
fi

printf 'stub gt: unexpected call past the town root guard: %s\n' "$*" >&2
exit 91
SH
  chmod +x "$bin_dir/gt"
}

setup_case() {
  TEST_TMP=$(mktemp -d)
  CLEANUP_DIRS+=("$TEST_TMP")
  export STUB_TOWN_ROOT_VALUE="$TEST_TMP/town"
  mkdir -p "$STUB_TOWN_ROOT_VALUE" "$TEST_TMP/bin"
  write_stub_gt "$TEST_TMP/bin"
  export PATH="$TEST_TMP/bin:$ORIGINAL_PATH"
  unset GT_TOWN_ROOT
}

# assert_fails_loudly runs one plugin with the stub in the given mode and
# requires a non-zero exit plus a FATAL message naming the town root.
assert_fails_loudly() {
  local plugin="$1" mode="$2" label="$3"
  local script="$ROOT_DIR/plugins/$plugin/run.sh"
  local out="$TEST_TMP/$plugin.$mode.log"
  local status=0

  export STUB_TOWN_ROOT_MODE="$mode"
  bash "$script" > "$out" 2>&1 || status=$?

  if [ "$status" -eq 0 ]; then
    record_fail "$label: exits non-zero"
    printf '  %s exited 0; output:\n' "$plugin"
    sed 's/^/    /' "$out" 2>/dev/null || true
    return
  fi
  if [ "$status" -eq 91 ]; then
    record_fail "$label: exits non-zero"
    printf '  %s ran past the town root guard and called gt again\n' "$plugin"
    sed 's/^/    /' "$out" 2>/dev/null || true
    return
  fi
  record_pass "$label: exits non-zero"

  if grep -q 'FATAL.*town root' "$out"; then
    record_pass "$label: reports FATAL town root"
  else
    record_fail "$label: reports FATAL town root"
    sed 's/^/    /' "$out" 2>/dev/null || true
  fi
}

test_unresolvable_town_root_is_fatal() {
  local plugin
  for plugin in "${PLUGINS[@]}"; do
    setup_case
    assert_fails_loudly "$plugin" fail "$plugin / gt town root fails"
  done
}

test_help_text_town_root_is_fatal() {
  local plugin
  for plugin in "${PLUGINS[@]}"; do
    setup_case
    assert_fails_loudly "$plugin" help "$plugin / gt town root prints help"
  done
}

# The guard must not fire on the healthy path. dolt-log-rotate is the safe
# probe: with a resolvable root and no log file it exits 0 having done
# nothing. (rebuild-gt and stuck-agent-dog are exercised on their happy paths
# by their own tests; both need far more of gt stubbed out.)
test_resolvable_town_root_proceeds() {
  setup_case
  export STUB_TOWN_ROOT_MODE=ok
  local out="$TEST_TMP/dolt-log-rotate.ok.log"
  local status=0

  bash "$ROOT_DIR/plugins/dolt-log-rotate/run.sh" > "$out" 2>&1 || status=$?

  if [ "$status" -eq 0 ]; then
    record_pass "dolt-log-rotate / resolvable root: exits 0"
  else
    record_fail "dolt-log-rotate / resolvable root: exits 0"
    printf '  exit %s; output:\n' "$status"
    sed 's/^/    /' "$out" 2>/dev/null || true
  fi

  if grep -Fq "$STUB_TOWN_ROOT_VALUE/daemon/dolt.log" "$out"; then
    record_pass "dolt-log-rotate / resolvable root: derived path from the real root"
  else
    record_fail "dolt-log-rotate / resolvable root: derived path from the real root"
    sed 's/^/    /' "$out" 2>/dev/null || true
  fi
}

# GT_TOWN_ROOT still wins, and `gt town root` is not consulted when it is set.
test_env_town_root_wins() {
  setup_case
  export GT_TOWN_ROOT="$STUB_TOWN_ROOT_VALUE"
  export STUB_TOWN_ROOT_MODE=fail
  local out="$TEST_TMP/dolt-log-rotate.env.log"
  local status=0

  bash "$ROOT_DIR/plugins/dolt-log-rotate/run.sh" > "$out" 2>&1 || status=$?

  if [ "$status" -eq 0 ]; then
    record_pass "dolt-log-rotate / GT_TOWN_ROOT set: gt town root not consulted"
  else
    record_fail "dolt-log-rotate / GT_TOWN_ROOT set: gt town root not consulted"
    printf '  exit %s; output:\n' "$status"
    sed 's/^/    /' "$out" 2>/dev/null || true
  fi
}

# An empty or whitespace-only GT_TOWN_ROOT must not slip through as a path.
test_blank_env_town_root_falls_back() {
  setup_case
  export GT_TOWN_ROOT=""
  export STUB_TOWN_ROOT_MODE=fail
  local out="$TEST_TMP/dolt-log-rotate.blank.log"
  local status=0

  bash "$ROOT_DIR/plugins/dolt-log-rotate/run.sh" > "$out" 2>&1 || status=$?

  if [ "$status" -ne 0 ] && grep -q 'FATAL.*town root' "$out"; then
    record_pass "dolt-log-rotate / empty GT_TOWN_ROOT: falls back and fails loudly"
  else
    record_fail "dolt-log-rotate / empty GT_TOWN_ROOT: falls back and fails loudly"
    printf '  exit %s; output:\n' "$status"
    sed 's/^/    /' "$out" 2>/dev/null || true
  fi
}

# No plugin may reintroduce the swallow-and-continue idiom. Comment lines are
# exempt — the plugins document the old idiom in the comment above the guard.
test_no_plugin_swallows_town_root_errors() {
  local offenders
  offenders=$(grep -rn 'gt town root 2>/dev/null' "$ROOT_DIR"/plugins/*/run.sh 2>/dev/null \
    | grep -v ':[[:space:]]*#' || true)
  if [ -z "$offenders" ]; then
    record_pass "no plugin discards gt town root's stderr"
  else
    record_fail "no plugin discards gt town root's stderr"
    printf '%s\n' "$offenders" | sed 's/^/    /'
  fi
}

test_unresolvable_town_root_is_fatal
test_help_text_town_root_is_fatal
test_resolvable_town_root_proceeds
test_env_town_root_wins
test_blank_env_town_root_falls_back
test_no_plugin_swallows_town_root_errors

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

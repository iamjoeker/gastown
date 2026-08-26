#!/usr/bin/env bash
# Tests for the Makefile sync-plugins target and its two callers.
#
# What regressed (gt-vrf5): plugins execute from <townRoot>/plugins/, so the
# copy step in the Makefile is the whole deploy mechanism for a plugin fix.
# It hung off `install` only. `install` is the path a human takes; the only
# path that runs unattended is rebuild-gt's hourly `safe-install`, which had no
# sync at all. Result: the town executed a rebuild-gt from 2026-08-02 for three
# weeks while its fixes sat merged on main.
#
# So the load-bearing assertion here is not "sync-plugins works" — it is "BOTH
# install paths reach it". That is checked against the real dependency graph
# with `make -n`, which expands prerequisites and recurses into $(MAKE) lines
# without running anything.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MAKE_BIN="$(command -v "${MAKE:-make}")"
TMPDIR=""
PASS=0
FAIL=0

cleanup() {
  if [[ -n "$TMPDIR" && -d "$TMPDIR" ]]; then
    rm -rf "$TMPDIR"
  fi
}
trap cleanup EXIT

# setup_stub_gt writes a fake gt into a throwaway INSTALL_DIR. It records its
# own argv so the test can assert what the recipe actually invoked, rather than
# asserting on the recipe text.
setup_stub_gt() {
  local exit_code="$1" stdout_msg="$2" stderr_msg="$3"
  TMPDIR="$(mktemp -d)"
  INSTALL_DIR="$TMPDIR/bin"
  ARGV_LOG="$TMPDIR/argv.log"
  mkdir -p "$INSTALL_DIR"
  cat > "$INSTALL_DIR/gt" <<EOF
#!/usr/bin/env sh
echo "\$@" >> "$ARGV_LOG"
[ -n "$stdout_msg" ] && echo "$stdout_msg"
[ -n "$stderr_msg" ] && echo "$stderr_msg" >&2
exit $exit_code
EOF
  chmod +x "$INSTALL_DIR/gt"
}

# run_sync invokes the real target. stdout and stderr are captured separately:
# the whole point of the change is that a failure reason reaches stderr instead
# of /dev/null, and merging the streams would not be able to tell.
run_sync() {
  set +e
  "$MAKE_BIN" --no-print-directory -C "$REPO_ROOT" sync-plugins \
    INSTALL_DIR="$INSTALL_DIR" >"$TMPDIR/out" 2>"$TMPDIR/err"
  RC=$?
  set -e
  OUT="$(cat "$TMPDIR/out")"
  ERR="$(cat "$TMPDIR/err")"
}

# dry_run_target expands a target's recipe without executing it. -n still
# recurses into $(MAKE) lines, which is how the sub-make call is observed.
dry_run_target() {
  local target="$1"
  "$MAKE_BIN" -n --no-print-directory -C "$REPO_ROOT" "$target" \
    SKIP_UPDATE_CHECK=1 INSTALL_DIR="$TMPDIR/unused-bin" 2>&1
}

ok()   { echo "  PASS: $1"; PASS=$((PASS + 1)); }
bad()  { echo "  FAIL: $1"; shift; printf '%s\n' "$@"; FAIL=$((FAIL + 1)); }

assert_contains() {
  local name="$1" haystack="$2" needle="$3"
  if [[ "$haystack" == *"$needle"* ]]; then
    ok "$name"
  else
    bad "$name" "  expected to contain: $needle" "  got: $haystack"
  fi
}

echo "=== sync-plugins tests ==="

# --- The regression itself: both install paths must reach the sync ----------
#
# `install` had it and `safe-install` did not, and no test compared them. These
# two cases are the ones that fail if the hook is ever dropped from either.

TMPDIR="$(mktemp -d)"
for target in install safe-install; do
  recipe="$(dry_run_target "$target")"
  assert_contains "$target reaches sync-plugins" "$recipe" "plugin sync --source $REPO_ROOT/plugins"
done
cleanup

# --- Success path -----------------------------------------------------------

setup_stub_gt 0 "Plugins already up to date (14 checked)" ""
run_sync
if [[ "$RC" -eq 0 ]]; then ok "succeeds when gt plugin sync succeeds"; else bad "succeeds when gt plugin sync succeeds" "  rc=$RC"; fi
assert_contains "invokes plugin sync with an explicit source" "$(cat "$ARGV_LOG")" "plugin sync --source $REPO_ROOT/plugins"
assert_contains "relays the sync's own report" "$OUT" "Plugins already up to date"
cleanup

# --- Failure path -----------------------------------------------------------
#
# Two separate properties, and they pull in opposite directions: the reason has
# to be visible (it used to go to /dev/null), but the target must not fail (the
# binary is already installed by then, so a non-zero exit would report the
# install as broken). Both are asserted, so neither can be "fixed" by dropping
# the other.

setup_stub_gt 1 "" "Error: not in a Gas Town workspace"
run_sync
if [[ "$RC" -eq 0 ]]; then ok "a failed sync does not fail the install"; else bad "a failed sync does not fail the install" "  rc=$RC"; fi
assert_contains "failure is reported at all" "$ERR" "Warning: plugin sync failed"
assert_contains "the REASON survives, not just the fact" "$ERR" "not in a Gas Town workspace"
assert_contains "names the command to retry" "$ERR" "Retry with: $INSTALL_DIR/gt plugin sync"
if [[ -z "$OUT" ]]; then ok "nothing reassuring is printed on stdout"; else bad "nothing reassuring is printed on stdout" "  got: $OUT"; fi
cleanup

# --- Missing binary ---------------------------------------------------------
#
# An absent gt is the case where a silent `|| true` is most misleading: no
# sync can possibly have happened, and the old recipe printed nothing at all.

TMPDIR="$(mktemp -d)"
INSTALL_DIR="$TMPDIR/empty-bin"
mkdir -p "$INSTALL_DIR"
run_sync
if [[ "$RC" -eq 0 ]]; then ok "missing gt does not fail the install"; else bad "missing gt does not fail the install" "  rc=$RC"; fi
assert_contains "missing gt is reported, not silent" "$ERR" "is not executable; plugins NOT synced"
cleanup

echo "Results: $PASS passed, $FAIL failed"
[[ "$FAIL" -eq 0 ]] && exit 0 || exit 1

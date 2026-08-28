#!/usr/bin/env bash
# git-hygiene/run_test.sh — the branch-landed predicate, exercised.
#
# This is the only step in the plugin that can destroy work nobody kept a copy
# of, and it has two opposite failure modes that a single test cannot cover:
#
#   too strict  -> rebase-landed branches are never collected and accumulate on
#                  the remote forever (measured on gastown at 17 of 41)
#   too loose   -> branches holding real unlanded work are deleted
#
# So every case below is paired: a branch that MUST go and a branch that MUST
# stay, plus the undetermined readings that must fall to the safe side. A suite
# that only asserts the deletions would be satisfied by "delete everything".
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT_DIR/plugins/git-hygiene/run.sh"
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

assert_landed() {
  local ref="$1" want_evidence="$2" label="$3"
  if branch_is_landed "$REPO" "$ref" "$TARGET"; then
    if [ "$LANDED_EVIDENCE" = "$want_evidence" ]; then
      record_pass "$label"
    else
      record_fail "$label"
      printf '  evidence = %q, want %q\n' "$LANDED_EVIDENCE" "$want_evidence"
    fi
  else
    record_fail "$label"
    printf '  branch_is_landed %s returned KEEP; wanted DELETE (%s)\n' "$ref" "$want_evidence"
  fi
}

assert_kept() {
  local ref="$1" label="$2"
  if branch_is_landed "$REPO" "$ref" "$TARGET"; then
    record_fail "$label"
    printf '  branch_is_landed %s returned DELETE (%s); wanted KEEP\n' "$ref" "$LANDED_EVIDENCE"
  else
    record_pass "$label"
  fi
}

# The predicate is EXTRACTED from run.sh rather than copied, so this suite
# cannot drift into testing a stale duplicate of the thing it guards.
load_predicate() {
  local extracted
  extracted=$(sed -n '/^LANDED_EVIDENCE=""$/,/^}$/p' "$SCRIPT")
  if ! printf '%s' "$extracted" | grep -q '^branch_is_landed() {'; then
    printf 'FAIL: could not extract branch_is_landed from %s\n' "$SCRIPT"
    exit 1
  fi
  # run.sh logs through log(); the predicate's KEEP paths say why.
  log() { printf '%s\n' "$*" >>"$LOG"; }
  eval "$extracted"
}

git_q() { git -C "$REPO" "$@" >/dev/null 2>&1; }

commit_file() {
  local name="$1" content="$2"
  printf '%s\n' "$content" >"$REPO/$name"
  git_q add "$name"
  git_q commit -m "$name"
}

# build_fixture makes one repo carrying every case at once, so the predicate is
# asked the same question about branches that differ only in how they landed.
build_fixture() {
  REPO=$(mktemp -d)
  CLEANUP_DIRS+=("$REPO")
  LOG="$REPO/.predicate.log"
  : >"$LOG"

  git_q init -b main
  git_q config user.email "test@test.com"
  git_q config user.name "Test User"
  commit_file README.md "root"
  TARGET=main

  # ancestor: merged the ordinary way, contained by sha.
  git_q checkout -b landed/ancestor
  commit_file ancestor.txt "ancestor work"
  git_q checkout main
  git_q merge --no-ff -m "merge ancestor" landed/ancestor

  # rebase-landed: same patch on main under a different sha, never an ancestor.
  git_q checkout -b landed/rebased main
  commit_file rebased.txt "rebased work"
  local rebased_tip
  rebased_tip=$(git -C "$REPO" rev-parse landed/rebased)
  git_q checkout main
  commit_file interleaved.txt "someone else landed first"
  git_q cherry-pick "$rebased_tip"

  # unlanded: real work that exists nowhere else. This is the branch the
  # too-loose predicate destroys.
  git_q checkout -b unlanded/real main
  commit_file unlanded.txt "work that has landed nowhere"

  # partial: one commit landed, one did not. A branch is not landed because
  # SOME of it is.
  git_q checkout -b partial/half main
  commit_file partial-landed.txt "this half lands"
  local half_tip
  half_tip=$(git -C "$REPO" rev-parse partial/half)
  commit_file partial-open.txt "this half does not"
  git_q checkout main
  git_q cherry-pick "$half_tip"

  git_q checkout main
}

test_ancestor_is_collected() {
  assert_landed landed/ancestor "ancestor" \
    "an ancestor is collected, by sha"
}

test_rebase_landed_is_collected() {
  # The fixture is only a fixture if ancestry genuinely fails on it: if this
  # branch were an ancestor, the old ancestry-only predicate would pass too.
  if git -C "$REPO" merge-base --is-ancestor landed/rebased "$TARGET" 2>/dev/null; then
    record_fail "rebase-landed fixture is not a rebase landing"
    return
  fi
  assert_landed landed/rebased "patch-identical, rebase-landed" \
    "a rebase-landed branch is collected, by patch identity"
}

test_unlanded_work_is_kept() {
  assert_kept unlanded/real \
    "a branch holding unlanded work is kept"
}

test_partially_landed_work_is_kept() {
  assert_kept partial/half \
    "a branch with one landed and one unlanded commit is kept"
}

test_unreadable_comparison_is_kept() {
  assert_kept "refs/heads/no/such/branch" \
    "a branch git cannot compare is kept, not deleted"
  if grep -q "KEEPING refs/heads/no/such/branch" "$LOG"; then
    record_pass "an unreadable comparison says so instead of passing silently"
  else
    record_fail "an unreadable comparison says so instead of passing silently"
    sed 's/^/    /' "$LOG"
  fi
}

if ! command -v git >/dev/null 2>&1; then
  printf 'SKIP: git not available\n'
  exit 0
fi

build_fixture
load_predicate

test_ancestor_is_collected
test_rebase_landed_is_collected
test_unlanded_work_is_kept
test_partially_landed_work_is_kept
test_unreadable_comparison_is_kept

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

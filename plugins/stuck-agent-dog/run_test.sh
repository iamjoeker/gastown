#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT_DIR/plugins/stuck-agent-dog/run.sh"
ORIGINAL_PATH="$PATH"
PASS=0
FAIL=0
CLEANUP_DIRS=()

cleanup() {
  for dir in "${CLEANUP_DIRS[@]}"; do
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

assert_file_empty() {
  local file="$1"
  local label="$2"
  if [ ! -s "$file" ]; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  unexpected contents of %s:\n' "$file"
    sed 's/^/    /' "$file"
  fi
}

assert_file_contains() {
  local file="$1"
  local needle="$2"
  local label="$3"
  if grep -Fq -- "$needle" "$file"; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  expected %q in %s\n' "$needle" "$file"
    sed 's/^/    /' "$file" 2>/dev/null || true
  fi
}

assert_file_not_contains() {
  local file="$1"
  local needle="$2"
  local label="$3"
  if ! grep -Fq -- "$needle" "$file" 2>/dev/null; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  did not expect %q in %s\n' "$needle" "$file"
    sed 's/^/    /' "$file" 2>/dev/null || true
  fi
}

assert_line_count() {
  local file="$1"
  local expected="$2"
  local label="$3"
  local actual=0

  if [ -f "$file" ]; then
    actual=$(wc -l < "$file" | tr -d ' ')
  fi
  if [ "$actual" = "$expected" ]; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  expected %s lines in %s, got %s\n' "$expected" "$file" "$actual"
    sed 's/^/    /' "$file" 2>/dev/null || true
  fi
}

write_fake_commands() {
  local bin_dir="$1"

  cat > "$bin_dir/gt" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  town)
    if [ "${2:-}" = "root" ]; then
      if [ -f "$TEST_STATE/town_root_fail" ]; then
        printf 'Error: not in a Gas Town workspace\n' >&2
        exit 1
      fi
      if [ -f "$TEST_STATE/town_root_help" ]; then
        # A gt binary predating gt-cr2, where `root` was not a subcommand:
        # Cobra printed `gt town`'s help to STDOUT and exited 0.
        printf 'Commands for town-level operations including session cycling.\n'
        printf '\nUsage:\n  gt town [command]\n'
        exit 0
      fi
      cat "$TEST_STATE/town_root_value"
      exit 0
    fi
    ;;
  hook)
    if [ "${2:-}" = "show" ]; then
      target="${3:-}"
      name="${target##*/}"
      printf '%s|%s\n' "$PWD" "$*" >> "$TEST_STATE/hook_calls.log"
      if [ "${4:-}" != "--json" ]; then
        exit 1
      fi
      if [ -f "$TEST_STATE/hook_fail/$name" ]; then
        exit 1
      fi
      if [ -f "$TEST_STATE/nohook/$name" ]; then
        printf '{"agent":"%s","status":"empty"}\n' "$target"
      else
        status="hooked"
        if [ -f "$TEST_STATE/hook_status/$name" ]; then
          status=$(sed -n '1p' "$TEST_STATE/hook_status/$name" | tr -d '\n')
          if [ "$(wc -l < "$TEST_STATE/hook_status/$name" | tr -d ' ')" -gt 1 ]; then
            sed '1d' "$TEST_STATE/hook_status/$name" > "$TEST_STATE/hook_status/$name.tmp"
            mv "$TEST_STATE/hook_status/$name.tmp" "$TEST_STATE/hook_status/$name"
          fi
        fi
        printf '{"agent":"%s","bead_id":"gt-hook-%s","status":"%s"}\n' "$target" "$name" "$status"
      fi
      exit 0
    fi
    ;;
  polecat)
    if [ "${2:-}" = "list" ]; then
      if [ -f "$TEST_STATE/polecat_list_fail" ]; then
        exit 1
      fi
      if [ -f "$TEST_STATE/polecat_list_raw" ]; then
        cat "$TEST_STATE/polecat_list_raw"
        exit 0
      fi
      printf '['
      sep=""
      while IFS='|' read -r rig name ctc; do
        [ -n "$rig" ] || continue
        printf '%s{"rig":"%s","name":"%s","state":"done","counts_toward_capacity":%s}' "$sep" "$rig" "$name" "$ctc"
        sep=","
      done < "$TEST_STATE/capacity.list"
      printf ']\n'
      exit 0
    fi
    if [ "${2:-}" = "check-recovery" ]; then
      target="${3:-}"
      name="${target##*/}"
      printf '%s\n' "$target" >> "$TEST_STATE/check_recovery_calls.log"
      if [ -f "$TEST_STATE/check_recovery_fail/$name" ]; then
        exit 1
      fi
      needs_recovery=false
      if [ -f "$TEST_STATE/needs_recovery/$name" ]; then
        needs_recovery=true
      fi
      printf '{"needs_recovery":%s}\n' "$needs_recovery"
      exit 0
    fi
    ;;
  mq)
    if [ "${2:-}" = "list" ]; then
      rig="${3:-}"
      printf '%s\n' "$*" >> "$TEST_STATE/mq_calls.log"
      if [ -f "$TEST_STATE/mq_list_fail" ]; then
        exit 1
      fi
      if [ -f "$TEST_STATE/mq_list_raw" ]; then
        cat "$TEST_STATE/mq_list_raw"
        exit 0
      fi
      printf '['
      sep=""
      while IFS='|' read -r mr_rig source_issue mr_status; do
        [ -n "$mr_rig" ] || continue
        [ "$mr_rig" = "$rig" ] || continue
        printf '%s{"id":"%s-mr-%s","title":"Merge: %s","status":"%s","description":"branch: polecat/x/%s@abc\\ntarget: main\\nsource_issue: %s\\nrig: %s\\n"}' \
          "$sep" "$mr_rig" "$source_issue" "$source_issue" "$mr_status" "$source_issue" "$source_issue" "$mr_rig"
        sep=","
      done < "$TEST_STATE/mr.list"
      printf ']\n'
      exit 0
    fi
    ;;
  rig)
    if [ "${2:-}" = "list" ] && [ "${3:-}" = "--json" ]; then
      if [ -f "$TEST_STATE/rig_list_fail" ]; then
        exit 1
      fi
      if [ -f "$TEST_STATE/rig_list.json" ]; then
        cat "$TEST_STATE/rig_list.json"
      else
        printf '[{"name":"gastown","beads_prefix":"gt","status":"operational"}]\n'
      fi
      exit 0
    fi
    ;;
  session)
    if [ "${2:-}" = "health" ]; then
      session="${3:-}"
      shift 3
      max_inactivity="0s"
      while [ "$#" -gt 0 ]; do
        case "$1" in
          --max-inactivity)
            max_inactivity="${2:-}"
            shift 2
            ;;
          *)
            shift
            ;;
        esac
      done

      status="healthy"
      if [ -f "$TEST_STATE/health/$session" ]; then
        status=$(sed -n '1p' "$TEST_STATE/health/$session" | tr -d '\n')
        if [ "$(wc -l < "$TEST_STATE/health/$session" | tr -d ' ')" -gt 1 ]; then
          sed '1d' "$TEST_STATE/health/$session" > "$TEST_STATE/health/$session.tmp"
          mv "$TEST_STATE/health/$session.tmp" "$TEST_STATE/health/$session"
        fi
      fi
      printf '%s --max-inactivity %s\n' "$session" "$max_inactivity" >> "$TEST_STATE/health_calls.log"
      healthy=false
      zombie=false
      case "$status" in
        healthy) healthy=true ;;
        agent-dead|agent-hung) zombie=true ;;
      esac
      printf '{"session":"%s","status":"%s","healthy":%s,"zombie":%s,"max_inactivity_seconds":0}\n' "$session" "$status" "$healthy" "$zombie"
      exit 0
    fi
    ;;
  mail)
    if [ "${2:-}" = "send" ]; then
      printf '%s\n' "$*" >> "$TEST_STATE/mail.log"
      while IFS= read -r _line; do :; done
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

  cat > "$bin_dir/tmux" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

arg_after_t() {
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "-t" ]; then
      printf '%s\n' "${2:-}"
      return 0
    fi
    shift
  done
  return 1
}

case "${1:-}" in
  has-session)
    session=$(arg_after_t "$@" || true)
    [ -n "$session" ] && [ -f "$TEST_STATE/sessions/$session" ]
    ;;
  kill-session)
    session=$(arg_after_t "$@" || true)
    printf '%s\n' "$session" >> "$TEST_STATE/kill.log"
    ;;
  list-panes)
    printf '999\n'
    ;;
  display-message)
    date +%s
    ;;
  capture-pane)
    printf 'active opencode research in progress\n'
    ;;
  *)
    printf 'unexpected tmux call: %s\n' "$*" >&2
    exit 1
    ;;
esac
SH
  chmod +x "$bin_dir/tmux"

  cat > "$bin_dir/bd" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

case "${1:-}" in
  show)
    bead="${2:-}"
    status="open"
    if [ -f "$TEST_STATE/status/$bead" ]; then
      status=$(tr -d '\n' < "$TEST_STATE/status/$bead")
    fi
    printf '[{"status":"%s"}]\n' "$status"
    ;;
  list)
    printf '[]\n'
    ;;
  create)
    printf '%s\n' "$*" >> "$TEST_STATE/bd.log"
    ;;
  *)
    printf 'unexpected bd call: %s\n' "$*" >&2
    exit 1
    ;;
esac
SH
  chmod +x "$bin_dir/bd"

  cat > "$bin_dir/ps" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

if [ "${1:-}" = "-o" ] && [ "${2:-}" = "comm=" ]; then
  printf 'bash\n'
  exit 0
fi

printf 'unexpected ps call: %s\n' "$*" >&2
exit 1
SH
  chmod +x "$bin_dir/ps"
}

setup_case() {
  TEST_TMP=$(mktemp -d)
  CLEANUP_DIRS+=("$TEST_TMP")
  export TEST_STATE="$TEST_TMP/state"
  export GT_TOWN_ROOT="$TEST_TMP/town"
  export GT_STUCK_AGENT_DOG_STATE_DIR="$TEST_TMP/dogstate"
  local bin_dir="$TEST_TMP/bin"

  mkdir -p "$TEST_STATE/health" "$TEST_STATE/hook_fail" "$TEST_STATE/hook_status" "$TEST_STATE/nohook" "$TEST_STATE/sessions" "$TEST_STATE/status" "$TEST_STATE/needs_recovery" "$TEST_STATE/check_recovery_fail" "$bin_dir"
  mkdir -p "$GT_TOWN_ROOT/gastown/polecats" "$GT_TOWN_ROOT/deacon"
  printf '{"rigs":{"gastown":{"beads":{"prefix":"gt"}}}}\n' > "$GT_TOWN_ROOT/rigs.json"
  : > "$TEST_STATE/mail.log"
  : > "$TEST_STATE/kill.log"
  : > "$TEST_STATE/escalate.log"
  : > "$TEST_STATE/health_calls.log"
  : > "$TEST_STATE/hook_calls.log"
  : > "$TEST_STATE/bd.log"
  : > "$TEST_STATE/capacity.list"
  : > "$TEST_STATE/mr.list"
  : > "$TEST_STATE/mq_calls.log"
  printf '%s\n' "$GT_TOWN_ROOT" > "$TEST_STATE/town_root_value"
  touch "$TEST_STATE/sessions/hq-deacon"

  write_fake_commands "$bin_dir"
  export PATH="$bin_dir:$ORIGINAL_PATH"
  export GT_STUCK_AGENT_DOG_MAX_INACTIVITY=0s
  unset GT_STUCK_AGENT_DOG_MASS_DEATH_THRESHOLD
  unset GT_STUCK_AGENT_DOG_CRASH_PERSIST_SECONDS
  unset GT_STUCK_AGENT_DOG_MASS_DEATH_FRACTION_PCT
}

# add_mr puts a merge request in the queue for a source issue. status defaults
# to open; "closed" is what a MERGED MR looks like, which is the fury case.
add_mr() {
  local rig="$1" source_issue="$2" status="${3:-open}"

  printf '%s|%s|%s\n' "$rig" "$source_issue" "$status" >> "$TEST_STATE/mr.list"
}

# seed_crash_candidate fakes a PRIOR observation of a crash candidate, aged by
# `age` seconds. Without one, a candidate is on its first observation and the
# persistence gate holds it in PENDING — which is the point of the gate, so
# every test that expects a restart has to say out loud that the condition
# already persisted.
seed_crash_candidate() {
  local rig="$1" pcat="$2" bead="$3" age="$4"

  mkdir -p "$GT_STUCK_AGENT_DOG_STATE_DIR"
  printf '%s/%s/%s\t%s\n' "$rig" "$pcat" "$bead" "$(( $(date +%s) - age ))" \
    >> "$GT_STUCK_AGENT_DOG_STATE_DIR/crash-candidates.tsv"
}

add_polecat() {
  local name="$1"
  local status="$2"
  local capacity="${3:-true}"

  add_polecat_in_rig gastown gt "$name" "$status" "$capacity"
}

add_polecat_in_rig() {
  local rig="$1"
  local prefix="$2"
  local name="$3"
  local status="$4"
  local capacity="${5:-true}"
  local session="$prefix-$name"

  mkdir -p "$GT_TOWN_ROOT/$rig/polecats/$name"
  touch "$TEST_STATE/sessions/$session"
  printf '%s\n' "$status" > "$TEST_STATE/health/$session"
  # capacity=absent means the polecat exists on disk but not in the capacity
  # inventory — the unknown case, which must fail open into UNCOUNTED.
  if [ "$capacity" != "absent" ]; then
    printf '%s|%s|%s\n' "$rig" "$name" "$capacity" >> "$TEST_STATE/capacity.list"
  fi
}

run_script() {
  bash "$SCRIPT" > "$TEST_STATE/output.log" 2>&1
}

# run_script_capturing_status runs the plugin without aborting this test file
# when it exits non-zero, and records the exit status for assertion.
run_script_capturing_status() {
  SCRIPT_STATUS=0
  bash "$SCRIPT" > "$TEST_STATE/output.log" 2>&1 || SCRIPT_STATUS=$?
}

assert_status() {
  local expected="$1"
  local label="$2"
  if [ "$SCRIPT_STATUS" = "$expected" ]; then
    record_pass "$label"
  else
    record_fail "$label"
    printf '  expected exit %s, got %s\n' "$expected" "$SCRIPT_STATUS"
    sed 's/^/    /' "$TEST_STATE/output.log" 2>/dev/null || true
  fi
}

test_healthy_runtime() {
  local runtime="$1"

  setup_case
  add_polecat "$runtime" healthy
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "$runtime healthy: no session kill"
  assert_file_empty "$TEST_STATE/mail.log" "$runtime healthy: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "$runtime healthy: no escalation"
  assert_file_contains "$TEST_STATE/health_calls.log" "gt-$runtime --max-inactivity 0s" "$runtime healthy: used central health"
}

# --- check-recovery cross-check (gt-zt4o, mirrors hq-h6c2i) ------------------
# The measured hq-h6c2i population showed a zombie counted healthy and a
# NEEDS_RECOVERY polecat dropped entirely, both via the session-health arms.
# These two cover the same shape of defect reached through a healthy session:
# check-recovery's own verdict, not the session probe, is authoritative for
# NEEDS_RECOVERY.

test_healthy_session_but_needs_recovery_is_not_counted_healthy() {
  setup_case
  add_polecat wedged healthy
  touch "$TEST_STATE/needs_recovery/wedged"
  run_script

  assert_file_contains "$TEST_STATE/check_recovery_calls.log" "gastown/wedged" "needs-recovery mismatch: check-recovery consulted"
  assert_file_contains "$TEST_STATE/output.log" "NEEDS_RECOVERY: gt-wedged session healthy but check-recovery verdict says needs_recovery=true" "needs-recovery mismatch: named"
  assert_file_contains "$TEST_STATE/output.log" "0 crashed, 0 stuck, 0 healthy, 0 observed, 0 uncounted, 0 terminal, 0 post-submission, 0 pending, 1 needs_recovery" "needs-recovery mismatch: split out of healthy"
  assert_file_empty "$TEST_STATE/mail.log" "needs-recovery mismatch: no restart mail (reporting only)"
  assert_file_not_contains "$TEST_STATE/output.log" "WARN:" "needs-recovery mismatch: denominator still balances"
}

test_check_recovery_unavailable_trusts_session_health() {
  setup_case
  add_polecat chrome healthy
  touch "$TEST_STATE/check_recovery_fail/chrome"
  run_script

  assert_file_contains "$TEST_STATE/output.log" "NOTICE: check-recovery unavailable for gastown/chrome; trusting session health" "check-recovery unavailable: degradation announced"
  assert_file_contains "$TEST_STATE/output.log" "1 healthy" "check-recovery unavailable: still counted healthy"
  assert_file_not_contains "$TEST_STATE/output.log" "NEEDS_RECOVERY: gt-chrome" "check-recovery unavailable: not falsely flagged"
}

test_agent_hung_observe_only() {
  setup_case
  export GT_STUCK_AGENT_DOG_MAX_INACTIVITY=30m
  add_polecat research agent-hung
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "active research: no session kill"
  assert_file_empty "$TEST_STATE/mail.log" "active research: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "active research: no mass-death escalation"
  assert_file_contains "$TEST_STATE/output.log" "OBSERVE: gt-research runtime alive" "active research: observed live runtime"
  # NOT healthy: the health API reports healthy:false, zombie:true for
  # agent-hung. The restraint above (no kill) is policy; the summary must not
  # launder it into a clean bill of health.
  assert_file_contains "$TEST_STATE/output.log" "0 crashed, 0 stuck, 0 healthy, 1 observed, 0 uncounted" "active research: counted observed, not healthy"
}

# A polecat that never finished spawning holds no hook, so the restartable
# gate is false and it used to fall through to no bucket at all — shrinking the
# denominator and reading as an all-clear. The failure mode produces exactly the
# condition that hides it, so this arm needs its own guard.
test_session_dead_without_hook_is_uncounted() {
  setup_case
  add_polecat spawnfail session-dead
  touch "$TEST_STATE/nohook/spawnfail"
  run_script

  assert_file_empty "$TEST_STATE/mail.log" "spawn-failed: no restart mail"
  assert_file_contains "$TEST_STATE/output.log" "UNCOUNTED: gt-spawnfail session-dead, no restartable hook" "spawn-failed: named the held capacity slot"
  assert_file_contains "$TEST_STATE/output.log" "0 crashed, 0 stuck, 0 healthy, 0 observed, 1 uncounted" "spawn-failed: kept in the denominator"
}

# The measured population from gt-vj63: four non-terminal polecats, two of them
# NEEDS_RECOVERY. The defective accounting reported "0 crashed, 0 stuck, 3
# healthy" — denominator 3, asserting health the probe denied.
test_summary_denominator_covers_every_polecat() {
  setup_case
  export GT_STUCK_AGENT_DOG_MAX_INACTIVITY=30m
  add_polecat chrome healthy
  add_polecat crater healthy
  add_polecat ace agent-hung
  add_polecat synth session-dead
  touch "$TEST_STATE/nohook/synth"
  run_script

  assert_file_contains "$TEST_STATE/output.log" "0 crashed, 0 stuck, 2 healthy, 1 observed, 1 uncounted" "four polecats: denominator is 4, not 3"
}

# --- Probe reachability (gt-ucj8 Part 2) --------------------------------------
# `--max-inactivity 0s` DISABLES level 3 of tmux.CheckSessionHealth, so with a
# 0s default the agent-hung arm above was unreachable and OBSERVED was fed by
# dead code. Every other case in this file pins the env override, which is
# exactly why the defect survived a green suite: no case ever exercised the
# default. These three do.

test_default_probe_threshold_enables_activity_check() {
  setup_case
  unset GT_STUCK_AGENT_DOG_MAX_INACTIVITY
  add_polecat alpha healthy
  run_script

  assert_file_contains "$TEST_STATE/health_calls.log" "gt-alpha --max-inactivity 30m" "default threshold: activity check enabled"
  assert_file_not_contains "$TEST_STATE/health_calls.log" "--max-inactivity 0s" "default threshold: not the disabling 0s"
}

test_zero_max_inactivity_remains_an_explicit_opt_out() {
  setup_case
  export GT_STUCK_AGENT_DOG_MAX_INACTIVITY=0s
  add_polecat alpha healthy
  run_script

  assert_file_contains "$TEST_STATE/health_calls.log" "gt-alpha --max-inactivity 0s" "explicit 0s: opt-out honoured"
}

test_max_inactivity_env_override_wins() {
  setup_case
  export GT_STUCK_AGENT_DOG_MAX_INACTIVITY=45m
  add_polecat alpha healthy
  run_script

  assert_file_contains "$TEST_STATE/health_calls.log" "gt-alpha --max-inactivity 45m" "env override: threshold passed through"
}

# --- UNCOUNTED capacity gate (gt-ucj8 Part 3) ---------------------------------
# Measured before the gate: 36 uncounted, of which 34 were ordinary done
# polecats that correctly have no session and no hook. One genuine instance in
# 36 rows is a field an operator learns to ignore.

test_terminal_done_polecat_is_not_uncounted() {
  setup_case
  add_polecat retired session-dead false
  touch "$TEST_STATE/nohook/retired"
  run_script

  assert_file_contains "$TEST_STATE/output.log" "TERMINAL: gt-retired session-dead, no restartable hook, holds no capacity slot" "terminal done: named as terminal"
  assert_file_not_contains "$TEST_STATE/output.log" "UNCOUNTED: gt-retired" "terminal done: not reported as uncounted"
  assert_file_contains "$TEST_STATE/output.log" "0 observed, 0 uncounted, 1 terminal" "terminal done: excluded from uncounted"
}

test_capacity_holder_is_still_uncounted() {
  setup_case
  add_polecat spawnfail session-dead true
  touch "$TEST_STATE/nohook/spawnfail"
  run_script

  assert_file_contains "$TEST_STATE/output.log" "UNCOUNTED: gt-spawnfail session-dead, no restartable hook (still holds a capacity slot)" "capacity holder: still surfaced"
  assert_file_contains "$TEST_STATE/output.log" "0 observed, 1 uncounted, 0 terminal" "capacity holder: counted as uncounted"
}

# A loaded, non-empty inventory that simply has no row for this polecat. This is
# the arm an empty-map test cannot reach: the map exists and the lookup misses.
test_polecat_absent_from_inventory_fails_open() {
  setup_case
  add_polecat retired session-dead false
  add_polecat ghost session-dead absent
  touch "$TEST_STATE/nohook/retired" "$TEST_STATE/nohook/ghost"
  run_script

  assert_file_contains "$TEST_STATE/output.log" "UNCOUNTED: gt-ghost" "absent from inventory: fails open to uncounted"
  assert_file_contains "$TEST_STATE/output.log" "TERMINAL: gt-retired" "absent from inventory: map was loaded and did discriminate"
  assert_file_contains "$TEST_STATE/output.log" "1 uncounted, 1 terminal" "absent from inventory: no bucket collapse"
}

test_capacity_lookup_unavailable_fails_open() {
  setup_case
  add_polecat retired session-dead false
  touch "$TEST_STATE/nohook/retired"
  touch "$TEST_STATE/polecat_list_fail"
  run_script

  assert_file_contains "$TEST_STATE/output.log" "gt polecat list --all --json unavailable" "capacity lookup down: logged"
  assert_file_contains "$TEST_STATE/output.log" "UNCOUNTED: gt-retired" "capacity lookup down: fails open to uncounted"
}

test_capacity_lookup_unparseable_fails_open() {
  setup_case
  add_polecat retired session-dead false
  touch "$TEST_STATE/nohook/retired"
  printf 'not-json\n' > "$TEST_STATE/polecat_list_raw"
  run_script

  assert_file_contains "$TEST_STATE/output.log" "gt polecat list --all --json not parseable" "capacity lookup unparseable: logged"
  assert_file_contains "$TEST_STATE/output.log" "UNCOUNTED: gt-retired" "capacity lookup unparseable: fails open to uncounted"
}

test_agent_dead_without_hook_gated_on_capacity() {
  setup_case
  add_polecat retired agent-dead false
  add_polecat wedged agent-dead true
  touch "$TEST_STATE/nohook/retired" "$TEST_STATE/nohook/wedged"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "agent-dead no hook: no kills"
  assert_file_contains "$TEST_STATE/output.log" "TERMINAL: gt-retired agent-dead, no restartable hook" "agent-dead no hook: terminal named"
  assert_file_contains "$TEST_STATE/output.log" "UNCOUNTED: gt-wedged agent-dead, no restartable hook" "agent-dead no hook: slot holder named"
  assert_file_contains "$TEST_STATE/output.log" "1 uncounted, 1 terminal" "agent-dead no hook: split by capacity"
}

# An inconclusive probe is an unknown, not a terminal state. Resolving it with
# the capacity verdict would let "we could not tell" quietly become "finished".
test_inconclusive_probe_stays_uncounted_regardless_of_capacity() {
  setup_case
  add_polecat murky unknown-status false
  run_script

  assert_file_contains "$TEST_STATE/output.log" "SKIP gt-murky: central liveness probe inconclusive" "inconclusive probe: logged"
  assert_file_contains "$TEST_STATE/output.log" "1 uncounted, 0 terminal" "inconclusive probe: not laundered into terminal"
}

test_denominator_conservation_is_stated() {
  setup_case
  unset GT_STUCK_AGENT_DOG_MAX_INACTIVITY
  add_polecat chrome healthy
  add_polecat ace agent-hung
  add_polecat synth session-dead true
  add_polecat retired session-dead false
  add_polecat spent session-dead false
  touch "$TEST_STATE/nohook/synth" "$TEST_STATE/nohook/retired" "$TEST_STATE/nohook/spent"
  run_script

  assert_file_contains "$TEST_STATE/output.log" "0 crashed, 0 stuck, 1 healthy, 1 observed, 1 uncounted, 2 terminal" "mixed population: buckets split"
  assert_file_contains "$TEST_STATE/output.log" "Denominator: 5 bucketed == 5 polecat directories enumerated" "mixed population: denominator balances"
  assert_file_not_contains "$TEST_STATE/output.log" "WARN:" "mixed population: no dropped rows"
}

test_hook_show_uses_json_and_rig_workdir() {
  setup_case
  add_polecat alpha agent-dead
  run_script

  assert_file_contains "$TEST_STATE/hook_calls.log" "$GT_TOWN_ROOT/gastown|hook show gastown/polecats/alpha --json" "hook show: used rig workdir and json"
}

test_dead_agent_restarts_one() {
  setup_case
  add_polecat alpha agent-dead
  run_script

  assert_line_count "$TEST_STATE/kill.log" 1 "dead agent: one session kill"
  assert_file_contains "$TEST_STATE/kill.log" "gt-alpha" "dead agent: killed target session"
  assert_line_count "$TEST_STATE/mail.log" 1 "dead agent: one restart mail"
  assert_file_contains "$TEST_STATE/mail.log" "gastown/witness" "dead agent: mailed rig witness"
  assert_file_empty "$TEST_STATE/escalate.log" "dead agent: no mass-death escalation"
}

test_in_progress_hook_restarts_one() {
  setup_case
  add_polecat alpha agent-dead
  printf 'in_progress\n' > "$TEST_STATE/hook_status/alpha"
  run_script

  assert_line_count "$TEST_STATE/kill.log" 1 "in_progress hook: one session kill"
  assert_line_count "$TEST_STATE/mail.log" 1 "in_progress hook: one restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "in_progress hook: no mass-death escalation"
}

test_dead_session_restarts_one() {
  setup_case
  add_polecat beta session-dead
  seed_crash_candidate gastown beta gt-hook-beta 3600
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "dead session: no session kill"
  assert_line_count "$TEST_STATE/mail.log" 1 "dead session: one restart mail"
  assert_file_contains "$TEST_STATE/mail.log" "RESTART_POLECAT: gastown/beta" "dead session: restart requested"
  assert_file_empty "$TEST_STATE/escalate.log" "dead session: no mass-death escalation"
}

test_closed_hook_skips_restart() {
  setup_case
  add_polecat alpha agent-dead
  printf 'closed\n' > "$TEST_STATE/hook_status/alpha"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "closed hook: no session kill"
  assert_file_empty "$TEST_STATE/mail.log" "closed hook: no restart mail"
  assert_file_contains "$TEST_STATE/output.log" "status=closed not actionable" "closed hook: status checked"
}

test_no_hook_dead_sessions_do_not_mass_death() {
  setup_case
  add_polecat alpha session-dead
  add_polecat beta session-dead
  add_polecat gamma session-dead
  touch "$TEST_STATE/nohook/alpha" "$TEST_STATE/nohook/beta" "$TEST_STATE/nohook/gamma"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "idle no-hook: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "idle no-hook: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "idle no-hook: no escalation"
  assert_file_not_contains "$TEST_STATE/output.log" "MASS DEATH" "idle no-hook: no mass death"
  assert_file_contains "$TEST_STATE/output.log" "0 crashed, 0 stuck" "idle no-hook: not counted"
}

test_non_actionable_hook_statuses_do_not_mass_death() {
  setup_case
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  printf 'open\n' > "$TEST_STATE/hook_status/alpha"
  printf 'closed\n' > "$TEST_STATE/hook_status/beta"
  printf 'deferred\n' > "$TEST_STATE/hook_status/gamma"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "stale statuses: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "stale statuses: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "stale statuses: no escalation"
  assert_file_contains "$TEST_STATE/output.log" "status=open not actionable" "stale statuses: open skipped"
  assert_file_not_contains "$TEST_STATE/output.log" "MASS DEATH" "stale statuses: no mass death"
}

test_docked_rig_skipped() {
  setup_case
  cat > "$TEST_STATE/rig_list.json" <<'JSON'
[{"name":"gastown","beads_prefix":"gt","status":"operational"},{"name":"dockedrig","beads_prefix":"dk","status":"docked"}]
JSON
  add_polecat_in_rig dockedrig dk alpha agent-dead
  add_polecat_in_rig dockedrig dk beta agent-dead
  add_polecat_in_rig dockedrig dk gamma agent-dead
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "docked rig: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "docked rig: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "docked rig: no escalation"
  assert_file_not_contains "$TEST_STATE/health_calls.log" "dk-alpha" "docked rig: alpha not health-checked"
  assert_file_not_contains "$TEST_STATE/output.log" "MASS DEATH" "docked rig: no mass death"
}

test_rig_list_unavailable_fails_closed() {
  setup_case
  touch "$TEST_STATE/rig_list_fail"
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "rig list unavailable: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "rig list unavailable: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "rig list unavailable: no escalation"
  assert_file_empty "$TEST_STATE/health_calls.log" "rig list unavailable: no health checks"
  assert_file_contains "$TEST_STATE/output.log" "gt rig list --json unavailable" "rig list unavailable: logged fail-closed"
}

test_rig_list_unparseable_fails_closed() {
  setup_case
  printf 'not-json\n' > "$TEST_STATE/rig_list.json"
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "rig list unparseable: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "rig list unparseable: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "rig list unparseable: no escalation"
  assert_file_empty "$TEST_STATE/health_calls.log" "rig list unparseable: no health checks"
  assert_file_contains "$TEST_STATE/output.log" "gt rig list --json not parseable" "rig list unparseable: logged fail-closed"
}

test_no_operational_rigs_fails_closed() {
  setup_case
  cat > "$TEST_STATE/rig_list.json" <<'JSON'
[{"name":"gastown","beads_prefix":"gt","status":"docked"}]
JSON
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "no operational rigs: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "no operational rigs: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "no operational rigs: no escalation"
  assert_file_empty "$TEST_STATE/health_calls.log" "no operational rigs: no health checks"
  assert_file_contains "$TEST_STATE/output.log" "no operational rigs found" "no operational rigs: logged fail-closed"
}

test_mass_death_recheck_recovered() {
  setup_case
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  printf 'agent-dead\nhealthy\n' > "$TEST_STATE/health/gt-alpha"
  printf 'agent-dead\nhealthy\n' > "$TEST_STATE/health/gt-beta"
  printf 'agent-dead\nhealthy\n' > "$TEST_STATE/health/gt-gamma"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "recovered mass candidates: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "recovered mass candidates: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "recovered mass candidates: no escalation"
  assert_file_contains "$TEST_STATE/output.log" "dropped to 0 after live re-check" "recovered mass candidates: recheck suppressed critical"
}

test_mass_death_recheck_hook_cleared() {
  setup_case
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  printf 'hooked\nempty\n' > "$TEST_STATE/hook_status/alpha"
  printf 'hooked\nempty\n' > "$TEST_STATE/hook_status/beta"
  printf 'hooked\nempty\n' > "$TEST_STATE/hook_status/gamma"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "cleared hooks: no kills"
  assert_file_empty "$TEST_STATE/mail.log" "cleared hooks: no restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "cleared hooks: no mass-death escalation"
  assert_file_contains "$TEST_STATE/output.log" "dropped to 0 after live re-check" "cleared hooks: hook recheck suppressed critical"
}

test_mass_death_recheck_one_remaining_restarts() {
  setup_case
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  printf 'agent-dead\nagent-dead\n' > "$TEST_STATE/health/gt-alpha"
  printf 'agent-dead\nhealthy\n' > "$TEST_STATE/health/gt-beta"
  printf 'agent-dead\nhealthy\n' > "$TEST_STATE/health/gt-gamma"
  run_script

  assert_line_count "$TEST_STATE/kill.log" 1 "one remaining: one kill"
  assert_file_contains "$TEST_STATE/kill.log" "gt-alpha" "one remaining: killed confirmed zombie"
  assert_line_count "$TEST_STATE/mail.log" 1 "one remaining: one restart mail"
  assert_file_empty "$TEST_STATE/escalate.log" "one remaining: no mass-death escalation"
  assert_file_contains "$TEST_STATE/output.log" "dropped to 1 after live re-check" "one remaining: recheck downgraded"
}

test_mass_death_recheck_reclassifies_dead_statuses() {
  setup_case
  add_polecat alpha agent-dead
  add_polecat beta session-dead
  add_polecat gamma agent-dead
  seed_crash_candidate gastown beta gt-hook-beta 3600
  printf 'agent-dead\nsession-dead\n' > "$TEST_STATE/health/gt-alpha"
  printf 'session-dead\nagent-dead\n' > "$TEST_STATE/health/gt-beta"
  printf 'agent-dead\nagent-dead\n' > "$TEST_STATE/health/gt-gamma"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "reclassified mass: no session kills"
  assert_file_empty "$TEST_STATE/mail.log" "reclassified mass: no restart mail"
  assert_line_count "$TEST_STATE/escalate.log" 1 "reclassified mass: one escalation"
  assert_file_contains "$TEST_STATE/escalate.log" "Mass agent death: 3 of 3 live polecats down" "reclassified mass: confirmed all dead"
  assert_file_contains "$TEST_STATE/escalate.log" "--fingerprint stuck-agent-dog:mass-death" "reclassified mass: fingerprint set"
}

test_mass_death_skips_actions() {
  setup_case
  add_polecat alpha agent-dead
  add_polecat beta agent-dead
  add_polecat gamma agent-dead
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "mass death: no session kills"
  assert_file_empty "$TEST_STATE/mail.log" "mass death: no restart mail"
  assert_line_count "$TEST_STATE/escalate.log" 1 "mass death: one escalation"
  assert_file_contains "$TEST_STATE/escalate.log" "--source plugin:stuck-agent-dog" "mass death: source set"
  assert_file_contains "$TEST_STATE/escalate.log" "--fingerprint stuck-agent-dog:mass-death" "mass death: fingerprint set"
  assert_file_contains "$TEST_STATE/output.log" "Skipping per-agent restart/kill actions" "mass death: action loops skipped"
}

# --- Throughput is not mortality (gt-0g5r) ------------------------------------
# Two false CRITICAL "mass agent death" escalations fired within fifteen minutes
# on 2026-08-22 with nobody dead, and the count ROSE from 3 to 6 as the merge
# queue CLEARED. A polecat that finishes its work submits an MR and exits, but
# its hook bead cannot close until that MR merges — so "succeeded and exited" is
# byte-for-byte identical to "crashed" at the session probe.
#
# The two cases below are the named evidence this fix has to satisfy. A rule
# that cannot separate them is not ready, and neither may be reported as an
# aggregate.

# NAMED CASE (a) — fury, measured live 2026-08-22: hook=gt-7k76 HOOKED with
# open_MR=0, which reads CRASHED under an open-MR rule. It was NOT crashed: its
# MR had merged 29 SECONDS earlier and the bead had not closed yet. Every
# successful merge traverses that window, which is why the false-positive rate
# tracked merge throughput. MUST NOT read as crashed.
test_fury_merged_mr_with_open_hook_is_not_crashed() {
  setup_case
  add_polecat fury session-dead
  add_mr gastown gt-hook-fury closed
  seed_crash_candidate gastown fury gt-hook-fury 3600
  run_script

  assert_file_empty "$TEST_STATE/mail.log" "fury: no restart mail for a merged polecat"
  assert_file_empty "$TEST_STATE/kill.log" "fury: no session kill"
  assert_file_empty "$TEST_STATE/escalate.log" "fury: no escalation"
  assert_file_contains "$TEST_STATE/output.log" "POST-SUBMISSION: gt-fury" "fury: named as post-submission"
  assert_file_not_contains "$TEST_STATE/output.log" "CRASHED: gt-fury" "fury: not classified crashed"
  assert_file_contains "$TEST_STATE/output.log" "0 crashed, 0 stuck, 0 healthy, 0 observed, 0 uncounted, 0 terminal, 1 post-submission" "fury: counted in its own bucket"
}

# The same case with the MR still OPEN. An open-MR rule gets this one right; it
# is here so a later change cannot fix the merged case by breaking this one.
test_open_mr_also_reads_as_post_submission() {
  setup_case
  add_polecat guzzle session-dead
  add_mr gastown gt-hook-guzzle open
  seed_crash_candidate gastown guzzle gt-hook-guzzle 3600
  run_script

  assert_file_empty "$TEST_STATE/mail.log" "open MR: no restart mail"
  assert_file_contains "$TEST_STATE/output.log" "POST-SUBMISSION: gt-guzzle" "open MR: named as post-submission"
}

# NAMED CASE (b) — chrome@19:50, the ONE genuine zombie in the 2026-08-22
# dataset: session dead, bead non-terminal, no MR in ANY state, and git-state
# CLEAN. It required a restart and then completed. MUST read as crashed.
#
# The clean git state is why this fix does not use a "work at risk" clause: the
# proposed four-clause rule added `git-state not CLEAN` and thereby produced a
# false negative on the only true positive there was. Nothing here reads git.
test_chrome_real_zombie_with_clean_git_is_crashed() {
  setup_case
  add_polecat chrome session-dead
  seed_crash_candidate gastown chrome gt-hook-chrome 3600
  run_script

  assert_line_count "$TEST_STATE/mail.log" 1 "chrome zombie: restart requested"
  assert_file_contains "$TEST_STATE/mail.log" "RESTART_POLECAT: gastown/chrome" "chrome zombie: correct target"
  assert_file_contains "$TEST_STATE/output.log" "CRASHED: gt-chrome" "chrome zombie: classified crashed"
  assert_file_contains "$TEST_STATE/output.log" "no MR in any state" "chrome zombie: stated the discriminator"
}

# The persistence gate: any point-in-time predicate is racing the
# merge -> bead-terminal transition, so a candidate has to look crashed TWICE,
# more than one window apart, before anything acts on it.
test_crash_candidate_pends_on_first_observation() {
  setup_case
  add_polecat chrome session-dead
  run_script

  assert_file_empty "$TEST_STATE/mail.log" "first observation: no restart mail"
  assert_file_contains "$TEST_STATE/output.log" "PENDING: gt-chrome" "first observation: held pending"
  assert_file_contains "$TEST_STATE/output.log" "0 terminal, 0 post-submission, 1 pending" "first observation: counted pending"
}

# ...and the second observation, once the window has elapsed, does act. Run the
# script twice against a persistence window of zero-plus-one-second so the
# recorded first observation is real rather than seeded.
test_crash_candidate_acts_on_second_observation() {
  setup_case
  export GT_STUCK_AGENT_DOG_CRASH_PERSIST_SECONDS=0
  add_polecat chrome session-dead
  run_script
  assert_line_count "$TEST_STATE/mail.log" 1 "zero window: acts immediately"

  setup_case
  export GT_STUCK_AGENT_DOG_CRASH_PERSIST_SECONDS=600
  add_polecat chrome session-dead
  run_script
  assert_file_empty "$TEST_STATE/mail.log" "second observation: first run held"
  run_script
  assert_file_empty "$TEST_STATE/mail.log" "second observation: still inside the window"
  assert_file_contains "$GT_STUCK_AGENT_DOG_STATE_DIR/crash-candidates.tsv" "gastown/chrome/gt-hook-chrome" "second observation: candidate recorded"
}

# A candidate that recovers, merges, or is reassigned must DROP OUT of the store
# so its clock restarts. A store that only ever grows would eventually age every
# polecat past the window.
test_recovered_candidate_drops_out_of_the_store() {
  setup_case
  add_polecat chrome session-dead
  printf 'session-dead\nhealthy\n' > "$TEST_STATE/health/gt-chrome"
  run_script
  run_script

  assert_file_not_contains "$GT_STUCK_AGENT_DOG_STATE_DIR/crash-candidates.tsv" "gastown/chrome" "recovered candidate: cleared from the store"
}

# Ordering matters: the MR join runs BEFORE the persistence gate, so a polecat
# that submitted after being seen as a candidate is released rather than acted
# on once its clock expires.
test_mr_appearing_later_releases_an_aged_candidate() {
  setup_case
  add_polecat crater session-dead
  seed_crash_candidate gastown crater gt-hook-crater 3600
  add_mr gastown gt-hook-crater open
  run_script

  assert_file_empty "$TEST_STATE/mail.log" "late MR: aged candidate released, no restart"
  assert_file_contains "$TEST_STATE/output.log" "POST-SUBMISSION: gt-crater" "late MR: reclassified"
}

# The agent-dead arm KILLS the session before requesting a restart, so a live MR
# there costs the same orphaned work. No persistence gate on this arm — a dead
# runtime inside a live session is not the merge/terminal race.
test_agent_dead_with_an_mr_is_not_killed() {
  setup_case
  add_polecat foundation agent-dead
  add_mr gastown gt-hook-foundation open
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "agent-dead with MR: session not killed"
  assert_file_empty "$TEST_STATE/mail.log" "agent-dead with MR: no restart mail"
  assert_file_contains "$TEST_STATE/output.log" "POST-SUBMISSION: gt-foundation" "agent-dead with MR: named"
}

# The literal 2026-08-22 alarm: five polecats flagged, every one holding a live
# MR, and `deacon/` stated the consequence — RECOVERY ACTION ON THESE WOULD
# ORPHAN LIVE WORK. It must produce no CRITICAL and no action at all.
test_post_submission_polecats_do_not_mass_death() {
  setup_case
  for name in crater fury guzzle foundation deathclaw; do
    add_polecat "$name" session-dead
    add_mr gastown "gt-hook-$name" open
    seed_crash_candidate gastown "$name" "gt-hook-$name" 3600
  done
  run_script

  assert_file_empty "$TEST_STATE/escalate.log" "post-submission five: no escalation"
  assert_file_empty "$TEST_STATE/mail.log" "post-submission five: no restart mail"
  assert_file_empty "$TEST_STATE/kill.log" "post-submission five: no kills"
  assert_file_not_contains "$TEST_STATE/output.log" "MASS DEATH" "post-submission five: no mass death"
  assert_file_contains "$TEST_STATE/output.log" "5 post-submission" "post-submission five: visible, not silently dropped"
}

# An unreachable merge queue must fail TOWARDS detection. Silencing the dog
# would cost real zombie detection, and the one true positive proves the
# session-death heuristic finds genuine P0 zombies.
test_mr_query_unavailable_does_not_silence_the_dog() {
  setup_case
  add_polecat chrome session-dead
  seed_crash_candidate gastown chrome gt-hook-chrome 3600
  touch "$TEST_STATE/mq_list_fail"
  run_script

  assert_line_count "$TEST_STATE/mail.log" 1 "mq unavailable: still restarts a real zombie"
  assert_file_contains "$TEST_STATE/output.log" "cannot separate post-submission from crashed" "mq unavailable: said so out loud"
}

# An unwritable state directory must not turn the dog off either — it degrades
# to the single-observation behaviour and says so.
test_unwritable_state_dir_fails_open() {
  setup_case
  add_polecat chrome session-dead
  export GT_STUCK_AGENT_DOG_STATE_DIR="$TEST_STATE/mail.log/nested"
  run_script

  assert_file_contains "$TEST_STATE/output.log" "crash persistence gate disabled" "unwritable state: degradation announced"
  assert_file_contains "$TEST_STATE/output.log" "CRASHED: gt-chrome" "unwritable state: still detects"
}

# --- Mass-death threshold is a fraction, not a raw count (gt-0g5r) ------------
# Firing CRITICAL on a raw count is what produced two false CRITICALs in a
# quarter-hour. Three of thirty polecats down is a bad night the restart path
# handles; three of four is the town dying.

test_minority_deaths_escalate_high_and_still_restart() {
  setup_case
  local name=""
  for name in alpha beta gamma; do
    add_polecat "$name" session-dead
    seed_crash_candidate gastown "$name" "gt-hook-$name" 3600
  done
  for name in h1 h2 h3 h4 h5 h6 h7; do
    add_polecat "$name" healthy
  done
  run_script

  assert_line_count "$TEST_STATE/escalate.log" 1 "minority deaths: one escalation"
  assert_file_contains "$TEST_STATE/escalate.log" "Elevated polecat deaths: 3 of 10 live polecats down" "minority deaths: named the denominator"
  assert_file_contains "$TEST_STATE/escalate.log" "-s HIGH" "minority deaths: HIGH, not CRITICAL"
  assert_file_not_contains "$TEST_STATE/escalate.log" "stuck-agent-dog:mass-death" "minority deaths: not the mass-death fingerprint"
  assert_line_count "$TEST_STATE/mail.log" 3 "minority deaths: restarts still requested"
  assert_file_not_contains "$TEST_STATE/output.log" "Skipping per-agent restart/kill actions" "minority deaths: actions not suppressed"
}

test_majority_deaths_escalate_critical_and_suppress_actions() {
  setup_case
  local name=""
  for name in alpha beta gamma; do
    add_polecat "$name" session-dead
    seed_crash_candidate gastown "$name" "gt-hook-$name" 3600
  done
  add_polecat h1 healthy
  run_script

  assert_line_count "$TEST_STATE/escalate.log" 1 "majority deaths: one escalation"
  assert_file_contains "$TEST_STATE/escalate.log" "Mass agent death: 3 of 4 live polecats down" "majority deaths: named the denominator"
  assert_file_contains "$TEST_STATE/escalate.log" "-s CRITICAL" "majority deaths: CRITICAL"
  assert_file_empty "$TEST_STATE/mail.log" "majority deaths: actions suppressed"
}

# TERMINAL polecats are finished and cannot die. Counting them in the
# denominator would dilute a genuine mass death behind a wall of successful
# exits — the same "throughput reads as health" error, inverted.
test_terminal_polecats_are_not_in_the_live_denominator() {
  setup_case
  local name=""
  for name in alpha beta gamma; do
    add_polecat "$name" session-dead
    seed_crash_candidate gastown "$name" "gt-hook-$name" 3600
  done
  for name in spent1 spent2 spent3 spent4 spent5 spent6 spent7; do
    add_polecat "$name" session-dead false
    touch "$TEST_STATE/nohook/$name"
  done
  run_script

  assert_file_contains "$TEST_STATE/escalate.log" "Mass agent death: 3 of 3 live polecats down" "terminal exclusion: denominator is the live population"
  assert_file_contains "$TEST_STATE/escalate.log" "-s CRITICAL" "terminal exclusion: CRITICAL"
}

test_zero_fraction_restores_raw_count_escalation() {
  setup_case
  export GT_STUCK_AGENT_DOG_MASS_DEATH_FRACTION_PCT=0
  local name=""
  for name in alpha beta gamma; do
    add_polecat "$name" session-dead
    seed_crash_candidate gastown "$name" "gt-hook-$name" 3600
  done
  for name in h1 h2 h3 h4 h5 h6 h7; do
    add_polecat "$name" healthy
  done
  run_script

  assert_file_contains "$TEST_STATE/escalate.log" "Mass agent death: 3 of 10 live polecats down" "zero fraction: raw-count CRITICAL restored"
  assert_file_contains "$TEST_STATE/escalate.log" "-s CRITICAL" "zero fraction: CRITICAL"
}

# --- Town root resolution (gt-cr2) -------------------------------------------
# `gt town root` did not exist: it printed `gt town`'s help to STDOUT and
# exited 0, so the GT_TOWN_ROOT fallback assigned help text to TOWN_ROOT and
# every "$TOWN_ROOT/$rig" probe silently found nothing — the dog reported a
# healthy town while inspecting directories that do not exist.

test_town_root_resolved_from_gt_when_env_unset() {
  setup_case
  add_polecat alpha agent-dead
  unset GT_TOWN_ROOT
  run_script_capturing_status

  assert_status 0 "town root via gt: plugin succeeds"
  assert_line_count "$TEST_STATE/mail.log" 1 "town root via gt: restart mail sent"
  assert_file_contains "$TEST_STATE/output.log" "1 stuck" "town root via gt: inspected the real town"
}

test_town_root_unresolvable_fails_loudly() {
  setup_case
  add_polecat alpha agent-dead
  touch "$TEST_STATE/town_root_fail"
  unset GT_TOWN_ROOT
  run_script_capturing_status

  assert_status 1 "town root unresolvable: non-zero exit"
  assert_file_contains "$TEST_STATE/output.log" "FATAL: could not resolve town root" "town root unresolvable: loud failure"
  assert_file_empty "$TEST_STATE/mail.log" "town root unresolvable: no restart mail"
  assert_file_empty "$TEST_STATE/health_calls.log" "town root unresolvable: no health checks"
}

test_town_root_help_text_fails_loudly() {
  setup_case
  add_polecat alpha agent-dead
  touch "$TEST_STATE/town_root_help"
  unset GT_TOWN_ROOT
  run_script_capturing_status

  assert_status 1 "town root help text: non-zero exit"
  assert_file_contains "$TEST_STATE/output.log" "FATAL: resolved town root is not a directory" "town root help text: rejected as a path"
  assert_file_empty "$TEST_STATE/mail.log" "town root help text: no restart mail"
  assert_file_empty "$TEST_STATE/health_calls.log" "town root help text: no health checks"
  assert_file_not_contains "$TEST_STATE/output.log" "0 crashed, 0 stuck" "town root help text: no phantom all-healthy report"
}

test_invalid_mass_death_threshold_defaults() {
  setup_case
  export GT_STUCK_AGENT_DOG_MASS_DEATH_THRESHOLD=0
  run_script

  assert_file_empty "$TEST_STATE/escalate.log" "zero threshold: no empty mass-death escalation"
  assert_file_not_contains "$TEST_STATE/output.log" "MASS DEATH" "zero threshold: no mass death"
}

test_healthy_runtime opencode
test_healthy_runtime bun
test_healthy_runtime node
test_healthy_runtime claude
test_healthy_session_but_needs_recovery_is_not_counted_healthy
test_check_recovery_unavailable_trusts_session_health
test_agent_hung_observe_only
test_session_dead_without_hook_is_uncounted
test_summary_denominator_covers_every_polecat
test_default_probe_threshold_enables_activity_check
test_zero_max_inactivity_remains_an_explicit_opt_out
test_max_inactivity_env_override_wins
test_terminal_done_polecat_is_not_uncounted
test_capacity_holder_is_still_uncounted
test_polecat_absent_from_inventory_fails_open
test_capacity_lookup_unavailable_fails_open
test_capacity_lookup_unparseable_fails_open
test_agent_dead_without_hook_gated_on_capacity
test_inconclusive_probe_stays_uncounted_regardless_of_capacity
test_denominator_conservation_is_stated
test_hook_show_uses_json_and_rig_workdir
test_dead_agent_restarts_one
test_in_progress_hook_restarts_one
test_dead_session_restarts_one
test_closed_hook_skips_restart
test_no_hook_dead_sessions_do_not_mass_death
test_non_actionable_hook_statuses_do_not_mass_death
test_docked_rig_skipped
test_rig_list_unavailable_fails_closed
test_rig_list_unparseable_fails_closed
test_no_operational_rigs_fails_closed
test_mass_death_recheck_recovered
test_mass_death_recheck_hook_cleared
test_mass_death_recheck_one_remaining_restarts
test_mass_death_recheck_reclassifies_dead_statuses
test_mass_death_skips_actions
test_fury_merged_mr_with_open_hook_is_not_crashed
test_open_mr_also_reads_as_post_submission
test_chrome_real_zombie_with_clean_git_is_crashed
test_crash_candidate_pends_on_first_observation
test_crash_candidate_acts_on_second_observation
test_recovered_candidate_drops_out_of_the_store
test_mr_appearing_later_releases_an_aged_candidate
test_agent_dead_with_an_mr_is_not_killed
test_post_submission_polecats_do_not_mass_death
test_mr_query_unavailable_does_not_silence_the_dog
test_unwritable_state_dir_fails_open
test_minority_deaths_escalate_high_and_still_restart
test_majority_deaths_escalate_critical_and_suppress_actions
test_terminal_polecats_are_not_in_the_live_denominator
test_zero_fraction_restores_raw_count_escalation
test_town_root_resolved_from_gt_when_env_unset
test_town_root_unresolvable_fails_loudly
test_town_root_help_text_fails_loudly
test_invalid_mass_death_threshold_defaults

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

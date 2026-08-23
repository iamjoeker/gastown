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
  local bin_dir="$TEST_TMP/bin"

  mkdir -p "$TEST_STATE/health" "$TEST_STATE/hook_fail" "$TEST_STATE/hook_status" "$TEST_STATE/nohook" "$TEST_STATE/sessions" "$TEST_STATE/status" "$bin_dir"
  mkdir -p "$GT_TOWN_ROOT/gastown/polecats" "$GT_TOWN_ROOT/deacon"
  printf '{"rigs":{"gastown":{"beads":{"prefix":"gt"}}}}\n' > "$GT_TOWN_ROOT/rigs.json"
  : > "$TEST_STATE/mail.log"
  : > "$TEST_STATE/kill.log"
  : > "$TEST_STATE/escalate.log"
  : > "$TEST_STATE/health_calls.log"
  : > "$TEST_STATE/hook_calls.log"
  : > "$TEST_STATE/bd.log"
  : > "$TEST_STATE/capacity.list"
  printf '%s\n' "$GT_TOWN_ROOT" > "$TEST_STATE/town_root_value"
  touch "$TEST_STATE/sessions/hq-deacon"

  write_fake_commands "$bin_dir"
  export PATH="$bin_dir:$ORIGINAL_PATH"
  export GT_STUCK_AGENT_DOG_MAX_INACTIVITY=0s
  unset GT_STUCK_AGENT_DOG_MASS_DEATH_THRESHOLD
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
  printf 'agent-dead\nsession-dead\n' > "$TEST_STATE/health/gt-alpha"
  printf 'session-dead\nagent-dead\n' > "$TEST_STATE/health/gt-beta"
  printf 'agent-dead\nagent-dead\n' > "$TEST_STATE/health/gt-gamma"
  run_script

  assert_file_empty "$TEST_STATE/kill.log" "reclassified mass: no session kills"
  assert_file_empty "$TEST_STATE/mail.log" "reclassified mass: no restart mail"
  assert_line_count "$TEST_STATE/escalate.log" 1 "reclassified mass: one escalation"
  assert_file_contains "$TEST_STATE/escalate.log" "Mass agent death: 3 agents down" "reclassified mass: confirmed all dead"
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
test_town_root_resolved_from_gt_when_env_unset
test_town_root_unresolvable_fails_loudly
test_town_root_help_text_fails_loudly
test_invalid_mass_death_threshold_defaults

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

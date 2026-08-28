#!/usr/bin/env bash
# stuck-agent-dog/run.sh — Context-aware stuck/crashed agent detection.
#
# SCOPE: Only polecats and deacon. NEVER touches crew, mayor, witness, or refinery.
# The daemon detects; this plugin inspects context before acting.

set -euo pipefail

log() { echo "[stuck-agent-dog] $*"; }

# Resolve the town root, failing loudly. `gt town root` used to be a
# nonexistent subcommand: it printed `gt town`'s help to STDOUT and exited 0,
# so this branch succeeded with help text in TOWN_ROOT and every
# "$TOWN_ROOT/$rig" probe below silently found nothing — the dog reported a
# healthy town while checking directories that do not exist (gt-cr2). The -d
# check keeps that failure loud even against a gt binary predating the fix.
TOWN_ROOT="${GT_TOWN_ROOT:-}"
if [ -z "$TOWN_ROOT" ]; then
  if ! TOWN_ROOT=$(gt town root); then
    log "FATAL: could not resolve town root; set GT_TOWN_ROOT or run inside a Gas Town workspace." >&2
    exit 1
  fi
fi
if [ ! -d "$TOWN_ROOT" ]; then
  log "FATAL: resolved town root is not a directory: '$TOWN_ROOT'" >&2
  exit 1
fi

integer_or_default() {
  local value="$1"
  local default="$2"

  case "$value" in
    ''|*[!0-9]*) echo "$default" ;;
    *) echo "$value" ;;
  esac
}

positive_integer_or_default() {
  local value="$1"
  local default="$2"

  case "$value" in
    ''|*[!0-9]*) echo "$default" ;;
    *)
      if [ "$value" -ge 1 ]; then
        echo "$value"
      else
        echo "$default"
      fi
      ;;
  esac
}

# The activity check is ON by default. `gt session health --max-inactivity 0s`
# DISABLES level 3 of tmux.CheckSessionHealth entirely, so with a 0s default the
# agent-hung branch below was unreachable: the only probe call site is
# session_health_status, it passes this value, and agent-hung can therefore
# never be returned. The OBSERVED bucket was fed by dead code (gt-ucj8).
#
# 30m, not the 10-15m the CheckSessionHealth doc suggests: this plugin never
# kills or restarts on agent-hung, it only reports, so the cost of a late
# detection is a delayed log line while the cost of an early one is teaching
# operators that OBSERVE means nothing. 30m clears a long research turn and
# still surfaces a wedged runtime within one patrol cycle of the daemon's own
# thresholds. GT_STUCK_AGENT_DOG_MAX_INACTIVITY overrides it; 0s remains an
# explicit opt-out.
POLECAT_MAX_INACTIVITY="${GT_STUCK_AGENT_DOG_MAX_INACTIVITY:-30m}"
[ "$POLECAT_MAX_INACTIVITY" = "0" ] && POLECAT_MAX_INACTIVITY="0s"
DEACON_STALE_SECONDS=$(integer_or_default "${GT_STUCK_AGENT_DOG_DEACON_STALE_SECONDS:-}" 1200)
ACTIVITY_GRACE_SECONDS=$(integer_or_default "${GT_STUCK_AGENT_DOG_ACTIVITY_GRACE_SECONDS:-}" "$DEACON_STALE_SECONDS")
MASS_DEATH_THRESHOLD=$(positive_integer_or_default "${GT_STUCK_AGENT_DOG_MASS_DEATH_THRESHOLD:-}" 3)

# --- Crash-candidate persistence window (gt-0g5r) -----------------------------
# THERE IS A WINDOW BETWEEN AN MR CLOSING AS MERGED AND ITS SOURCE BEAD GOING
# TERMINAL, AND EVERY SUCCESSFUL MERGE TRAVERSES IT. Inside that window a
# just-succeeded polecat — session exited, bead still hooked — is byte-for-byte
# identical to a crashed one. That is the single most common transition in the
# system, so the false-positive rate of any point-in-time predicate is coupled
# to MERGE THROUGHPUT: the alarm gets louder exactly as the queue clears. Two
# CRITICAL "mass agent death" escalations fired inside fifteen minutes on
# 2026-08-22 with nobody dead.
#
# The MR join below closes that window directly. This is the second, independent
# gate: a crashed candidate must still look crashed on a LATER observation
# separated by more than the window. 600s is two cooldown cycles of this plugin
# and orders of magnitude longer than a merge/terminal transition, so a healthy
# just-merged polecat has closed its bead long before the second look, while a
# genuine zombie looks identical every time it is measured.
#
# Cost: recovery of a real crash is delayed by up to one window. That is the
# deliberate trade — a false restart can orphan a live MR, a late restart cannot.
CRASH_PERSIST_SECONDS=$(integer_or_default "${GT_STUCK_AGENT_DOG_CRASH_PERSIST_SECONDS:-}" 600)

# Fraction of the live polecat population that must be down before a raw count
# is allowed to mean "mass agent death". 3 of 30 is a bad night; 3 of 4 is the
# town dying, and only the second deserves a CRITICAL. 0 disables the fraction
# gate (CRITICAL on raw count, the pre-gt-0g5r behaviour).
MASS_DEATH_FRACTION_PCT=$(integer_or_default "${GT_STUCK_AGENT_DOG_MASS_DEATH_FRACTION_PCT:-}" 50)
[ "$MASS_DEATH_FRACTION_PCT" -gt 100 ] && MASS_DEATH_FRACTION_PCT=100

STATE_DIR="${GT_STUCK_AGENT_DOG_STATE_DIR:-$TOWN_ROOT/.runtime/stuck-agent-dog}"
CANDIDATE_FILE="$STATE_DIR/crash-candidates.tsv"

heartbeat_epoch() {
  local file="$1"
  local ts=""

  ts=$(jq -r '(.timestamp // empty) | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601? // empty' "$file" 2>/dev/null || true)
  if [ -n "$ts" ]; then
    echo "$ts"
    return 0
  fi

  # Fallback for malformed legacy files: use mtime rather than failing open.
  # GNU stat (-c %Y) first: on GNU, 'stat -f' is filesystem mode and dumps a
  # multi-line "File: ..." block to stdout BEFORE failing, polluting the
  # command substitution and breaking downstream arithmetic (hq-wisp-0vrp).
  # BSD/macOS stat (-f %m) is the fallback.
  stat -c %Y "$file" 2>/dev/null || stat -f %m "$file" 2>/dev/null
}

has_in_progress_work() {
  local locations=("$TOWN_ROOT")
  local rig=""
  local loc=""
  local output=""
  local count=""

  while IFS='|' read -r rig _prefix; do
    [ -z "$rig" ] && continue
    [ -d "$TOWN_ROOT/$rig" ] && locations+=("$TOWN_ROOT/$rig")
  done <<< "$RIG_PREFIX_MAP"

  for loc in "${locations[@]}"; do
    output=$(cd "$loc" && bd list --status=in_progress --json --limit=1 2>/dev/null) || return 0
    count=$(printf '%s' "$output" | jq 'length' 2>/dev/null || echo 1)
    if [ "${count:-1}" -gt 0 ]; then
      return 0
    fi
  done

  return 1
}

# --- Beads resolution helpers -------------------------------------------------
# Plugin scripts may run outside a beads workspace. Resolve hook and status
# lookups from the target rig workspace, and make missing/inactive rigs
# non-fatal so one bad rig does not abort the dog under `set -e` (hq-9e770).

rig_workdir() {
  local rig="$1"

  if [ -d "$TOWN_ROOT/$rig/mayor/rig" ]; then
    printf '%s\n' "$TOWN_ROOT/$rig/mayor/rig"
    return 0
  fi

  if [ -d "$TOWN_ROOT/$rig" ]; then
    printf '%s\n' "$TOWN_ROOT/$rig"
    return 0
  fi

  return 1
}

rig_hook_assignment() {
  local rig="$1" pcat="$2" dir=""
  local hook_json="" bead="" status=""

  if ! dir=$(rig_workdir "$rig"); then
    return 0
  fi

  hook_json=$( ( cd "$dir" 2>/dev/null && gt hook show "$rig/polecats/$pcat" --json 2>/dev/null ) || true )
  if [ -z "$hook_json" ]; then
    return 0
  fi

  bead=$(printf '%s' "$hook_json" | jq -r '.bead_id // empty' 2>/dev/null || true)
  status=$(printf '%s' "$hook_json" | jq -r '.status // empty' 2>/dev/null || true)
  [ -n "$bead" ] || return 0

  printf '%s|%s\n' "$bead" "$status"
}

hook_restartable() {
  local session="$1" bead="$2" status="$3"

  case "$status" in
    hooked|in_progress) [ -n "$bead" ] && return 0 ;;
    empty|"") log "  SKIP $session: no active hook" ;;
    *) log "  SKIP $session: hook=$bead status=$status not actionable" ;;
  esac

  return 1
}

session_health_status() {
  local session_name="$1"
  local health_json=""
  local status=""

  health_json=$(gt session health "$session_name" --json --max-inactivity "$POLECAT_MAX_INACTIVITY" 2>/dev/null) || return 1
  status=$(printf '%s' "$health_json" | jq -r '.status // empty' 2>/dev/null || true)
  [ -n "$status" ] || return 1
  printf '%s\n' "$status"
}

# --- check-recovery cross-check (gt-zt4o, mirrors hq-h6c2i) -------------------
# session_health_status answers "is the runtime alive", not "does this polecat
# need recovery". `gt polecat check-recovery` is the authoritative source for
# NEEDS_RECOVERY: its verdict reads cleanup_status, active_mr, and git-state
# predicates the session probe never touches. A polecat can look healthy at
# the session layer (tmux alive, recent activity) while check-recovery's own
# verdict disagrees, and without this cross-check the HEALTHY bucket asserts
# something no probe here actually confirmed — the same shape of defect as the
# zombie counted healthy, reached through a different arm than agent-hung.
#
# Fails OPEN (trusts session health) when check-recovery itself is unreachable
# or unparseable: an unrelated outage in the recovery-check surface must not
# turn every healthy polecat into a false NEEDS_RECOVERY report.
needs_recovery_per_check_recovery() {
  local rig="$1" pcat="$2"
  local json=""

  if ! json=$(gt polecat check-recovery "$rig/$pcat" --json 2>/dev/null); then
    log "  NOTICE: check-recovery unavailable for $rig/$pcat; trusting session health"
    return 1
  fi
  [ "$(printf '%s' "$json" | jq -r '.needs_recovery // false' 2>/dev/null)" = "true" ]
}

operational_rig_prefix_map() {
  local rig_json="" rows=""

  if ! rig_json=$(cd "$TOWN_ROOT" 2>/dev/null && gt rig list --json 2>/dev/null); then
    log "SKIP: gt rig list --json unavailable; cannot verify operational rig state" >&2
    return 0
  fi

  if ! rows=$(printf '%s' "$rig_json" | jq -r '
    if type == "array" then .[] else empty end
    | select((.status // "" | ascii_downcase) == "operational")
    | select((.name // "") != "" and (.beads_prefix // "") != "")
    | "\(.name)|\(.beads_prefix)"
  ' 2>/dev/null); then
    log "SKIP: gt rig list --json not parseable; cannot verify operational rig state" >&2
    return 0
  fi

  printf '%s\n' "$rows" | awk -F'|' 'NF >= 2 && $1 != "" && $2 != ""'
}

# --- Merge-request join (gt-0g5r) ---------------------------------------------
# A polecat that finishes its work SUBMITS AN MR AND EXITS, but its hook bead
# cannot close until that MR MERGES. So "submitted successfully and exited" and
# "crashed" present identically to the session probe. The merge queue is the
# only surface that tells them apart, and it must be queried at --status=all:
#
#   OPEN-ONLY IS NOT ENOUGH. Measured live on 2026-08-22: polecat fury read
#   hook=gt-7k76 HOOKED with open_MR=0, which is CRASHED under an open-only
#   rule. It was not — its MR had merged 29 SECONDS earlier and the bead had
#   not closed yet. A merged MR is still proof of submission; it is proof the
#   work is already safe.
#
# KNOWN GAP, NOT CLOSED HERE: when a polecat's work lands under ANOTHER
# polecat's branch (cross-branch conflict resolution — the ghoul case), no MR
# joins to its own hook bead and it still reads as a crash candidate. That costs
# a wasted restart; it does not orphan work, which is why this fix does not
# reach for the git-state clause that would supposedly cover it. See plugin.md.
MR_SOURCE_ISSUES=""

load_mr_sources() {
  local rig="" json="" rows=""

  MR_SOURCE_ISSUES=""
  while IFS='|' read -r rig _prefix; do
    [ -z "$rig" ] && continue
    if ! json=$(cd "$TOWN_ROOT" 2>/dev/null && gt mq list "$rig" --status=all --json 2>/dev/null); then
      # Fail TOWARDS detection, not away from it: an unreachable merge queue
      # must not silently reclassify every crash as a success. The candidate
      # still has to survive the persistence gate before anything acts on it.
      log "NOTICE: gt mq list $rig --status=all --json unavailable; cannot separate post-submission from crashed in $rig"
      continue
    fi
    if ! rows=$(printf '%s' "$json" | jq -r '
      if type == "array" then .[] else empty end
      | (.description // "")
      | split("\n")[]
      | select(startswith("source_issue:"))
      | ltrimstr("source_issue:")
      | sub("^[ \t]+"; "") | sub("[ \t\r]+$"; "")
      | select(. != "")
    ' 2>/dev/null); then
      log "NOTICE: gt mq list $rig --status=all --json not parseable; cannot separate post-submission from crashed in $rig"
      continue
    fi
    while IFS= read -r source_issue; do
      [ -n "$source_issue" ] || continue
      MR_SOURCE_ISSUES="${MR_SOURCE_ISSUES}${rig}|${source_issue}"$'\n'
    done <<< "$rows"
  done <<< "$RIG_PREFIX_MAP"
}

# has_submitted_mr answers "did this polecat's assignment reach the merge queue,
# in any state?". True means POST-SUBMISSION: the work is in the queue or
# already merged, and recovery action on this agent would orphan it.
has_submitted_mr() {
  local rig="$1" bead="$2"

  [ -n "$bead" ] || return 1
  [ -n "$MR_SOURCE_ISSUES" ] || return 1
  printf '%s' "$MR_SOURCE_ISSUES" | grep -Fxq -- "$rig|$bead"
}

# --- Crash-candidate persistence store ----------------------------------------
# Keyed on rig/polecat/hook_bead: a NEW assignment restarts the clock, because a
# different bead is a different claim and must earn its own second observation.
CANDIDATE_STATE=""
CANDIDATE_SEEN=""
CANDIDATE_STATE_OK=1
CANDIDATE_AGE=0

load_crash_candidates() {
  if ! mkdir -p "$STATE_DIR" 2>/dev/null; then
    # Fail OPEN on persistence. A dog that can never reach its second
    # observation is a SILENT dog, and the one true positive in the 2026-08-22
    # dataset proves the session-death heuristic catches real P0 zombies. The
    # MR join above is unaffected and still blocks the dangerous case.
    log "NOTICE: cannot use $STATE_DIR; crash persistence gate disabled (single observation will act)"
    CANDIDATE_STATE_OK=0
    return 0
  fi
  if [ -f "$CANDIDATE_FILE" ]; then
    CANDIDATE_STATE=$(cat "$CANDIDATE_FILE" 2>/dev/null || true)
  fi
}

# crash_candidate_persisted records this observation and reports whether the
# same candidate was already seen more than CRASH_PERSIST_SECONDS ago. Sets
# CANDIDATE_AGE for the log line so the wait is visible rather than mysterious.
crash_candidate_persisted() {
  local rig="$1" pcat="$2" bead="$3"
  local key="$rig/$pcat/$bead" first_seen="" now=""

  now=$(date +%s)
  CANDIDATE_AGE=0

  if [ "$CANDIDATE_STATE_OK" -eq 0 ]; then
    return 0
  fi

  first_seen=$(printf '%s' "$CANDIDATE_STATE" | awk -F'\t' -v k="$key" '$1 == k { print $2; exit }')
  case "$first_seen" in
    ''|*[!0-9]*) first_seen="$now" ;;
  esac

  CANDIDATE_SEEN="${CANDIDATE_SEEN}${key}"$'\t'"${first_seen}"$'\n'
  CANDIDATE_AGE=$(( now - first_seen ))
  [ "$CANDIDATE_AGE" -ge "$CRASH_PERSIST_SECONDS" ]
}

# persist_crash_candidates writes back ONLY what was observed this run, so a
# candidate that recovered, merged, or was reassigned drops out and its clock
# resets. Silence on failure would strand a stale file; say so instead.
persist_crash_candidates() {
  [ "$CANDIDATE_STATE_OK" -eq 1 ] || return 0
  if ! printf '%s' "$CANDIDATE_SEEN" > "$CANDIDATE_FILE.tmp" 2>/dev/null; then
    log "NOTICE: could not write $CANDIDATE_FILE.tmp; crash persistence state not updated"
    return 0
  fi
  mv "$CANDIDATE_FILE.tmp" "$CANDIDATE_FILE" 2>/dev/null ||
    log "NOTICE: could not update $CANDIDATE_FILE; crash persistence state not updated"
}

# --- Capacity map -------------------------------------------------------------
# counts_toward_capacity is the capacity system's own verdict on whether a
# polecat still occupies a slot (internal/polecat/workstate.go). A terminal
# `done` polecat correctly has no session and no hook — that is the normal end
# state, not an anomaly. Reporting those as UNCOUNTED buried the one real
# instance in 35 benign rows, which trains operators to ignore the field.
#
# One call for the whole town; the map is keyed "<rig>/<name>".
CAPACITY_MAP=""

load_capacity_map() {
  local json=""

  if ! json=$(cd "$TOWN_ROOT" 2>/dev/null && gt polecat list --all --json 2>/dev/null); then
    log "NOTICE: gt polecat list --all --json unavailable; every polecat will be treated as capacity-holding"
    return 0
  fi

  if ! CAPACITY_MAP=$(printf '%s' "$json" | jq -r '
    if type == "array" then .[] else empty end
    | select((.rig // "") != "" and (.name // "") != "")
    | "\(.rig)/\(.name)|\(.counts_toward_capacity)"
  ' 2>/dev/null); then
    log "NOTICE: gt polecat list --all --json not parseable; every polecat will be treated as capacity-holding"
    CAPACITY_MAP=""
  fi
}

# holds_capacity_slot fails OPEN (true) for anything the map does not answer for.
# A polecat missing from the inventory, or an inventory that would not load, is
# precisely the unknown this bucket exists to surface — resolving it to "no slot"
# would let the denominator shrink again through a different door.
holds_capacity_slot() {
  local rig="$1" pcat="$2" value=""

  if [ -z "$CAPACITY_MAP" ]; then
    return 0
  fi

  value=$(printf '%s\n' "$CAPACITY_MAP" | awk -F'|' -v key="$rig/$pcat" '$1 == key { print $2; exit }')
  if [ "$value" = "false" ]; then
    return 1
  fi
  return 0
}

# classify_unbucketed routes an enumerated polecat that matched no actionable
# arm. UNCOUNTED means "classified nowhere and STILL HOLDS A CAPACITY SLOT";
# TERMINAL means "classified nowhere because it is finished". Both stay in the
# denominator — the point is to separate the signal from the steady state, not
# to drop rows.
classify_unbucketed() {
  local rig="$1" pcat="$2" session="$3" reason="$4"

  if holds_capacity_slot "$rig" "$pcat"; then
    UNCOUNTED=$((UNCOUNTED + 1))
    log "  UNCOUNTED: $session $reason (still holds a capacity slot)"
  else
    TERMINAL=$((TERMINAL + 1))
    log "  TERMINAL: $session $reason, holds no capacity slot"
  fi
}

confirm_current_polecat_outage() {
  local session="$1" rig="$2" pcat="$3"
  local health_status="" hook_assignment="" hook_bead="" hook_status=""

  health_status=$(session_health_status "$session" || true)
  case "$health_status" in
    session-dead|session_dead)
      hook_assignment=$(rig_hook_assignment "$rig" "$pcat" || true)
      IFS='|' read -r hook_bead hook_status <<< "$hook_assignment"
      if hook_restartable "$session" "$hook_bead" "$hook_status"; then
        if has_submitted_mr "$rig" "$hook_bead"; then
          log "  NOTICE: $session is post-submission (hook=$hook_bead has an MR); dropped from mass-death count"
        else
          CONFIRMED_CRASHED+=("$session|$rig|$pcat|$hook_bead")
        fi
      fi
      ;;
    agent-dead|agent_dead)
      hook_assignment=$(rig_hook_assignment "$rig" "$pcat" || true)
      IFS='|' read -r hook_bead hook_status <<< "$hook_assignment"
      if hook_restartable "$session" "$hook_bead" "$hook_status"; then
        if has_submitted_mr "$rig" "$hook_bead"; then
          log "  NOTICE: $session is post-submission (hook=$hook_bead has an MR); dropped from mass-death count"
        else
          CONFIRMED_STUCK+=("$session|$rig|$pcat|$hook_bead|agent_dead")
        fi
      fi
      ;;
    healthy|agent-hung|agent_hung)
      log "  NOTICE: $session recovered before mass-death escalation (health=$health_status)"
      ;;
    *)
      log "  NOTICE: $session not confirmed before mass-death escalation (health=${health_status:-unknown})"
      ;;
  esac
}

confirm_polecat_outages() {
  local entry="" session="" rig="" pcat="" _hook="" _reason=""

  CONFIRMED_CRASHED=()
  CONFIRMED_STUCK=()

  # Re-read the merge queue, not just session health. The gap between
  # enumeration and escalation is exactly where a batch of MRs lands, and that
  # is precisely when this alarm used to fire hardest.
  load_mr_sources

  for entry in ${CRASHED[@]+"${CRASHED[@]}"}; do
    [ -n "$entry" ] || continue
    IFS='|' read -r session rig pcat _hook <<< "$entry"
    confirm_current_polecat_outage "$session" "$rig" "$pcat"
  done

  for entry in ${STUCK[@]+"${STUCK[@]}"}; do
    [ -n "$entry" ] || continue
    IFS='|' read -r session rig pcat _hook _reason <<< "$entry"
    confirm_current_polecat_outage "$session" "$rig" "$pcat"
  done
}

# --- Enumerate agents ---------------------------------------------------------

log "=== Checking agent health ==="

# Build operational rig_name|prefix mapping. The rig registry is the live
# parked/docked filter; if it is unavailable, fail closed.
RIG_PREFIX_MAP=$(operational_rig_prefix_map)
if [ -z "$RIG_PREFIX_MAP" ]; then
  log "SKIP: no operational rigs found"
  exit 0
fi

# --- Check polecat health ----------------------------------------------------

CRASHED=()
STUCK=()
HEALTHY=0
# OBSERVED: probe says healthy:false but policy is deliberately not to act
# (e.g. agent-hung: a quiet live runtime may be a long research turn).
# These are NOT healthy — counting them as such made the receipt assert
# something its own probe denied.
OBSERVED=0
# UNCOUNTED: enumerated but classified into no bucket, so the denominator
# would otherwise silently shrink. A polecat that never finished spawning
# holds no hook, which is exactly the condition that used to make it
# invisible here — the failure mode produced its own concealment.
# Gated on counts_toward_capacity: only slot-holders land here.
UNCOUNTED=0
# TERMINAL: enumerated, classified into no actionable bucket, and holding no
# capacity slot — a done polecat with no session and no hook. The ordinary end
# state. Kept as its own bucket so the denominator still balances.
TERMINAL=0
# POST_SUBMISSION: session gone, hook bead still non-terminal, and an MR
# references that bead. This polecat SUCCEEDED — the bead is only still hooked
# because the MR has not merged (or merged seconds ago and has not propagated).
# It was the majority of every false "mass agent death" alarm on 2026-08-22.
# Given its own bucket rather than folded into TERMINAL: the state is real and
# operators should be able to see it, and a silent exclusion is how a
# discriminator stops being auditable.
POST_SUBMISSION=0
# PENDING: a crash candidate on its FIRST observation. Not acted on and not
# counted toward mass death until it survives CRASH_PERSIST_SECONDS.
PENDING=0
# NEEDS_RECOVERY_MISMATCH: session health said healthy, but `gt polecat
# check-recovery`'s own verdict says needs_recovery=true. Split out of HEALTHY
# rather than folded into it, for the same reason OBSERVED was split out of
# HEALTHY for agent-hung: the summary must not assert a clean bill of health
# the check-recovery probe itself denies.
NEEDS_RECOVERY_MISMATCH=0
# ENUMERATED: polecat directories walked. Every one of them must land in exactly
# one bucket; the guard after the loop says so out loud rather than trusting it.
ENUMERATED=0

load_capacity_map
load_mr_sources
load_crash_candidates

while IFS='|' read -r RIG PREFIX; do
  [ -z "$RIG" ] && continue
  POLECAT_DIR="$TOWN_ROOT/$RIG/polecats"
  [ -d "$POLECAT_DIR" ] || continue

  for PCAT_PATH in "$POLECAT_DIR"/*/; do
    [ -d "$PCAT_PATH" ] || continue
    PCAT_NAME=$(basename "$PCAT_PATH")
    SESSION_NAME="${PREFIX}-${PCAT_NAME}"
    ENUMERATED=$((ENUMERATED + 1))

    HEALTH_STATUS=$(session_health_status "$SESSION_NAME" || true)
    case "$HEALTH_STATUS" in
      healthy)
        if needs_recovery_per_check_recovery "$RIG" "$PCAT_NAME"; then
          NEEDS_RECOVERY_MISMATCH=$((NEEDS_RECOVERY_MISMATCH + 1))
          log "  NEEDS_RECOVERY: $SESSION_NAME session healthy but check-recovery verdict says needs_recovery=true"
        else
          HEALTHY=$((HEALTHY + 1))
        fi
        ;;
      agent-dead|agent_dead)
        HOOK_ASSIGNMENT=$(rig_hook_assignment "$RIG" "$PCAT_NAME")
        IFS='|' read -r HOOK_BEAD HOOK_STATUS <<< "$HOOK_ASSIGNMENT"
        if hook_restartable "$SESSION_NAME" "$HOOK_BEAD" "$HOOK_STATUS"; then
          if has_submitted_mr "$RIG" "$HOOK_BEAD"; then
            # This arm KILLS the session before requesting a restart, so the
            # cost of getting it wrong on a polecat holding a live MR is the
            # same orphaned work. No persistence gate here: a dead runtime
            # inside a live session is not the merge/terminal race.
            POST_SUBMISSION=$((POST_SUBMISSION + 1))
            log "  POST-SUBMISSION: $SESSION_NAME runtime dead but hook=$HOOK_BEAD has an MR; not killing a submitted polecat"
          else
            STUCK+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD|agent_dead")
            log "  ZOMBIE: $SESSION_NAME (agent runtime dead, hook=$HOOK_BEAD)"
          fi
        else
          classify_unbucketed "$RIG" "$PCAT_NAME" "$SESSION_NAME" "agent-dead, no restartable hook"
        fi
        ;;
      agent-hung|agent_hung)
        # A live runtime with quiet output can be a long research turn. Do not
        # kill it here; operators can tune the threshold and inspect manually.
        # NOT counted healthy: the health API reports healthy:false, zombie:true
        # for this status. The restraint is policy; the accounting must not
        # launder it into a clean bill of health.
        OBSERVED=$((OBSERVED + 1))
        log "  OBSERVE: $SESSION_NAME runtime alive but inactive beyond $POLECAT_MAX_INACTIVITY; not restarting"
        ;;
      session-dead|session_dead)
        HOOK_ASSIGNMENT=$(rig_hook_assignment "$RIG" "$PCAT_NAME")
        IFS='|' read -r HOOK_BEAD HOOK_STATUS <<< "$HOOK_ASSIGNMENT"
        if hook_restartable "$SESSION_NAME" "$HOOK_BEAD" "$HOOK_STATUS"; then
          if has_submitted_mr "$RIG" "$HOOK_BEAD"; then
            POST_SUBMISSION=$((POST_SUBMISSION + 1))
            log "  POST-SUBMISSION: $SESSION_NAME exited with hook=$HOOK_BEAD still open, but an MR references it — submitted, not crashed"
          elif crash_candidate_persisted "$RIG" "$PCAT_NAME" "$HOOK_BEAD"; then
            CRASHED+=("$SESSION_NAME|$RIG|$PCAT_NAME|$HOOK_BEAD")
            log "  CRASHED: $SESSION_NAME (hook=$HOOK_BEAD, unchanged for ${CANDIDATE_AGE}s, no MR in any state)"
          else
            PENDING=$((PENDING + 1))
            log "  PENDING: $SESSION_NAME (hook=$HOOK_BEAD, no MR) seen ${CANDIDATE_AGE}s ago; needs ${CRASH_PERSIST_SECONDS}s to rule out a just-merged polecat"
          fi
        else
          # Dead session with no restartable hook. Two very different things
          # land here: a polecat that never finished spawning (agent_state=
          # spawning never took a hook, so the restartable gate is false — it
          # used to vanish from the counts entirely), and an ordinary done
          # polecat that has released its slot. counts_toward_capacity is what
          # tells them apart.
          classify_unbucketed "$RIG" "$PCAT_NAME" "$SESSION_NAME" "session-dead, no restartable hook"
        fi
        ;;
      *)
        # An inconclusive probe is never terminal-by-inference: we do not know
        # what this polecat is doing, so it stays in UNCOUNTED regardless of the
        # capacity verdict.
        UNCOUNTED=$((UNCOUNTED + 1))
        log "  SKIP $SESSION_NAME: central liveness probe inconclusive"
        ;;
    esac
  done
done <<< "$RIG_PREFIX_MAP"

persist_crash_candidates

log ""
log "Polecat health: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, $HEALTHY healthy, $OBSERVED observed, $UNCOUNTED uncounted, $TERMINAL terminal, $POST_SUBMISSION post-submission, $PENDING pending, $NEEDS_RECOVERY_MISMATCH needs_recovery"

# Conservation guard. The defect this plugin keeps re-acquiring is a bucket that
# silently drops rows, and a shrinking denominator reads exactly like an
# all-clear. State the identity instead of assuming it. POST_SUBMISSION and
# PENDING are excluded from action but NOT from the denominator, for the same
# reason: an exclusion that does not show up in the arithmetic is invisible.
BUCKET_TOTAL=$(( ${#CRASHED[@]} + ${#STUCK[@]} + HEALTHY + OBSERVED + UNCOUNTED + TERMINAL + POST_SUBMISSION + PENDING + NEEDS_RECOVERY_MISMATCH ))
if [ "$BUCKET_TOTAL" -eq "$ENUMERATED" ]; then
  log "Denominator: $BUCKET_TOTAL bucketed == $ENUMERATED polecat directories enumerated"
else
  log "WARN: $BUCKET_TOTAL bucketed != $ENUMERATED polecat directories enumerated — a classification arm is dropping rows"
fi

# --- Check deacon health -----------------------------------------------------

log ""
log "=== Deacon Health ==="

DEACON_SESSION="hq-deacon"
DEACON_ISSUE=""
DEACON_DIVERGENCE=""
DEACON_PROCESS_ALIVE=0

if ! tmux has-session -t "$DEACON_SESSION" 2>/dev/null; then
  log "  CRASHED: Deacon session is dead"
  DEACON_ISSUE="crashed"
else
  DEACON_PID=$(tmux list-panes -t "$DEACON_SESSION" -F '#{pane_pid}' 2>/dev/null | head -1 || true)
  DEACON_COMM=$(ps -o comm= -p "$DEACON_PID" 2>/dev/null || true)
  if [ -z "$DEACON_COMM" ]; then
    log "  ZOMBIE: Deacon process dead (pid=$DEACON_PID), session alive"
    DEACON_ISSUE="zombie"
  else
    log "  Process alive: pid=$DEACON_PID comm=$DEACON_COMM"
    DEACON_PROCESS_ALIVE=1
  fi

  HEARTBEAT_FILE="$TOWN_ROOT/deacon/heartbeat.json"
  if [ -z "$DEACON_ISSUE" ] && [ -f "$HEARTBEAT_FILE" ]; then
    HEARTBEAT_TIME=$(heartbeat_epoch "$HEARTBEAT_FILE" || true)
    NOW=$(date +%s)
    HEARTBEAT_AGE=$(( NOW - ${HEARTBEAT_TIME:-0} ))

    if [ "$HEARTBEAT_AGE" -gt "$DEACON_STALE_SECONDS" ]; then
      # Cross-check tmux activity before declaring stuck: heartbeat.json is
      # only ONE of three heartbeat stores (hq-qxl9). A live session with
      # recent activity means the file-write path diverged (e.g. a long
      # turn, or the agent refreshing a different store) — not a stuck
      # Deacon. Escalating that as stuck caused a false-positive storm.
      ACTIVITY_TIME=$(tmux display-message -t "$DEACON_SESSION" -p '#{window_activity}' 2>/dev/null || true)
      case "$ACTIVITY_TIME" in
        ''|*[!0-9]*) ACTIVITY_AGE="" ;;
        *) ACTIVITY_AGE=$(( NOW - ACTIVITY_TIME )) ;;
      esac
      if [ -n "$ACTIVITY_AGE" ] && [ "$ACTIVITY_AGE" -le "$ACTIVITY_GRACE_SECONDS" ]; then
        log "  DIVERGENCE: heartbeat file stale (${HEARTBEAT_AGE}s) but session active ${ACTIVITY_AGE}s ago — write divergence, not stuck"
        DEACON_DIVERGENCE="heartbeat_write_divergence_${HEARTBEAT_AGE}s_active_${ACTIVITY_AGE}s"
      elif [ "$DEACON_PROCESS_ALIVE" -eq 1 ] && ! has_in_progress_work; then
        log "  SKIP: Deacon heartbeat stale (${HEARTBEAT_AGE}s old) but process is alive and no in_progress work exists"
      else
        log "  STUCK: Deacon heartbeat stale (${HEARTBEAT_AGE}s old, >${DEACON_STALE_SECONDS}s threshold), no recent session activity"
        DEACON_ISSUE="stuck_heartbeat_${HEARTBEAT_AGE}s"
      fi
    else
      log "  OK: Deacon heartbeat ${HEARTBEAT_AGE}s old"
    fi
  fi
fi

# --- Mass death check ---------------------------------------------------------

# A raw count is not a mass-death signal on its own. Three of thirty polecats
# down is a bad night that the ordinary restart path handles; three of four is
# the town dying. Firing CRITICAL on the raw count is how two of these landed
# inside fifteen minutes with nobody dead, and a CRITICAL that is wrong twice in
# a quarter-hour trains everyone to ignore the next one, which may be real.
#
# The live population is every enumerated polecat that still holds a capacity
# slot. TERMINAL polecats are finished and cannot die, so counting them would
# dilute a genuine mass death behind a wall of successful exits.
LIVE_POPULATION=$(( ENUMERATED - TERMINAL ))

TOTAL_ISSUES=$(( ${#CRASHED[@]} + ${#STUCK[@]} ))
MASS_DEATH=0
if [ "$TOTAL_ISSUES" -ge "$MASS_DEATH_THRESHOLD" ]; then
  log ""
  log "Mass-death candidate threshold reached ($TOTAL_ISSUES); re-checking live health and the merge queue before escalation"
  confirm_polecat_outages
  CRASHED=("${CONFIRMED_CRASHED[@]}")
  STUCK=("${CONFIRMED_STUCK[@]}")
  CONFIRMED_TOTAL=$(( ${#CRASHED[@]} + ${#STUCK[@]} ))
  [ "$LIVE_POPULATION" -lt "$CONFIRMED_TOTAL" ] && LIVE_POPULATION="$CONFIRMED_TOTAL"

  if [ "$CONFIRMED_TOTAL" -lt "$MASS_DEATH_THRESHOLD" ]; then
    log "NOTICE: mass-death candidates dropped to $CONFIRMED_TOTAL after live re-check; no escalation"
  elif [ $(( CONFIRMED_TOTAL * 100 )) -ge $(( LIVE_POPULATION * MASS_DEATH_FRACTION_PCT )) ]; then
    MASS_DEATH=1
    log "MASS DEATH: $CONFIRMED_TOTAL of $LIVE_POPULATION live polecats down confirmed — escalating instead of restarting"
    gt escalate "Mass agent death: $CONFIRMED_TOTAL of $LIVE_POPULATION live polecats down" \
      -s CRITICAL \
      --source "plugin:stuck-agent-dog" \
      --fingerprint "stuck-agent-dog:mass-death" 2>/dev/null || true
  else
    # Above the count threshold but a minority of the town. Report it — a real
    # cluster of crashes is worth knowing about — but do NOT suppress the
    # restarts, because restarting is the correct response to a handful of
    # genuine crashes and suppression is what leaves them lying there.
    log "ELEVATED DEATHS: $CONFIRMED_TOTAL of $LIVE_POPULATION live polecats down (below the ${MASS_DEATH_FRACTION_PCT}% mass-death fraction) — escalating HIGH and still restarting"
    gt escalate "Elevated polecat deaths: $CONFIRMED_TOTAL of $LIVE_POPULATION live polecats down, $POST_SUBMISSION post-submission excluded" \
      -s HIGH \
      --source "plugin:stuck-agent-dog" \
      --fingerprint "stuck-agent-dog:elevated-deaths" 2>/dev/null || true
  fi
fi

# --- Take action --------------------------------------------------------------

if [ "$MASS_DEATH" -eq 1 ]; then
  log "Skipping per-agent restart/kill actions during mass-death escalation"
else
  # Crashed polecats: notify witness to restart
  # Note: `"${arr[@]:-}"` expands an empty array to a single empty string under
  # `set -u`, which would fire a phantom `RESTART_POLECAT: /` notification. The
  # `${arr[@]+"${arr[@]}"}` form expands to nothing when the array is empty.
  for ENTRY in ${CRASHED[@]+"${CRASHED[@]}"}; do
    IFS='|' read -r SESSION RIG PCAT HOOK <<< "$ENTRY"
    log "Requesting restart for $RIG/polecats/$PCAT (hook=$HOOK)"
    gt mail send "$RIG/witness" -s "RESTART_POLECAT: $RIG/$PCAT" --stdin <<BODY || log "  WARN: restart mail failed for $RIG/$PCAT"
Polecat $PCAT crash confirmed by stuck-agent-dog plugin.
hook_bead: $HOOK
action: restart requested
BODY
  done

  # Zombie polecats: kill zombie session, then request restart
  for ENTRY in ${STUCK[@]+"${STUCK[@]}"}; do
    IFS='|' read -r SESSION RIG PCAT HOOK REASON <<< "$ENTRY"
    log "Killing zombie session $SESSION and requesting restart"
    tmux kill-session -t "$SESSION" 2>/dev/null || true
    gt mail send "$RIG/witness" -s "RESTART_POLECAT: $RIG/$PCAT (zombie cleared)" --stdin <<BODY || log "  WARN: restart mail failed for $RIG/$PCAT"
Polecat $PCAT zombie session cleared by stuck-agent-dog plugin.
hook_bead: $HOOK
reason: $REASON
action: restart requested
BODY
  done
fi

# Deacon issues: escalate
if [ -n "$DEACON_ISSUE" ]; then
	log "Escalating deacon issue: $DEACON_ISSUE"
	DEACON_SEVERITY="HIGH"
	DEACON_FINGERPRINT="stuck-agent-dog:deacon:$DEACON_ISSUE"
	case "$DEACON_ISSUE" in
		stuck_heartbeat_*)
			DEACON_SEVERITY="MEDIUM"
			DEACON_FINGERPRINT="stuck-agent-dog:deacon:stuck-heartbeat"
			;;
	esac
	gt escalate "Deacon $DEACON_ISSUE detected by stuck-agent-dog" \
		-s "$DEACON_SEVERITY" \
		--source "plugin:stuck-agent-dog" \
		--fingerprint "$DEACON_FINGERPRINT" 2>/dev/null || true
fi

# --- Report -------------------------------------------------------------------

SUMMARY="Agent health: ${#CRASHED[@]} crashed, ${#STUCK[@]} stuck, $HEALTHY healthy, $OBSERVED observed, $UNCOUNTED uncounted, $TERMINAL terminal, $POST_SUBMISSION post-submission, $PENDING pending, $NEEDS_RECOVERY_MISMATCH needs_recovery"
[ -n "$DEACON_ISSUE" ] && SUMMARY="$SUMMARY, deacon=$DEACON_ISSUE"
[ -n "$DEACON_DIVERGENCE" ] && SUMMARY="$SUMMARY, deacon=$DEACON_DIVERGENCE (not escalated)"
log ""
log "=== $SUMMARY ==="

gt plugin record-run --plugin stuck-agent-dog --result success \
  --title "stuck-agent-dog: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true

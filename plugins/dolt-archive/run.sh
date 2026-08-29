#!/usr/bin/env bash
# dolt-archive/run.sh — Deterministic JSONL backup + git push + dolt push.
#
# Exports production databases to JSONL, commits to git backup repo,
# and pushes Dolt remotes. JSONL is the last-resort recovery layer.
#
# Usage: ./run.sh [--databases db1,db2,...] [--skip-git] [--skip-dolt-push]

set -euo pipefail

# --- Configuration -----------------------------------------------------------

DOLT_HOST="${GT_DOLT_HOST:-${DOLT_HOST:-127.0.0.1}}"
DOLT_PORT="${GT_DOLT_PORT:-${DOLT_PORT:-3307}}"
DOLT_USER="${DOLT_USER:-root}"
# Town root. NEVER hardcode $HOME/gt — the town is not necessarily there, and this
# script previously wrote 80MB of "last-resort recovery layer" JSONL into ~/gt while
# every reader (e.g. internal/cmd/vitals.go) looked under the real town root. The two
# never met, so the archive was invisible to the tooling that existed to check it,
# and deleting the stray ~/gt destroyed the only copy (hq-uwxo / gt-3sz class).
GT_TOWN_ROOT_RESOLVED="${GT_TOWN_ROOT:-${GT_ROOT:-}}"
if [[ -z "$GT_TOWN_ROOT_RESOLVED" ]]; then
  echo "[dolt-archive] FATAL: neither GT_TOWN_ROOT nor GT_ROOT is set." >&2
  echo "[dolt-archive] Refusing to guess a town root — guessing is what put the" >&2
  echo "[dolt-archive] archive somewhere nothing read from. Set GT_ROOT and re-run." >&2
  exit 1
fi
if [[ ! -d "$GT_TOWN_ROOT_RESOLVED/.dolt-data" ]]; then
  echo "[dolt-archive] FATAL: $GT_TOWN_ROOT_RESOLVED/.dolt-data does not exist." >&2
  echo "[dolt-archive] That is not a town root. Refusing to export." >&2
  exit 1
fi

DOLT_DATA_DIR="${DOLT_DATA_DIR:-$GT_TOWN_ROOT_RESOLVED/.dolt-data}"
JSONL_EXPORT_DIR="$GT_TOWN_ROOT_RESOLVED/.dolt-archive/jsonl"
BACKUP_REPO="$GT_TOWN_ROOT_RESOLVED/.dolt-archive/git"
DEFAULT_DBS="auto"
SKIP_GIT=false
SKIP_DOLT_PUSH=false

# --- Argument parsing --------------------------------------------------------

while [[ $# -gt 0 ]]; do
  case "$1" in
    --databases)    DEFAULT_DBS="$2"; shift 2 ;;
    --skip-git)     SKIP_GIT=true; shift ;;
    --skip-dolt-push) SKIP_DOLT_PUSH=true; shift ;;
    --help|-h)
      echo "Usage: $0 [--databases db1,db2,...] [--skip-git] [--skip-dolt-push]"
      exit 0
      ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# --- Helpers -----------------------------------------------------------------

log() {
  echo "[dolt-archive] $*"
}

LOGFILE=$(mktemp /tmp/dolt-archive-stderr.XXXXXX)
trap 'rm -f "$LOGFILE"' EXIT

dolt_query() {
  local db="$1"
  local query="$2"
  local args=(dolt --host "$DOLT_HOST" --port "$DOLT_PORT" --no-tls -u "$DOLT_USER" -p "")
  if [[ -n "$db" ]]; then
    args+=(--use-db "$db")
  fi
  args+=(sql -q "$query" --result-format csv)
  "${args[@]}" 2>>"$LOGFILE" | tail -n +2 | tr -d '\r'
}

dolt_query_json() {
  local db="$1"
  local query="$2"
  dolt --host "$DOLT_HOST" --port "$DOLT_PORT" --no-tls -u "$DOLT_USER" -p "" \
    --use-db "$db" sql -q "$query" --result-format json 2>>"$LOGFILE"
}

# --- Step 1: JSONL export ----------------------------------------------------

# Auto-discover production databases or use the explicit list.
if [[ "$DEFAULT_DBS" == "auto" ]]; then
  PROD_DBS=()
  while IFS= read -r line; do
    PROD_DBS+=("$line")
  done < <(
    dolt_query "" "SHOW DATABASES" \
      | grep -v -E '^(information_schema|mysql|dolt_cluster)$' \
      | grep -v -E '^(testdb_|beads_t|beads_pt|doctest_)'
  )
  if [[ ${#PROD_DBS[@]} -eq 0 ]]; then
    log "ERROR: No production databases found via auto-discovery"
    exit 1
  fi
else
  IFS=',' read -ra PROD_DBS <<< "$DEFAULT_DBS"
fi

log "Starting archive cycle (databases: ${PROD_DBS[*]})"
mkdir -p "$JSONL_EXPORT_DIR"

EXPORTED=0
EXPORT_FAILED=0
EXPORT_ERRORS=""

# Wisp/wisp-event tables hold mail, MRs, and escalations (~14866+ rows
# town-wide). They are excluded from Dolt commit history (dolt_ignore) and
# from the `issues` table this loop otherwise exports, so without a separate
# export they have zero coverage in this last-resort recovery layer (hq-6loo).
WISP_TABLES=("wisps" "wisp_events")
WISP_EXPORTED=0
WISP_EXPORT_FAILED=0
WISP_EXPORT_ERRORS=""

for DB in "${PROD_DBS[@]}"; do
  EXPORT_FILE="$JSONL_EXPORT_DIR/${DB}-$(date +%Y%m%d-%H%M).jsonl"
  LATEST_LINK="$JSONL_EXPORT_DIR/${DB}-latest.jsonl"

  log "Exporting $DB..."

  # Skip databases without an issues table (e.g. gastown config DB)
  if ! dolt_query "$DB" "SHOW TABLES LIKE 'issues'" 2>/dev/null | grep -q 'issues'; then
    log "  $DB: skipped (no issues table)"
    continue
  fi

  # Export via Dolt SQL (reliable for all databases with an issues table)
  if dolt_query_json "$DB" "SELECT * FROM issues ORDER BY id" > "$EXPORT_FILE" 2>/dev/null && [[ -s "$EXPORT_FILE" ]]; then
    LINE_COUNT=$(wc -l < "$EXPORT_FILE" | tr -d ' ')
    log "  $DB: exported via SQL ($LINE_COUNT lines)"
    ln -sf "$(basename "$EXPORT_FILE")" "$LATEST_LINK"
    EXPORTED=$((EXPORTED + 1))
  else
    log "  WARN: $DB export failed"
    rm -f "$EXPORT_FILE"
    EXPORT_FAILED=$((EXPORT_FAILED + 1))
    EXPORT_ERRORS="${EXPORT_ERRORS}${DB} "
  fi

  # Export wisp tables alongside issues, when present, into their own files.
  for TABLE in "${WISP_TABLES[@]}"; do
    if ! dolt_query "$DB" "SHOW TABLES LIKE '$TABLE'" 2>/dev/null | grep -q "$TABLE"; then
      log "  $DB/$TABLE: skipped (no $TABLE table)"
      continue
    fi

    WISP_EXPORT_FILE="$JSONL_EXPORT_DIR/${DB}-${TABLE}-$(date +%Y%m%d-%H%M).jsonl"
    WISP_LATEST_LINK="$JSONL_EXPORT_DIR/${DB}-${TABLE}-latest.jsonl"

    if dolt_query_json "$DB" "SELECT * FROM \`$TABLE\` ORDER BY id" > "$WISP_EXPORT_FILE" 2>/dev/null && [[ -s "$WISP_EXPORT_FILE" ]]; then
      WISP_LINE_COUNT=$(wc -l < "$WISP_EXPORT_FILE" | tr -d ' ')
      log "  $DB/$TABLE: exported via SQL ($WISP_LINE_COUNT lines)"
      ln -sf "$(basename "$WISP_EXPORT_FILE")" "$WISP_LATEST_LINK"
      WISP_EXPORTED=$((WISP_EXPORTED + 1))
    else
      log "  WARN: $DB/$TABLE export failed"
      rm -f "$WISP_EXPORT_FILE"
      WISP_EXPORT_FAILED=$((WISP_EXPORT_FAILED + 1))
      WISP_EXPORT_ERRORS="${WISP_EXPORT_ERRORS}${DB}/${TABLE} "
    fi
  done
done

# Prune old exports (keep last 24 snapshots per DB, per table)
for DB in "${PROD_DBS[@]}"; do
  SNAPSHOTS=$(ls -t "$JSONL_EXPORT_DIR/${DB}-2"*.jsonl 2>/dev/null | tail -n +25)
  if [[ -n "$SNAPSHOTS" ]]; then
    echo "$SNAPSHOTS" | xargs rm -f
    log "Pruned old $DB snapshots"
  fi
  for TABLE in "${WISP_TABLES[@]}"; do
    WISP_SNAPSHOTS=$(ls -t "$JSONL_EXPORT_DIR/${DB}-${TABLE}-2"*.jsonl 2>/dev/null | tail -n +25)
    if [[ -n "$WISP_SNAPSHOTS" ]]; then
      echo "$WISP_SNAPSHOTS" | xargs rm -f
      log "Pruned old $DB/$TABLE snapshots"
    fi
  done
done

log "JSONL export: $EXPORTED succeeded, $EXPORT_FAILED failed"
log "Wisp table export: $WISP_EXPORTED succeeded, $WISP_EXPORT_FAILED failed"

# --- Step 2: Git commit and push ---------------------------------------------

GIT_PUSHED=false
GIT_CONFIGURED=false

if [[ -d "$BACKUP_REPO/.git" ]]; then
  GIT_CONFIGURED=true
fi

if ! $SKIP_GIT && [[ -d "$BACKUP_REPO/.git" ]]; then
  log ""
  log "=== Git Push ==="

  # Copy latest JSONL files to git repo (issues, plus wisp tables when present)
  for DB in "${PROD_DBS[@]}"; do
    LATEST="$JSONL_EXPORT_DIR/${DB}-latest.jsonl"
    if [[ -L "$LATEST" ]]; then
      REAL_FILE="$JSONL_EXPORT_DIR/$(readlink "$LATEST")"
      if [[ -f "$REAL_FILE" ]]; then
        cp "$REAL_FILE" "$BACKUP_REPO/${DB}.jsonl"
      fi
    elif [[ -f "$LATEST" ]]; then
      cp "$LATEST" "$BACKUP_REPO/${DB}.jsonl"
    fi

    for TABLE in "${WISP_TABLES[@]}"; do
      WISP_LATEST="$JSONL_EXPORT_DIR/${DB}-${TABLE}-latest.jsonl"
      if [[ -L "$WISP_LATEST" ]]; then
        WISP_REAL_FILE="$JSONL_EXPORT_DIR/$(readlink "$WISP_LATEST")"
        if [[ -f "$WISP_REAL_FILE" ]]; then
          cp "$WISP_REAL_FILE" "$BACKUP_REPO/${DB}-${TABLE}.jsonl"
        fi
      elif [[ -f "$WISP_LATEST" ]]; then
        cp "$WISP_LATEST" "$BACKUP_REPO/${DB}-${TABLE}.jsonl"
      fi
    done
  done

  cd "$BACKUP_REPO"

  if git diff --quiet && git diff --staged --quiet; then
    log "No changes to commit"
  else
    git add *.jsonl 2>/dev/null || true
    git commit -m "Archive snapshot $(date +%Y-%m-%d-%H%M)" \
      --author="Gas Town Archive <archive@gastown.local>" 2>/dev/null || true

    if git remote get-url origin > /dev/null 2>&1; then
      if git push origin main 2>/dev/null; then
        GIT_PUSHED=true
        log "Pushed to GitHub"
      else
        log "WARN: Git push to remote failed"
      fi
    else
      log "WARN: No git remote configured for backup repo"
    fi
  fi
elif ! $SKIP_GIT; then
  log "No git backup repo at $BACKUP_REPO — skipping git push"
fi

# --- Step 3: Dolt native push ------------------------------------------------

DOLT_PUSHED=0
DOLT_PUSH_FAILED=0
DOLT_REMOTES_CONFIGURED=0

if ! $SKIP_DOLT_PUSH; then
  log ""
  log "=== Dolt Push ==="

  for DB in "${PROD_DBS[@]}"; do
    DB_DIR="$DOLT_DATA_DIR/$DB"

    if [[ ! -d "$DB_DIR/.dolt" ]]; then
      log "  $DB: no .dolt directory, skipping"
      continue
    fi

    REMOTES=$(cd "$DB_DIR" && { dolt remote -v 2>/dev/null | grep -v "^$" | head -5 || true; })
    if [[ -z "$REMOTES" ]]; then
      log "  $DB: no remotes configured, skipping"
      continue
    fi
    DOLT_REMOTES_CONFIGURED=$((DOLT_REMOTES_CONFIGURED + 1))

    log "  $DB: pushing to remotes..."
    cd "$DB_DIR"

    for REMOTE_NAME in $(dolt remote -v 2>/dev/null | awk '{print $1}' | sort -u || true); do
      if timeout 120 dolt push "$REMOTE_NAME" main 2>/dev/null; then
        log "    $REMOTE_NAME: pushed"
        DOLT_PUSHED=$((DOLT_PUSHED + 1))
      else
        log "    $REMOTE_NAME: FAILED"
        DOLT_PUSH_FAILED=$((DOLT_PUSH_FAILED + 1))
      fi
    done
  done

  log "Dolt push: $DOLT_PUSHED succeeded, $DOLT_PUSH_FAILED failed"
fi

# --- Step 4: Report results --------------------------------------------------

log ""
log "=== Archive Cycle Complete ==="

# No offsite layer exists at all when neither the git backup repo nor any
# Dolt remote is configured. Local JSONL export succeeding is not offsite
# protection — a machine loss destroys it along with the live databases.
# Only flag this when both layers were eligible to run (neither was
# explicitly skipped via flag); an explicit --skip-git/--skip-dolt-push
# invocation is not a surprise finding.
NO_OFFSITE_LAYER=false
if ! $SKIP_GIT && ! $SKIP_DOLT_PUSH && ! $GIT_CONFIGURED && [[ "$DOLT_REMOTES_CONFIGURED" -eq 0 ]]; then
  NO_OFFSITE_LAYER=true
fi

SUMMARY="Archive: jsonl=$EXPORTED/$((EXPORTED + EXPORT_FAILED)), wisps=$WISP_EXPORTED/$((WISP_EXPORTED + WISP_EXPORT_FAILED)), git=${GIT_PUSHED}, dolt_push=$DOLT_PUSHED/$((DOLT_PUSHED + DOLT_PUSH_FAILED))"
if $NO_OFFSITE_LAYER; then
  SUMMARY="$SUMMARY, offsite=NONE CONFIGURED"
fi
log "$SUMMARY"

RESULT="success"
if [[ "$EXPORT_FAILED" -gt 0 ]] || [[ "$WISP_EXPORT_FAILED" -gt 0 ]] || [[ "$DOLT_PUSH_FAILED" -gt 0 ]]; then
  RESULT="warning"
fi
if $NO_OFFSITE_LAYER; then
  RESULT="failure"
fi

gt plugin record-run --plugin dolt-archive --result "$RESULT" \
  --title "$SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true

if $NO_OFFSITE_LAYER; then
  gt escalate "dolt-archive: no offsite backup layer configured — JSONL export succeeded but git push and dolt push are both unconfigured" \
    -s critical \
    --reason "Neither a git backup repo ($BACKUP_REPO) nor any Dolt remote is configured. Local JSONL export is not offsite protection: it lives on the same disk as the databases it copies. Production data currently has no protection against machine loss." 2>/dev/null || true
fi

if [[ "$EXPORT_FAILED" -gt 0 ]]; then
  gt escalate "dolt-archive: JSONL export failed for $EXPORT_FAILED databases ($EXPORT_ERRORS)" \
    -s critical \
    --reason "JSONL is our last-resort recovery layer. Failed databases: $EXPORT_ERRORS" 2>/dev/null || true
fi

if [[ "$WISP_EXPORT_FAILED" -gt 0 ]]; then
  gt escalate "dolt-archive: wisp table export failed for $WISP_EXPORT_FAILED database/table pairs ($WISP_EXPORT_ERRORS)" \
    -s critical \
    --reason "Wisps (mail, MRs, escalations) have no Dolt commit history and are otherwise unrecoverable. Failed: $WISP_EXPORT_ERRORS" 2>/dev/null || true
fi

log "Done."

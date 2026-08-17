#!/usr/bin/env bash
#
# town-sweep.sh — gitignore-blind content sweep over a Gas Town tree.
#
# WHY THIS EXISTS (gt-emm)
#
# Recursive search in the agent shell respects .gitignore. Gas Town gitignores
# every working clone:
#
#     **/polecats/   **/mayor/rig/   **/refinery/rig/   **/crew/   **/deacon/dogs/
#
# Those are exactly the directories that hold the checkouts. A recursive sweep
# from the town root therefore skips ALL polecat sandboxes and ALL rig clones and
# reports a perfect all-clear. Measured on one machine, same pattern, same instant:
#
#     find + per-file grep for 'wisp gc --age 1h --force'  -> 16 files
#     grep -rl  (gitignore-aware)  over the same root      ->  0 files
#
# The blindness is perfectly correlated with where risk lives: working clones are
# gitignored precisely BECAUSE they are derived, and derived copies are exactly
# where stale, divergent, or pre-patch content accumulates. It fails silently
# rather than erroring, so a correct probe string delivered by a blind traversal
# still yields a false clean.
#
# This script is the traversal half of a verification sweep. It walks with `find`
# (not gitignore-aware) and hands grep an explicit file list (ignore logic bypassed
# a second time), so no ignore rule can subtract from its coverage.
#
# See docs/guides/verification-sweeps.md for the full recipe and the rules that
# govern positive controls.
#
set -uo pipefail

PROG="$(basename "$0")"

usage() {
    cat <<'EOF'
town-sweep.sh — gitignore-blind content sweep over a Gas Town tree

USAGE
  town-sweep.sh [options] PATTERN
  town-sweep.sh --self-test [-r ROOT]

OPTIONS
  -r, --root DIR       Root to sweep (default: $GT_ROOT, else current directory)
  -i, --include GLOB   Only scan files whose basename matches GLOB (repeatable)
  -x, --exclude-dir D  Prune directories with this name (repeatable; default: .git)
      --include-git    Do not prune .git directories
  -c, --count          Print only the number of matching files
  -E, --regex          Treat PATTERN as an extended regex (default: fixed string)
      --text-only      Skip binary files (grep -I)
      --self-test      Run the built-in positive/negative controls and exit
  -h, --help           Show this help

EXIT CODES
  0  matches found (grep -l semantics)
  1  no matches
  2  usage error, or the sweep could not guarantee full coverage

COVERAGE NOTES
  * .git directories are pruned by default (object stores are noise, not content).
    Pass --include-git to sweep them too.
  * Symlinks are not followed, so the walk cannot loop or escape the root.
  * Binary files ARE searched by default. Silently skipping content is the exact
    anti-pattern this script exists to fix; pass --text-only to opt out.
  * If find or grep writes anything to stderr (unreadable directory, vanished
    path), the sweep exits 2 rather than printing a partial result. An incomplete
    sweep must never be mistaken for a clean one.
EOF
}

die() {
    echo "$PROG: $*" >&2
    exit 2
}

# ── Argument parsing ────────────────────────────────────────────────────────
ROOT=""
PATTERN=""
COUNT_ONLY=false
REGEX=false
TEXT_ONLY=false
SELF_TEST=false
PRUNE_GIT=true
INCLUDES=()
EXCLUDE_DIRS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        -r|--root)        [[ $# -ge 2 ]] || die "missing argument to $1"; ROOT="$2"; shift 2 ;;
        -i|--include)     [[ $# -ge 2 ]] || die "missing argument to $1"; INCLUDES+=("$2"); shift 2 ;;
        -x|--exclude-dir) [[ $# -ge 2 ]] || die "missing argument to $1"; EXCLUDE_DIRS+=("$2"); shift 2 ;;
        --include-git)    PRUNE_GIT=false; shift ;;
        -c|--count)       COUNT_ONLY=true; shift ;;
        -E|--regex)       REGEX=true; shift ;;
        --text-only)      TEXT_ONLY=true; shift ;;
        --self-test)      SELF_TEST=true; shift ;;
        -h|--help)        usage; exit 0 ;;
        --)               shift; break ;;
        -*)               die "unknown option: $1" ;;
        *)                break ;;
    esac
done

if [[ $# -gt 0 ]]; then
    PATTERN="$1"
    shift
fi
[[ $# -gt 0 ]] && die "unexpected extra argument: $1"

if [[ "$PRUNE_GIT" == "true" ]]; then
    EXCLUDE_DIRS+=(".git")
fi

# Number of files the last sweep() call actually visited.
SWEEP_SCANNED=0

# Build the find expression for the current INCLUDES/EXCLUDE_DIRS settings.
# Result is left in the global array FIND_ARGS.
build_find_args() {
    local root="$1"
    FIND_ARGS=("$root")
    local d
    for d in "${EXCLUDE_DIRS[@]}"; do
        FIND_ARGS+=(-type d -name "$d" -prune -o)
    done
    FIND_ARGS+=(-type f)
    if [[ ${#INCLUDES[@]} -gt 0 ]]; then
        FIND_ARGS+=(\()
        local first=true glob
        for glob in "${INCLUDES[@]}"; do
            [[ "$first" == "true" ]] || FIND_ARGS+=(-o)
            FIND_ARGS+=(-name "$glob")
            first=false
        done
        FIND_ARGS+=(\))
    fi
    FIND_ARGS+=(-print0)
}

# ── The sweep ───────────────────────────────────────────────────────────────
# Walks with find and greps an explicit file list. Neither half consults
# .gitignore, so ignored subtrees are covered like any other.
#
# Prints matching file paths on stdout, one per line. Sets SWEEP_SCANNED.
# Returns 0 if any file matched, 1 if none, 2 if coverage could not be guaranteed.
sweep() {
    local root="$1"
    local pattern="$2"

    if [[ ! -d "$root" ]]; then
        echo "$PROG: root is not a directory: $root" >&2
        return 2
    fi

    build_find_args "$root"

    local listfile errfile
    listfile="$(mktemp)" || { echo "$PROG: mktemp failed" >&2; return 2; }
    errfile="$(mktemp)" || { rm -f "$listfile"; echo "$PROG: mktemp failed" >&2; return 2; }

    find "${FIND_ARGS[@]}" >"$listfile" 2>"$errfile"
    local find_rc=$?

    # An incomplete walk must never read as a clean sweep.
    if [[ $find_rc -ne 0 ]] || [[ -s "$errfile" ]]; then
        echo "$PROG: traversal was incomplete — result is NOT a clean bill of health" >&2
        [[ -s "$errfile" ]] && sed "s|^|$PROG: find: |" "$errfile" >&2
        rm -f "$listfile" "$errfile"
        return 2
    fi

    SWEEP_SCANNED=$(tr -cd '\0' <"$listfile" | wc -c | tr -d ' ')

    if [[ ! -s "$listfile" ]]; then
        rm -f "$listfile" "$errfile"
        return 1
    fi

    local grep_args=(-l)
    [[ "$TEXT_ONLY" == "true" ]] && grep_args+=(-I)
    if [[ "$REGEX" == "true" ]]; then
        grep_args+=(-E)
    else
        grep_args+=(-F)
    fi

    local matches
    matches="$(xargs -0 grep "${grep_args[@]}" -e "$pattern" -- <"$listfile" 2>"$errfile")"

    # grep noise on stderr means some file was not actually read. Same rule as find.
    if [[ -s "$errfile" ]]; then
        echo "$PROG: some files could not be read — result is NOT a clean bill of health" >&2
        sed "s|^|$PROG: grep: |" "$errfile" >&2
        rm -f "$listfile" "$errfile"
        return 2
    fi
    rm -f "$listfile" "$errfile"

    [[ -n "$matches" ]] || return 1
    printf '%s\n' "$matches"
    return 0
}

# Find one directory under $1 that git confirms is ignored. Used to place the
# live control where the defect can actually appear.
first_ignored_dir() {
    local root="$1"
    command -v git >/dev/null 2>&1 || return 0
    [[ -d "$root" ]] || return 0
    local d top
    while IFS= read -r d; do
        [[ -w "$d" ]] || continue
        top="$(git -C "$d" rev-parse --show-toplevel 2>/dev/null)" || continue
        [[ -n "$top" ]] || continue
        if git -C "$top" check-ignore -q "$d" 2>/dev/null; then
            printf '%s\n' "$d"
            return 0
        fi
    done < <(find "$root" -mindepth 1 -maxdepth 4 -type d -name .git -prune -o -type d -print 2>/dev/null)
    return 0
}

# ── Self-test: positive and negative controls, both inside an ignored subtree ─
#
# A positive control that fires OUTSIDE the ignored subtree certifies nothing —
# it validates the probe in a frame where the defect cannot appear. Every control
# below plants its canary INSIDE a gitignored directory, which is the only place
# the defect is observable.
self_test() {
    local pass=0 fail=0
    local tmp
    tmp="$(mktemp -d)" || die "could not create temp dir"
    trap 'rm -rf "$tmp"' EXIT

    local canary="wisp gc --age 1h --force"

    # Hermetic fixture: a repo that gitignores polecats/, with the canary inside it.
    mkdir -p "$tmp/repo/polecats/chrome" "$tmp/repo/visible"
    printf 'polecats/\n' >"$tmp/repo/.gitignore"
    printf 'cmd = "%s"\n' "$canary" >"$tmp/repo/polecats/chrome/mol.formula.toml"
    printf 'cmd = "harmless"\n' >"$tmp/repo/visible/mol.formula.toml"

    local saved_includes=()
    [[ ${#INCLUDES[@]} -gt 0 ]] && saved_includes=("${INCLUDES[@]}")

    echo "== control 1: blind sweep must SEE a canary inside the ignored subtree"
    INCLUDES=("*.formula.toml")
    local hits
    hits="$(sweep "$tmp/repo" "$canary")"
    if [[ "$hits" == *"polecats/chrome/mol.formula.toml" ]]; then
        echo "  PASS: sweep found the canary at $hits"
        pass=$((pass + 1))
    else
        echo "  FAIL: sweep missed the canary (got: ${hits:-<nothing>})"
        fail=$((fail + 1))
    fi

    echo "== control 2: a gitignore-aware search must MISS it (the defect is real)"
    if command -v git >/dev/null 2>&1; then
        git -C "$tmp/repo" init -q >/dev/null 2>&1
        git -C "$tmp/repo" add -A >/dev/null 2>&1
        local gitgrep
        gitgrep="$(git -C "$tmp/repo" grep -l --untracked -F -e "$canary" -- 2>/dev/null || true)"
        if [[ "$gitgrep" != *polecats* ]]; then
            echo "  PASS: gitignore-aware search reported '${gitgrep:-<nothing>}' — blind to polecats/"
            pass=$((pass + 1))
        else
            echo "  FAIL: gitignore-aware search saw polecats/ — fixture does not reproduce the defect"
            fail=$((fail + 1))
        fi
    else
        echo "  SKIP: git not available"
    fi

    echo "== control 3: the live root's ignored subtrees are actually reachable"
    INCLUDES=()
    local live_dir
    live_dir="$(first_ignored_dir "$ROOT")"
    if [[ -n "$live_dir" ]]; then
        local live_canary="$live_dir/.town-sweep-canary.$$"
        if printf '%s\n' "$canary" >"$live_canary" 2>/dev/null; then
            local live_hits
            live_hits="$(sweep "$ROOT" "$canary")"
            rm -f "$live_canary"
            if [[ "$live_hits" == *".town-sweep-canary.$$"* ]]; then
                echo "  PASS: sweep of $ROOT reached the gitignored path $live_dir"
                pass=$((pass + 1))
            else
                echo "  FAIL: sweep of $ROOT did NOT reach the gitignored path $live_dir"
                fail=$((fail + 1))
            fi
        else
            echo "  SKIP: no writable gitignored directory under $ROOT"
        fi
    else
        echo "  SKIP: found no gitignored directory under $ROOT"
    fi

    INCLUDES=()
    [[ ${#saved_includes[@]} -gt 0 ]] && INCLUDES=("${saved_includes[@]}")

    echo
    echo "self-test: $pass passed, $fail failed"
    [[ $fail -eq 0 ]] || return 1
    return 0
}

# ── Resolve root ────────────────────────────────────────────────────────────
if [[ -z "$ROOT" ]]; then
    ROOT="${GT_ROOT:-$PWD}"
fi
ROOT="${ROOT%/}"
[[ -d "$ROOT" ]] || die "root is not a directory: $ROOT"

# ── Dispatch ────────────────────────────────────────────────────────────────
if [[ "$SELF_TEST" == "true" ]]; then
    self_test
    exit $?
fi

if [[ -z "$PATTERN" ]]; then
    usage >&2
    exit 2
fi

RESULT="$(sweep "$ROOT" "$PATTERN")"
RC=$?
[[ $RC -eq 2 ]] && exit 2

HITS=0
[[ -n "$RESULT" ]] && HITS="$(printf '%s\n' "$RESULT" | wc -l | tr -d ' ')"

echo "$PROG: scanned $SWEEP_SCANNED files under $ROOT (gitignore-blind), $HITS matched" >&2

if [[ "$COUNT_ONLY" == "true" ]]; then
    echo "$HITS"
else
    [[ -n "$RESULT" ]] && printf '%s\n' "$RESULT"
fi

exit "$RC"

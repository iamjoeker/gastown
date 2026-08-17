#!/usr/bin/env bash
#
# town-sweep.sh — gitignore-blind content sweep over a Gas Town tree.
#
# WHY THIS EXISTS (gt-emm)
#
# Recursive search in the agent shell respects .gitignore. The agent shell
# shadows `grep` with ugrep and passes `--ignore-files`, which makes every
# recursive search honor .gitignore. Gas Town gitignores every working clone:
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
# The same shadow also passes `-I`, so the interactive `grep` silently skips
# binary files. That is a second silent-skip on the same tool; this script
# searches binaries by default and makes skipping them an explicit opt-in.
#
# This script is the traversal half of a verification sweep. It walks with `find`
# (not gitignore-aware — the shell's `find` shadow is bfs, which does not read
# ignore files either) and hands grep an explicit file list, so the ignore logic
# is bypassed twice and no ignore rule can subtract from its coverage.
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
  town-sweep.sh --self-test [-r ROOT] [--live-control DIR]

OPTIONS
  -r, --root DIR       Root to sweep (default: $GT_ROOT, else current directory)
  -i, --include GLOB   Only scan files whose basename matches GLOB (repeatable)
  -x, --exclude-dir D  Prune directories with this name (repeatable; default: .git)
      --include-git    Do not prune .git directories
  -c, --count          Print only the number of matching files
  -E, --regex          Treat PATTERN as an extended regex (default: fixed string)
      --text-only      Skip binary files (grep -I)
      --self-test      Run the built-in positive/negative controls and exit
      --live-control DIR
                       Self-test only: also plant a live canary in DIR, which must
                       be a gitignored directory in a tree YOU own. Writes and then
                       removes one dot-file. Omitted by default — see SELF-TEST.
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

SELF-TEST
  --self-test builds a hermetic fixture (a repo that gitignores polecats/, with a
  canary inside it) and asserts both halves: this sweep SEES the canary, and a
  gitignore-aware search MISSES it. That is a complete proof of the mechanism and
  it writes only under a fresh mktemp -d.

  --live-control DIR additionally proves that one real tree is reachable. It
  writes a canary INSIDE DIR, which the script verifies is genuinely gitignored —
  a control planted outside an ignored subtree validates the probe in a frame
  where the defect cannot appear, and certifies nothing. Point it at a tree you
  own (your own sandbox), never at another agent's live working directory.
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
LIVE_CONTROL=""
PRUNE_GIT=true
INCLUDES=()
EXCLUDE_DIRS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        -r|--root)        [[ $# -ge 2 ]] || die "missing argument to $1"; ROOT="$2"; shift 2 ;;
        -i|--include)     [[ $# -ge 2 ]] || die "missing argument to $1"; INCLUDES+=("$2"); shift 2 ;;
        -x|--exclude-dir) [[ $# -ge 2 ]] || die "missing argument to $1"; EXCLUDE_DIRS+=("$2"); shift 2 ;;
        --live-control)   [[ $# -ge 2 ]] || die "missing argument to $1"; LIVE_CONTROL="$2"; shift 2 ;;
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

# Outputs of the last sweep() call. sweep() sets globals rather than printing,
# because a command substitution would run it in a subshell and throw the scanned
# count away — reporting "scanned 0 files" for a sweep that read thousands.
SWEEP_SCANNED=0
SWEEP_MATCHES=""

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
# Sets SWEEP_MATCHES (newline-separated paths) and SWEEP_SCANNED.
# Returns 0 if any file matched, 1 if none, 2 if coverage could not be guaranteed.
# MUST NOT be called inside a command substitution — the globals would be lost.
sweep() {
    local root="$1"
    local pattern="$2"

    SWEEP_SCANNED=0
    SWEEP_MATCHES=""

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

    SWEEP_MATCHES="$(xargs -0 grep "${grep_args[@]}" -e "$pattern" -- <"$listfile" 2>"$errfile")"

    # grep noise on stderr means some file was not actually read. Same rule as find.
    if [[ -s "$errfile" ]]; then
        SWEEP_MATCHES=""
        echo "$PROG: some files could not be read — result is NOT a clean bill of health" >&2
        sed "s|^|$PROG: grep: |" "$errfile" >&2
        rm -f "$listfile" "$errfile"
        return 2
    fi
    rm -f "$listfile" "$errfile"

    [[ -n "$SWEEP_MATCHES" ]] || return 1
    return 0
}

# ── Self-test: positive and negative controls, both inside an ignored subtree ─
#
# A positive control that fires OUTSIDE the ignored subtree certifies nothing —
# it validates the probe in a frame where the defect cannot appear. Every control
# below plants its canary INSIDE a gitignored directory, which is the only place
# the defect is observable.
SELF_TEST_TMP=""
self_test_cleanup() {
    [[ -n "$SELF_TEST_TMP" ]] && rm -rf "$SELF_TEST_TMP"
}

self_test() {
    local pass=0 fail=0 skip=0
    SELF_TEST_TMP="$(mktemp -d)" || die "could not create temp dir"
    trap self_test_cleanup EXIT

    local tmp="$SELF_TEST_TMP"
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
    sweep "$tmp/repo" "$canary"
    if [[ "$SWEEP_MATCHES" == *"polecats/chrome/mol.formula.toml" ]]; then
        echo "  PASS: sweep found the canary at $SWEEP_MATCHES"
        pass=$((pass + 1))
    else
        echo "  FAIL: sweep missed the canary (got: ${SWEEP_MATCHES:-<nothing>})"
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
        skip=$((skip + 1))
    fi

    echo "== control 3: a designated live tree's ignored subtree is reachable"
    INCLUDES=()
    if [[ -z "$LIVE_CONTROL" ]]; then
        echo "  SKIP: no --live-control DIR given (controls 1-2 already prove the mechanism)"
        skip=$((skip + 1))
    elif [[ ! -d "$LIVE_CONTROL" || ! -w "$LIVE_CONTROL" ]]; then
        echo "  FAIL: --live-control $LIVE_CONTROL is not a writable directory"
        fail=$((fail + 1))
    elif ! live_dir_is_ignored "$LIVE_CONTROL"; then
        # Refusing here is the point: a control outside an ignored subtree passes
        # trivially and certifies nothing. That is the failure mode that survived
        # the old checklist.
        echo "  FAIL: --live-control $LIVE_CONTROL is not gitignored — a control there proves nothing"
        fail=$((fail + 1))
    else
        local live_canary="$LIVE_CONTROL/.town-sweep-canary.$$"
        if printf '%s\n' "$canary" >"$live_canary" 2>/dev/null; then
            sweep "$ROOT" "$canary"
            local rc=$?
            rm -f "$live_canary"
            if [[ $rc -eq 2 ]]; then
                echo "  FAIL: sweep of $ROOT could not guarantee coverage"
                fail=$((fail + 1))
            elif [[ "$SWEEP_MATCHES" == *".town-sweep-canary.$$"* ]]; then
                echo "  PASS: sweep of $ROOT reached the gitignored path $LIVE_CONTROL"
                pass=$((pass + 1))
            else
                echo "  FAIL: sweep of $ROOT did NOT reach the gitignored path $LIVE_CONTROL"
                fail=$((fail + 1))
            fi
        else
            echo "  FAIL: could not write the canary into $LIVE_CONTROL"
            fail=$((fail + 1))
        fi
    fi

    INCLUDES=()
    [[ ${#saved_includes[@]} -gt 0 ]] && INCLUDES=("${saved_includes[@]}")

    echo
    echo "self-test: $pass passed, $fail failed, $skip skipped"
    [[ $fail -eq 0 ]] || return 1
    return 0
}

# True when git confirms DIR is an ignored path. Used to reject a live control
# planted where the defect cannot appear.
live_dir_is_ignored() {
    local dir="$1" top
    command -v git >/dev/null 2>&1 || return 1
    top="$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null)" || return 1
    [[ -n "$top" ]] || return 1
    git -C "$top" check-ignore -q "$dir" 2>/dev/null
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

[[ -n "$LIVE_CONTROL" ]] && die "--live-control is only meaningful with --self-test"

if [[ -z "$PATTERN" ]]; then
    usage >&2
    exit 2
fi

sweep "$ROOT" "$PATTERN"
RC=$?
[[ $RC -eq 2 ]] && exit 2

HITS=0
[[ -n "$SWEEP_MATCHES" ]] && HITS="$(printf '%s\n' "$SWEEP_MATCHES" | wc -l | tr -d ' ')"

echo "$PROG: scanned $SWEEP_SCANNED files under $ROOT (gitignore-blind), $HITS matched" >&2

if [[ "$COUNT_ONLY" == "true" ]]; then
    echo "$HITS"
else
    [[ -n "$SWEEP_MATCHES" ]] && printf '%s\n' "$SWEEP_MATCHES"
fi

exit "$RC"

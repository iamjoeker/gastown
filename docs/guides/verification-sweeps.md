# Verification Sweeps

**Rule: never verify a Gas Town tree with a recursive `grep`.** It respects
`.gitignore`, Gas Town gitignores every working clone, and the result is a
perfect all-clear over the subtrees most likely to be wrong.

This guide is the recipe. `scripts/town-sweep.sh` is the executable form of it.

---

## The defect (gt-emm)

The agent shell shadows `grep` with `ugrep` and passes `--ignore-files`:

```
ARGV0=ugrep "$claude" -G --ignore-files --hidden -I --exclude-dir=.git ... "$@"
```

`--ignore-files` makes every recursive search honor `.gitignore`. Gas Town's
town-root `.gitignore` excludes exactly the directories that hold checkouts:

```
polecats/    mayor/    refinery/    crew/    deacon/dogs/
```

So a recursive sweep from the town root silently skips ALL polecat sandboxes and
ALL rig clones, and reports clean. Measured on one machine, same pattern, same
instant (2026-08-03):

| traversal | files found |
|---|---|
| `find` + per-file `grep` for `wisp gc --age 1h --force` | **16** |
| `grep -rl` over the same root | **0** |

Zero versus sixteen. The sweep is not merely incomplete — it returns a perfect
all-clear while sixteen stale copies sit on disk, and it fails silently rather
than erroring.

Still live. Re-measured 2026-08-17 with a string that exists today:

```
$ grep -rl --include='*.formula.toml' 'mol-polecat-work' /home/jkerby/src/gt | wc -l
4
$ scripts/town-sweep.sh -r /home/jkerby/src/gt -i '*.formula.toml' -c 'mol-polecat-work'
town-sweep.sh: scanned 767 files under /home/jkerby/src/gt (gitignore-blind), 56 matched
56
```

**Why it is dangerous beyond any one hunt.** The blindness is perfectly
correlated with where risk lives. Working clones are gitignored precisely
BECAUSE they are derived, and derived copies are exactly where stale, divergent
or pre-patch content accumulates. The default verification tool cannot see the
one class of location most likely to be wrong.

A correct probe string delivered by a blind traversal still yields a false clean.
**Getting the pattern right is not enough. The traversal has to be right too.**

### Second silent skip on the same tool

The same shadow also passes `-I`, so the interactive `grep` skips binary files
without saying so. If you are auditing a binary, pipe it: `strings <file> | grep
-c <pattern>` reads stdin and crosses no ignore boundary. `town-sweep.sh`
searches binaries by default and makes skipping them an explicit `--text-only`.

### What is NOT blind

- **`find` is safe.** The shell shadows it with `bfs`, which does not read
  ignore files. A `find`-driven walk covers ignored subtrees.
- **Explicit paths are safe.** `grep pattern path/to/file` never consults
  `.gitignore`. Only the recursive walk does.
- **The load path is not in the blind spot.** The town's own `.beads/formulas/`
  IS reachable by a recursive grep from the town root. Verifications of what
  agents actually load stand; the defect is confined to derived checkouts.

---

## The recipe

### Preferred: the script

```bash
scripts/town-sweep.sh -r "$GT_ROOT" -i '*.formula.toml' 'wisp gc --age 1h --force'
scripts/town-sweep.sh -r "$GT_ROOT" -c 'some string'          # count only
scripts/town-sweep.sh --help
```

It walks with `find` and hands `grep` an explicit file list, so the ignore logic
is bypassed twice. Exit codes are `0` = matches, `1` = none, `2` = **the sweep
could not guarantee coverage**. Treat 2 as "unknown", never as clean: if `find`
or `grep` wrote anything to stderr, the script refuses to print a result at all,
because a partial sweep must never be mistaken for a clean one. It also reports
how many files it actually visited — a census with no denominator is not a census.

### By hand, when the script is not available

```bash
find "$GT_ROOT" -type d -name .git -prune -o -type f -name '*.formula.toml' -print0 \
  | xargs -0 grep -l -F -e 'PATTERN' --
```

Both halves are immune: `find` is not ignore-aware, and `-print0`/`xargs -0`
hands `grep` explicit paths so the ignore logic is bypassed a second time.

Acceptable alternatives:

- `grep` with an explicit file list (`grep -F -e PAT -- $(cat filelist)`).
- A `--no-ignore` equivalent where the tool supports one (`rg --no-ignore
  --hidden`, `ugrep` without `--ignore-files`). Verify the flag actually took
  effect with a control (below) — the shell function may re-add the flag after
  yours.
- `git grep` **only** when you have confirmed the target subtree is tracked in
  the repo you are running it from. Across a town, it is not.

### Never

- `grep -r` / `grep -R` / recursive `ugrep` / `rg` defaults over a town or rig
  tree, for any verification whose conclusion is "N found" or "clean".
- The Grep **tool** for the same purpose — it is ripgrep-backed and respects
  `.gitignore` by default.

---

## Controls

A verification is only as good as the control that certifies it, and the control
is the part that has already failed here.

**A positive control must be planted INSIDE the ignored subtree.** A control
that fires outside one validates the probe in a frame where the defect cannot
appear, and certifies nothing. This happened: a control was run, it fired, and
it proved nothing, because the synthetic file went into a scratchpad outside any
`.gitignore`. "I ran a control" is not sufficient evidence — *where* it ran is
the evidence.

A complete control is a pair:

1. **Positive** — the blind sweep SEES a canary planted inside a gitignored
   directory.
2. **Negative** — a gitignore-aware search MISSES that same canary. Without
   this, you have not shown your traversal is doing anything the broken one
   wasn't.

Both are built in:

```bash
scripts/town-sweep.sh --self-test
```

It builds a hermetic fixture under `mktemp -d` (a repo that gitignores
`polecats/`, canary inside) and asserts both halves. That is the full proof of
mechanism and it touches nothing you own.

To additionally prove that one real tree is reachable:

```bash
scripts/town-sweep.sh --self-test -r <root> --live-control <ignored-dir-in-that-root>
```

The script verifies with `git check-ignore` that the directory really is
ignored, and **fails the control** if it is not — refusing to hand you a
trivially-passing test. It writes one dot-file and removes it.

> **Point `--live-control` at a tree you own.** Never at another agent's live
> sandbox. Writing into a polecat's working directory while it is working is not
> a verification, it is an incident.

---

## Reporting a census

Any count you report from a sweep must carry the traversal that produced it.

```
census: 56 files match 'mol-polecat-work' under /home/jkerby/src/gt
traversal: scripts/town-sweep.sh (find + explicit file list, gitignore-blind)
scanned: 767 files
controls: --self-test 2 passed, 0 failed (canary inside gitignored subtree)
```

A bare "0 found" is not a result. It is indistinguishable from a blind sweep,
and that ambiguity is what produced the false all-clear this guide exists to
prevent.

**A count of zero deserves more scrutiny than a count of many.** Zero is what
the defect produces. Before reporting a clean sweep, confirm the traversal
visited a non-zero number of files, and that its controls passed.

---

## Related

- `scripts/town-sweep.sh` — the executable recipe
- Deacon patrol formula (`mol-deacon-patrol`), "Verification Sweeps" — binds
  this rule to the Deacon's census
- gt-emm — the originating bug, with the full 0-vs-16 reproduction

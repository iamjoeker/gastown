# Lineage in this repo: patch-id, not SHA

**Rule: `main` contains commits that are content-duplicates of other commits in
`main` under different SHAs. Patch-id EQUALITY is the only proof of sameness
here. SHA comparison, commit counts, and merge-base distance all answer the
wrong question.**

This is a known and accepted property of the repository, recorded so the next
person reasoning about lineage does not rediscover it mid-merge (gt-9ff4, filed
at the mayor's request as hq-qxzzj). The tree content on `main` is correct. No
work was lost and nothing needs reverting — this is a DAG property, not a
correctness bug.

---

## What is measurably true

Measured on `origin/main` at `5e84c5b44`, 2026-08-26, over the full history
(no path filter, no `--since`):

| Probe | Result |
|---|---|
| `git rev-list --count origin/main` | 8560 |
| `git rev-list --no-merges origin/main \| wc -l` | 7268 |
| non-merge commits that yield a patch-id | 7233 |
| non-merge commits that yield **no** patch-id (empty commits) | 35 |
| patch-ids occurring more than once | 208 |
| commits sitting in a duplicate-patch-id group | 449 |
| redundant copies (group size minus one, summed) | 241 |

Reproduce with:

```bash
git log --no-merges --format='commit %H' -p origin/main | git patch-id --stable > /tmp/pids
awk '{print $1}' /tmp/pids | sort | uniq -c | sort -rn | awk '$1>1'
```

Read the last three rows as an **upper bound on the phenomenon, not a census of
it**. Patch-id equality proves same content, never same provenance: a revert
followed by a reapply, a change replayed deliberately, and two independently
identical one-line bumps all land in the same bucket as a duplicated lineage.
The number is useful because it is reproducible and because it is small enough
to inspect, not because every row in it is damage.

## How it got there

On 2026-08-23 the refinery merged `gt-wisp-xscm` (deathclaw, gt-vj63) into
`main`. The merge was clean and the tests were green — 109/109, which is the
whole of the refinery's gate — and the submitted head `0a6c4e36` is a proper
ancestor of `main` today. But the branch carried a re-created copy of `main`'s
own history, and the merge brought it in:

```bash
git rev-list --count a3bc0a3d..0a6c4e36   #  350
```

The cause was the gt-lj2n defect: on fork-backed rigs `gt done` rebased MR-bound
polecat branches onto `upstream/main` instead of `origin/main`, so branches grew
private re-created copies of trunk history under new SHAs. That is **fixed** and
landed as `5512b73f` (2026-08-22 21:25), so new branches should stop acquiring
this shape — but the copies already in `main` are permanent unless someone
rewrites a shared branch, which is not the refinery's call.

## The fingerprint

A rebase over a merge leaves a two-commit signature. `main` carries one of each
for the same change:

```
8a8a625c  parent 7bb5e0eb7           fix(git-hygiene): ... (gt-x6ji)
a3bc0a3d  parents 7bb5e0eb7 8a8a625c Merge polecat/brahmin/gt-x6ji+mt50cu5a into main
8127893d  parent 887f02e71           fix(git-hygiene): ... (gt-x6ji)   <- re-sha'd copy
fffcacac  parent 8127893dd           Merge polecat/brahmin/gt-x6ji+mt50cu5a into main   <- EMPTY
```

All four are ancestors of `origin/main`. Two things to notice:

- `8a8a625c` and `8127893d` share the patch-id `23ccf84f…` — same change, two
  SHAs, both in trunk.
- The **merge became an empty single-parent commit.** `a3bc0a3d` has two
  parents; its copy `fffcacac` has one and introduces no diff at all. A merge
  cannot be replayed as a patch, so the rebase flattened it and replayed its
  second parent's content separately. An empty commit whose subject begins
  `Merge … into main` is the tell.

## Four ways the obvious probe lies here

### 1. `git patch-id` emits *nothing* for merges and for empty commits

Not a zero, not an error — no output line at all.

```bash
git show a3bc0a3d | wc -l                          # 12  (header, no diff: it is a merge)
git show a3bc0a3d | git patch-id --stable | wc -l  # 0
git show fffcacac | git patch-id --stable | wc -l  # 0   (empty commit)
```

A loop that pastes a SHA and then reads the patch-id command's output
positionally silently shifts by one and attributes the *next* commit's patch-id
to this one. 35 of `main`'s 7268 non-merge commits do this, plus every merge.
Always key on the SHA `git patch-id` prints in its own second column, and treat
an absent line as UNKNOWN rather than as a mismatch. This is a false zero of
exactly the kind [`false-zero-queries.md`](false-zero-queries.md) catalogues.

To get a merge's first-parent patch-id anyway:

```bash
git show -m --first-parent a3bc0a3d | git patch-id --stable
# 23ccf84f… a3bc0a3d572778e15e46d3bcea0d2112e64a89c0
```

### 2. Patch-id INEQUALITY is not evidence of divergence

Patch-ids legitimately differ when the same change is replayed onto a different
base and the context lines move. Only equality proves anything. Inequality is a
prompt to read the diff, never a verdict — the same asymmetry `gt patrol
branches --deletable` is built on (gt-l65a).

### 3. `git cherry` says nothing about a commit already contained by SHA

```bash
git cherry -v origin/main 8127893dd 887f02e71   # prints nothing, rc=0
git cherry -v 8a8a625c3  8127893dd 887f02e71   # - 8127893dd… fix(git-hygiene): …
```

`git cherry` only lists commits reachable from `<head>` but not from
`<upstream>`. A commit that is already an ancestor of upstream is dropped
entirely, so empty output is ambiguous between "fully contained", "range empty"
and "nothing to say" — and it will never surface duplicates that live *inside*
upstream. Empty `git cherry` output is not an all-clear about `main`'s internal
shape.

### 4. Divergence counts overstate by hundreds

Do not infer "this work is missing from `main`" from a large
`git rev-list --count origin/main..<branch>`. A branch showing ~700 commits of
divergence at merge-base `f069df60` is carrying the duplicated lineage, not 700
commits of real work. This is what put two branches in front of the refinery
that then conflicted in 17 and 12 files with their merge base 700 commits back
(gt-0w2l), and what produced chrome's four phantom conflicts against a base that
was not the current one (gt-82cw) — the merge-base had moved `867a02e5` →
`fffcacac`.

## What to do instead

| Question | Ask it this way |
|---|---|
| Is this change in `main`? | patch-id equality, or `git cherry <target> <branch>` reading `-` rows |
| Is this *commit* in `main`? | `git merge-base --is-ancestor <sha> origin/main` |
| Does this branch carry real work? | compare patch-id sets, not commit counts |
| Will it merge? | `gt mq list <rig> --merge-check` — a rehearsal, not a count |
| Is a big divergence real? | check the merge-base and look for empty `Merge … into main` commits before believing it |

## The open question

Whether `main`'s duplicated pairs should be cleaned up is deliberately not acted
on. It would require history rewriting on a shared branch, which needs the mayor
and a human. Recording the property is the safe action; this guide is that
record.

## See also

- [`false-zero-queries.md`](false-zero-queries.md) — probes that answer zero
  because they cannot see, not because there is nothing there
- [`verification-sweeps.md`](verification-sweeps.md) — why recursive search is
  blind across a town tree
- [`durable-records.md`](durable-records.md) — `gt:record`, the label that keeps
  a bead like gt-9ff4 from being dispatched as work

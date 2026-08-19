# Formula Resolution Architecture

> **Status: three-tier resolution is IMPLEMENTED and live.** The rig > town >
> embedded order described below is what `gt prime` actually executes today, via
> `formula.ResolveFormulaContent()` in `internal/formula/embed.go`. Do not read
> the tier table as aspirational — a disk copy at `.beads/formulas/` shadows the
> binary's embedded formula, with no rebuild involved. Still planned: the
> `--tier` / `--resolve` flags, tier labels in `formula list`, Mol Mall
> integration, and HOP federation.
>
> For the operational consequences of editing a resolved formula file — what
> survives a rebuild, and when `gt upgrade` will silently overwrite your edit —
> see [Editing a Formula File Directly](directives-and-overlays.md#editing-a-formula-file-directly).

> Where formulas live, how they're found, and how they'll scale to Mol Mall

## The Problem

Formulas exist in multiple locations:
- `internal/formula/formulas/` (source of truth, embedded in binary)
- `.beads/formulas/` (provisioned at runtime by `gt install`)
- Crew directories have their own `.beads/formulas/` (diverging copies)

For the formula text `gt prime` renders to an agent, the precedence below is now
enforced in code. What is still unclear is `bd`'s own lookup, which `gt formula
list`/`show` delegate to and which uses a **different, four-entry** search order
(run `bd formula list --help` for the authoritative list). Two different
resolvers can therefore disagree about which file "the" formula is — so when
auditing a live town, checksum the file that `ResolveFormulaContent()` would
pick rather than the first copy you find.

## Design Goals

1. **Predictable resolution** - Clear precedence rules
2. **Local customization** - Override system defaults without forking
3. **Project-specific formulas** - Committed workflows for collaborators
4. **Mol Mall ready** - Architecture supports remote formula installation
5. **Federation ready** - Formulas are shareable across towns via HOP (Highway Operations Protocol)

## Three-Tier Resolution

```
┌─────────────────────────────────────────────────────────────────┐
│                     FORMULA RESOLUTION ORDER                     │
│                    (most specific wins)                          │
└─────────────────────────────────────────────────────────────────┘

TIER 1: PROJECT (rig-level)
  Location: <project>/.beads/formulas/
  Source:   Committed to project repo
  Use case: Project-specific workflows (deploy, test, release)
  Example:  ~/gt/gastown/.beads/formulas/mol-gastown-release.formula.toml

TIER 2: TOWN (user-level)
  Location: ~/gt/.beads/formulas/
  Source:   Mol Mall installs, user customizations
  Use case: Cross-project workflows, personal preferences
  Example:  ~/gt/.beads/formulas/mol-polecat-work.formula.toml (customized)

TIER 3: SYSTEM (embedded)
  Location: Compiled into gt binary
  Source:   internal/formula/formulas/ at build time
  Use case: Defaults, blessed patterns, fallback
  Example:  mol-polecat-work.formula.toml (factory default)
```

### Resolution Algorithm

As implemented in `formula.ResolveFormulaContent()`
(`internal/formula/embed.go`). Note that the rig tier is an explicit
`townRoot` + `rigName` join, **not** a walk-up from the working directory — the
caller supplies both, and a tier is skipped when its input is empty:

```go
func ResolveFormulaContent(name, townRoot, rigName string) ([]byte, error) {
    filename := name + ".formula.toml" // if not already suffixed

    // Tier 1: rig-level (most specific)
    if townRoot != "" && rigName != "" {
        path := filepath.Join(townRoot, rigName, ".beads", "formulas", filename)
        if content, err := os.ReadFile(path); err == nil {
            return content, nil
        }
    }

    // Tier 2: town-level
    if townRoot != "" {
        path := filepath.Join(townRoot, ".beads", "formulas", filename)
        if content, err := os.ReadFile(path); err == nil {
            return content, nil
        }
    }

    // Tier 3: embedded (system fallback)
    return GetEmbeddedFormulaContent(name)
}
```

Because tier 1 is a plain join rather than a walk-up, a formula file sitting in
a crew or polecat worktree's own `.beads/formulas/` is **not** on this path and
never resolves. Only the rig root's copy counts.

### This Is Not the Only Resolver

`gt prime` (`internal/cmd/prime_molecule.go`) is the sole production caller of
`ResolveFormulaContent()`. Other paths resolve differently, and the differences
are load-bearing:

| Path | Resolver | Order |
|------|----------|-------|
| `gt prime` step rendering | `ResolveFormulaContent()` | rig → town → embedded |
| `bd cook` at sling time | `bd`'s own search (gt shells out); `internal/cmd/sling_helpers.go` extracts the embedded copy to a temp file only if `bd` finds nothing | `bd formula list --help` |
| `compose.expand` sub-formulas | `formula.loadFormulaByName()` | **embedded first**, then search paths |
| `gt formula list` / `show` | delegated to `bd` | `bd formula list --help` |

The `compose.expand` inversion is the surprising one: an expansion formula
pulled in by another formula takes the *embedded* copy even when a disk override
exists. Unifying these onto one resolver is unfinished work.

### Why This Order

**Project wins** because:
- Project maintainers know their workflows best
- Collaborators get consistent behavior via git
- CI/CD uses the same formulas as developers

**Town is middle** because:
- User customizations override system defaults
- Mol Mall installs don't require project changes
- Cross-project consistency for the user

**System is fallback** because:
- Always available (compiled in)
- Factory reset target
- The "blessed" versions

## Drift: When the Executing Copy Is Not the Shipped Default

The precedence above has a consequence that has bitten repeatedly: because a
disk copy shadows the embedded one, **a fix merged into the embedded corpus
never reaches a town that has a disk copy**. Rebuilding gt only changes tier 3.

Worse, it cannot self-heal. `UpdateFormulas` compares each disk file against the
hash recorded in `.beads/formulas/.installed.json`; if they differ it classifies
the file as user-modified and skips it rather than clobbering local edits. A town
whose copy was ever hand-edited is pinned to that edit permanently. gt-kr6's P1
witness fix sat inert for two days looking done, because `git log` and the
embedded corpus both showed it landed (gt-0wm7).

`formula.ResolveFormula()` answers "is what will run the thing we just shipped?"
alongside "what will run". It classifies the executing copy:

| Kind | Meaning | Repair |
|------|---------|--------|
| `outdated` | Unmodified older install; a newer default has shipped | `gt doctor --fix` |
| `untracked` | No install hash recorded; gt cannot tell stale from deliberate | `gt doctor --fix` (town tier only) |
| `pinned` | Locally edited **and** the default moved afterwards | human merge |
| `customized` | Locally edited, default has not moved — **not drift** | none needed |

`customized` is deliberately excluded from the warning: nothing is being missed
there, and a warning that fires on every deliberate customization gets ignored.

Where it surfaces:

- **`gt prime`** prints a drift notice inline with the formula steps whenever the
  copy about to be executed shadows a newer default. Agents read the checklist,
  so the warning has to travel with it.
- **`gt doctor`** reports the count and names the files.
- **`gt upgrade`** calls out pinned formulas after updating, which is the moment
  a shipped fix is most likely to be assumed live.
- **`gt formula drift`** lists them, prints the shipped default for diffing
  (`--embedded`), and offers the two reconcile outcomes: `--accept-embedded`
  discards the local edits, `--mark-reconciled` keeps them and records the
  current embedded hash as the new baseline.

`--mark-reconciled` is the piece that makes hand-merging terminate. Without it a
file a human has already merged keeps reporting as pinned forever, because the
recorded hash still names the pre-fix default.

## Formula Identity

### Current Format

```toml
formula = "mol-polecat-work"
version = 4
description = "..."
```

### Extended Format (Mol Mall Ready)

```toml
[formula]
name = "mol-polecat-work"
version = "4.0.0"                          # Semver
author = "steve@gastown.io"                # Author identity
license = "MIT"
repository = "https://github.com/steveyegge/gastown"

[formula.registry]
uri = "hop://molmall.gastown.io/formulas/mol-polecat-work@4.0.0"
checksum = "sha256:abc123..."              # Integrity verification
signed_by = "steve@gastown.io"             # Optional signing

[formula.capabilities]
# What capabilities does this formula exercise? Used for agent routing.
primary = ["go", "testing", "code-review"]
secondary = ["git", "ci-cd"]
```

### Version Resolution

When multiple versions exist:

```bash
bd cook mol-polecat-work          # Resolves per tier order
bd cook mol-polecat-work@4        # Specific major version
bd cook mol-polecat-work@4.0.0    # Exact version
bd cook mol-polecat-work@latest   # Explicit latest
```

## Crew Directory Problem

### Current State

Crew directories (`gastown/crew/max/`) are git worktrees of the rigged repo. They have:
- Their own `.beads/formulas/` (from the worktree)
- These can diverge from `mayor/rig/.beads/formulas/`

### The Fix

Crew should NOT have their own formula copies. Options:

**Option A: Symlink/Redirect**
```bash
# crew/max/.beads/formulas -> ../../mayor/rig/.beads/formulas
```
All crew share the rig's formulas.

**Option B: Provision on Demand**
Crew directories don't have `.beads/formulas/`. Resolution falls through to:
1. Town-level (~/gt/.beads/formulas/)
2. System (embedded)

**Option C: Gitignore Exclusion**
Exclude `.beads/formulas/` from crew worktrees via `.gitignore`.

**Recommendation: Option B** - Crew shouldn't need project-level formulas. They work on the project, they don't define its workflows.

## Commands

### Existing

```bash
bd formula list              # Available formulas (should show tier)
bd formula show <name>       # Formula details
bd cook <formula>            # Formula → Proto
```

### Enhanced

```bash
# List with tier information
bd formula list
  mol-polecat-work          v4    [project]
  mol-polecat-code-review   v1    [town]
  mol-witness-patrol        v2    [system]

# Show resolution path
bd formula show mol-polecat-work --resolve
  Resolving: mol-polecat-work
  ✓ Found at: ~/gt/gastown/.beads/formulas/mol-polecat-work.formula.toml
  Tier: project
  Version: 4

  Resolution path checked:
  1. [project] ~/gt/gastown/.beads/formulas/ ← FOUND
  2. [town]    ~/gt/.beads/formulas/
  3. [system]  <embedded>

# Override tier for testing
bd cook mol-polecat-work --tier=system    # Force embedded version
bd cook mol-polecat-work --tier=town      # Force town version
```

### Future (Mol Mall)

```bash
# Install from Mol Mall
gt formula install mol-code-review-strict
gt formula install mol-code-review-strict@2.0.0
gt formula install hop://acme.corp/formulas/mol-deploy

# Manage installed formulas
gt formula list --installed              # What's in town-level
gt formula upgrade mol-polecat-work      # Update to latest
gt formula pin mol-polecat-work@4.0.0    # Lock version
gt formula uninstall mol-code-review-strict
```

## Migration Path

### Phase 1: Resolution Order (Now)

1. Implement three-tier resolution in `bd cook`
2. Add `--resolve` flag to show resolution path
3. Update `bd formula list` to show tiers
4. Fix crew directories (Option B)

### Phase 2: Town-Level Formulas

1. Establish `~/gt/.beads/formulas/` as town formula location
2. Add `gt formula` commands for managing town formulas
3. Support manual installation (copy file, track in `.installed.json`)

### Phase 3: Mol Mall Integration

1. Define registry API (see mol-mall-design.md)
2. Implement `gt formula install` from remote
3. Add version pinning and upgrade flows
4. Add integrity verification (checksums, optional signing)

### Phase 4: Federation (HOP)

1. Add capability tags to formula schema
2. Track formula execution for agent accountability
3. Enable federation (cross-town formula sharing via Highway Operations Protocol)
4. Author attribution and validation records

## Related Documents

- [Mol Mall Design](mol-mall-design.md) - Registry architecture
- [Molecules](../concepts/molecules.md) - Formula → Proto → Mol lifecycle

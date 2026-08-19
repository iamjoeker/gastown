# Configuration Layers

Gas Town reads configuration from several independent mechanisms. This page
explains the mechanisms and how they interact. It deliberately does **not** list
the keys — a hand-maintained key list drifts, and drifting is the failure this
page exists to prevent. Ask the town instead:

```bash
gt config list          # keys some layer has set
gt config list --all    # every key, including the ones sitting at their default
gt config list --json   # machine-readable; diff it between towns, check it in
```

## Why an unset key is not a missing key

A key nobody has set still has a value, and that value can change how the whole
town behaves. The canonical example:

```
scheduler.max_polecats = -1   (the default)
```

`-1` means **direct dispatch**: the scheduler only runs beads that were
explicitly slung and never pulls from the ready queue. A town with 56 ready
beads reports `Scheduled: 0 total, 0 ready` and nothing moves. The town is not
broken; it is unconfigured — and until `gt config list` existed there was no way
to tell those apart, or to discover that the setting existed at all.

## The layers

Ordered by precedence, lowest first. Precedence only ever compares two copies of
the *same key* in the *same scope*; the layers are not one global stack.

| Layer | Where | Scope |
|-------|-------|-------|
| `default` | compiled into the `gt` binary | — |
| `beads-yaml` | `<namespace>/.beads/config.yaml` | one beads namespace |
| `formula-var` | `.beads/formulas/*.formula.toml` `[vars.*]` | one formula |
| `town-settings` | `<town>/settings/config.json` | the town |
| `rig-settings` | `<town>/<rig>/settings/config.json` | one rig |
| `daemon-json` | `<town>/mayor/daemon.json` | the town's patrols |
| `dolt-config` | the `config` table inside a Dolt database | one beads namespace |
| `git-config` | `git config beads.*` in the repo owning a namespace | one beads namespace |
| `env` | exported `GT_*`, `BEADS_*`, `BD_*`, `DOLT_*` | the process |

A **scope** is the object a key configures: `town/settings`, `town/daemon`,
`rig/gastown`, `beads/hq`, `formula/mol-wisp-gc`, `env`. Two layers can only
shadow each other inside one scope, which is why `config.yaml`, `git config` and
the Dolt config table share the `beads/<database>` scope: they hold the same key
names and one of them quietly wins.

## Shadowing

The three beads layers overlap, and the higher one wins silently. Measured:

```
$ bd config unset routing.mode
Unset routing.mode (in config.yaml)      # reported success
                                          # changed nothing — the Dolt row was
                                          # the acting value all along
```

`gt config list` marks every key that more than one layer supplies:

```
beads/hq
  KEY           VALUE         DEFAULT  SOURCE
  beads.role    maintainer    —        git-config
    └ shadowed  maintainer             beads-yaml @ /home/you/gt/.beads/config.yaml
```

The `└ shadowed` line is the copy that has no effect. Editing or unsetting it
will report success and change nothing.

## Reading the output

- **VALUE** — what the code gets when it reads this key today.
- **DEFAULT** — the compiled-in fallback. Read out of the code by calling the
  same accessor production code calls, not from a docs table, so it cannot
  disagree with the binary. `—` means the code has no default for that key.
- **SOURCE** — the layer that supplied VALUE, or `default` when nothing did.

The **Layers** table at the top reports every layer with its read status:
`ok`, `--` (absent), or `FAIL`. This is what makes a short listing
interpretable. If any layer failed, the command names it and exits non-zero — an
empty listing that means "I could not reach Dolt" must never look like
"nothing is set".

```bash
gt config list --no-dolt   # list offline; the Dolt layer reports absent, not ok
```

## Where the key list comes from

The listing is derived, never curated:

- Struct-backed layers (`town-settings`, `rig-settings`, `daemon-json`) are
  reflected over their `json` tags, so a field added to a config struct appears
  without anyone editing a list.
- File and table layers (`beads-yaml`, `dolt-config`, `git-config`,
  `formula-var`, `env`) are read key-by-key from the source, so keys Gas Town has
  no struct for are still listed.

`internal/cmd/config_list_test.go` asserts that every key `gt config set`
accepts appears in the listing. Adding a setter key without extending that map
fails the test.

## Changing a value

`gt config set <key> <value>` writes the town-level layers (`settings/config.json`
and `mayor/daemon.json`) and uses friendly aliases: `lifecycle.reaper.interval`
writes `patrols.wisp_reaper.interval`. `gt config set --help` lists what it
accepts. Beads-namespace keys are owned by `bd config`; check afterwards with
`gt config list --scope beads/<database>` that the layer you wrote is the one
that wins.

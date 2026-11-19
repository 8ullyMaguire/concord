# Step 8 — delete the corpses

Date: 2026-11-19
Status: spec accepted; plan in `.hermes/plans/2026-11-19_043000-step8-corpses.md`.

## Problem

Measured at `8357c02`:
```
0 polaris-healthz
0 polaris-ledger
0 polaris-slug
1 polaris-authz
1 polaris-comments
2 polaris-ratelimit
51 polaris-core
```
The three zero-count crates are workspace members and workspace dependencies, so they compile, appear in `Cargo.lock`
and can drift, but nothing imports them. A copy that will drift and cannot be told from a dependency is the exact
risk ADR 0013 accepted temporarily.

## Goals

1. Delete `vendor/polaris-healthz`, `vendor/polaris-ledger`, `vendor/polaris-slug` from the tree.
2. Remove their workspace member entries and workspace dependency entries; regenerate `Cargo.lock`.
3. Add a gate check that no remaining vendored crate has zero importers, so a new corpse fails the gate.
4. Keep the four used vendored crates (core, authz, comments, ratelimit) untouched; ADR 0013 stays in force until the
   last one is published or removed.

## Non-goals

- Publishing vendored crates to the family forge; that needs a forge token `[REDACTED]` (owner: user), but deletion is
  the other allowed branch of this roadmap row, so this row is not blocked.
- Removing `polaris-ratelimit` (2 importers) or any other used crate.
- Rewriting ADR 0013 while used copies remain.

## Decisions

- D-123: delete the three unused vendored crates rather than publish them. Deletion is the smaller reversible change
  and needs no credential; `git` keeps the history if a crate is needed again.
- D-124: the corpse gate counts importers by directory name with `-`/`_` normalisation, matching the roadmap's
  verification command exactly; it fails on 0 for any remaining vendored crate.

## Success criteria

1. `ls vendor` shows four crates plus README.md; the three deleted directories are gone.
2. `for c in vendor/*/; do …; done` prints nonzero counts for all four remaining crates.
3. `cargo check --workspace --locked` passes with the regenerated lockfile.
4. Gate `PASS`, `corpse check: PASS — 4 vendored crates, 0 with zero importers`, `pages: 132/132 ok`.
5. Stamped deploy, live version, CI green.

## Order

1. Add row 8 and D-123/D-124; commit spec and plan.
2. Delete directories and Cargo.toml entries; regenerate lockfile; run the roadmap verification command.
3. Add the corpse check to `scripts/gate.sh`; prove RED by temporarily adding an unused vendored crate directory, then
   remove it and prove GREEN.
4. Gate, deploy, live probe, docs/vault/handoff.

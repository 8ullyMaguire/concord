# 0013 — the estate crates are vendored into this repository

Date: 2026-11-15
Status: accepted

## Problem

Concord's workspace depended on seven crates by path:

    polaris-core-rust   polaris-healthz   polaris-authz   polaris-ledger
    polaris-ratelimit   polaris-slug      polaris-comments

Every one of them lived in a sibling directory of the checkout, and none of them existed in any repository — `git remote -v`
printed nothing in all seven, and the repository URL `polaris-authz` names (`https://git.polarisocial.xyz/hirrolot19/polaris-authz`)
answers `Repository not found`. Together they are 4,052 lines.

The consequence is not theoretical. A clean clone cannot compile:

    error: failed to load manifest for workspace member `…/crates/db`
    Caused by: failed to load manifest for dependency `polaris-authz`
    Caused by: failed to read `…/polaris-authz/Cargo.toml`
      No such file or directory (os error 2)

That is exactly what GitHub's gate reported on its only run (2026-11-14, `70890ab`), and it is why CI has never been green:
the gate could not reach step 1. It also means the service cannot be rebuilt on another host, and cannot be rebuilt at all if
that one directory is lost.

## Decision

Copy the seven crates into `vendor/<crate>`, verbatim, as workspace members, and point the workspace dependencies at them.
`crates/db`'s two four-level paths become `polaris-comments.workspace = true` and `polaris-authz.workspace = true`, so each
dependency has one spelling.

The copies were checked with `diff -r` at the moment of the move (excluding `target/`, `.git/`, `Cargo.lock`, and the nested
duplicate of `polaris-authz` which is byte-identical to its root `src/`): **all seven verbatim**. No code changed, so nothing
was forked — there is no upstream to diverge from. `vendor/Cargo.lock` files are deliberately not copied: the workspace has
one lockfile.

`polaris-authz`'s `repository` field still names a remote that does not exist. It is left as-is rather than edited: a vendored
copy should be recognisable as a copy, and the provenance note in `vendor/README.md` says where it came from and when.

## Alternatives that lost

* **Publish each crate to Forgejo and use git dependencies.** The estate's shape, and the right destination eventually. It loses
  now on two counts: it needs seven repositories created (a naming and ownership decision, not mine), and GitHub's CI cannot
  read a private Forgejo repository without a credential that does not exist yet. Vendoring unblocks CI today and is reversible —
  swapping `path = "vendor/x"` for `git = …` is mechanical.
* **Give CI a token and clone the siblings.** Same credential problem, and it leaves the build unreproducible for anybody
  without that token — the defect is that the code is not in the repository, so a workaround that fetches it from somewhere
  else keeps it out of the repository.
* **Make the gate skip what it cannot build.** A gate that runs half of itself and reports green is worse than a red one.

## Consequences

* A clean clone now compiles, so CI can run the gate it was written for.
* `cargo test --workspace` also runs the vendored crates' own tests (authz's five suites, ledger's, ratelimit's). They were
  never run by anything before; if one of them is red, the gate will now say so.
* If a vendored crate ever gains its own repository, this ADR is the thing to supersede: the change is `vendor/x` → `git = …`
  in one manifest with the workspace inheriting it everywhere else.
* Orbital depends on the same siblings by path. It is untouched by this ADR and still builds from its own tree; the crates now
  exist in two places until they are published, which is a duplication this ADR accepts deliberately rather than hides.

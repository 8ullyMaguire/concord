# vendored estate crates

Seven crates that Concord depends on, copied here on 2026-11-15 because they existed only as sibling directories outside this
repository and in no repository at all (ADR 0013). A clean clone could not compile without them, which is the failure GitHub's
gate had been reporting since 2026-11-14.

| directory | source path (thinkcentre) | lines | its own commits | remote |
|---|---|---|---|---|
| `polaris-core` | `/personal/documents/code/polaris-core-rust` | 1,044 | 5 | none |
| `polaris-healthz` | `/personal/documents/code/polaris-healthz` | 156 | 1 | none |
| `polaris-authz` | `/personal/documents/code/polaris-authz` | 1,249 | 2 | none (`repository` names a Forgejo repo that answers 404) |
| `polaris-ledger` | `/personal/documents/code/polaris-ledger` | 466 | 4 | none |
| `polaris-ratelimit` | `/personal/documents/code/polaris-ratelimit` | 592 | 2 | none |
| `polaris-slug` | `/personal/documents/code/polaris-slug` | 169 | 1 | none |
| `polaris-comments` | `/personal/documents/code/polaris-comments` | 376 | 3 | none |

4,052 lines total. Each copy was compared with `diff -r` against its source at the moment of the move, excluding `target/`,
`.git/`, `Cargo.lock` (the workspace has one lockfile) and, for `polaris-authz`, its nested `polaris-authz/` directory, which is
byte-identical to its root `src/`. All seven compared **verbatim**: no code was edited on the way in.

Rules for this directory:

* **Edit nothing here without a reason that belongs to Concord.** These are copies; a change made here is a change the estate
  cannot see. If the crate needs to change, the change belongs in the crate's own repository once it has one — and then this ADR
  is superseded by a `git = …` dependency.
* **Do not split a vendored crate's tests out.** They run under `cargo test --workspace`, which is the only place they have ever
  run.
* The `repository` fields that name a Forgejo remote which does not exist are left as they are: a copy should look like a copy.

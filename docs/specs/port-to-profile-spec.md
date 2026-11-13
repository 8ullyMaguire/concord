# Spec — port Concord onto the estate profile

**Status: spec'd and planned, not started.** Owner confirmed all twelve decisions on
2026-11-13. The decision record is `code/estate/docs/adr/0011-concord-joins-the-profile.md`;
the executable order is `docs/plans/port-to-profile.md`.

## 1. Problem

Concord is the only live product on the estate declared `profile = "reference"` while not being
on the reference profile (`~/code/estate/docs/STACK.md` §1). `estate.toml` records the mismatch
as a `reason` on the `[site.stack]` row, which is what gate 17 accepts in place of agreement.

Two things follow from that, and both are costs rather than embarrassments:

- **The family's decision arithmetic lives outside the family.** Arenas, Glicko2, quorum and the
  moderation vocabulary are in Concord, in Go, and no `polaris-*` crate owns them (all 25
  checkouts were checked). The next product that needs pairwise ranking writes it again.
- **Two family crates are hand-restated here.** `internal/ledger` re-implements
  `polaris-ledger`'s rules in Go and calls the result conformance (estate backlog row 419);
  the rate limiter, slug rule and health answer are local copies of crates that exist.

Measured on the tree, 2026-11-13:

| what | how much |
|---|---|
| backend | `93,000` lines of Go, three direct dependencies (chi v5, sqlcipher-free sqlite, x/crypto) |
| schema | `39` migration files, `91` tables, SQLite, FTS5 in three places |
| data | `73` projects, `5,760` features, `2,402` complaints, `88` accounts |
| frontend | `1,602` lines of `html/template`, `12,850` lines of hand-written JS |
| tests | `1,296` Go tests, `326` Playwright tests in `28` suites, `60` UI mutants, `33` Go mutation arms |
| binaries | `8` — `concord` plus capableseed, embedbackfill, forgeseed, granttrust, healthsync, proposalsweep, statsnapshot |

## 2. Goals

1. `concord.polarisocial.xyz` served by a Rust/Axum service on the reference profile, with the
   Go service retired and its `reason` deleted from `estate.toml`.
2. **No visible regression.** The parity corpus (§4) replays byte-identical, and the Playwright
   suite — unchanged except where a deliberate API change is asserted — passes against the new
   service.
3. `polaris-ledger`, `polaris-authz`, `polaris-ratelimit`, `polaris-slug`, `polaris-healthz` and
   `polaris-core` imported where Concord hand-rolled the equivalent, so gate 19
   (`check-duplication`) and gate 45 (`check-authz`) and gate 46 (`check-ratelimit`) pass for
   this host.
4. A site-level `GET /api/v1/capabilities` and the estate error envelope, so gates 5, 18 and 23b
   can see Concord the same way they see every other member.
5. The arena/Glicko2/quorum engine extracted as `polaris-ranking`, with Concord as its first
   adopter and the estate's other products able to adopt it without a rewrite.

## 3. Non-goals

- **No product changes during the port.** V6 (milestone-level consensus) is spec'd, planned and
  deferred: it is the first feature on the new engine, not a Go feature to be ported twice.
  Exception: if the port slips past six weeks, V6 ships in Go first — an idea that waits on a
  refactor is worse than a refactor that waits on an idea.
- **No data-model redesign.** The port moves the schema and the rows; it does not renormalise
  complaints, merge arenas, or rename the domain. Anything of that kind is a separate spec.
- **No new frontend design.** SvelteKit renders the same pages with the same words, adopting
  `@polaris/ui` tokens where a token exists.
- **No shared database.** One cluster, one product, per `STACK.md` §1 ("no cross-database joins,
  no shared tables").
- **No rewrite of the Playwright suite** (except the deliberate `/api/v2` assertions). It is the
  gate, not the work.
- **No `polaris-boards` adoption.** It is an imageboard system; Concord's board is a kanban of
  features. The name collision is recorded in ADR 0011 so a later session does not "reuse" it.
- **No new comment system.** Concord's threaded comments stay as they are in the port; adopting
  `polaris-comments` for their rules is a post-port row, not a rewrite inside the port.

## 4. The parity corpus — how "no visible regression" is measured

A port of this size cannot be judged by its own unit tests: a ported test proves the port agrees
with itself. The corpus is the outside view.

1. **Record.** A recorder proxies `/api/v1` on the live host for one week, storing
   `(method, path, body-shape, status, headers-of-interest, response-body)` per distinct shape,
   deduplicated like W11 phase 0 does for Stash's GraphQL documents.
2. **Freeze.** The corpus is checksummed and committed outside the repo
   (`/var/lib/estate/parity/concord/`), with two independent recordings diffed against each other
   first — a harness that cannot reproduce the present cannot judge the future.
3. **Replay.** `cargo run -p concord-parity -- replay <corpus>` issues each recorded request
   against the candidate service on a scratch port and diffs the JSON structurally (key order and
   timestamps normalised, ids compared by shape unless the corpus names them).
4. **Gate.** The slice is done when its subset of the corpus is byte-identical and its Playwright
   suites are green. The whole corpus is green before the cutover.

The deliberate divergences are enumerated in the corpus itself as `expected_change` entries, one
per route, each naming the ADR decision that caused it (envelope shape, `public_id`, capabilities).

## 5. Slices

Five vertical slices, in this order, each shipped to the live port only when green:

| # | slice | surfaces | definition of done |
|---|---|---|---|
| S1 | projects, documents, board | `/api/v1/projects*`, documents, board columns/cards, project page | corpus subset + `projects_e2e`, `board_e2e`, `pages_e2e` green |
| S2 | ideas, complaints, ranking | features, complaints, links, milestones, roadmap, deps, arenas, rank page | corpus subset + `rank_*`, `milestones_e2e`, `feature_*` green |
| S3 | consensus, governance, ledger | consensus calls, positions, objections, close, decision record, audit log | corpus subset + `consensus_*`, `audit_e2e` green; gate 49 |
| S4 | moderation, inbox, notifications | moderation acts, modlog, notifications, curation queue | corpus subset + `notifications_e2e`, `queue_e2e` green; gates 22, 45 |
| S5 | stats, feeds, admin binaries | `/stats`, feeds (xml/atom/json), healthsync/proposalsweep/statsnapshot | corpus + `stats_e2e`, feed parse; gates 4, 5, 47; timers migrated to `polaris-loops` |

Then **P6 — the frontend** (SvelteKit + `@polaris/ui`, server-rendered head), and **P7 — cutover**
(tunnel repoint, `estate.toml` rows, `reason` deleted, Go tree archived).

## 6. Risks, each with the thing that catches it

- **The port drifts from the live data.** The corpus is recorded from the live host, and each
  slice's cutover re-runs it against the live candidate. A slice that cannot be replayed is not
  done.
- **Two writers on one schema during a cutover.** Feature freeze during each cutover window,
  sev bugs excepted; the Go service keeps the SQLite file until the slice is green.
- **The frontend is the long pole** (suki's survey quoted 2–3 months for 111 templates). Mitigation:
  the pages are ported by *surface* and each page ships when its data route does, not at the end.
- **Identity migration loses an account.** 88 accounts move to `polaris-id` on first login by
  email; the local table stays readable for one release so nothing is unrecoverable.
- **The estate's profile says PG 18 and this host runs 16.** The clusters on thinkcentre are
  `16.15` (main 5432, boards 55442, suki 55440, suki-test 55441). Two options are recorded in the
  plan's M0: install 18 from PGDG and give Concord its own 18 cluster (the profile's answer), or
  run 16 and declare a `[site.stack]` deviation with a reason (the gate's answer). The decision
  rule is in the plan, not left to whoever is typing.

## 7. Open questions for the owner

1. **Does Concord keep its slugs as the public key** (`/projects/concord`) while ULIDs become the
   API key, or do slugs retire? Recommend: keep them — they are the estate's own convention
   ("Slugs exist for humans but are never the API key", W0 §3).
2. **Do the 88 local accounts get a claim path** (`check-claim`, gate 15) or do they migrate by
   email match on first sign-in? Recommend: the claim path, since gate 15 already asks for it.
3. **`polaris-ranking` as an extraction in this project, or its own?** Recommend: inside the port,
   as a crate in the Concord workspace that is later moved to its own repository — moving a crate
   that already has tests and an adopter is cheap; writing one in the abstract is not.

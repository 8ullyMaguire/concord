# Concord → estate profile: complete context for an outside advisor

Written 2026-11-14. Self-contained: everything below is meant to let a reader who has **no access to the repository, the
server, or the conversation history** understand the project, the state, and give useful advice.

If you are a language model being handed this file: the owner wants advice on how to finish faster. Sections 1–5 are the
situation, 6–9 are what remains, 10–13 are hard-won technical detail you should not re-derive, 14–16 are the specific
questions worth answering.

---

## 1. What the project is

**Concord** is a web product for software projects: people file feature ideas, complaints and field reports; features and
solutions compete in pairwise-vote arenas with a Glicko-style rating; proposals move through a consensus/governance model
(collective, maintainer-led, etc.) with quorums, approvals and an append-only audit ledger; there are documents, milestones,
dependencies and a "finder" that asks a visitor questions to narrow 73 real projects down to a shortlist.

The product currently runs as a **Go** monolith (chi router, SQLite file, server-rendered HTML with progressive enhancement)
on one host, publicly at `https://concord.polarisocial.xyz` behind Cloudflare.

**The task in progress** is not a new product: it is a **port** of that product onto the owner's "estate profile" —
Rust/Axum + PostgreSQL + SvelteKit + the family's shared crates — as a strangler, while the Go service keeps serving production.
Nothing new may be designed; the port must behave like the reference. "A port, not a rewrite" is a standing rule.

The owner's estate: ~16 sites under `polarisocial.xyz` on one host (`thinkcentre`), Cloudflare tunnels, shared Rust crates
(`polaris-*`), a design-token package `@polaris/ui`, and a set of "estate gates" (numbered checks: duplication, adoption of
shared crates, doc-number accuracy, etc.).

---

## 2. The artefacts that define state (nothing else counts)

1. `docs/plans/port-to-profile.md` — **the ledger**. A table of rows 1…13; a row is Done only when its gate command ran and its
   output is quoted in the entry below it. Every turn starts by reading this file. It is 3,594 lines, of which ~2,300 are
   per-part essays written as the work happened.
2. `docs/specs/port-to-profile-spec.md` — the what and why.
3. `~/code/estate/docs/adr/0011-concord-joins-the-profile.md` — the twelve decisions the owner confirmed (2026-11-13).

**Ledger state today: 3 rows open, 0 blocked.**

| row | what | state |
|---|---|---|
| 11 | S5: every JSON API route, stats, feeds, admin binaries | in progress — **57 parts landed**; 19 JSON routes remain |
| 12 | P6: the SvelteKit frontend on `@polaris/ui` + 11 browser suites | not started |
| 13 | P7: cutover — Rust binary serves the domain, unit swapped, gate 17 green | not started |

Progress metric the repo maintains itself: `python3 scripts/parity/routes.py` parses both trees and prints
`go routes: 216   port routes: 189   missing: 29 (19 json, 10 page)`. (The 10 "page" routes are row 12's, and are rendered by the
frontend rather than by the API.)

---

## 3. Standing invariants (violating any of these is a defect, not a shortcut)

- The **Go service keeps serving** `concord.polarisocial.xyz` until row 13. It must never be restarted with modified data.
- **Every claim about the reference is a probe, not a memory.** If you believe the Go answers X, curl it and paste the output.
- **No new product features** until the port is done.
- **No invented rows, numbers, or gate output.** A green build, a 200, or a passing suite is not proof of a behaviour.
- **Grep a document's symbols and counts before trusting them** — the docs in this repo have been wrong repeatedly.
- A row blocked on a credential, a person, or an undecided question goes into a Blocked section with what and on whom, and the
  next unblocked row starts immediately.

---

## 4. Environment, hosts, and how anything is run

Two machines:

- **cachyos** — the workstation. `~/code/` is a mount of `thinkcentre:/personal/documents/code`, so writes through it land on the
  server. The Rust checkout is `~/code/rust/concord`; the Go reference checkout is `~/code/archive/concord`.
- **thinkcentre** — the Linux host that runs everything (systemd user units, nginx, cloudflared, PostgreSQL).

Deployment of the candidate service (the usual loop, all commands real):

```bash
# build (30–35 s incremental) and ship
cd ~/code/rust/concord
CARGO_TARGET_DIR=~/.cargo-target/concord cargo build --release -p concord-cli
scp -q ~/.cargo-target/concord/release/concord-cli thinkcentre:/tmp/concord-cli.bin

# restart it on 8108 against the production database
ssh thinkcentre 'pkill -9 -x concord-cli; sleep 1; cp -f /tmp/concord-cli.bin /tmp/concord-cli; \
  set -a; . $HOME/.config/concord/.env; set +a; cd /tmp; \
  setsid -f env CONCORD_DB_URL="$DATABASE_URL" /tmp/concord-cli serve --addr 127.0.0.1:8108 \
  > /tmp/parity-cand.log 2>&1 < /dev/null; sleep 2'
```

Notes that cost hours the first time:
- the candidate listens on `127.0.0.1:8108` and is **not** proxied publicly; it is reached by `ssh thinkcentre curl …`;
- `setsid -f` is required: without it the ssh session hangs because the child inherits the tty;
- the DSN lives in `~/.config/concord/.env` as `DATABASE_URL` (PostgreSQL on the host, database `concord`);
- the reference service is reached over the internet at `https://concord.polarisocial.xyz`, and **Cloudflare 1010s a
  `Python-urllib` user agent** — send a desktop UA or curl's;
- probes are run on thinkcentre with `CONCORD_E2E_DSN="$DATABASE_URL"` exported, because their fixture helper writes through
  psycopg2 and defaults to SQLite without it.

---

## 5. Code map (what exists, so nothing is rebuilt)

Rust workspace `crates/`:

| crate | owns |
|---|---|
| `domain` | value types: `Role`, charter, visibility, kinds |
| `db` | **all SQL**, one module per surface: `projects`, `features`, `complaints`, `consensus`, `arena`, `arena_writes`, `votes_writes`, `voteweight`, `ranking` glue, `solution_writes`, `coverage_writes`, `feature_work`, `invite_writes`, `merge_writes`, `document_writes`, `finder_reads`, `auth`, `identity`, `alternatives`, `charter`, `audit` … |
| `app` | Axum router + ~180 handlers in one `lib.rs` (~7,500 lines) |
| `cli` | the binary: `serve --addr`, migrations, export |
| `parity` | the corpus comparer (`concord-parity --corpus crates/parity/corpus.json`) |
| `ranking` | Glicko update, `vote_weight`, role/expertise/age multipliers |
| `governance` | quorum and gate arithmetic, `Role::parse` |
| `finder` | the question-choosing engine — pure, no pool, no DB (11+1 tests) |
| `embed` | near-duplicate embedder (HTTP model server or a local hashed fallback) |

Probe harness `scripts/parity/probe_*.py` — 65 probes, ~1,600 assertions total. Each is a standalone Python script with a
`check(name, got, want)` pattern that prints `ok`/`FAIL` lines and a final `N/M ok`. They talk to `$CANDIDATE`
(default `http://127.0.0.1:8108`) over urllib, and use `tests/e2e/harness.py` (`harness.connect("unused")`) to write fixtures
with SQLite-style `?` placeholders that the harness translates to psycopg2.

---

## 6. Row 11: what has been built (57 parts)

Everything below is done, probed, and committed. A new session should **not** redo any of it, and should reuse the modules
rather than write new ones.

- **S1 auth**: register, `/auth/me`, argon2id at the Go's parameters, `clt_`-prefixed tokens, `base64.RawStdEncoding(sha256)`
  token hashing (compatible with the Go's `api_tokens`).
- **S2 ideas/complaints/ranking/search/stats**: complaints, links to features, ranking oracle, `/stats`, `/stats/summary`,
  `/search`.
- **S3 consensus & governance**: calls, positions, objections, override windows, emergency holds, audit ledger, merge requests.
- **S3b arenas**: feature-priority arena, pairwise votes, leaderboards, `next` pair, standings, verification.
- **S4 moderation/comments/inbox**: curation queue, threads, moderation acts, notifications — adopting the family's
  `polaris-comments` and `polaris-authz` crates rather than hand-rolling.
- **S5 (row 11)**: work items/claims/deps/evidence; solution call; filing solutions; solution arena votes; coverage claims and
  contests; feature votes; invites; merge requests; documents and threads; the finder (engine crate + 6 routes + differential
  probe); alternatives arenas; the identity cluster (guest, consent gate, login, logout, fold-on-login).

Working method per part, which is why each part is a turn:

1. read the Go handler + store function (the reference);
2. write the failing probe / capture the 404 output **before** code;
3. implement only to that part's gate;
4. prove it by running the probe against the deployed candidate (and, where possible, **differentially** against the live Go);
5. run **negative controls** — mutate the one rule the part is about and show the probe fails;
6. commit with the gate line in the message, push to both forges, write a ledger entry with the date, commit and quoted output.

Average cost per part: 60–100 minutes and one context window. 57 parts ≈ 50 hours of agent time.

---

## 7. Row 11: exactly what remains (19 JSON routes)

```
GET  /admin-ledger
GET  /badge/{}
GET  /contribute/next
GET  /lists
GET  /projects/{}/votes/next
GET  /projects/{}/work-items
GET  /scout
GET  /stats/rising
GET  /taxonomy/proposals
GET  /taxonomy/tags
POST /complaints/{}/merge/{}
POST /emergency-holds/{}/release
POST /hooks/forgejo
POST /projects/{}/arenas/{}/recompute
POST /projects/{}/field-reports
POST /projects/{}/field-reports/{}/responses
POST /projects/{}/join
POST /taxonomy/proposals
POST /taxonomy/proposals/{}/consent
```

They cluster into six groups that share store code and fixtures:

1. **Contribution feed** — `GET /contribute/next`, `GET /projects/{}/votes/next`, `GET /projects/{}/work-items`. All read
   machinery that already exists (finder candidate scoring, priority arena `next_pair`, `feature_work`). This is the cheapest
   cluster and it unblocks the `/contribute` page.
2. **Taxonomy** — `GET /taxonomy/tags`, `GET /taxonomy/proposals`, `POST /taxonomy/proposals`,
   `POST /taxonomy/proposals/{}/consent`. Needs the **eligible tagger set** (the collaborators a proposal is put to), which row 10
   deliberately refused to fake: a consent without it lets anyone ratify a proposal never put to them.
3. **Membership & field reports** — `POST /projects/{}/join`, `POST /projects/{}/field-reports`,
   `POST /projects/{}/field-reports/{}/responses`. Writes onto tables that already exist.
4. **Admin/ops reads** — `GET /admin-ledger`, `GET /badge/{}`, `GET /stats/rising`, `GET /scout`.
5. **Admin/ops writes** — `POST /emergency-holds/{}/release`, `POST /projects/{}/arenas/{}/recompute`,
   `POST /complaints/{}/merge/{}`.
6. **Webhook** — `POST /hooks/forgejo` (shared-secret authenticated; the only route exempt from the consent gate besides auth).

---

## 8. Row 12 and row 13: the real shape of what is left

**Row 12 (frontend)** is the SvelteKit app plus eleven browser suites: `feature_e2e`, `feature_ui_mutants`, `milestones_e2e`,
`rank_links_e2e`, and — inherited from row 9 — `consensus_e2e` and `audit_e2e`, whose fixtures must first be moved from
`sqlite3.connect` to `harness.connect`. Ten page routes (`/`, `/contribute`, `/finder`, `/login`, `/register`, `/taxonomy`,
`/projects/{}/rank`, `/ranking`, `/requests/{}`, `/settings`) are rendered by the frontend, not by the API.

**Row 13 (cutover)** is comparatively mechanical: set `CONCORD_BASE_URL`, install the systemd user unit pointing at the Rust
binary, swap the tunnel/nginx upstream, re-run the sequence repair, and make `concord.polarisocial.xyz/api/v1/version` answer
from the Rust binary with estate gate 17 green and no `reason` on the row.

---

## 9. Why it has taken 57 turns, stated plainly

- **The row is not one unit of work.** Row 11's stated gate ("corpus + `stats_e2e` and feed parse; estate gates 4, 5, 47") does
  not decompose, so each of the 57 parts invents its own gate, fixture plumbing, controls, commit and ledger essay.
- **Each part pays a fixed tax**: read the Go handler, read the Go store function, read the port's equivalents, write a probe,
  build (30 s), deploy (10 s), restart, run the probe, mutate the code and rebuild twice for controls (35 s each), commit,
  push to two forges, write the ledger entry.
- **Fixture churn** has been the single biggest source of wasted turns: probes that clean up on entry only, that cannot delete a
  project because it now carries a board/charter/ledger rows behind real foreign keys, and duplicate slugs across runs. Two
  turns were spent this session just deleting leaked fixtures.
- **The ledger grew to 3,594 lines**; re-reading it after each context compaction costs real budget, and writing it costs more.
- **Controls cost two full rebuilds each.** They are the most valuable part of the method (they have caught real defects) but
  the rebuild-per-mutation is pure wall-clock.

---

## 10. Hard-won technical knowledge (do not re-derive)

**Data shape / SQL**
- The Go stores timestamps as float epoch seconds; the Postgres schema uses `timestamptz`. Bind `now()` on write and
  `extract(epoch from …)::double precision` on read; an `f64` bound to a timestamp column errors.
- Postgres names an unaliased expression column `?column?` or after the function (`coalesce`). Every `select` expression must be
  aliased or `row.get("…")` panics the worker — this produced a 500 on the very first empty-query search.
- `public_id` is `NOT NULL` with **no default** on most tables (ULID written at insert). Forgetting it is a 500 on every write.
- `arena_entries` has a composite key `(arena_id, entity_type, entity_id)` and **no surrogate id**; the fork relation between
  solutions lives on `solutions.parent_solution_id`.
- `audit_log` columns are `(project_id, actor_id, action, entity, entity_id, detail, created_at, undoes)` — `entity`, not
  `entity_type`; `reputation_events` has `kind`, not `reason`.
- `project_documents` is unique at `(kind, slug)`; a write is an upsert that bumps `revision` and keeps the row id.
- Postgres has no `INSERT OR REPLACE`/`UPDATE OR IGNORE`; emulate with `on conflict` or a savepoint per statement.

**Auth / identity**
- argon2id parameters are the Go's: m=65536 KiB, t=1, p=2, 32-byte output, 16-byte salt, PHC string, so either side can verify.
- Tokens: 32 random bytes, base64url no padding, `clt_` prefix; stored as `base64.RawStdEncoding(sha256(token))`.
- "no such user", "wrong password" and "identity has no password" must be **the same 401 with the same body**; an unknown
  username still performs a hash so the answer cannot be read off a stopwatch.
- A guest is a `users` row with `is_guest = true` and empty `password_hash`; it acts via its token and becomes an account by
  **registering** (promotion in place, same id, no copying).
- Logging in from a guest **folds** the guest's rows onto the account; the column list is **derived from `pg_constraint`** at fold
  time plus two FK-less carriers found by name-and-type (`audit_log.actor_id`, `admin_ledger.actor_id`). A hand-maintained list
  is how "my votes disappeared" happens.
- The navigation cookie is accepted for logout and for page GETs, **never** for `/api/` calls — that split is the CSRF defence.

**Consent**
- Every mutating request must carry `Cookie: concord_consent=<version>`; otherwise the reference answers
  `403 {"code":"consent_required"}` **before** looking at the token. Verified against the live Go. Registration, login, logout,
  `/consent`, `/guest` and the Forgejo webhook are exempt. The port enforces this as one middleware layer, and all 65 probes
  carry the cookie through a single monkey-patch shim.

**Arena / ranking**
- Vote weight = reputation × role multiplier, then expertise (solutions only, tag-scoped), then **account-age gate last** —
  a Sybil defence an expertise multiplier can halve is not a defence.
- Feature votes ignore role; solution votes do not.
- "Do nothing" is a **solution** arena concept (a baseline entry). Alternatives arenas have **no baseline** — the incumbent is
  the arena's subject.
- A skipped vote is recorded and moves nothing; a fork inherits the parent's doubled RD **unclamped**.
- Coverage claims are refused if the complaint is not linked to the solution's feature (denominator gaming).

**Finder**
- Pure engine, no DB: unknown is not "no" (kept, demoted), skip never filters, a constant dimension is never asked, a full match
  scores `1 - penalty × coverage` (renormalised, 0.975 — not 0.15), the diversity penalty is 0.7 applied to the same family as the
  previous question.
- The port's gate for it is a **differential probe**: the same seed and the same answers into the live Go and into the port,
  comparing question key, option order, survivor counts, stop verdict, top five and fits to 1e-9 at every step.

**Verification discipline that produced real findings**
- A probe that cannot fail is not a gate: the diversity penalty 0.7→1.0 **passed** the differential probe, because the live
  catalogue's first three questions never repeat a family; the rule now has a hand-built unit test that does fail under mutation.
- The differential probe caught 18 leaked probe fixtures in the public catalogue — the port's own fixtures, visible to visitors.
- Assertions on *status codes alone* hide the difference between "you may not" and "you may later" (the merge-quorum case).

---

## 11. Decisions already taken (so they are not re-litigated)

1. Two lineages, one product name: the Go tree is the reference; the Rust tree is the port. Never call the product "lorehaven".
2. The port keeps the reference's shapes even when they look odd (`/auth/me` does not carry `is_guest`; the thread kind
   `release` reads as `[]` but refuses writes with 400) — but untruthful fields are not copied (`is_guest` is omitted rather than
   hard-coded `false` for guests).
3. Skeletons stay in memory (finder sessions, one-hour age-out) exactly as the reference has them.
4. Non-member versus non-existent: a 404 where a 403 would confirm the resource exists.
5. Shared family crates are adopted wherever they own the capability (`polaris-comments`, `polaris-authz`), not re-implemented.
6. Route inventory numbers come from `scripts/parity/routes.py`, and its own regex bug (routes with a comment between `.route(`
   and the path counted missing) was fixed rather than worked around.

---

## 12. Open questions the owner has not had to answer yet

- Row 12 will need a design decision: does the SvelteKit frontend reproduce the Go's server-rendered pages 1:1, or take the
  `@polaris/ui` look? The spec says "every page its slice owns renders from the API with the server-rendered head" — the visual
  system is `@polaris/ui`, which will look different from today's site.
- The taxonomy eligibility rule (who may ratify a tag proposal) is specified in the Go store's comments but its exact query has
  never been ported. It is the only *design* question left in row 11.
- Row 13 needs a maintenance window (a few minutes of downtime at cutover), which the owner has not scheduled.

---

## 13. What the remaining work would look like if batched

Rough shape, if the owner wants to finish in ~10 turns instead of ~30:

| batch | routes/work | gate |
|---|---|---|
| A | contribution feed (3 routes) | one probe, one differential check against the live Go's `/contribute/next` |
| B | membership + field reports (3 routes) | one probe: join changes role, field report visible, response attaches |
| C | taxonomy (4 routes) incl. the eligible set | one probe: a proposal only ratifies with the eligible set's consent |
| D | admin/ops reads (4 routes) | one probe + corpus shapes |
| E | admin/ops writes + webhook (4 routes) | one probe; the webhook's shared secret is the interesting part |
| F | row 12a: SvelteKit shell + 10 pages against the live API | Playwright smoke on each page + the server-rendered head |
| G | row 12b: the 11 browser suites, fixtures migrated to `harness.connect` | suites green |
| H | row 13: unit swap, tunnel, version probe, gate 17 | `curl …/api/v1/version` served by Rust; gate 17 green |

---

## 14. Questions worth answering (for the advisor)

1. Is batching by shared plumbing (A–E above) the right compression, or should the remaining 19 routes be finished one per turn
   for the audit trail the ledger provides?
2. Should the ledger's per-part essays be capped (gate line + commit + one quoted line + dated decisions) to stop it growing
   faster than the code?
3. Should negative controls be made **runtime-switchable** (`CONCORD_MUTANT=…` read at startup) so a control costs one restart
   instead of a 35-second rebuild — accepting that a mutant switch ships in the release binary?
4. Is it better to start row 12 (frontend) **in parallel** with the remaining row-11 routes, given the API it needs is ~90 %
   present and the frontend does not depend on the last 19 routes?
5. What is the cheapest way to make probe fixtures self-cleaning, given projects now carry boards, charters and ledger rows
   behind real foreign keys (a bare `delete` fails)?

## 15. Command cheat sheet

```bash
cd ~/code/rust/concord                       # the port
cd ~/code/archive/concord                    # the Go reference (serving production)

python3 scripts/parity/routes.py             # go 216 / port 189 / missing 29 (19 json, 10 page)
CANDIDATE=http://127.0.0.1:8108 concord-parity --corpus crates/parity/corpus.json
CONCORD_E2E_DSN="$DATABASE_URL" CANDIDATE=http://127.0.0.1:8108 python3 scripts/parity/probe_identity.py
cargo test -p concord-db --lib               # 38 passed
cargo test -p concord-finder                 # 12 passed
git push forgejo port-rust && git push github port-rust   # both forges, same branch
```

## 16. Glossary

- **arena** — a set of items ranked by pairwise votes (feature-priority, solution, alternatives, use-case, list, request).
- **baseline** — a solution arena's "do nothing" entry; other arenas have none.
- **charter** — per-project parameters: vote weight cap, quorum, halves, halflife.
- **fold** — moving a guest's rows onto an account at login.
- **gate (estate)** — a numbered cross-repo check (`gate 17`, `gate 45`) the family runs over all its sites.
- **ledger** — `docs/plans/port-to-profile.md`; the only state list.
- **probe** — a standalone Python script asserting observable behaviour over HTTP, with `ok/FAIL` lines.
- **control (negative control)** — a deliberate mutation of the rule under test, proving the probe fails without it.
- **parity corpus** — recorded live response shapes (`crates/parity/corpus.json`) compared field by field.

---

## 17. Where each remaining route lives in the reference (reading guide)

The Go tree is `internal/httpapi/` (handlers, one file per surface) and `internal/store/` (SQL). The 19 routes, with their
starting point:

| route | read first |
|---|---|
| `GET /contribute/next` | `internal/httpapi/contribution.go` — ranks candidate work items for one person across projects; reuses the finder's candidate scoring and `feature_work` |
| `GET /projects/{}/votes/next` | `internal/httpapi/arena.go` — the project's pending pairs for this voter; the port already has `arena::next_pair` for the feature-priority arena |
| `GET /projects/{}/work-items` | `internal/store/workitems.go` — the port has the table module (`db::feature_work`) but not the list route |
| `GET /taxonomy/tags` | `internal/store/taxonomy.go` — tag inventory with counts |
| `GET|POST /taxonomy/proposals` | same file — proposals are per (project, tag) with a ratify/refuse lifecycle |
| `POST /taxonomy/proposals/{}/consent` | same file, plus `eligibleTaggers` — **the missing piece**: the set of collaborators a proposal is put to. Nothing ratifies until that query is ported |
| `POST /projects/{}/join` | `internal/httpapi/membership.go` — self-join with the project's `intake_mode` deciding open/request/closed |
| `POST /projects/{}/field-reports` (+ `/responses`) | `internal/store/fieldreports.go` — a field report is a *structured* row (version, use_case, environment, scale, duration, outcome, caveats, workaround, advice), not a document; responses attach to it |
| `GET /admin-ledger` | `internal/store/adminledger.go` — operator actions, append-only, `actor_id` is FK-less on purpose |
| `GET /badge/{}` | `internal/httpapi/badges.go` — an SVG badge for a project's health/ranking |
| `GET /stats/rising` | `internal/store/stats.go` — projects ranked by recent activity delta |
| `GET /scout` | `internal/httpapi/scout.go` — a discovery list for visitors |
| `POST /emergency-holds/{}/release` | `internal/store/consensus.go` — releasing a hold is itself a ledger event |
| `POST /projects/{}/arenas/{}/recompute` | `internal/store/arenas.go` — replays votes into fresh ratings; the port has the rating maths and needs the replay |
| `POST /complaints/{}/merge/{}` | `internal/store/complaints.go` — moves links and votes from one complaint to another, idempotent, refuses across projects |
| `POST /hooks/forgejo` | `internal/httpapi/hooks.go` — shared-secret HMAC, signature compared in constant time, then a project/board update |

---

## 18. The 65 probes, and what each one guards

Grouped, because the names alone do not say what they hold:

- **API surface & shapes**: `probe_s1_api`, `probe_s3_api`, plus `concord-parity` (16 recorded shapes; two are declared
  divergent and named in every run rather than hidden).
- **Arena & ranking**: `probe_s3b_arena`, `probe_s5_arenavotes`, `probe_s5_nextpair`, `probe_s5_standings`,
  `probe_s5_solution`, `probe_s5_solutionwrites`, `probe_s5_solutionvote`, `probe_s5_solutioncall`, `probe_s5_featurevote`,
  `probe_alternatives`.
- **Consensus & governance**: `probe_s5_accept`, `probe_s5_composite`, `probe_s5_criteria(writes)`, `probe_s5_deps`,
  `probe_s5_evidence`, `probe_s5_tallies`, `probe_s5_timers`, `probe_s5_strategy`, `probe_s5_merge`, `probe_s5_invites`.
- **Moderation, comments, inbox**: `probe_s4_comments`, `probe_s4_curation`, `probe_s4_inbox`, `probe_s4_moderation`.
- **Work & coverage**: `probe_s5_workitems`, `probe_s5_coverage`, `probe_s5_milestone(writes)`, `probe_s5_featurewrites`,
  `probe_s5_listwrites`, `probe_s5_complaintwrites`, `probe_s5_requestwrites`, `probe_s5_filing`, `probe_s5_documents`.
- **Discovery**: `probe_s5_search`, `probe_s5_lists`, `probe_s5_panels`, `probe_s5_themes`, `probe_s5_stats`,
  `probe_s5_taxonomy`, `probe_s5_kinds`, `probe_s5_links`, `probe_s5_capabilities`, `probe_s5_fieldreports`,
  `probe_s5_healthsync`, `probe_s5_forgefetch`.
- **Differential** (the only ones that compare against the live Go rather than assert expectations): `probe_s6_finder_diff`
  (35 checks: same seed, same answers, same question at every step).
- **Identity**: `probe_identity` (65 checks: guest, consent gate, login, logout, fold, promotion).

Two probes are red **by design** and are row 12's: `probe_s5_feeds` (drives `/projects/{}/feed.xml`, a page route, outside
`/api/v1`) and the corpus's page-shaped entries.

---

## 19. What one "part" actually costs (worked example)

Part 57 (the identity cluster: 5 routes) ran as: read four Go files (auth handlers, guest handlers, guest store, fold list);
write `crates/db/src/identity.rs` (~470 lines); add five handlers and a middleware to `crates/app/src/lib.rs` (~380 lines);
write `probe_identity.py` (~65 checks, 400 lines); build; deploy; **six** probe runs while fixing: an `/auth/me` shape
assumption, a guest-count off-by-two, an audit_log column name (`entity_type` vs `entity`), a `reputation_events` column name
(`reason` vs `kind`), a fold expectation (the guest's token counts as a moved row), and a cookie-scope deviation I had introduced
that let the nav cookie authenticate API calls; then two controls (each a 35 s rebuild + deploy + probe run); then the commit and
a ~60-line ledger entry. Total: one full context window.

The lesson for batching: **six probe runs were data-shape discovery**, not behaviour work. A single "shape probe" that prints
the actual column names and response keys of the tables/routes a cluster touches would have removed five of those six cycles.
That is the highest-leverage tool still missing.

---

## 20. Concrete defects the method found (evidence that the discipline pays)

1. The live Go's search cannot find 2 of its own 73 projects — its FTS index is stale; the port's generated index is correct.
   Declared, not fixed (row 13 owns the decision).
2. 18 probe fixtures were **publicly listed** in the port's catalogue; a differential probe that asserted catalogue equality
   caught them and named every slug.
3. `public_id` `NOT NULL` with no default: every document write was a 500 until supplied on insert.
4. An unaliased `0::double precision` made the first empty-query search 500 (`?column?` read back as `score`).
5. A duplicate route registration panicked the service at boot (two GET `/deps`).
6. `coalesce(f.difficulty,'')` read back as column `coalesce` → worker panic.
7. Merge approval counted after the author became eligible: quorum rose 1-of-1 → 1-of-2 mid-flow; a status-only assertion could
   not see it, an assertion on the *reason* could.
8. A probe fixture with `max_uses = 1` asserted that a second person could redeem — the cap was working, the probe was wrong.
9. The finder's diversity penalty could be deleted without any probe noticing (fixed with a hand-built unit test).
10. The nav cookie, if read for API calls, would let a cross-site form post write with a visitor's cookie (caught by writing the
    probe before the handler).

---

## 21. Numbers

- 216 reference routes; 189 ported; 29 missing (19 JSON + 10 page).
- 65 probes; ~1,600 assertions; 2 red by design (row 12).
- 3,594-line ledger; 57 parts; 449 commits on the branch.
- Rust unit tests: `concord-db` 38, `concord-finder` 12, `concord-governance` 9 + 3 golden.
- Build 30–35 s incremental; deploy ~12 s; a probe 1–3 s; a control (build+deploy+probe) ~50 s.
- Two forges, same branch `port-rust`, pushed on every part.


---

## 22. Per-cluster notes: what exists, what is missing, what the probe must assert

**A. Contribution feed (3 routes).** Exists: `db::finder_reads` (candidate loading, per-project attributes, arena standings),
`db::arena::next_pair`, `db::feature_work` (work items, claims, evidence), `ranking` weights. Missing: a cross-project
*ranking of candidate actions for one person* and the two thin routes that expose it. Probe must assert: (a) a guest and a
member get the same list shape but different eligibility; (b) an item the person already voted on does not reappear; (c) the
list is stable for identical state (no randomness in ordering); (d) `votes/next` returns `null` rather than an error when the
arena has one entry.

**B. Membership + field reports (3 routes).** Exists: `members` table, `Role`, charter, `field_reports` + responses tables.
Missing: the `intake_mode`-dependent join decision (open / request / closed), and the two write routes. Probe must assert:
joining twice is idempotent; a `closed` project refuses with the same 404 a non-existent one gives; a field report with a missing
required column is a 400 naming it; a response attaches to a report in the same project only.

**C. Taxonomy (4 routes).** Exists: nothing but the tables. Missing: tag inventory, proposal lifecycle, and — the real work —
the **eligible tagger set**. Probe must assert: a consent from a non-eligible account does not ratify; ratification requires the
charter's quorum of the *eligible* set, not of everyone; a proposal that is already decided refuses further consent.

**D. Admin/ops reads (4 routes).** Exists: `admin_ledger`, `audit_log`, health/ranking numbers. Missing: four read handlers and
their serialisers. Cheapest cluster; the corpus shapes are the gate.

**E. Admin/ops writes (3 routes) + webhook.** Exists: `emergency_holds`, arena rating maths, complaint links and votes.
Missing: hold release (which writes a ledger row), arena replay, complaint merge (moves links/votes, refuses across projects),
and the HMAC webhook. Probe must assert: releasing a hold twice is idempotent; a merge across projects is refused; the webhook
rejects a wrong signature and does not create a second delivery record.

**F. Row 12 — frontend.** The API the pages need is ~90 % present. Missing pages: `/`, `/contribute`, `/finder`, `/login`,
`/register`, `/taxonomy`, `/projects/{}/rank`, `/ranking`, `/requests/{}`, `/settings`. Each needs the server-rendered head
(title, og tags, consent state) and the SvelteKit app shell. `@polaris/ui` supplies tokens and components — adopting it changes
the look, which is a *design* decision the owner should confirm before the first page ships.

**G. Row 12 — suites.** Eleven Playwright suites, six of them inherited; two (`consensus_e2e`, `audit_e2e`) still open SQLite
directly and must move to `harness.connect` before they can run against Postgres.

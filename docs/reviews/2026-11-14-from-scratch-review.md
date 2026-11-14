# Concord, built from scratch: what I would do differently

Written 2026-11-14, after the port to Rust took over `concord.polarisocial.xyz` and the first five post-port roadmap steps landed.
This is a critique of the product as it exists, not of the port as a project: the port did what it set out to do, and most of what
follows is a critique of the design the port inherited and faithfully reproduced.

Every claim below is a measurement, and the command that produced it is in the sentence. Where I am guessing, it says so.

## Evidence base

| fact | value | how |
|---|---|---|
| Rust source | 45,473 lines across 11 crates | `find crates -name '*.rs' \| xargs wc -l` |
| Client JavaScript | 13,024 lines, no build step, no modules | `find crates/web/assets/js -name '*.js' \| xargs wc -l` |
| Routes registered in the app | 177 `.route(` calls | `grep -c '\.route(' crates/app/src/lib.rs` |
| Database modules | 61 files in `crates/db/src` | `ls crates/db/src/*.rs \| wc -l` |
| Tables | 80 | `information_schema.tables` on production |
| Users | 89, of which 3 guest | production query |
| Projects | 73, of which 5 state a repository | production query |
| Browser suites | 29 files, 97 tests in the eight suites run every time | `ls tests/e2e/*_e2e.py` |
| CI | none | `ls .github/workflows` → absent |
| Pages with a server-rendered `<h1>` | 5 of 12 checked have none | `curl` + `grep '<h1'` |

## The ten things I would do differently

### 1. A migration system, from the first table

**What exists.** One file, `crates/db/sql/0001_baseline.sql`, and no runner: the baseline is applied by hand, and this session's
`ai_involvement` column was added to the baseline **and** to the live database with an `alter table … add column if not exists` typed into
a shell.

**Why it is wrong.** The two statements can disagree and nothing notices. A fresh database gets the column; the running one gets it only
because somebody remembered. The failure is silent until a feature reads a column that exists in one place and not the other, and it fails
in production first. This is the single highest-risk decision in the tree.

**What I would do.** Numbered migrations, `0001` … `00N`, applied in order at startup behind a `schema_version` table, with the binary
refusing to serve if the database is ahead of it. Every change is then one artefact that both a fresh database and the running one consume.

**Cost to change now:** medium, and it should be the next thing built. Roughly an afternoon: move the baseline to `0001`, add a
`migrations` table seeded with the live maximum, add an `apply` step to the CLI, and a gate that fails when a file is skipped.

### 2. One renderer per page, decided once

**What exists.** Some pages are rendered on the server (`/`, `/projects`, `/taxonomy`, `/stats`, `/scout`), some are a `<div>` plus a
script (`/projects/{slug}`, `/search`, `/finder`, `/rank`, `/ranking`, `/curation`, `/contribute`, `/notifications`). The second group has
**no `<h1>` in the HTML at all** until JavaScript runs, and `/projects/{slug}` is 5.5 KB of `aria-busy="true"`.

**Why it is wrong.** It is not one design with a performance trade-off; it is two designs that both have to be maintained, and this session
paid for it directly: I added the stage badge to `projects.js`, a file whose `card()` renders exactly the markup a test looked for, and seven
tests failed against a correct page because `/projects` is server-rendered and that script never runs. Twenty minutes of my life, and a
permanent invitation to the next person.

It also means the product is invisible to anything that does not run JavaScript: crawlers, link previews, and readers on a bad connection
get an empty box where the project is.

**What I would do.** Server-render every page that has a URL, and use JavaScript for interaction only (vote, filter, the arena pair, the
finder session). A project page is a document with a title, a description, and its panels — that belongs in HTML.

**Cost to change now:** medium per page, and the same work twice if the JS is kept in parallel. Do them one at a time, delete the client
branch in the same commit, and let the existing browser suites prove the interaction still works.

### 3. The API should have been designed, not frozen

**What exists.** 177 routes, additive-only since the port began, because the Go-era Playwright tests speak HTTP and the port's gate was
"byte-identical shapes". Two shapes now carry a `stage`, a `stage_because` and an `ai_involvement` that the reference never had, and the
parity corpus had to be re-recorded twice in one session.

**Why it is wrong.** "Additive-only" was the right rule for a port and the wrong rule for a product. It made every shape a compatibility
surface before anyone depended on it, and it means a wrong field name is permanent. `project_id` is a slug in paths and a number in bodies;
`project` JSON carries a computed stage next to a stored visibility next to a measured health score with no marker of which is which.

**What I would do.** Two clients exist — the browser and the CLI — and both are ours. Design the API for them: one versioned surface,
objects that say whether a field is measured or declared (`{"stage": {"value": "dormant", "because": "…"}, "ai_involvement": null}`), and
delete the routes nobody calls. Grep is the whole audit: every route with no reader is a route to remove.

**Cost to change now:** high, and it is a decision rather than a task. A middle path worth taking: publish one new `/api/v2/projects` that
is designed rather than inherited, keep v1 until the web client migrates, then delete.

### 4. The cutover moved the rows and not the writers

**What exists.** On 2026-11-14 the Rust binary took over `concord.polarisocial.xyz` and the Go service was stopped, with its SQLite file
kept as the rollback. **The Go timers were not stopped with it.** Measured today:

```
$ systemctl --user list-unit-files 'concord*'
concord-healthsync.timer    enabled
concord-proposalsweep.timer enabled      # both the Go units, still on
$ journalctl --user -u concord-healthsync -n 1
Nov 14 20:06:15 healthsync: synced 23, skipped 0, deferred 0, failed 0 of 23
```

So a service that is "stopped" still runs two jobs a day, and they still write to the rollback database. The rollback is not a snapshot of
the cutover; it is a diverging copy.

**And it shows in the data.** The Go database has **23 projects with a `repo_url` and 23 with a measured health score**; PostgreSQL has
**5 and 5**, because the repository identity I applied today came from `seed/forge_repos.json`, which states five and skips sixty-eight.
The dead lineage measures more of the product than the live one does.

**Why it is wrong.** A cutover is a data question *and* a writer question. I wrote the first half down (the reconciliation script, in the
cutover ledger) and missed the second, and the cost is that the live instance is less informed than the corpse.

**What I would do.** The cutover checklist has three items, not two: reconcile the rows, move the **writers**, and disable the losing side's
units in the same change. Then the rollback really is frozen and its timestamp means something.

**Fixed the same day, after writing this:** `concord-rs.service` was *disabled* while `concord.service` was *enabled*, so a reboot
would have silently rolled production back to Go. Both are now the right way round and the two Go timers are stopped and disabled.

**Cost to change now:** minutes for the timers (`systemctl --user disable concord-healthsync.timer concord-proposalsweep.timer`), an hour
for the eighteen repository identities that already exist in the Go database and have never been copied forward.

### 5. Health ingest: the code existed, the job did not — and I said the opposite here first

**What I wrote in the first draft of this review, and it was wrong.** I claimed `healthsync` needs `--from measurements.json` and that
"nothing produces that file". `grep -n 'healthsync_fetch' crates/cli/src/lib.rs` says otherwise: `healthsync --fetch` has existed since the
port and it works —

```
$ concord-rs healthsync --fetch --dry-run --only kindred,commons,orbital
dry-run commons:  commits=257 contributors=0 releases90=0 review latency unknown
dry-run kindred:  commits=128 contributors=0 releases90=0 review latency unknown
dry-run orbital:  commits=255 contributors=0 releases90=0 review latency unknown
```

I read the `--from` signature in the CLI and the Go-era note that the port "moved the scraping out", and wrote a conclusion from two
half-facts. The lesson is the review's own rule, applied to the review: grep the symbols before trusting the document, including this one.

**What was actually wrong, and it is worse than a missing feature.** The installed unit was

    ExecStart=/personal/documents/code/rust/concord/deploy/concord-cli healthsync

— a **local build artefact** rather than the deployed binary, and **no `--fetch`**, so the job would have failed every run demanding a
file. It was also disabled. Three defects in one line, and none of them visible from the code: the capability was written, tested and
unreachable, which is the failure mode this repository has produced more often than any other (see `docs/KNOWN-ISSUES.md`, "every page
existed").

**What I did instead of writing the ingest.** Fixed the unit (`%h/concord-deploy/concord-rs healthsync --fetch`), added it to `deploy/`
with an hourly timer, enabled it, and ran it through systemd rather than my shell:

```
Nov 14 21:15:17 DEFERRED zeph: https://api.github.com/... -> 403 Forbidden: API rate limit exceeded
Nov 14 21:15:17 healthsync: synced 16, deferred 7, skipped 0, failed 0
```

`deferred 7` is the honest number: the seven GitHub repositories exceed the unauthenticated 60-requests-an-hour budget once a few calls
each are made, and the job reports a deferral instead of writing a measurement of zero. Against the database: 23 of 23 stated repositories
have a `last_commit_at` and a fresh `health_score` — `stash-box 0.656`, `stash 0.582`, `gravity 0.491`.

**Still blocked, and it is a credential:** `CONCORD_FORGE_TOKEN` would take the seven GitHub repositories from deferred to measured. Until
then the timer measures the 16 Forgejo ones every hour and says so.

### 6. There should be one discovery engine, not four

**What exists.** `search` (Postgres `ts_rank` over name and description), `embeddings` + `/similar/{kind}` (cosine over `nomic`, 5342
feature vectors and 146 project vectors), `finder` (entropy-driven questionnaire with its own scoring, 12 tests), `scout` (opportunity
radar, 22 tests), plus `taxonomy` for the vocabulary and `ranking` for arenas.

**Why it is wrong.** They answer overlapping questions — "what should I work on", "what exists like this", "what is missing" — with four
scoring paths over the same tables, and each has its own idea of what "unknown" means. This session's step 3 exists because of this: I wrote
a fifth (term-overlap intent scoring), the compiler refused it, and the right answer turned out to be the `/similar` route that was already
there. The lesson was cheap only because the compiler was in the way.

**What I would do.** One candidate scorer with modes (`by_text`, `by_questionnaire`, `by_gap`), one place that decides what a score means,
and the four surfaces as thin readers of it. Or, more honestly: keep two, freeze two, and say which two in a sentence.

**Cost to change now:** high. The defensible minimum is a written statement of which engine owns which question, so the next feature does
not add a fifth.

### 7. No CI, so every gate is a ritual

**What exists.** `cargo test`, `probe_pages.py`, `probe_project_segments.py`, `concord-parity`, eight browser suites, four mutation
harnesses — all run by hand, from memory, on one host, in an order I chose each time. There is no `.github/workflows`, no `Makefile`
target that runs the set, and no staging instance the probes can be pointed at that is not production.

**Why it is wrong.** A gate that depends on somebody remembering it is a gate that will be skipped on the day it matters, and this session
skipped `ui_mutants` and `probe_s5_*` for hours at a time without noticing, because nothing told me.

**What I would do.** One `make gate` that runs unit tests, the two probes against a scratch server, the parity replay and the suites; a CI
workflow that runs it on every push with a Postgres service container; and a nightly job that runs the mutation harnesses, because they are
slow and they only need to be proved once a day.

**Cost to change now:** low to medium, and it is the cheapest quality improvement available.

### 8. The lifecycle model should have been (measured stage) + (acts), from the start

**What exists.** A computed stage — `seed`/`sprout`/`mature`/`dormant`/`unknown` — and two acts that are absent from it: `archived` and
`forked & alive`. The sweep that marks dormancy writes an `audit_log` row rather than a column.

**Why it is wrong.** Not the split, which is right, but the order in which I found it: the note asks for six stages, I built four and
documented at length why the other two could not be inferred — which is a design discovery that belongs in the first hour, not at
implementation time. A stage that the code assigns to somebody's project is a claim; an act is a person's. That sentence should have been
the design brief, and then `archived` would have existed on day one as a column plus a route, instead of being deferred to a step that is
blocked on data.

**What I would do (and would still do).** Model every lifecycle fact as either *measured* (derived on read, never stored) or *declared*
(a row with an actor, a date and a reason). Then `archived` is a declaration with a route, `forked` is a declaration with a parent pointer,
and the projection is a join, not a rule.

**Cost to change now:** low for `archived` (a column, a route, an audit row, a badge — about two hours), and it should be done before the
adoption pool, because "declared abandoned" is the pool's entry condition.

### 9. The frontend should not be 13,000 lines of scripts with no build

**What exists.** `crates/web/assets/js/*.js` — 13,024 lines, loaded by URL, sharing state through `window` and `localStorage`, escaped by a
local `esc()` per file, with no module system, no bundler, no type checking and no unit tests. The interaction tests are Playwright suites
that drive a real browser (97 of them), which is the most expensive way to test a pure function and the only way to test these files.

**Why it is wrong.** Not because vanilla JS is wrong — it is fast and dependency-free, and that is a real virtue. It is wrong because there
is no boundary: `project.js` is 1,696 lines that fetch ten endpoints, render a board, run an arena, and post a vote. A bug in a date format
is found by a browser suite, and the escaping rule is re-implemented eleven times.

**What I would do.** Keep it build-free if you like, but split by capability (`board.js`, `arena.js`, `comments.js`), put the API calls in
one module with one error model, and share one `esc`. If a build step is acceptable, Svelte or Preact with a bundler buys components and
typing for about a day of setup — and the Playwright suites survive it unchanged, because they speak HTTP and the DOM.

**Cost to change now:** medium, and it is not urgent. The one thing worth doing immediately is extracting the fetch/error/escape helpers,
because that is where the subtle bugs live.

### 10. Docs that describe the dead lineage

**What exists.** `docs/concord-spec-r4.md` (826 lines, `§`-numbered), `docs/PLAN-r4.md`, `docs/REMAINING.md`, `docs/HANDOFF.md` — all
describing the **Go** implementation, with a gate (`check_doc_numbers.py`) that enforces numbers against a tree that is stopped. The Rust
port has one roadmap file and a ledger, both of which I wrote.

**Why it is wrong.** The governing documents of this product describe code that is not running. `HANDOFF.md` "names what is next" for a
service that is stopped. A new session reads the spec, believes it, and implements something the live binary does not do — which is exactly
the failure the port's own ledger documented, in a different form.

**What I would do.** One living document per concern: an architecture overview that says which binary serves and which is rollback, a spec
for the *product* (not the Go implementation), and the ledger for the port as history. Mark the Go docs `(historical)` in their first line,
the way the docs gate already knows how to read.

**Cost to change now:** low, purely editorial, and it prevents the next wrong turn.

## Smaller things, each a one-line fix

- **`/projects/new` is orphaned.** I built the create page this session and no page links to it: `grep -rn 'projects/new' crates/web` finds
  the route and the handler and nothing else. A page nobody can reach is a page that does not exist. *Fixed in the same commit as this
  review: a link on `/projects`, and the empty state names it.*
- **Seven pages have no `<h1>` in the HTML** (`/search`, `/finder`, `/curation`, `/rank`, `/ranking`, `/contribute`, `/notifications`) —
  a consequence of decision 2, and the cheapest accessibility win available.
- **`/projects/{slug}` answers 5.5 KB to a crawler**, so a project's content is invisible to search engines and link previews even though its
  `og:` tags are correct.
- **Finder sessions live in a mutex**, and every response says `persisted: false`. That is honest, and it means a restart loses a reader's
  session without warning beyond that field.
- **Rate limiting is per-process and per-`X-Forwarded-For`**, so it is a concurrency control only while there is one process, and it is
  spoofable by a client that sets the header. Behind Cloudflare that is mostly fine; it is not a security control and should not be read as
  one.
- **`embedbackfill` only prints at the end**, so a killed run looks like a completed one. It cost this session an hour and a false "done".
- **Three Rust timer units are installed disabled** (`-healthsync`, `-statsnapshot`, `-proposalsweep`) — wired to nothing, and the Go
  equivalents are what actually run. Either they run or they should be removed; "installed disabled" is the state that reads as done and is
  not.
- **Two lineages have no common ancestor** (`git log` for the Rust tree starts at the port). That is fine, but it means `git blame` on this
  codebase answers "the port", so provenance has to come from the ledger, and the ledger is a markdown file.

## What is genuinely good, and should be kept

Being critical is not the same as being dismissive. These are decisions I would make again:

- **Unknown is not zero.** `health_score`, `stage`, the finder's answers and the AI declaration all distinguish "not measured" from "zero",
  and the code enforces it with `Option` rather than a sentinel. This is the single best property of the codebase.
- **The stage is computed, not stored.** Adding repository identity changed 61/12 to 59/14 with no migration and no job — the model answered
  a new fact immediately. That is what a derived value buys.
- **Acts, not deletes.** Moderation, dormancy, withdrawal of a declaration are all append-only audit rows with the previous state carried.
- **Gates that can fail.** The mutation harnesses, the "refuses to apply an empty mapping" in `forgeseed`, the corpus replay catching a new
  field before a reader did. Each of these was proved by watching it fail once.
- **The honesty of the ledger.** Every row in `docs/plans/port-to-profile.md` carries the command and the quoted line that proves it. That
  habit is worth more than any of the code.

## If I had one week from scratch, in order

1. Migrations and a `schema_version` table (2 days).
2. One health ingest job that fetches and writes, replacing the file (2 days).
3. Server-render the project page, delete `project.js`'s render half (1 day).
4. `make gate` + CI, so 1–3 are defended while they are fresh (1 day).
5. Disable the Go timers and copy the 18 repository identities forward (1 hour, and it unblocks step 5 of the current roadmap).

Everything else on this list can wait; those five are the difference between a system that is maintained and a system that is curated by
memory.

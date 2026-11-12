# Concord — every feature, one line each

The inventory, not the argument. One line per capability a person can actually use or
that runs on their behalf; grouping is by where you meet it, and a line covers a route
family rather than a route (the tree registers 231 routes for the 106 features below —
`GET/POST /projects/{p}/features/` is one feature, "file and list ideas").

Kept honest by two things: every entry is reachable in the tree today (the route table
in `internal/httpapi/server.go` plus the packages under `internal/`), and the ones that
are deliberately absent are listed at the end with the reason. Design argument lives in
`docs/concord-spec-r4.md`; what is being worked on lives in `docs/PLAN-r4.md`; whether
something is finished lives in `docs/REMAINING.md`.

## Decide what to build

- **Complaints** — work starts as a structured problem report (who is hurt, how often, what the workaround costs), not a feature request.
- **"I have this too"** — anyone can add impact to a complaint; impact is counted, never asserted by the filer.
- **Validate / reject a complaint** — reviewers settle whether a complaint is real, with the reason kept.
- **Merge duplicate complaints** — a duplicate points at the one it duplicates, so impact adds up in one place instead of splitting.
- **Link features to complaints** — a feature's traceability is a set of validated complaints it claims to solve.
- **Features (ideas)** — the unit of planned work: title, body, effort size, status, tags.
- **Intake modes** — a project chooses complaint-first (default), idea-first, or both; idea-first lets an idea be filed before anyone is hurt.
- **Evidence gate** — a feature cannot reach `ready` without evidence: a validated complaint, a project document, or a field report.
- **Evidence alternatives** — attach a document or a field report to a feature instead of a complaint.
- **Detach a complaint** — unlink one the feature was filed against by mistake, and back the change with an audit line; the feature keeps its status, and the next move forward is what re-checks that evidence exists.
- **Evidence badge** — the feature page says whether it is complaint-backed or idea-first.
- **Field reports** — "I tried this and here is what happened", with responses, as first-class evidence from users rather than reviewers.
- **Feature claim** — a contributor says "I am building this", and can unclaim it.
- **Work item link** — a feature carries the external ref (branch, MR, ticket) that implements it.
- **Strategic weight** — a maintainer-set multiplier for a feature whose value the pairwise votes cannot see.
- **Status advance** — `draft → discussion → consensus → ready → in_progress → review → shipped`, each advance gated by its own rule.
- **Feature tags** — classify features by the same taxonomy as projects.

## Rank them

- **Pairwise voting** — "which should we build first, A or B?", one question at a time, cheap to answer and hard to spam.
- **Glicko-2 ratings** — every feature carries a rating and a deviation; priority is computed from comparisons, never hand-assigned.
- **Vote verification** — a sampled check that recorded votes match what the reader was shown.
- **Recompute** — rebuild the ratings from the vote log when a bug or a change of rule demands it.
- **Ranking page** — every feature of a project in rating order, with the standing-dependent Moderate link.
- **Composite rank** — a configurable blend of rating and strategic weight, exposed as a route so the page cannot disagree with the API.
- **Ranking criteria** — the project writes what "better" means, and members vote on criteria, not just features.
- **Criteria profiles** — saved weightings over the criteria, so a project can rank for "fast to ship" or "widest reach".
- **Tallies and priorities** — the counts a reader needs to see why the order is the order.

## Decide together (consensus)

- **Consent rounds** — consent, stand aside, abstain, or block with a stated principle and remedy.
- **Quorum** — a round only closes with enough of the project's standing present.
- **Objections** — a block is recorded with its principle and remedy, and resolved explicitly.
- **Block override** — an unresolved block needs a supermajority, or a logged safety veto that can be overridden.
- **Fallback** — what happens when a round cannot close, stated rather than improvised.
- **Close and outcome** — the round's result is written down, not inferred from the last position taken.
- **Per-feature consensus** — the same machinery scoped to one feature's readiness.
- **Emergency holds** — a safety stop that suspends movement and has a release path with a record.
- **Maintainer-led mode** — a project can opt out of collective consensus; the default is collective.
- **Charter** — the project's own rules (quorum, thresholds, roles) as a document the code reads.

## Plan the work (milestones and sequencing)

- **Milestones** — a named group of features with an optional target date and a status (planned, active, done, cancelled).
- **Milestone page** — its features grouped by status, with assign, unassign, reorder and close.
- **Assign / unassign** — from the feature page, or in bulk for a selection.
- **Order inside a milestone** — explicit position, moved with up/down rather than drag.
- **Roadmap page** — one column per milestone on a timeline, with the project's own figures.
- **Roadmap read route** — the page and the API read one shape, so their totals cannot disagree.
- **Milestone progress** — counts by status, with `seeded` and `unshipped` reported separately so an imported row cannot look delivered.
- **Slippage** — planned versus shipped, computed from the audit log; `null` rather than `0` when nothing has shipped.
- **At-risk flag** — derived when the target date has passed with open features.
- **Close with successor** — unfinished features carry forward to another milestone, one audit act each, or the milestone is cancelled without moving anything.
- **Next milestone** — the project overview names what is being planned, or says nothing is.
- **Milestones feed** — a feed item when a milestone opens or closes.
- **Feature dependencies** — `blocks` / `blocked_by`, with a refusal to start work whose blocker has not shipped.
- **Cycle refusal** — a dependency that would close a loop is refused, and the refusal names the cycle.
- **Blocked badge** — the feature page names each blocking feature and its status.

## Move the work (board)

- **Board** — columns per phase, cards derived for every feature, WIP limits per column.
- **Move a card** — explicit placement, recorded; a derived card gets a row only when somebody moves it.
- **Milestone filter** — the same board narrowed to one milestone, never a separate board.
- **Board feeds** — RSS and Atom of board movement.
- **Merge requests** — open a request tied to a feature, then approve, reject or execute it.
- **Approvals** — a technical approval plus a quorum of collaborator confirmations before an execute.

## Talk about it

- **Threads** — comments on any object kind (feature, complaint, solution, request), with votes and delete.
- **Solutions** — concrete proposals under a feature, competing in their own arena.
- **Solution coverage** — which complaints a solution claims to solve, and a contest path for a claim someone disputes.
- **Alternatives arenas** — side-by-side comparisons of any set of entries, with their own rankings.

## Classify and find

- **Project tags** — a shared taxonomy, applied to projects; completeness is a first-class measure.
- **Tag proposals** — a new or changed tag goes through consent, not a maintainer's whim.
- **Languages** — per-project language mix with percentages, not a single label.
- **Project metrics** — the measured facts a reader ranks on (commits, releases, contributors), each component exposed.
- **Maintenance health** — commit recency, review latency, contributor breadth, release cadence as one transparent score.
- **Responsiveness** — how fast questions and reviews get answered, with an explicit "not measured" state distinct from "fast".
- **Search** — a query language over tags, language, license, governance and health, with facets on every result.
- **Finder** — a short questionnaire that narrows to projects that fit, with back, session and lift-the-constraint steps.
- **Scout** — discovery of projects on other forges, harvested as candidates rather than added silently.
- **Curation queue** — human review of what discovery found and what tagging proposed, ranked and votable.
- **Capabilities** — a catalog of what a project can do (API, webhooks, plugins…), each with assertions that are confirmed or disputed, where "nobody has said anything" is a different state from "somebody recorded unknown".
- **Documents** — project-level documents with a kind, full-text search, and delete, which also serve as evidence for a feature.
- **Stats** — site and per-project summaries, history, and rising projects.
- **Similar (duplicate detection)** — text in, near-duplicates out, for complaints, features, requests and projects; an unconfigured instance says so rather than returning an empty list that reads as "nothing similar exists".

## Ask for help (the request board)

- **Requests** — describe what you need in plain language; the community sorts it out.
- **Answers** — anyone can answer a request, and answers are voted, not chosen by the asker alone.
- **Accept an answer** — the asker accepts (and can unaccept); accepting is what closes the loop.
- **Request tags** — what a request is about, so it can be found by the people who know.

## Lists

- **Lists** — curated collections ("awesome" lists) as data rather than a markdown file, so entries can be classified and ranked.
- **List entries** — added, described, and voted; merge wars become votes.

## People and access

- **Register / log in / log out / me** — token auth, no passwords sent anywhere but the login route.
- **Invites** — mint, list, revoke, and redeem; a project grows by invitation.
- **Join a project** — membership with a role (owner, maintainer, reviewer, contributor).
- **Members** — the roster and who holds which role.
- **Guest access** — a bounded, MAU-limited guest identity so a drive-by contribution is possible without an account.
- **Trust levels** — standing computed from behaviour, used for what a reader may do without being trusted by name.
- **Visibility** — public, unlisted or private, changed as an audited act.
- **Cookie consent** — the notice is recorded server-side against the reader, so what is stored and why is answered before anything is.
- **Contribute** — "what should I do next", a single ranked suggestion drawn from the work that is actually unblocked.

## Trust the record

- **Audit log** — every state change, filterable by action and by named family (the moderation log is one filter, not a separate table).
- **Moderation acts** — remove and restore an arena entry, reject and unreject a feature, each undone by its inverse.
- **Audit actions route** — the list of act names the log can be filtered by, read from the store rather than a second copy in the client.
- **Admin ledger** — the small set of actions that only an instance admin can take, kept apart from project acts.
- **Proposal sweep** — expired proposals close by themselves, on a schedule, with a record.
- **Feeds** — per-project RSS and Atom for the project, board, consensus, documents, milestones, plus the site feed.
- **Badge** — an SVG health badge per project for a README.

## Runs on its own

- **Forge seeding** — a CLI that enumerates repositories on configured forges into candidates.
- **Health sync** — a scheduled crawl that refreshes each seeded project's metrics, skipping what has not changed and deferring what it cannot fetch.
- **Stats snapshots** — scheduled measurement, so "rising" is computed against history rather than a guess.
- **Embeddings and backfill** — project text embedded for similarity, with a backfill path for what was written before.
- **Capability seeding** — the plugin/capability inventory assembled from manifests.
- **Trust granting** — an admin CLI for the cases trust cannot be earned in-band.
- **Forgejo webhook** — real push and pull-request events arrive and land in the event table, so review latency is measured rather than assumed.
- **Single binary** — CGO-free Go server plus SQLite, with the migrations, templates, JS and CSS embedded.
- **Health and version** — `/api/v1/healthz` and `/api/v1/version` for the deploy and the tunnel.

## Deliberately absent

- **No issue tracker** — complaints are structured problems, not tickets with a thread and a label.
- **No hand-set priority** — priority is computed; the only manual multiplier is strategic weight, which is visible.
- **No git hosting** — Concord reads forges (Forgejo, GitHub) rather than being one; merge requests reference external refs.
- **No per-milestone board** — one board, filtered; a column's WIP limit is a claim about the project's capacity, and a per-milestone limit would make the same column mean two things.
- **No drag-and-drop ordering** — position moves up and down, so the order has a keyboard path.
- **No milestone-level consensus** — a consensus call is per feature; grouping features into a milestone does not make them one decision (this is the next milestone in `docs/PLAN-r4.md`).

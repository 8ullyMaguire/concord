# Concord — feature depth catalog

_Generated from `seed/concord_ideas.json` on 2026-10-30. 26 shipped surfaces, 6 planned milestones, 311 filed ideas across 24 themes._

Three parts, and the order matters:

1. **Shipped** — what the instance runs today, so a reader can tell an idea from an absence.
2. **Planned** — the milestones that already have a ticket, so the new list does not re-file them.
3. **Filed** — ideas that were NOT already on the roadmap, grouped by the complaint they answer.

Every idea in part 3 exists on the live instance as a draft feature linked to that theme's complaint, filed by `scripts/file_ideas.py`. The effort tag is the filer's estimate (**S** hours, **M** a day or two, **L** a week, **XL** more), never a promise.

---

## Part 1 — shipped

### Platform

- **Complaint-first development** — Every feature must answer a validated complaint; an unlinked feature ranks on nothing. Complaints carry severity, frequency and a strategic multiplier.
- **Pairwise ranking with Glicko-2** — Features and solutions are ranked by judged pairwise comparisons, with rating deviation and volatility, not by star counts or upvote totals.
- **Consensus with quorum, blocks and an override window** — Calls close on quorum, a standing block can hold them, and a second window lets a block be overridden with a written remedy.
- **Decision records** — Every closed call writes a record: the alternatives, the tally, the agreed fallback and the date, linked from the feature.
- **Trust and reputation events** — Contribution events carry points, roles are granted with a reason, and guest accounts can be folded into a real account without losing rows.
- **Kanban board with phase gates** — Draft to ready to in-progress to shipped, with the gates that stop a feature skipping a phase.
- **Audit ledger** — Every privileged action writes a row that cannot be edited; the ledger is the trust anchor for everything else.
- **Documents, lists and requests** — Long-form documents, curated lists and a public request board, each with its own routes and readers.
- **Taxonomy, themes and criteria** — Categories, strategic themes with consensus-ratified weights, and criteria profiles evaluated per project.
- **Search with facets and a query language** — tag:, language:, NOT, phrase search, facets per dimension, and a relevance sort that is explainable.
- **Embedding-based duplicate detection** — Near-identical complaints and features are detected at filing time and shown as an advisory warning rather than a refusal.
- **Arena and alternatives** — Competing solutions to one complaint are judged in pairs, with an arena weight and a verification path.
- **Field reports and capabilities** — People who used a shipped solution report whether it worked, weighted by reputation; capabilities are confirmed by more than one reporter.
- **Finder** — A session-based question flow that narrows the catalog by information gain, with gaps shown when no question is worth asking.
- **Merge gate and redirects** — Duplicate projects can be merged with their rows moved and their slug redirected, rather than deleted.
- **Guest accounts and consent** — Anonymous participation with a consent state that is decided server-side on first paint.

### R1

- **Arenas** — Judged pairs of alternative solutions, with an arena weight and a verification path.

### R2

- **Solutions** — A solution answers one or more complaints and can be judged against a rival.

### R3

- **Solution ranking and coverage** — Ranked solutions with a coverage claim that says which complaints a solution answers.

### R4

- **Ranking feeding consensus** — The ranking gates which alternatives reach a call, so a call is a decision among ranked options.

### R5

- **Field reports and capabilities** — Outcome reports from people who used a solution, plus confirmed capabilities per project.

### R6

- **Scout** — Ask whether a project should exist: a structured interrogation with a verdict and its reasoning.

### R8

- **Administration surface** — Visibility, tags, languages and invites on the settings page, with the read halves that were missing.

### R9

- **Repository identity and health ingest** — Canonical repository URLs per project, a forgeseed command that refuses an incomplete mapping, and an hourly ingest that measures recency, breadth, cadence and review latency -- with unknown distinct from zero.

### R10

- **Work-item contract** — Difficulty, an optional time budget, needs-mentor and a claim with a partial unique index, exposed as the work-items API and a project panel.

### R11

- **Contribution feed** — Open work items filtered by language, time budget, difficulty and mentor, ranked through the Finder engine and a project-health tiebreak, on a server-rendered page.

---

## Part 2 — planned (already ticketed)

| id | milestone | what it does |
|---|---|---|
| R12 | **Newcomer responsiveness** | Median hours to a first response for a first-time pull-request author, published only with its sample size and never folded into the health score. |
| R13 | **Census with a provenance split** | Composition of the instance with seeded rows separated from organic ones, because 925 of 1,874 features have no creation event and a growth chart of those is a chart of one import. |
| R14 | **Decision latency and exposure** | Three medians -- complaint to validation, ready to shipped, call opened to closed -- each with its n and its unfinished count, plus a stats page, SVG badges and a feed item. |
| R15 | **Snapshot history and derived surfaces** | A daily snapshot table, a 90-day history route, and only then rising trends and a responsiveness leaderboard. |
| R7 | **Documentation sync** | Keep the specs, the plan and the remaining-work log in step with the code as a gate rather than a habit. In progress. |
| Q5 | **Repository mapping** | Sixty-nine of seventy-four catalog projects have no stated repository, so they cannot be measured; the mapping is an owner decision, not an implementation step. |

---

## Part 3 — filed: 311 ideas

Grouped by theme. The line under each heading is the complaint the ideas are filed against on the instance — the reason the group exists, in pain terms rather than feature terms.

### Discovery and search (13)

> **Complaint:** _I cannot find the thing I know exists._ Search and browsing return the wrong projects, or nothing, and there is no way to tell why. Finding a project by the problem it solves -- rather than by its name -- does not work.

1. **Rank search results by complaint pain, not recency** (M) — A search for 'export' should surface the project whose complaint volume says export is its worst problem, not the project that edited its description yesterday. The scorer already has pain scores; the search ORDER BY does not read them.
2. **Explain every search result's rank in one click** (S) — Each hit gets a 'why this?' disclosure listing the terms that matched, the pain score, the arena standing and the health term with their weights, so a surprising order is diagnosable instead of mysterious.
3. **Search inside complaint and feature bodies, not just titles** (S) — Full-text is already indexed for entities; the query parser only admits titles in some paths. Unified search over title+body with match highlighting per field.
4. **Saved searches with a diff feed** (M) — Persist a query and a baseline result set; the feed then reports 'three projects entered, one left, two moved up' since the last visit instead of a static list.
5. **Faceted search over governance model, license and language at once** (M) — The facet SQL exists per dimension; a combined facet panel with counts that update as each filter narrows, and a 'no results because of X' breakdown when the intersection is empty.
6. **A 'similar projects' panel built from the embedding index** (M) — Every entity already has an embedding; project-level similarity is one aggregation away and gives a reader a way out of a dead end without a second search.
7. **Search that understands complaints as queries** (L) — Type a sentence of pain ('I cannot tell which call was decided') and get ranked features that answer it, with the complaint text used as the query rather than keywords.
8. **Zero-result recovery with query rewriting** (S) — On no hits: drop the rarest term, then the filter with the smallest population, and say which one was dropped. Today a zero result is a dead end.
9. **Taxonomy-guided browse that shows population before you click** (S) — Every taxonomy node carries a live count; a node with zero members is visibly empty rather than a link to an empty page.
10. **Pinned 'start here' paths per intent** (M) — Curated three-step journeys (evaluate, contribute, complain) assembled from real routes, stored as data so they can be edited without a deploy.
11. **Cross-project duplicate detection at proposal time** (S) — When a feature is filed, the similarity search runs instance-wide and tells the filer which other project already ships it, with a link.
12. **A public sitemap of every project's surfaces** (S) — Human-readable and machine-readable (/sitemap.xml), generated from the router so a new page cannot be invisible to crawlers.
13. **Search analytics that never store the searcher** (M) — Count queries, zero-result rate and click-through per term with a k-anonymity floor, so the catalog can be improved without logging who searched.

### Onboarding and first steps (13)

> **Complaint:** _I do not know what to do first here._ A newcomer lands on the site and cannot tell what the first useful action is: which page to read, what a complaint is, or how to contribute a fifteen-minute task.

1. **A first-session checklist generated from real state** (M) — Four steps (read a complaint, vote on a pair, claim a task, file a complaint) where each is checked by querying the database, not by a client-side flag.
2. **A 'your first hour' guided tour built from the contribution feed** (M) — One route that walks a newcomer from their language to a 15-minute task, using the feed's own ranking so the tour cannot recommend something the feed would not.
3. **Explain the complaint-first rule in the place it bites** (S) — The feature form already refuses an unlinked feature; inline it with a one-paragraph rationale and a 'file the complaint first' link that preserves what was typed.
4. **Sample data in a scratch project a newcomer can break** (M) — A sandbox project, seeded with a small governance history, that accepts votes and claims from anyone and is excluded from every public ranking.
5. **Pair a newcomer with a mentor from the task's own field** (M) — Work items already carry needs_mentor; a mentor opt-in on the profile turns that flag into an actual name and a channel, instead of a label.
6. **A per-project 'what this place is for' paragraph an owner writes** (S) — The description is a tagline today; a longer intent statement, rendered above the complaint box, is what stops off-topic filings.
7. **Onboarding email that arrives only after a real action** (S) — Send the welcome after the first vote or complaint, not at signup, so it can reference what the person actually did.
8. **Keyboard-first navigation with a discoverable cheat sheet** (S) — A '?' overlay listing every shortcut, generated from the same table the handlers use so it cannot drift.
9. **A glossary of Concord's own vocabulary** (S) — Complaint, validated, ratified, quorum, override window, arena, field report: each defined once, linked inline with a tooltip from every use.
10. **Show the trust ladder with the next rung's requirement** (M) — A member sees what their current role permits and exactly what would raise it, computed from the same rules the guard enforces.
11. **A 'what changed since you last looked' strip on the home page** (M) — One line per project the visitor follows, summarising the decisions since their last visit rather than a wall of timestamps.
12. **Interactive example of a consensus decision** (L) — A replayable toy call with five voters where the reader can change a vote and watch quorum, block and override behave, using the real governance package compiled to WASM or mirrored in JS from the same test vectors.
13. **Refuse nothing silently: every disabled control says why** (S) — A shared pattern for disabled buttons with the reason and the remedy inline, replacing the current mix of hidden and greyed-out controls.

### Complaint quality (13)

> **Complaint:** _Complaints are vague, duplicated or impossible to act on._ Reports arrive without reproduction steps, restate each other, or name a symptom nobody can reproduce, so validating them is guesswork and the good ones are lost in the noise.

1. **A rejection reason taxonomy instead of free text** (S) — Rejecting a complaint currently writes a free-text note; a short fixed list (not reproducible, duplicate, not about this project, abuse) makes rejection patterns measurable and appealable.
2. **Merge duplicates instead of rejecting the second one** (M) — A merge keeps both reporters' weight and links the survivor to both, so a popular complaint filed twice does not lose half its evidence.
3. **Ask for reproduction steps with a structured field** (S) — Three optional fields (what you did, what happened, what you expected) that render as a blockquote on the complaint page and feed the embedding.
4. **Auto-draft the title from the body** (S) — A filer who writes two sentences gets a suggested title they can edit; most vague titles come from writing the body last.
5. **A validation queue with the evidence summary inline** (M) — The queue shows severity, frequency, reporter trust and similar existing complaints side by side, so validating takes one screen instead of four.
6. **Time-boxed validation with an escalation** (M) — An open complaint older than N days is surfaced to the project's maintainers with the option to validate, reject or explicitly defer, so nothing rots in 'open'.
7. **Complaint clustering by embedding, with a canonical example** (L) — Group near-identical complaints into a cluster whose page shows all reporters and the canonical wording, then let features link the cluster rather than one row.
8. **Severity calibrated against outcomes** (L) — Compare the severity a reporter claimed with the impact the shipped fix had (field report rate); publish the calibration per reporter so severity stops being a free dial.
9. **Require a 'what I tried' field before a duplicate is claimed** (S) — Duplicate detection is advisory; asking what the filer already searched turns a lazy duplicate into a genuine one or a search that failed, and the second is a bug.
10. **Complaint expiry for projects that stopped caring** (M) — A complaint untouched for a year on an archived project is marked stale rather than open, so instance-wide pain scores are not inflated by abandoned projects.
11. **A public complaint quality score per project** (M) — Ratio of validated to rejected to stale, with the same n-always rule as the other metrics, so a project that rejects everything is visible.
12. **Link complaints to the exact surface that hurt** (S) — An optional path field (/projects/{slug}/board) recorded when the complaint is filed from that page, which makes the pain locatable and the fix testable.
13. **Report a complaint's resolution back to its reporters** (M) — When a feature linked to a complaint ships, every reporter gets one notification with the diff, closing the loop that otherwise ends at 'validated'.

### Feature proposals and planning (13)

> **Complaint:** _The backlog proposes things without evidence or cost._ Features are filed without a linked complaint, without a size anyone can interpret, and without showing what they would displace, so the roadmap is a pile rather than a plan.

1. **A cost model that turns effort sizes into hours** (M) — S/M/L/XL is a label nobody can compare; a per-project table calibrating each size to a median actual, derived from claim-to-close time, makes the sizes mean something.
2. **Automatic impact estimate from the linked complaints' pain** (S) — When a feature links a complaint, prefill impact with the complaint's pain score band, marked as a suggestion the author can override with a reason.
3. **Show what a feature would displace** (M) — Every proposal ranked by the board is really a claim on the same capacity; a panel showing 'this is above N other ready features' makes the trade-off explicit at filing time.
4. **Split a feature into work items at proposal time** (M) — The work-item contract exists but is filled after the fact; a template with three sub-tasks on creation turns a proposal into something a newcomer can claim on day one.
5. **A 'what would have to be true' field for speculative features** (S) — One optional line stating the assumption that makes the feature worth building, surfaced again when the feature is picked up, so dead assumptions are noticed rather than inherited.
6. **Reject features whose every linked complaint is stale** (S) — A cheap guard: if all linked complaints are stale or rejected, the feature drops to the backlog with that reason instead of ranking on dead pain.
7. **Estimate confidence, not just impact** (M) — A three-point (low/likely/high) impact estimate per feature, shown as a spread; a single number hides the disagreement that matters most.
8. **A dependency field between features** (L) — Features can block each other; declaring it lets the board show a critical path and stops a feature being claimed before its prerequisite ships.
9. **Auto-close features superseded by a shipped one** (M) — When a feature ships, the similarity search runs over the remaining backlog and proposes supersede links for review, so the backlog does not keep proposing what exists.
10. **Public 'why not' notes on rejected features** (S) — A rejected feature keeps a one-paragraph rationale visible at its own URL, so the same idea is not re-filed every quarter by someone who never saw the first discussion.
11. **Board view grouped by complaint theme** (M) — The board is a flat list by status; grouping by the theme of the linked complaints shows whether the roadmap is coherent or nineteen unrelated things.
12. **Cycle-time forecast from the last N deliveries** (M) — Median time from ready to shipped, per effort size, rendered as 'this is likely to land in about three weeks' with its sample size.
13. **A 'smallest version' field enforced by a lint** (S) — Every XL feature must name a smaller version that delivers part of the value; a lint refuses to mark it ready otherwise. XL features are where backlogs go to die.

### Consensus and governance (13)

> **Complaint:** _Decisions stall, or get made by whoever is loudest._ Calls sit below quorum for weeks, voters miss deadlines in other timezones, and a contested decision leaves no record of why the minority disagreed.

1. **Quorum targets shown live on every open call** (S) — The consensus page states the rule; a countdown to quorum ('4 of 7 needed, 2 unreachable') turns a stalled call into an actionable one.
2. **Nudge the people whose vote would decide a call** (S) — When a call is one vote from quorum, notify exactly the members who have not voted, once, with the deadline — not a broadcast.
3. **A written minority report attached to every ratified decision** (M) — Objections are counted today; capturing the strongest dissent verbatim (one optional paragraph per objector) preserves the reasoning that the tally destroys.
4. **Decision records exported as readable documents** (M) — The decision record exists as rows; render it as a one-page markdown document with the alternatives, the tally, the dissent and the date, and link it from the feature.
5. **Re-open a decided call when its assumptions break** (L) — A superseded decision can be reopened by citing the changed fact, with quorum on the reopen rather than on the original question.
6. **Weight votes by participation history, with the rule stated** (L) — A member who has voted on 90% of calls should not weigh the same as one who votes on 5%, as long as the rule is published, ratifiable and visible on the call.
7. **Time-zone aware voting windows** (S) — Show the close time in the reader's own zone and guarantee at least one round-the-clock period, so a call cannot close while half the voters sleep.
8. **Abstentions that carry a reason** (S) — An abstain is currently a non-vote; letting it carry 'I lack context on X' routes real information to the proposer and distinguishes indifference from ignorance.
9. **Consensus health over time per project** (M) — Median time to quorum, share of calls decided by override, share closed at deadline: three numbers that diagnose a governance process better than any single vote.
10. **Simulate a proposal's likely fate before filing** (L) — Given the current membership and recent voting patterns, show the quorum it would need and the blocks it would attract, as a forecast explicitly labelled a forecast.
11. **Delegation with a public ledger** (L) — A member may delegate their vote on a theme to someone else; the delegation and its revocation are both audited and shown on every call it affects.
12. **Emergency holds that expire by default** (S) — An operator hold exists; give it a mandatory expiry and a written justification, so a hold cannot quietly become a decision.
13. **A 'consensus' page that teaches by example from this instance** (M) — Render three real closed calls (one unanimous, one contested, one overridden) as case studies with the tallies that decided them.

### Trust and reputation (13)

> **Complaint:** _Reputation is a black box and easy to game._ Nobody can see how their weight was computed, activity counts the same as accuracy, and a long history becomes a permanent advantage nobody can challenge.

1. **One page per member: what they did, what it earned** (M) — Reputation events exist as rows; a profile that lists them with the points each carried makes the score auditable instead of a mystery number.
2. **Decay reputation that is never exercised** (M) — A vote cast three years ago should not still weigh; an explicit half-life with the rule published, and the decayed value shown beside the raw one.
3. **Cap the weight a single member can hold** (S) — A hard ceiling with the excess redistributed proportionally, so a long history cannot become a permanent veto.
4. **Publish the reputation curve as a chart, not a formula** (S) — The formula is in the code; a rendered curve with the reader's own position marked is what makes it understandable.
5. **Separate accuracy from activity** (M) — Two scores: how often a member's predictions were right (validation accuracy, field-report accuracy) and how much they do. Conflating them rewards volume.
6. **Dispute a reputation event with evidence** (M) — Every negative event is appealable once, with the appeal and its outcome recorded on the member's page and visible to whoever set the role.
7. **Trust grants that expire** (S) — Granting contributor rights for a project can carry an expiry, so access granted for one release does not become permanent by inattention.
8. **A 'trusted by' panel on the project page** (S) — Who vouched for this project, and when, drawn from actual grants rather than membership count.
9. **Sybil resistance for guest accounts without an email** (L) — Guest contributions can be cheap to farm; a proof-of-work stamp or a rate that scales with account age, stated plainly at signup.
10. **Field-report weight tied to demonstrated skill** (L) — A report from someone who has shipped in the area counts more than one from a passer-by, computed from their own shipped work and published.
11. **A public log of every role change** (S) — Who promoted whom, with the reason field mandatory, in the audit ledger's existing format — promotion is the highest-leverage admin action and the least documented today.
12. **Revocation that explains itself to the revoked** (S) — Losing a role currently just changes what buttons work; a notification with the rule and the path to appeal.
13. **Reputation portability across projects on the instance** (L) — A member's accuracy on one project informs their starting weight on another, bounded so a bad first project cannot follow them forever.

### Moderation and abuse (13)

> **Complaint:** _Abuse has no proportionate response._ The only tools are a report box and a ban. Low-grade harassment, coordinated voting and spam from throwaway accounts have nothing between 'ignore it' and 'remove the person'.

1. **A graduated response ladder with published thresholds** (M) — Warn, then limit, then require review, then suspend: each step with a stated trigger and a time bound, so moderation is a policy rather than a mood.
2. **Shadow-limit instead of ban for low-severity abuse** (M) — The abuser keeps posting and sees their own content; nobody else does. Removes the incentive to sock-puppet while a review happens.
3. **Report categories that route to different queues** (M) — Spam, off-topic, harassment, illegal and security go to different people with different deadlines, instead of one undifferentiated report box.
4. **A reviewer-facing evidence bundle** (M) — One page per report: the content, the history of the reporter and the reported, prior reports, and the action taken last time — so decisions are consistent.
5. **Appeals with an independent second reviewer** (M) — An appeal goes to somebody who did not make the first decision, and the fact that they were independent is recorded.
6. **Rate-limit by behaviour, not just by IP** (L) — Bursts of near-identical text, many accounts from one browser fingerprint and rapid role-change attempts are the shapes that matter; IP limits catch almost none of them.
7. **Content warnings on complaints about third parties** (S) — A complaint that names a person can be filed with a warning flag, so the page does not broadcast an accusation as its first impression.
8. **Delete-with-tombstone instead of hard delete** (M) — Deleted content leaves a row saying what was deleted and why, so threads and audit trails do not develop holes that look like tampering.
9. **A transparency report generated from the audit ledger** (M) — Actions per month by type, appeals upheld, and the median time to first response, computed from rows that already exist and published as a page.
10. **Keyword walls that explain themselves** (S) — When a submission is refused for a blocked term, name the category rather than the word list, so a legitimate filer can adapt instead of guessing.
11. **Escalation on unanswered reports** (S) — A report with no action within the policy window escalates to the next role automatically and appears in a queue nobody can miss.
12. **Self-service data removal on request** (M) — A member can request removal of their own content; the request, the decision and the reason are recorded, and the outcome is a tombstone.
13. **Abuse-resistant voting on controversial calls** (L) — Coordinated voting is hard to prove; requiring quorum diversity (distinct accounts older than N days) on calls flagged as contested is a cheap structural defence.

### Notifications (13)

> **Complaint:** _I miss what matters and get pinged for noise._ Everything is either silent or a broadcast. There is no digest, no per-project control, and no notification when the complaint I filed is validated or the fix ships.

1. **One digest per person per day, with a truthful count** (M) — Every event type currently competes for attention; a single daily digest ranked by relevance, and nothing else, except for direct mentions.
2. **Per-project, per-event-type preferences that persist** (M) — A matrix of project × event (decision, ship, reply, mention) stored server-side, with a 'quiet project' switch that is one click.
3. **Email that works without a client** (M) — A plain-text multipart alternative and a one-click unsubscribe that needs no login, because a digest nobody can leave is a spam folder waiting to happen.
4. **Web push with a permission prompt that earns itself** (M) — Ask after the second visit and the first follow, never on load, and state what will be sent.
5. **A notification when your complaint is validated, rejected or shipped** (S) — Three specific moments a reporter cares about; today the reporter learns nothing until they poll.
6. **Do-not-disturb windows in the member's own timezone** (S) — A stated quiet window that holds non-urgent mail until it ends, with urgent (security, role change) exempt and marked.
7. **An inbox that survives the device** (M) — Read state stored server-side, so the badge is the same on the phone and the laptop.
8. **Batching for high-volume projects** (L) — A project with 200 watchers should not create 200 notification rows per event; fan out at read time from a single event.
9. **Watch a specific feature, not a whole project** (S) — The useful granularity for most people is one decision, and the current model only offers the project.
10. **Delivery diagnostics the member can see** (S) — A page listing what was sent, when, and whether it bounced, so 'I never got it' is answerable.
11. **A weekly 'what you missed' that is skimmable in 30 seconds** (M) — Five bullets, each a real decision with its outcome, ordered by how much the reader's own votes mattered.
12. **Notifications that link to the exact anchor** (S) — Every notification deep-links to the comment, tally or diff it refers to, not to the project root.
13. **Suppress notifications for your own actions** (S) — An author currently gets notified about their own edits on some paths; a single filter at emit time, with a test.

### Feeds and syndication (13)

> **Complaint:** _Progress is invisible from outside the site._ Following a project means visiting it. There is no usable feed, no per-decision subscription, and nothing for a reader who does not want an account.

1. **One RSS feed per project with full history** (S) — The feed item exists per decision; a proper Atom feed with stable IDs, pagination and an 'all history' variant lets readers follow without an account.
2. **JSON Feed alongside RSS** (S) — The same items in JSON Feed 1.1, so scripts do not parse XML.
3. **Per-feature feeds for long-running discussions** (S) — Someone following one contested decision should not have to follow the project.
4. **WebSub or a ping endpoint for feed readers** (M) — Announce new items instead of polling, so a feed reader sees the update in minutes.
5. **An instance-wide 'recently decided' feed** (S) — Across projects, what was ratified, overridden or blocked, with the tally — the single best signal that the instance is alive.
6. **Feed items that carry the reasoning, not just the title** (S) — Include the winning argument's first paragraph and the vote split in the item body, so the reader can judge without clicking.
7. **Stable feed IDs that survive a rename** (S) — IDs derived from the decision row, not the slug, so a project rename does not duplicate every item in every reader.
8. **A monthly archive feed** (S) — One item per month summarising decisions and ships, for people who want to follow at low frequency.
9. **Conditional GET and ETags on every feed** (S) — Feed readers poll constantly; a correct 304 keeps a tunnel's bandwidth honest.
10. **Feeds for a saved search** (M) — Follow a query rather than a project: 'everything matching export in any project'.
11. **A 'why this item is in your feed' line** (S) — Each item states the subscription that produced it, which makes a noisy feed diagnosable.
12. **Atom categories from the taxonomy** (S) — Tag every item with its theme so a reader can filter without a full-text rule.
13. **Feed discovery from the HTML head** (S) — Every page advertises its relevant feed with a link rel=alternate, so a reader extension can find it.

### Documents and knowledge (13)

> **Complaint:** _Knowledge lives in threads and is never written down._ Specs are documents nobody links to, decisions are rows nobody reads, and the same explanation is given again every few months in a comment.

1. **Docs that are versioned with the code, not in a wiki** (M) — The documents surface exists; bind a document to a path in a repository and show a diff when it changes, so the spec and the code cannot drift silently.
2. **Inline anchors for every spec section** (S) — Stable permalinks per heading, and every reference elsewhere (issues, decisions, code comments) can cite §section as a link rather than a string.
3. **A 'this spec is stale' flag computed from code churn** (M) — When the files a spec covers change more than N times without the spec changing, mark it visibly stale on its own page.
4. **Decision records linked from the code they explain** (M) — A comment convention (a decision URL) that the linter checks, so a non-obvious branch points at the reasoning instead of at a ticket.
5. **A glossary that is also a schema** (M) — One YAML file of terms; the glossary page, the tooltips and the lint that rejects undefined jargon all read it.
6. **Architecture diagrams generated from the module graph** (M) — Render the import graph with the layering rules annotated, so a violated dependency shows as a crossing arrow rather than a review comment.
7. **A changelog assembled from shipped features** (S) — Each ship event already has a title and a body; group by week and project into a public changelog with no extra authoring.
8. **Docs search that searches code too** (M) — One box that returns both spec sections and the symbols they describe.
9. **Markdown linting in CI with the project's own rules** (S) — Enforce heading order, link validity and a maximum ratio of prose to examples, and fail the build on a broken internal link.
10. **A 'how this project decides' one-pager per governance model** (S) — Generated from the governance package's parameters, so a model change updates the explanation automatically.
11. **Commentable specs with consensus on the paragraph** (L) — A spec paragraph can be attached to a call; the decision cites the paragraph and the paragraph shows the decision's state inline.
12. **Doc ownership with a reviewer field** (S) — Each document names an owner who is notified when it changes, ending the 'everybody's document' problem.
13. **Export a project's whole documentation as one PDF or EPUB** (M) — One command produces a readable artifact with a table of contents, for readers who do not read on the site.

### Requests from the outside (13)

> **Complaint:** _What users ask for never reaches the roadmap._ Requests arrive in a form, disappear into a list, and are answered by hand if at all; there is no count of who wants what and no honest 'we are not doing this'.

1. **Requests that can be promoted to complaints in one click** (M) — A user ask that many people repeat is a complaint with evidence; the promotion copies the thread and links the raised hands as frequency.
2. **Public counts of who wants a request** (S) — A +1 that is a real relationship row rather than a counter, so the count is attributable and retractable.
3. **Request status that mirrors the roadmap automatically** (M) — When a linked feature moves to ready or ships, the request's status follows, so nobody answers the same ask by hand.
4. **A monthly 'requests we are not doing, and why' page** (S) — Saying no in public, once, with a reason per request, is cheaper than refusing the same ask twelve times.
5. **Requests from inside a project page, with the current page attached** (S) — The most common request is 'this particular screen is wrong'; capture the URL automatically so the request is locatable.
6. **Anonymous requests with a reply channel** (M) — A guest can file without an account and still get an answer, via a ticket URL that works like a reply address.
7. **Cluster requests by embedding, show the cluster** (M) — Ten asks about export are one ask; the cluster page is the thing the roadmap links to.
8. **A response-time promise, measured and displayed** (S) — State 'we answer requests within five days' and show the actual median next to it, including when it is bad.
9. **Requests that expire with a re-ask** (S) — An unanswered request older than a quarter closes with an apology and a 'still want this?' that reopens it with the new signal.
10. **Route requests by theme to the people who own the area** (M) — A theme owner gets the request instead of a shared inbox, and the routing rule is visible on the form.
11. **Show the requester what happened to their ask, unprompted** (S) — One email at the moment the outcome is known — built, declined, deferred — not a dashboard they have to remember to check.
12. **A billing-style statement of what the maintainers did this month** (M) — Hours spent, decisions made, ships landed: the answer to 'is anyone home?' that a request board alone cannot give.
13. **Requests importable from email and chat** (M) — A forwarding address that turns a message into a request with the sender's consent recorded.

### Project health and metrics (13)

> **Complaint:** _Project health is unmeasured or misleading._ The health score shows a number with no components, 'no data' renders as zero, and a project that died six months ago still looks fine in a list.

1. **Show the health score's components on the project page** (S) — The breakdown exists in the package and the search UI shows a bar; the project page should show recency, responsiveness, breadth and cadence with their weights and unknowns.
2. **A health history sparkline from the snapshots** (M) — Once R15 lands, a 90-day line per component makes 'this project is dying' visible before it is dead.
3. **Distinguish 'no data' from 'bad' everywhere health appears** (S) — One shared badge component with an unknown state, replacing the current mix of dashes, zeros and absent chips.
4. **Health thresholds that trigger an automatic flag** (M) — A project whose recency drops below a stated line for a stated period gets a public 'needs maintainer' banner, not a silent score.
5. **Bus factor from commit concentration** (M) — A single number: the share of recent commits by the top author, with the measurement stated, because the honest answer to 'who would leave' is usually one person.
6. **Issue-response time as its own metric** (M) — Review latency is measured; first response to an issue is the number a user actually feels.
7. **Dependency freshness** (M) — Share of declared dependencies within one major of current, with the sources and the check date published.
8. **Test and CI signal imported from the forge** (M) — Passing rate and time-to-green over the last 30 runs, if the repository exposes it.
9. **A public methodology page with the exact formula** (S) — Every component, weight and cut-off on one page, versioned, so a disputed score can be recomputed by hand.
10. **Score a project's documentation, not just its code** (S) — Does it have a README with install steps, a changelog, an architecture note? Measured mechanically and stated as a checklist.
11. **Compare a project against its own history, not the instance** (M) — An absolute score ranks projects against each other; the useful question for a maintainer is whether this month beat last month.
12. **Let a maintainer dispute a metric with evidence** (M) — A disputed component is shown as disputed with the counter-evidence attached, rather than silently overriding the number.
13. **Health for projects with no repository, derived from site activity** (L) — Sixty-nine catalog entries have no forge; decisions, complaints and ships are still measurable signals of life.

### Data portability and exit (13)

> **Complaint:** _Leaving with my data is hard._ There is no export, no documented format, and no way to verify that a backup can be restored. What I contributed here is only readable here.

1. **A one-click full export of a project's data** (M) — Complaints, features, votes, decisions, documents and audit in a documented JSON layout, zipped, streamed, with a checksum — the honest counter to a walled garden.
2. **Import that round-trips the export** (L) — An export nobody can re-import is a backup, not portability; a test that exports, wipes a scratch instance and re-imports, comparing counts and IDs.
3. **A stable, versioned data format** (M) — Ship the export with a schema version and a migration note per change, so a third-party tool can rely on it.
4. **Public read API without authentication** (M) — Everything a logged-out reader can see should be readable by a script, with documented rate limits and pagination.
5. **A data retention policy the database enforces** (M) — A stated window per table with a job that applies it, so 'we keep everything forever' is a decision rather than an accident.
6. **Export a single thread as a readable document** (S) — Any complaint or call exports as markdown with its full history, for quoting outside the site.
7. **Account deletion that actually deletes** (M) — An honest deletion path: what is removed, what is tombstoned, what is retained for audit and for how long, stated before the confirmation button.
8. **Downloadable audit ledger per project** (L) — The ledger is the trust anchor; it should be exportable and independently verifiable (hash chain), not just viewable.
9. **A documented webhook event schema** (M) — Consumers need to know the shapes before they subscribe; publish the JSON schema with versioning and a changelog.
10. **Data portability for reputation** (L) — A signed statement of a member's contribution record they can present elsewhere, with the caveat that it is only as good as this instance's word.
11. **Backups documented and restore-tested** (M) — A written restore procedure with a measured restore time, exercised on a schedule — the difference between a backup and a hope.
12. **A tombstone registry for deleted projects** (S) — A deleted project keeps a URL explaining that it existed and when, so old links do not 404 into silence.
13. **Legal pages generated from configuration** (S) — Privacy, terms and licence pages whose factual claims (what is stored, how long) are read from the same config the code uses.

### Accessibility and language (13)

> **Complaint:** _The site excludes people who need accessibility or another language._ Keyboard traversal is inconsistent, focus is not always visible, there is no second language, and nothing checks contrast or announcements in CI.

1. **An axe-core gate on every page in CI** (M) — The browser suites already run Playwright; an accessibility assertion per page turns 'we care about a11y' into a build failure.
2. **Full keyboard traversal with visible focus** (M) — A test that tabs through each page and asserts every interactive element is reachable exactly once, with a focus ring that meets contrast.
3. **Screen-reader announcements for live updates** (M) — The finder does this; extend it to votes, tallies and feed refreshes, with aria-live regions that announce outcomes, not raw JSON.
4. **Localisation infrastructure with one real second language** (L) — Message files, a language switch, plural and date handling — and at least one fully translated locale, because a half-built i18n layer rots.
5. **Right-to-left layout support** (M) — Logical CSS properties, mirrored chevrons and a test that flips dir=rtl and asserts no overflow.
6. **Plain-language mode** (L) — A toggle that shortens sentences and defines jargon inline, generated from the glossary, for readers who are not native speakers.
7. **Colour-blind safe status encoding** (S) — Status is colour plus shape plus text everywhere, with a test that greps for colour-only styling.
8. **Respect prefers-reduced-motion and prefers-contrast** (S) — Every animation and gradient behind those media queries, with a test page listing what changes.
9. **Alt text enforced at upload** (S) — An image without a description is refused or marked decorative explicitly, because 'add alt text later' never happens.
10. **Error messages that say what to do next** (S) — Every 4xx page gets an action line and a link, and a test asserts no error page lacks one.
11. **A reading level measured in CI** (S) — A simple readability score on user-facing help text, failing above a stated threshold.
12. **Translated taxonomy labels** (M) — The categories and themes are English strings in the database; a translation table keeps the schema and translates the labels.
13. **A language-aware search that does not split on accents** (M) — Stemming and diacritic folding per locale, with a test per language the site claims to support.

### Performance (13)

> **Complaint:** _Pages and search are slow._ The project page loads six panels at once, search re-queries on every keystroke, and no budget stops the JavaScript from growing release by release.

1. **Budget the page weight and fail the build over it** (M) — A stated kB budget per page type, measured in the e2e run, so the site cannot drift into 400 kB of JavaScript one feature at a time.
2. **Cache every read route that has no session dependency** (M) — Public project pages change rarely; an ETag plus a short TTL removes most database work from the hot path.
3. **N+1 detection in the test suite** (M) — Count queries per request in tests and assert a ceiling per route, which is how an N+1 is caught before it becomes a 300 ms page.
4. **Cursor pagination everywhere** (M) — Offset pagination degrades and duplicates rows under writes; the feeds and search should share one cursor implementation.
5. **A slow-query log with a stated threshold** (S) — Anything over 50 ms recorded with its route and its plan, reviewed weekly.
6. **Precompute the instance-wide aggregates on a schedule** (L) — Pain scores and health are recomputed per request today; a snapshot table makes the expensive numbers cheap and comparable over time.
7. **Compress and fingerprint every asset** (S) — Stable hashed filenames with long cache lifetimes; the asset helper already versions by query string, which busts caches on every deploy.
8. **Lazy-load the panels that are below the fold** (M) — The project page fetches six panels on load; three of them are never seen by most readers.
9. **A load profile that runs on every release** (L) — A recorded scenario (browse, search, vote) replayed against a seeded database, with the p95 compared to the last release.
10. **Indexes justified by query plans in CI** (M) — A test asserting each documented hot query uses an index, so a schema change cannot silently drop one.
11. **Stream the export instead of buffering** (M) — A large project export currently builds in memory; chunked streaming keeps it alive on a small host.
12. **Debounce and cancel in-flight searches** (S) — Typing in the search box issues a request per keystroke; an AbortController plus a delay cuts the load by an order of magnitude.
13. **A cold-start measurement for the server** (S) — Time to first successful request after a restart, tracked per release, because this instance restarts on every deploy.

### Security and privacy (13)

> **Complaint:** _Security and privacy are bolted on._ There is no second factor, no session list, no CSRF token discipline and no way to see who read a private project; secrets and backups are handled by convention.

1. **Two-factor authentication with recovery codes** (M) — TOTP plus ten single-use codes, shown once, with an audit row for enrolment and for each use.
2. **Session list with remote revocation** (M) — Every session visible with its last-seen IP and agent, and a revoke button that actually invalidates the token server-side.
3. **Password breach checks against k-anonymised ranges** (S) — Refuse a password found in a breach corpus without sending the password anywhere, using the range API.
4. **Rate limits keyed on identity as well as address** (M) — The limiter keys on IP today; an authenticated abuser on a shared address is invisible. Key on account with a separate, higher budget.
5. **CSRF tokens on every state-changing form** (M) — One middleware, a per-session token in every form and header, and a test that a forged request is refused.
6. **A security policy with a disclosure route and a response clock** (S) — SECURITY.md, a contact, a stated acknowledgement time, and a page recording disclosures and fixes.
7. **Content Security Policy without unsafe-inline** (M) — The current policy allows inline script; moving to nonces removes the main XSS amplifier and is testable by asserting the header per page.
8. **Per-project visibility enforced in one place with a test matrix** (L) — Private, unlisted, protected and public each get a table of who may read what, and a test per cell — the current enforcement is spread across handlers.
9. **Audit every read of a private project** (M) — Nobody can see who looked at a private project today; if it is private, access should leave a trace.
10. **Encrypted backups** (M) — A backup on the same disk as the database is a copy, not a backup; encrypt off-host copies and document the key handling.
11. **Dependency and image scanning in CI** (S) — Vulnerability scan on every build with a stated severity policy for failing.
12. **Secrets that cannot reach the logs** (S) — A redaction layer on the logger plus a test that feeds a token-shaped string through every log path.
13. **Login anomaly notices** (S) — A new country or a new user agent sends one notice, with a link to revoke, and never blocks.

### Integrations with where the work happens (12)

> **Complaint:** _Work happens elsewhere and never reaches this site._ Commits, pull requests and CI live in a forge, discussion lives here, and nothing connects them: status is retyped by hand.

1. **A GitHub webhook that records events without a second database** (M) — The forge_events table exists for this; wire push, PR, review and comment so newcomer responsiveness can be measured from real data.
2. **A two-way issue mirror for linked features** (L) — A shipped feature opens an issue in the repository and closes it when the field report says it worked, with the mapping stored.
3. **CI status shown on the feature page** (M) — A feature's branch and its last CI result, read from the forge API, so 'is it done?' stops being a status field somebody edits.
4. **A Slack/Discord bridge for decisions** (S) — Ratified decisions post to a channel with a link, one message per decision, configurable per project.
5. **Generic outbound webhooks with signing** (M) — Any consumer can subscribe to decision, ship and complaint events, with an HMAC signature header and retry with backoff.
6. **An import from GitHub or GitLab issues** (L) — Point at a repository and turn its issues into complaints with the reporter and the URL preserved, so a project can migrate its pain.
7. **A CLI that files complaints and lists work from a terminal** (M) — One static binary using the existing API, for maintainers who live in a shell.
8. **A browser extension that files a complaint from any page** (M) — The report-this-project path is manual today; an extension captures the URL and opens the form pre-filled.
9. **Syndication to my own site via a static build** (M) — A generator that reads the public API and produces a static mirror of a project's public pages.
10. **Email-in complaints** (L) — A dedicated address per project that accepts a message and files it, with the sender verified by SPF and a reply-by-email thread.
11. **Calendar feeds for decision deadlines** (S) — An iCal feed per project of voting windows that close, so nobody misses a quorum because of a timezone.
12. **A single sign-on option for the instance's other sites** (L) — The network already shares an identity in practice; one OIDC provider removes a fourth password.

### Mobile use (13)

> **Complaint:** _The site is unusable on a phone._ Primary controls are out of thumb reach, the board scrolls sideways, text is small, and there is no offline reading, no install, and no mobile test in CI.

1. **A real PWA with offline reading** (M) — Service-worker caching of the pages a reader follows, an install prompt that appears after engagement, and an offline page that says what is unavailable.
2. **Thumb-reachable primary actions on mobile** (M) — The vote and claim controls sit at the top of the viewport; a mobile layout puts the three primary actions in a bottom bar.
3. **A share sheet integration** (S) — Sharing a decision produces a link with OpenGraph metadata and a one-line summary, so the receiving app shows something better than a bare URL.
4. **Mobile-first board columns** (M) — The kanban is horizontally scrolled on a phone; a stacked column view with a status switcher replaces it below 640 px.
5. **Tap targets and text at accessible sizes** (S) — A test asserting every interactive element is at least 44 px and body text at least 16 px at 375 px width.
6. **Native share targets for install** (S) — Web app manifest with icons, screenshots and a start URL per surface, so the installed app opens the feed rather than the home page.
7. **Pull-to-refresh on the feeds** (S) — One gesture, with a visible timestamp of the last successful fetch.
8. **Offline draft persistence** (S) — A complaint written on a train survives the tunnel; drafts are stored locally and offered for resume.
9. **Camera capture for screenshots in a complaint** (S) — Evidence is the difference between a vague complaint and a fixable one; a file input with capture and a size cap.
10. **A compact density mode** (S) — For people who scan a lot on a phone: less padding, more rows, remembered per device.
11. **Push notifications on install** (M) — The same web push subscription, requested at install time with the reason stated.
12. **App shortcuts in the manifest** (S) — Long-press an icon to jump to file-a-complaint or the contribution feed.
13. **A mobile e2e profile in CI** (M) — The Playwright suites run at desktop width only; a 375 px project runs the same assertions.

### Interface depth (14)

> **Complaint:** _The interface is thin and repetitive._ Every panel is a list with a button. Empty states are blank, destructive actions ask for confirmation instead of offering undo, and there is no command palette or comparison view.

1. **One design-token source for colour, spacing and type** (M) — The CSS repeats hex values in a dozen files; a token layer plus a test that forbids raw hex outside it makes a theme change one edit.
2. **A dark and light theme generated from the same tokens** (M) — With a contrast assertion per token pair, so 'dark mode' cannot ship a 2.1:1 label.
3. **Per-project accent colour chosen by the owner** (S) — A small, bounded palette so a project is recognisable in a list without breaking contrast rules.
4. **Dense and comfortable table modes** (S) — Two row heights on every list, remembered per member, because a table that suits a laptop is unusable on a projector.
5. **A command palette with every route and action** (M) — Ctrl-K over routes, projects and actions, built from the router's own table so it cannot list a page that does not exist.
6. **Inline editing everywhere with the same component** (M) — Titles and bodies are editable in place on some pages and behind a form on others; one component, one keyboard contract.
7. **Undo for destructive actions instead of a confirm dialog** (M) — A toast with a five-second undo backed by a soft delete, replacing the modal that everyone clicks through.
8. **A real empty state for every panel** (S) — Each panel explains what would fill it and links to the action that starts it — the current mix of blank boxes and 'no data' is the most visible thinness.
9. **Skeletons that match the final layout** (S) — Loaders currently reserve the wrong height, so the page jumps; one skeleton per panel type.
10. **A comparison view for two competing solutions** (M) — The arena asks for a judgement; a side-by-side view with the complaints each answers makes the judgement informed rather than a coin flip.
11. **Print stylesheets for decisions and specs** (S) — A decision record should print legibly on one page; today it prints the navigation twice.
12. **Progress indicators that state the scale** (S) — A bar with no denominator is decoration; every progress bar names its total and its unit.
13. **An activity timeline per project** (M) — One reverse-chronological stream of filings, validations, ships and decisions, filterable, replacing four separate lists.
14. **Keyboard-visible focus order that follows visual order** (M) — A test asserting DOM order matches visual order on each page, because tab order that jumps is a maze for keyboard users.

### Assisted editorial work (14)

> **Complaint:** _Repetitive editorial work is manual._ Grouping duplicates, drafting a complaint from a title, summarising a long thread and translating a report are all done by hand, badly and late.

1. **Draft the complaint body from a title** (M) — A model expands a one-line title into a structured complaint the filer then edits, cutting the most common quality problem at its source.
2. **Suggest the complaint a feature answers** (M) — On feature creation, rank the candidate complaints by similarity and show the top three with their pain scores, so an unlinked feature becomes linked.
3. **Cluster duplicate complaints nightly, propose merges** (L) — A batch job writes merge proposals with the members listed; a human approves. Never auto-merge.
4. **Summarise a long thread into the decision record** (L) — The ratified decision gets an extractive-then-generated summary that cites the comments it used, and the citations are checked before rendering.
5. **Translate a complaint into the maintainer's language** (M) — On-demand translation of complaint bodies with the original always visible and the model named.
6. **Detect a complaint that is actually a feature request** (S) — Classify on filing and offer the correct form with the text preserved, instead of letting it rot in the wrong queue.
7. **Flag feature proposals that restate shipped work** (S) — Similarity against shipped features at filing time, with links, which is the cheapest way to stop a backlog re-litigating the past.
8. **Estimate effort from comparable past features** (L) — Retrieve the nearest shipped features and report their actual claim-to-close time as an evidence-based size suggestion.
9. **An assistant that answers 'what does this project decide?'** (L) — Retrieval over the spec, decisions and documents with citations to sections, refusing when the corpus has no answer.
10. **Moderation triage that ranks reports by likely severity** (M) — A model-assisted queue ordering with the reason shown, and every decision still made by a human whose name is on it.
11. **Accessibility description suggestions for uploaded images** (S) — A suggested alt text the uploader must confirm or edit, so the field is never left empty.
12. **A weekly generated digest reviewed by a human before sending** (M) — Draft, preview, edit, send — the model writes, a person signs.
13. **Prompt and model versioning with a fallback** (M) — Every model call records the prompt version and model, and a failure falls back to the non-AI path rather than an error page.
14. **A hard rule: no AI content is published without a label** (S) — Anything model-drafted carries a visible marker and an audit row, including after a human edits it.

### Federation and portability of the network (13)

> **Complaint:** _This project is an island._ Nothing here is visible to the wider web except as a web page: no ActivityPub, no WebFinger, no static mirror, no way for another instance to describe the same project.

1. **ActivityPub outbox for projects** (L) — A project publishes its decisions and ships as ActivityPub activities, so a Mastodon user can follow it without an account here.
2. **An inbox that accepts follows and replies** (L) — Handle Follow, Undo and Create at least minimally, with the actor mapped to a project or a member.
3. **WebFinger and nodeinfo** (S) — Two small endpoints that make the instance discoverable and described to the wider fediverse.
4. **Cross-instance project identity** (L) — Two instances could describe the same project; a canonical URL and a claim flow would let the catalogs merge rather than duplicate.
5. **Federated complaints with provenance** (L) — A complaint received from another instance keeps its origin and its reporter's instance shown, so local weight is not granted by default.
6. **A static mirror for the small-web** (M) — Publish each project as plain HTML on a static host, regenerated on ship — the site survives its own server being down.
7. **IndieAuth or a rel=me verification for member profiles** (S) — A member can prove a personal site is theirs, which makes external identity a first-class thing rather than a bio link.
8. **Webmention for discussions elsewhere** (M) — A blog post linking a decision sends a webmention that appears as a linked reference on the decision page.
9. **An export designed to be re-hosted** (L) — The portability export plus a static generator equals a portable instance; document the minimal viable re-host.
10. **A public instance directory of Concord instances** (M) — Which instances exist, their governance models and their catalogs, so the network is visible rather than asserted.
11. **Signed instance identity** (L) — A keypair per instance and signed exports, so a mirrored catalog can be verified rather than trusted.
12. **Federated search with privacy-preserving queries** (L) — Ask several instances for counts, not results, and fetch only what the reader chooses.
13. **A bridge that mirrors a project's decisions into a wiki** (M) — Any MediaWiki or Outline instance can consume the same feed; the bridge is configuration, not code.

### Measurement (12)

> **Complaint:** _Nobody can see whether any of this works._ There is no funnel, no lead time, no retention, no search success rate: the instance cannot say whether a change helped.

1. **A funnel from visit to first contribution** (M) — Five steps, counted server-side with no personal identifiers, so 'is onboarding working?' has an answer that is not a guess.
2. **Decision throughput per week, with the trend** (M) — Calls opened versus decided per week, and the median time in each state — the single chart that says whether governance is functioning.
3. **Complaint to ship lead time, by project and by theme** (M) — The number a user feels; publish it with its sample size and its outliers named rather than smoothed away.
4. **Search success rate as a first-class metric** (S) — Share of searches that lead to a click within the session, and the top zero-result queries, reviewed weekly.
5. **Retention cohorts without tracking individuals** (L) — Weekly cohorts counted as aggregates on first-visit week, with a k-anonymity floor, and no per-user event log.
6. **A public metrics page generated from the database** (S) — Instance-level counts (projects, complaints, decisions, ships) rendered from the same queries the stats API serves.
7. **Experiment flags with a stated expiry** (M) — A flag registry where every flag has an owner and an end date, and the build fails when one expires — the only way flags do not become permanent branches.
8. **A/B results recorded as decisions** (M) — When an experiment concludes, its outcome becomes a decision record with the evidence, so the learning outlives the code.
9. **Which complaints predicted impact** (L) — Compare a complaint's severity and frequency with the field-report outcome of the fix; publish the correlation so severity means something.
10. **Dead-page detection from the access log** (S) — Routes nobody visits for a quarter appear in a report, because an unused surface is either broken or unnecessary.
11. **Cost per decision** (S) — Hosting and operational cost divided by decisions made per month: a governance number nobody computes but everyone should see.
12. **An anomaly alert on sudden drops** (M) — Ships per week or decisions per week falling below a stated band raises one alert to the operator, not a dashboard nobody opens.

### Community and retention (12)

> **Complaint:** _Contributors have no reason to stay._ Credit goes to the author only, there is no portfolio, no mentorship that reaches a person, and no public thank-you that is not a badge.

1. **Credit contributors on the ship they made** (S) — The feature page names who validated, who voted and who shipped, with the role each played, not just the author.
2. **A contributor's public portfolio page** (M) — What they filed, validated, shipped and decided, exportable and linkable — the thing a contributor actually wants to show.
3. **Thank-you notes that are not gamified badges** (S) — A single, rate-limited appreciation per person per month, displayed on their page; no streak counters, no leaderboard of hours.
4. **A monthly 'state of the instance' post, generated** (S) — Decisions, ships, new projects and one thing that went badly, assembled from the data with a short human preamble.
5. **Office hours as a recurring, timezone-rotated event** (S) — Two slots a month at different hours, with an agenda built from the open complaints in a theme.
6. **Mentor matching with an explicit opt-in** (M) — Mentors state the areas and the hours they can give; newcomers request a session; both sides get a calendar link and no obligation beyond it.
7. **First-contribution celebration that names the work** (S) — One notification on the first merged contribution naming the project and the maintainer who responded, because the second contribution depends on it.
8. **Project maintainer office hours in the contribution feed** (S) — The feed shows 'this project is answering questions Thursday' as a work item, so help-seeking is a task a newcomer can do.
9. **A 'we need you here' list per project** (M) — Which roles are unfilled, with the requirements stated, so a reader can see exactly how to be useful rather than generally volunteering.
10. **Recognition for review, not only for code** (S) — Response times and review counts are contributions; show them on the same portfolio page as the ships.
11. **A leaving page that asks one question** (S) — When someone deletes an account, one optional question with fixed answers, aggregated and published — the only reliable source of why people leave.
12. **Local meetups listed per project** (S) — A date, a place and a link; no ticketing, no integration, just a surface that says people meet.

### The project's own quality (13)

> **Complaint:** _The project's own quality is unverified._ Claims in the docs drift from the code, there is no status page, no incident log, no fuzzing and no measured restore; the gates exist but nobody publishes their scores.

1. **Every claim in the README re-checked by CI** (S) — Counts, test numbers and feature lists in the docs are asserted by the doc-numbers gate; extend the same discipline to the README's claims.
2. **A public status page fed by real probes** (M) — Health, last deploy, last successful healthsync, last backup, with the timestamps exposed on a page rather than in a journal.
3. **Green-not-broken: a failing test cannot merge** (S) — The repository's own gate discipline applied to the instance: a documented rule that no deploy ships with a red gate, and a script that refuses it.
4. **Property-based tests for the ranking engine** (M) — The pairwise model has invariants (a self-vote never counts, ratings stay bounded, order is deterministic) that a property test proves over random input.
5. **A mutation score published per package** (S) — The gates already compute killed/total; publishing the per-package score turns coverage into a number people can compete on sensibly.
6. **Fuzz the API with a grammar of valid bodies** (M) — Most 500s come from shapes nobody typed; a fuzzer over the OpenAPI schema finds them without a user.
7. **A regression corpus for reported bugs** (S) — Every fixed bug leaves one minimal test with the issue link in its name, so the same class of bug cannot return silently.
8. **Restore drills with a measured RTO** (M) — A scheduled job restores yesterday's backup into a scratch database and runs the smoke suite against it.
9. **An incident log with postmortems** (S) — One page per incident: impact, timeline, cause, what changed. The absence of this page is why incidents repeat.
10. **Dependency upgrade policy with an automated PR** (S) — Weekly automated upgrade PRs that the gates judge, with a stated policy for majors.
11. **A benchmark of the mutation gate's own sensitivity** (M) — Inject a known-untested function and assert the gate reports a survivor; a gate that cannot fail is a gate nobody should trust.
12. **Documentation for every public API route, generated** (M) — The router's table rendered as an OpenAPI document, with a test that a route without a description fails the build.
13. **A quarterly architecture review recorded as a decision** (S) — One decision record per quarter reviewing the module graph, the dependency rules and the known debt, so the architecture has a history rather than a legend.

---

## Totals

| theme | ideas |
|---|---|
| Discovery and search | 13 |
| Onboarding and first steps | 13 |
| Complaint quality | 13 |
| Feature proposals and planning | 13 |
| Consensus and governance | 13 |
| Trust and reputation | 13 |
| Moderation and abuse | 13 |
| Notifications | 13 |
| Feeds and syndication | 13 |
| Documents and knowledge | 13 |
| Requests from the outside | 13 |
| Project health and metrics | 13 |
| Data portability and exit | 13 |
| Accessibility and language | 13 |
| Performance | 13 |
| Security and privacy | 13 |
| Integrations with where the work happens | 12 |
| Mobile use | 13 |
| Interface depth | 14 |
| Assisted editorial work | 14 |
| Federation and portability of the network | 13 |
| Measurement | 12 |
| Community and retention | 12 |
| The project's own quality | 13 |
| **all** | **311** |

Effort mix: S 127, M 142, L 42, XL 0.

This file is generated. Edit `seed/concord_ideas.json`, then re-run `python3 scripts/render_ideas.py`; a test in `scripts/` fails when the two disagree.

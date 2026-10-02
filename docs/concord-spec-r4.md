# Concord: Decide What to Build, Find What Already Exists

> A forge and discovery engine in one. Real complaints drive development. Competing solutions are ranked head-to-head. Decisions come from rough consensus among collaborators. The same community-ranked catalog helps you find, compare, and choose any project when you're about to use or build something.

**Status:** concept spec, nothing built yet.
**Revision 4:** full rewrite. Supersedes the earlier collective-governance, discovery, lists, and request-board revisions, and folds them into one model.

### What changed in this revision

- **Solutions are first-class and ranked.** A feature now states *what outcome and why*. Competing **solutions** state *how*, and each feature has its own ranked solution arena with a built-in fallback chain (§6.3–6.5).
- **One ranking engine, many arenas.** Feature priority, solution ranking, list entries, request answers, and "alternatives to X" are all instances of one *arena* (§5).
- **Discovery is a standalone product and the adoption wedge.** The catalog indexes external repos and packages, so Concord is useful before any project migrates. It adds hybrid search, a **Scout** workflow for new projects, alternatives and complements, a capability matrix, field reports, dependency intelligence, and an opportunity radar (§4).
- **Spec bugs fixed:**
  - "Collaborator" is now defined (§8.2).
  - Stand-aside no longer counts against consent (§6.6).
  - The maintainer veto is an emergency hold, not a block override (§6.6).
  - Strategic weight is set by consensus, not by maintainers (§6.2).
  - The global tag taxonomy runs through quorum, not maintainers (§4.3).
  - Security reports get a private embargo track (§6.1).

---

## 1. Vision

Open source has two unsolved problems:

1. **Deciding what to build next.** Roadmaps are opaque, feature requests are noisy, and maintainers burn out.
2. **Knowing what already exists.** People reinvent projects they never found, adopt abandoned ones, and cannot compare options honestly. Search is keyword-and-stars, and awesome-lists are markdown files with merge wars.

Both problems have the same answer: **structured community judgment, made transparent.** Concord runs one loop:

```
   Need something? ──► DISCOVER ──► adopt / extend / compare existing projects
         │                                   │
         │ nothing fits, or it fits badly    │ field reports, complaints
         ▼                                   ▼
   COMPLAIN (real problem) ──► FEATURE (outcome) ──► SOLUTIONS (ranked, incl. "adopt X")
                                                          │
                                       CONSENSUS ──► KANBAN ──► MERGE QUORUM ──► SHIP
                                                          │
                              released projects feed back into the catalog ◄──┘
```

Discovery feeds decisions: a feature's candidate solutions include existing projects. Decisions feed discovery: shipped fixes, health, and field reports make the catalog trustworthy.

**Strategy:** discovery is the wedge, governance is the moat. Discovery works on day one for every public repo on the internet. Governance tooling attracts the projects that want it.

---

## 2. Principles

1. **Complaints over feature requests.** Work traces back to validated pain.
2. **Everything competes pairwise.** Features, solutions, list entries, answers, and alternatives are ranked by head-to-head comparison, not raw upvotes.
3. **Rank informs, consensus decides.** Rankings set the agenda and the fallback order. They never replace a consent process.
4. **Consensus, not unanimity.** The bar is no unresolved principled objection, and silence cannot decide.
5. **Transparent algorithms.** Every score has a "why this rank?" breakdown. Every ranking is recomputable from the public vote log.
6. **Reputation is earned.** Weight comes from contribution and decays. It is never bought, and bots have none.
7. **Collective by default.** Collaborators steer. Maintainers do housekeeping and safety, and maintainer-led mode is an opt-in decided by consensus.
8. **Admin-light.** Instance admins run the platform and hold no content authority. Admin actions are logged and reviewable.
9. **Discovery is first-class.** Finding the right existing project is as important as managing your own.
10. **Explain every recommendation.** No opaque feeds. Users can see, tune, or turn off any personalization.
11. **Kanban limits work.** WIP limits protect people.
12. **Fork-friendly, federated, exportable.** Leaving must always be possible.
13. **Machines assist, humans decide.** AI features are labeled, reproducible, and opt-out. Agents never vote or confirm.

---

## 3. Object Model

### 3.1 Vocabulary

| Term | Meaning |
|---|---|
| **Project** | A catalogued software project. It has a **trust tier**: *indexed* (external, metadata only), *claimed* (owners verified, can enrich), or *native* (hosted or synced, with governance, board, and charter). |
| **Complaint** | A structured problem report. |
| **Feature** | A desired *outcome* tied to validated complaints, with acceptance criteria. It says what and why, not how. |
| **Solution** | A concrete approach to a feature. Multiple solutions compete in the feature's solution arena. |
| **Arena** | A scoped set of competitors ranked by pairwise comparison (§5). |
| **Consensus Call** | A formal decision on one specific solution. |
| **Board** | Kanban execution of selected solutions. |
| **Tag** | A namespaced, hierarchical label with aliases. It is the primary discovery facet. |
| **Capability** | A structured, evidence-backed claim about what a project does (e.g. `wip-limits: yes`). |
| **Field Report** | A structured "I used X for Y, here's what happened" record. |
| **List** | A curated, ranked arena of entries (replaces awesome-lists). |
| **Request** | A natural-language "what fits my need?" question answered by ranked project references. |
| **Scout Report** | A generated build-vs-adopt analysis for an idea or stack. |

### 3.2 The graph

```
Project ─┬─ has many ─ Complaints ─┐
         │                         ├─ linked to ─► Feature ─ has many ─► Solutions
         │                                                      │  (one is always the baseline:
         │                                                      │   "do nothing / document workaround")
         ├─ has many ─ Features ─── competes in ─► Feature Arena
         ├─ tagged with ─ Tags      Solutions ──── compete in ─► Solution Arena (per feature)
         ├─ asserts ─ Capabilities
         ├─ receives ─ Field Reports
         ├─ depends on / used by ─ Projects (dependency graph)
         └─ appears in ─ Lists, Request answers, Alternatives arenas
```

A solution can be of type `build-new`, `extend-existing`, `integrate-external` (links to a catalog project), `config-or-docs-only`, `workaround`, or `do-nothing`. That is where discovery and decision-making join.

---

## 4. Discovery and Recommendation (first-class)

The goal: when you are thinking of using or building something, Concord finds every relevant project, tells you which ones fit and why, and shows what is missing.

### 4.1 The catalog

**Coverage.** Concord indexes more than what it hosts:

- Native Concord and connected Forgejo/Gitea repos.
- Public metadata from GitHub, GitLab, Codeberg, SourceHut, and other forges, respecting API terms, robots.txt, and licenses.
- Package registries: npm, PyPI, crates.io, Go modules, Maven, NuGet, RubyGems, Packagist, Hex, Homebrew, Nix, Debian/Alpine, Docker Hub/OCI, and others.

**Entity resolution.** One *project* unifies its repo(s), packages, container images, homepage, docs site, and fork family. Forks cluster under a family so results stay diverse (§4.6).

**Trust tiers.**

| Tier | Source | What it shows |
|---|---|---|
| Indexed | Public metadata only | Derived facts, tags, health, field reports |
| Claimed | Owner-verified | Owner can enrich, respond to reports, correct facts |
| Native | Hosted or synced | Full governance, board, complaints, roadmap |

Owners can claim, correct, or opt out of indexing. Opt-out removes enrichment, not public facts like the repo URL.

**Retired projects stay.** Archived and abandoned projects are kept, flagged, and linked to successors. Postmortem notes are community-editable. This is valuable prior art for anyone about to build the same thing.

### 4.2 Signals and facts

Every project carries machine-derived signals, all exposed and none vibes-based:

| Group | Signals |
|---|---|
| **Identity** | Type (library, CLI, service, app, framework, plugin, template, dataset, model), maturity stage, license (SPDX-detected), homepage, docs |
| **Code** | Languages and percentages (go-enry, correctable), size, build systems, platforms and architectures (OS, arm64, WASM…), runtime footprint where declared |
| **Dependencies** | Manifest and SBOM graph: direct and transitive deps, reverse deps, co-dependency patterns |
| **Health** | Release and commit recency, issue-triage and PR-review latency, contributor breadth, bus factor, CI status, advisory response time |
| **Security** | OSV/advisory feed, signed releases, provenance attestations, dependency-risk rollup |
| **Governance** | Model (collective or maintainer-led), decision-latency stats, charter link |
| **Community judgment** | Arena standings, field-report outcomes, capability confirmations, complaint stats |
| **Semantics** | Embeddings of README, description, docs, and capability text |

**Health score.** It is computed from the public signals above. Users can reweight any component with sliders, and the weights travel in shareable URLs. Disagreement is filed as a complaint against the score's inputs, not emailed into a void.

**Capability matrix.** Capabilities are structured claims, such as `wip-limits: yes | partial | no | unknown`, with evidence links, organized per category. Any contributor can assert one. Confirmation uses the same quorum machinery as everything else, and disputed claims show as disputed. The matrix powers filters (`cap:wip-limits`), side-by-side compare, and Scout.

### 4.3 Taxonomy (collaborative and exhaustive)

- **Namespaced tags** such as `topic:`, `domain:`, `platform:`, `lang:`, `role:`, `deploy:`, and `audience:`. They form a hierarchy (`is-a`, `part-of`): `kanban` is-a `project-management`.
- **Aliases** collapse duplicates (`js` ≡ `javascript`).
- **Any contributor applies tags.** Tags on a native project are confirmed by its collaborators. Anyone may suggest tags on indexed projects, with light quorum confirmation.
- **Global taxonomy changes** (merge, rename, alias, re-parent, delete) are *proposals decided by quorum* among eligible taggers: users with reputation in the tag's namespace. **Maintainers hold no unilateral taxonomy power.** All changes are logged and reversible. This resolves the earlier conflict with the admin-light principle.
- **Exhaustiveness is enforced by tooling:**
  - The tagging UI suggests existing tags before allowing new ones.
  - **Machine-suggested tags** from README, manifests, and embeddings are labeled `suggested` and need a human confirmation.
  - A completeness meter shows how well-tagged each project is.
  - An "under-tagged projects" queue and reputation credit make tagging a visible contribution.

### 4.4 Search

One query surface over everything, with a stable API that the web UI is just a client of.

**Hybrid retrieval.** Lexical (BM25) and semantic (embedding) retrieval are fused (reciprocal rank fusion), filtered by hard constraints, then reranked by user-tunable weights for relevance, quality, health, and community fit.

**Natural language in, structured query out.** Typing "self-hosted kanban with WIP limits that runs on a Raspberry Pi" produces editable filter chips (`tag:self-hosted`, `cap:wip-limits`, `platform:arm64`, `footprint<512MB`). The system never hides how it interpreted you.

**Query language:**

```
kanban tag:self-hosted lang:go license:(MIT OR Apache-2.0) health>=70 active_within:90d
cap:"wip-limits"=yes platform:arm64 model:collective NOT archived
lang_pct(rust)>=60 type:library tag:parser
similar:forgejo/forgejo           # content + graph neighbors
alt:slack                         # ranked alternatives
uses:fastapi   usedby:fastapi     # dependency edges
in:complaints "cannot resume upload" status:shipped   # who fixed this problem?
in:lists tag:debugging lang:rust  # entries across all lists, ranked
in:requests fits-score>=0.8
```

- AND / OR / NOT, grouping, and ranges.
- **Entity scopes:** `in:projects|packages|lists|entries|complaints|features|solutions|requests|reports|people` (people are permission-aware).
- **Facets always on**, returned with counts: tags, languages, licenses, governance models, health buckets, platforms, and project types.
- **Sort:** relevance, health, recently updated, newest, fewest open complaints, community-fit rank, or smallest footprint.
- **Saved, shareable, subscribable:** every query has a URL, an RSS/Atom feed, and notification alerts.
- **Explain-why:** each result shows why it matched (matched terms, tags, semantic similarity, which constraints passed).
- **Search by problem, not just by name.** `in:complaints` finds projects that already solved your exact pain, and shows whether someone is suffering the same thing right now.

**Result cards** show: license, health sparkline, last release, governance badge, top capabilities matched, field-report outcome rate, open-pain count, median response time, and arena badges ("#2 for *self-hosted kanban, small team*").

### 4.5 Recommendation surfaces

**1. Scout: "I'm thinking of building or using X."** The flagship new-project workflow. Input can be free text, a repo link, a stack file (`package.json`, `Cargo.toml`, `requirements.txt`, and so on), or a request.

Scout does the following:

1. **Decomposes** the idea into capabilities (editable by you).
2. **Matches** each capability to candidate projects, with per-candidate fit scores and reasons.
3. **Classifies each match** as one of:
   - **Adopt** as a dependency or service.
   - **Base on** (fork or template).
   - **Extend** (contribute upstream).
   - **Inspire** (study only).
   - **Avoid** (abandoned, license-incompatible, security-risky, with reasons).
4. **Shows a coverage heatmap:** capabilities that are covered, partly covered, or open gaps.
5. **Surfaces relevant known pain.** For each candidate, it lists the unresolved complaints and low field-report outcomes that touch *your* stated needs.
6. **Shows prior art, including dead projects**, and why they died if a postmortem exists.
7. **Suggests joining instead of duplicating.** It shows open features and solutions in existing projects that would cover your idea, with the effort needed, and live collaborator calls.
8. **Runs hard-constraint checks:** license compatibility (e.g. "compatible with my AGPL project"), platform, language, and self-hosting limits.
9. **Produces a Scout Report:** a shareable, versioned document and stack sketch. It exports to Markdown and JSON. One click turns it into a new Concord project, seeded with tags, a charter template, and an initial complaint/feature list drawn from the gaps.

**2. On every project page:**
- **Similar:** content plus graph neighbors.
- **Alternatives:** ranked by the project's alternatives arena (§7.3).
- **Complements:** "used together with" from co-dependency, co-listing, and field reports.
- **Upstream and downstream** (what it depends on, what depends on it).
- **Lineage:** fork-of, successor-of, replaced-by.

**3. Compare (2–5 projects).** Side-by-side capability matrix, health breakdown, license, governance model, dependency footprint, top unresolved complaints, field-report outcomes, and migration notes (community-written, ranked).

**4. For You (opt-in personalization).** Built from explicit signals: your projects' dependencies, tags you follow, saved searches, your votes, and your field reports.
- Every item states its reason ("because you depend on X").
- You control sliders for novelty, popularity, and health.
- You can mute anything and delete your profile.
- A local-only mode computes recommendations on-device.
- There is no engagement optimization: the system optimizes fit, not time-on-site.

**5. Dependency-aware alerts.** When a dependency of yours is abandoned, vulnerable, relicensed, or superseded, you get an alert with ranked replacement candidates, each with a migration-effort estimate.

**6. Where can I contribute?** Matching by language, tags, time budget, and skill to complaints needing validation, solutions needing implementers, and projects with low bus factor. It includes a "good first complaint" path for newcomers.

**7. Rising, not hyped.** The "rising" list uses health, adoption slope, field-report outcomes, and arena fit. Star velocity is shown but is not the main input, and it is vote-ring resistant (§10.5).

**8. Digest.** A weekly digest per subscription: new projects matching your searches, changes to your dependencies, and arena movement.

### 4.6 How recommendations are computed

A transparent four-stage pipeline:

1. **Candidate generation:** embedding kNN over README, docs, and capability text; graph co-occurrence (dependencies, lists, field reports); tag-hierarchy overlap; arena adjacency.
2. **Hard filters:** license, platform, language, type, maturity, and self-hosting constraints from the user or query.
3. **Ranking:** `fit × quality × health × community_judgment`, with all weights visible and user-tunable, and presets such as *stable*, *cutting-edge*, and *minimal-footprint*.
4. **Diversification:** collapse fork families, avoid ten near-identical results, and show deliberate variety (different languages, governance models, sizes) in "explore" mode.

Every recommendation carries an explanation object: the signals, the weights, and the nearest justification. Cold start for a brand-new project uses content signals only and is labeled as such.

### 4.7 Field Reports

A **field report** is a structured experience record, designed to replace the rants and "does anyone use X?" threads:

- Project, version, **use case** (tagged), environment, scale, and duration of use.
- **Outcome:** `worked` / `worked with caveats` / `abandoned` / `migrated away (to what?)`.
- Caveats, the workaround used, and what you'd tell a newcomer.
- Optional evidence.

Field reports feed project pages ("worked for 83% of reporters on arm64"), recommendations, and Scout. They are weighted by reputation and by report quality. Owners can respond but cannot delete reports. Abusive or fabricated reports are removed by quorum with appeal, and a report must be structured, so it cannot be just a rant. Reports on indexed projects are visible to the owner on claim.

### 4.8 Opportunity Radar (what should someone build?)

- **Unmet needs:** aggregated zero-result and low-confidence searches (privacy-preserving, k-anonymous), recurring unanswered requests, and Scout gaps. They are ranked and clustered.
- **Pain hotspots:** categories where every candidate has high unresolved pain.
- **Abandoned-but-needed:** high-reverse-dependency projects with collapsing health, flagged as "needs maintainers" and "needs a successor".
- Each item has a one-click path to **start a project** (via Scout) or **adopt an orphan**.

### 4.9 Dependency intelligence

- Full dependency and reverse-dependency graph, with transitive risk rollup.
- "What breaks if this goes away?" blast-radius view.
- License-conflict detection along the dependency tree.
- Supply-chain health: maintainer concentration, single-maintainer chokepoints.
- Replacement suggestions drawn from alternatives arenas.

### 4.10 Project page anatomy

Header badges (tier, license, governance model, health, maintenance status) → Overview → **Standings** (arena ranks by context) → **Capabilities** → **Health breakdown** → **Field reports** → **Known pain** (complaints for native projects, aggregated field-report issues for indexed ones) → **Roadmap** (native: ranked features, solution standings, board snapshot) → **Dependencies in/out** → **Similar / Alternatives / Complements** → **Lists featuring it** → Releases → Governance and charter.

### 4.11 Access: API, CLI, embeds, agents

- **Stable, documented, paginated search and recommendation APIs.** The web UI uses only public endpoints.
- **CLI:** `concord search`, `concord scout "idea"`, `concord similar owner/repo`, `concord alt owner/repo`, `concord why-not owner/repo` (why a candidate was rejected for your constraints), `concord report` (capture environment into a complaint or field report).
- **MCP server and agent API:** before an AI coding agent writes a new library, it can query "does this exist?" Agent calls are labeled and rate-limited.
- **Embeds and badges:** health, governance, standings, and "file a complaint" buttons for READMEs.
- **Bulk exports** of the catalog, with attribution, to keep it a commons.

### 4.12 Search and recommendation quality are tested

- **Judged query sets:** community arenas and accepted request answers provide ground-truth relevance labels.
- Offline metrics (nDCG, recall@k, zero-result rate) run in CI, and quality regressions block release like any other bug.
- Online: time-to-first-relevant-click, search-success rate, and Scout adoption rate (§15).
- Bias audits check for popularity bias, language bias, and tier bias (indexed vs native).

---

## 5. Arenas (the single ranking engine)

An **arena** is a set of competitors, a question, and a context. Everything ranked in Concord is an arena:

| Arena | Competitors | Question |
|---|---|---|
| **Feature arena** (per project) | Features | "Which should we prioritize first?" |
| **Solution arena** (per feature) | Solutions | "Which approach should we build?" |
| **List arena** | List entries | "Which is better for this category?" |
| **Request arena** | Project answers | "Which fits this request better?" |
| **Alternatives arena** (per project × use-case) | Projects | "Which is the better alternative to X for Y?" |
| **Use-case arena** | Projects | "Best for *self-hosted kanban, small team*" |

### 5.1 Engine

- **Glicko-2** with rating `r` (default 1500), deviation `RD` (350), and volatility `σ`.
- **Outcomes:** *A*, *B*, *Both* (draw), *Neither*, *Skip* (with optional reason: "I don't understand this" flags an unclear write-up). In arenas with a baseline, *Neither* counts as both competitors losing to the baseline. Elsewhere it is a no-contest that raises a pair-quality flag.
- **Weighted votes:** weight scales the update's influence (fractional-game treatment).
- **Batch rating periods:** ratings are recomputed deterministically from the **append-only vote log**. Anyone can verify a ranking. Recomputes run frequently so the UI feels live.
- **Display:** conservative score `r − 2·RD`, plus an uncertainty bar so "new and unsure" looks different from "ranked low".
- **Pair selection:** active learning (close ratings plus high RD) mixed with random exploration, a cap on repeated exposure, and no voting on your own items. Pairs include enough context to judge, and a vote can include a short reason.
- **Win probability** between competitors uses the standard Glicko-2 expectation `E(r₁, r₂, RD₁, RD₂)`, shown to users as "A is better with 87% confidence".
- **Cold start:** priors come from context (e.g. pain for features) with RD kept high.
- **Contexts:** votes carry the arena's context tags, so the same project can rank differently for different use-cases.

### 5.2 Anti-gaming (applies to every arena)

Vote weights are reputation-based and capped. Rating uncertainty limits swing. Graph analysis flags vote rings. Authors and affiliated accounts are disclosed and cannot vote on their own entries. Random audits and a per-user vote audit view feed appeals. Mass proposals are rate-limited, and low-reputation proposals need more confirmations.

---

## 6. Decide: Complaints → Features → Solutions → Consensus → Ship

### 6.1 Complaints

A structured problem report: title (pain statement), description, impact (who, how often, severity 1–5), workaround, environment, evidence, and tags.

- **"I have this too"** is one click and auto-attaches environment, version, and config, so impact data is richer without extra effort.
- **Duplicate detection at filing time** uses semantic similarity before submit, and also searches *other projects* ("this was fixed in X").
- **Reframe assistant:** turns "add dark mode" into "I can't use this at night". It is labeled as machine-assisted and the user confirms.
- **Completeness meter** scores template quality.
- **Secret-scrubbing uploader** redacts tokens in logs.
- **Status:** `Open → Validated → Linked → Closed / Rejected`. Validation is a collaborator action, and merges of duplicates are logged and reversible.
- **Security reports** go to a **private embargo track** visible only to the security team and the reporter, with coordinated disclosure and CVE/GHSA handling. Once disclosed they become ordinary public complaints. This is the one sanctioned exception to default transparency, and it is audited after disclosure.
- **Upstream links:** a complaint can be linked to an upstream dependency's project, so root causes are traceable across the graph.
- **Verification loop:** after release, filers get an "is it fixed?" poll. A negative majority reopens the complaint (closing the loop on post-release).

**Pain score:**
```
pain = Σ(impact_weight) · severity · frequency · log(1 + affected_users) · theme_multiplier
```
with impact weighted by reputation, an exponential decay with a configurable half-life, and `theme_multiplier` taken from consensus-ratified strategic themes (§6.2).

### 6.2 Features (outcomes, not designs)

A feature must link to at least one validated complaint. It contains:

- **Problem statement:** the complaints it addresses.
- **Desired outcome and acceptance criteria:** measurable if possible ("a user can resume an interrupted upload"). The criteria are what the post-release verification checks.
- **Non-goals** and constraints.
- **Dependencies:** other features, PRs, releases.

**Feature priority** (computed, never hand-assigned):
```
priority = (r − 2·RD) + λ·log(1 + pain_sum) + μ·theme_weight
```

**Strategic themes (fixes the steering loophole).** `theme_weight` comes from *themes*, which are tag-based weights such as "security hardening: +2". They are changed only by consensus (Normal tier), have a public changelog, and expire at the next release cycle unless renewed. Nobody can pin an item to the top by hand. Steering happens through public inputs: pain, pairwise votes, and ratified themes.

**Status:** `Draft → Solutioning → Consensus → Ready → In Progress → Review → Shipped / Rejected`.

**Prior-art check.** When a feature is drafted, Concord automatically searches the catalog and suggests `integrate-external` solutions and similar features in other projects. Drafts show what exists before anyone designs anything.

### 6.3 Solutions

A feature can have many solutions. Each solution includes:

- **Summary and design:** technical and UX.
- **Type:** `build-new`, `extend-existing`, `integrate-external` (a catalog link), `config-or-docs-only`, `workaround`, or `do-nothing`.
- **Complaint coverage:** which linked complaints it resolves, and which it explicitly leaves unresolved.
- **Trade-offs:** effort, risk, maintenance cost, breaking changes, migration path, performance and security notes.
- **Evidence:** prototype branch or draft PR, benchmarks, spike results, prior art links.
- **Affiliation:** if the author benefits (e.g. their own library), it is labeled.
- **Relationship:** `exclusive` (default, competes with the others) or `complementary` (stackable, phased, not compared to its complements).

**Every solution arena has a permanent baseline**: *do nothing / document the workaround*. A solution only matters if it beats the baseline, and the UI shows "beats doing nothing with 94% confidence".

**Forking a solution** ("same, but with X") creates a derived solution. It inherits the parent's rating with inflated RD, so good ideas aren't forced to start from zero. Hybrids are encouraged over rivalry.

### 6.4 Ranking solutions

Solutions compete in the feature's **solution arena** (§5) under the question "Which approach should we build?".

**Solution score:**
```
solution_score = (r − 2·RD) + κ · coverage
coverage = (pain of complaints this solution verifiably resolves) / (pain of all linked complaints)   # 0..1
```
`κ` defaults to 200 and is charter-configurable. **Coverage claims are challengeable.** A reviewer can contest a claim, and the claim must stand to count.

**Multi-criteria views.** Alongside the overall vote ("which should we build?"), voters may add per-criterion votes: *solves the complaints*, *simplicity and maintenance cost*, *risk*, *migration and compatibility*, *performance*, and *UX*. Overall ranking drives selection. Criteria rankings are displayed as a comparison table, so people can see *why* a solution leads (cheap but partial vs thorough but risky).

**Expertise-weighted.** Votes on solutions weight expertise tags more heavily (a Rust-heavy design is weighed by Rust-reputed voters), within the existing weight caps.

**The solution board.** The feature page shows a leaderboard: rank, score, confidence, coverage, effort, risk, type, and a compact pro/con digest from the thread. Voters see the comparison pair with full context, and a "show reasoning" box surfaces strongest arguments for each.

### 6.5 From ranking to consensus (agenda and fallbacks)

Ranking does not decide. It sets the agenda.

- A consensus call **opens on the leading solution** when all of these hold:
  - It beats the runner-up and the baseline with ≥ 80% confidence (Glicko-2 win probability).
  - It has enough distinct voters (project-configurable; default scaled to project size).
  - Its position has been stable for N hours (default 72).
- A collaborator may open a call earlier with a stated reason. Early calls are flagged as such.
- **The ranking is the fallback chain.** If the call stalls or a block is upheld, the next-ranked solution is the pre-agreed fallback. Blocks must include a remedy, and "adopt solution B" is a legitimate remedy. Nobody restarts from scratch.
- Possible outcomes: *accepted*, *accepted with amendments* (spawns a derived solution), *fall back to #2*, *rejected* (baseline wins), or *sent back*.
- The call's result, positions, objections, and remedies are auto-written into a **decision record (ADR)** attached to the feature.

### 6.6 Consensus

**Phases:** Draft → Problem Validation → Solutioning (the arena) → **Consent** → Decision → Implementation → Post-release.

**Positions in a Consent call:**
- **Consent:** I support this.
- **Abstain:** neutral. Counts toward quorum, not toward ratios.
- **Stand aside:** reservations, but I won't block.
- **Block:** a principled objection. It must include the principle at stake, why the proposal violates it, and a concrete remedy (enforced by form validation).

**Pass conditions** (all must hold; tier thresholds in §6.7, Normal shown):
1. **Quorum:** `min(eligible, max(3, ceil(0.2 · eligible)))` participants. Below quorum, the window extends and eligible non-voters are nudged. Silence cannot decide.
2. **Enough real support:** `consent / (consent + stand_aside + block) ≥ 0.50`.
3. **Decisive ratio:** `consent / (consent + block) ≥ 0.70`. Stand-asides are neutral here, so a stand-aside no longer counts against consent.
4. **No unresolved block.**

If stand-asides outnumber consents, the decision is flagged **reluctant consensus** and gets a recorded review date.

**Resolving a block:**
- The author amends the solution and the blocker withdraws or converts, or
- **Override:** a second window where ≥ 80% of non-abstain participants vote to override, with the objector's remedy answered in writing.

**Maintainer veto, clarified.** The veto is an **emergency hold** for security or legal reasons. It suspends a proposal, cannot override a block or force a result, must be justified in writing, triggers an automatic community confirmation vote within N days, and expires. It is logged.

**Hidden tally.** Running counts are hidden until the call closes, to prevent bandwagoning. Participation progress and the quorum bar remain visible.

### 6.7 Decision tiers

Process cost scales with stakes. Classification is by effort, breaking-change flag, and security/API/governance touch, and the classification itself can be challenged.

| Tier | Examples | Rule |
|---|---|---|
| **Trivial** | Docs, typos, small refactors, dependency bumps | **Lazy consensus:** passes after 72h unless a collaborator blocks (any block escalates to Normal) |
| **Normal** | Most features and solutions | Thresholds in §6.6 |
| **Major** | Breaking changes, large re-architecture, governance, charter, themes affecting priority globally | Quorum ≥ 33%, decisive ratio ≥ 0.80, 14-day window, override needs ≥ 90% |

Charters can tune each tier. A **what-if simulator** lets collaborators preview how a threshold change would have decided past calls before amending.

### 6.8 Board and gates

Columns are phases:

**Inbox → Triaged → Solutioning → Consensus → Ready → In Progress → Review → Done**, plus Rejected/Archived.

| Gate | Requirement |
|---|---|
| Inbox → Triaged | A collaborator validates the complaint |
| Triaged → Solutioning | A feature is linked and a baseline solution exists |
| Solutioning → Consensus | A call opens on the leading solution (§6.5) |
| **Consensus → Ready** | The call passes. **In collective mode this cannot be skipped by anyone, ever** |
| Ready → In Progress | A person claims it (competing spikes allowed for exclusive solutions) |
| Review → Done | Technical approval plus merge quorum (§6.9) and green CI. A manual move to Done is a logged exception |

- **WIP limits** are soft by default: exceeding one requires a logged reason. Charters can make them hard.
- **Swimlanes:** by tag, release, priority, or assignee. Cards show priority, pain, solution standing, tags, assignee, blockers, and linked PRs.
- **Automation:** consensus passed → Ready; PR merged (gate passed) → Done; complaint closed → linked to release.
- **Views:** dependency/blocker graph, auto-generated roadmap (ordered by computed priority), and a coverage map highlighting orphan complaints (no feature) and features without validated complaints.
- **Metrics:** cycle time, throughput, blocked time, WIP violations, decision latency.

### 6.9 Review and merge

A PR must link to a feature and a selected solution. Reviews are reputation-weighted.

- **Technical approval:** at least one reviewer signs off on correctness, safety, and scope.
- **Merge quorum:** `max(min_confirmations, ceil(merge_ratio · eligible))` distinct collaborator confirmations. Defaults: `min_confirmations = 2`, `merge_ratio = 0.25`. The PR author cannot confirm their own PR.
- Confirmers answer a narrow checklist ("does it implement the selected solution, is it safe and tested?"), not a re-litigation of priority.
- The quorum is enforced via branch protection and status checks. CI must be green.
- **Maintainer-led mode** may lower or drop the quorum, and every bypass is logged.
- **Divergence check:** if a PR departs from the selected solution, it is flagged and sent back to the solution thread, or a new solution is proposed.

### 6.10 Post-release

Release notes are generated from closed complaints. Acceptance criteria are checked, filers are polled, and the feature either reaches **Shipped** or reopens. Shipped solutions get confirmed field-report prompts ("did this fix it for you?"), which flow back to project pages and the catalog.

---

## 7. Curate: Lists, Requests, Alternatives

All three are arena applications (§5), sharing proposals, quorum, ranking, anti-spam, and audit.

### 7.1 Lists (awesome-lists as objects)

- A list is a curated collection of entries (title, URL or project reference, description, category), taggable, attachable to a project or instance-wide, searchable, and audit-logged.
- **Anyone proposes, quorum disposes.** Adding, editing, categorizing, or removing an entry is a proposal. There are no merge wars and no list dictator.
- **Entries are ranked** pairwise within and across categories.
- **Spam dies by reputation:** low-reputation proposals need more confirmations, and mass-proposals are rate-limited and audited.
- **Link-rot checker** flags dead entries. Entries that resolve to catalog projects pick up health and capability data automatically.
- **Importer** bootstraps lists from existing `awesome-*` repos.
- **Cross-list search:** "every Rust debugging tool, ranked, wherever it's curated."

### 7.2 Requests (natural-language fit)

- A request states the need, constraints, and deal-breakers (license, self-hosting, language, platform). Constraints are structured so they filter answers.
- **The catalog answers first.** Posting a request immediately runs Scout/search, and shows ranked existing answers before asking humans.
- **Answers are project references with a fit rationale** (why it fits, where it falls short). One answer per project, and duplicates merge.
- **The community ranks fit** in the request arena. The author can mark an accepted answer, and the badge does not override the ranking.
- **Affiliation labels** appear on answers about the proposer's own project.
- **Spam and astroturf** are removed by quorum, with appeal.
- Accepted fits feed search and "related projects". **Recurring unanswered requests flow to the Opportunity Radar** (§4.8) and become complaint or project seeds.

### 7.3 Alternatives

Each project has **alternatives arenas** per use case: "alternatives to X for Y". Community pairwise comparison produces a ranked "better for / worse for" list, with migration notes (community-written, ranked) attached to each edge. These feed the Alternatives panel, Compare, Scout, and dependency-replacement alerts.

---

## 8. Community

### 8.1 Roles

| Role | Capabilities |
|---|---|
| **Guest** | Read-only |
| **User** | File complaints, post field reports and requests, comment, vote in arenas (low weight), apply tags |
| **Contributor** | Has merged code/docs or equivalent accepted contribution; higher weight; proposes features and solutions |
| **Collaborator (eligible)** | A contributor with qualifying activity in the trailing window (§8.2). Votes in consensus, confirms merges, moves cards, validates complaints |
| **Reviewer** | Reviews PRs; can give technical approval |
| **Maintainer** | **Housekeeping and safety:** merge duplicates, manage project tags and board columns, call and close consensus, execute passed merges, run security embargo, emergency hold (§6.6). Cannot accept features, move cards into Ready, or merge without quorum in collective mode |
| **Owner** | Steward of charter and federation settings. In collective mode the charter changes only by consensus, so ownership is not a steering privilege |
| **Moderator** | Threads, reports, bans (orthogonal to technical roles) |
| **Admin** | Platform only (§9.3) |
| **Agent / bot** | API client with scoped tokens, labeled, **zero vote weight**, cannot confirm merges |

### 8.2 Who is an "eligible collaborator"?

Every threshold depends on this, so it is explicit:

> A user is an eligible collaborator of a project if, within the trailing window (default 12 months), they have at least one **qualifying contribution** (merged code or docs, a validated complaint that led to a shipped solution, an accepted solution, a substantive review, or an upheld moderation action), **or** they were seated by charter consensus. Agents are excluded. A user is excluded from votes and confirmations on their own submissions.

In tiny projects the quorum floor makes effectively everyone active decide. A **solo mode** is allowed: one collaborator decides alone, with decisions still logged, so a lone author is not blocked by process.

### 8.3 Reputation

Earned by shipped complaints, accepted solutions, merged PRs, reviews, docs, catalog contributions (tagging, capability confirmations, field reports), good-faith consensus participation, and upheld moderation. Time decay prevents permanent aristocracy.

```
weight = min(3, 1 + log10(1 + reputation))
       · stake_multiplier · expertise_multiplier · recency_factor
```

Capped to prevent plutocracy. **Money cannot buy weight.** Expertise is tag-scoped. New accounts start low, vouching and invites help against Sybils, and a **reputation dashboard** explains every multiplier.

### 8.4 Threads

Lemmy-style nested, collapsible, labeled (`question`, `objection`, `support`, `evidence`, `off-topic`), votable comments on every object. Votes affect sorting, not truth. Sorts: hot, top, new, controversial. Objections are highlighted and linked to the owning consensus call. Long threads get a labeled summary that preserves objections. A controversy detector flags polarized votes.

### 8.5 Moderation

- Reports go to **moderation juries**: randomly selected eligible members, a sortition model that avoids permanent cabals. Appeals go to a larger jury.
- Bans, locks, and removals are decided by quorum, are logged, and appeals are first-class.
- Projects can also configure elected or appointed moderators through their charter (decided by consensus).

### 8.6 Onboarding and contribution

"Good first complaint" routing, mentoring flags, contribution matching (§4.5.6), and guided first-complaint and first-field-report flows. Complaint fatigue is handled by templates, duplicate detection, reputation, and lightweight validation, not by gatekeeping.

---

## 9. Governance and Administration

### 9.1 Charter

Each native project has a public, readable **charter** covering: roles and permissions, **governance model**, decision tiers and thresholds, merge quorum, veto limits, strategic themes process, fork and federation policy, moderation and appeals, and eligible-collaborator window.

**Models:**
- **Collective (default).** Every direction decision (accepting a solution, Ready, merge confirmation) needs quorum.
- **Maintainer-led (opt-in).** A maintainer may accept solutions and bypass the merge quorum, with every bypass logged.
- Templates for BDFL, meritocracy, and sociocracy arrive later.

**Amending the charter is a Major-tier consensus decision**, including switching governance models. The default cannot be quietly flipped by whoever holds the owner role.

### 9.2 Transparency

All votes, positions, moderation actions, rating updates, and admin actions are in an **append-only, searchable audit log**. Users can export their data, and decisions carry generated records.

### 9.3 Admin-light instances

Concord separates **platform** from **content**.

- Admins configure auth, storage, backups, federation peering, and legal compliance. They hold **no content authority**.
- Moderation, list curation, taxonomy, and project direction all use the same quorum processes. An admin who wants to decide content does so as a regular member.
- Every admin action appears in a **public admin-action ledger**. An admin override of a user decision requires written justification and triggers an automatic supermajority confirmation vote.
- Legal takedowns are executed and logged, with the notice published where law permits.

### 9.4 Forking

If consensus fails or governance breaks down, anyone can fork. Forking from a failed call carries over complaints, features, and the rejected solution. Forks stay linked in the fork family, and federation lets forks share complaints, solutions, and code.

---

## 10. Platform

### 10.1 Architecture

**MVP approach:** don't rewrite Git. Use **Forgejo/Gitea** as the Git, PR, and CI substrate. Concord is the decision-and-discovery layer on top. Sync repos, PRs, and users, keep state in Concord, and use webhooks for status. Standalone hosting is a long-term option.

**Components:**
- **Catalog and Crawler:** ingestion workers, entity resolution, registry connectors, freshness scheduler.
- **Signals Service:** manifest and SBOM parsing, language detection, health computation, advisory feed.
- **Search and Recommendation:** hybrid index (lexical plus vector), query parser, NL→query translator, ranking, explanation service.
- **Arena Engine:** vote log, Glicko-2 batch recompute, pair selection.
- **Decision Engine:** complaints, features, solutions, consensus, tiers, gates, board.
- **Social:** threads, votes, moderation juries.
- **Reputation Service:** weights, decay, anti-abuse.
- **Notification Service:** email, web push, digests, saved-search alerts.
- **Federation Service:** ActivityPub/ForgeFed.
- **API Gateway:** GraphQL, REST, webhooks, MCP.

**Stack (suggested):** SvelteKit or React (PWA, SSR); Go or Rust backend; PostgreSQL with pgvector (and recursive CTEs for the dependency graph initially); a hybrid-capable search engine (Meilisearch, OpenSearch, or Tantivy); Redis; S3; Woodpecker/Drone or built-in runners; self-hostable embedding models, so that search works without sending data to third parties.

### 10.2 Data model (simplified)

```
users, orgs, projects(tier, family_id), repos, packages, images
project_signals, health_components, dependencies(project, depends_on, kind)
tags(namespace, name, parent), tag_aliases, project_tags, taxonomy_proposals
capabilities(project, key, value, evidence), capability_confirmations
field_reports(project, user, version, use_case, env, outcome, body)
complaints(id, project_id, severity, status, merged_into, embargoed)
complaint_impacts(user, complaint, env)
features(id, project_id, outcome, criteria, status)
feature_complaints(feature, complaint)
solutions(id, feature_id, type, coverage, effort, risk, relation, parent_id, external_project_id)
solution_complaint_claims(solution, complaint, status)
arenas(id, type, question, context_tags, baseline_id)
arena_entries(arena, entity_type, entity_id, r, rd, sigma)
pairwise_votes(voter, arena, a, b, outcome, criterion, weight, reason, created_at)  -- append-only
consensus_calls(solution_id, tier, phase, opens_at, closes_at, thresholds)
consensus_positions(user, call, position, reason)
objections(call, user, principle, violation, remedy, status)
decision_records
boards, columns, cards, swimlanes, themes(tag, weight, expires_at)
lists, list_entries, requests, request_answers
scout_reports(input, capabilities, matches, verdicts)
saved_searches, subscriptions
threads, comments, comment_votes
reputations, roles, charters, moderation_cases
audit_log, admin_ledger
```

### 10.3 APIs

GraphQL for UI, REST for integrations, webhooks for CI/chat (consensus opened/passed, merge quorum reached, card moved, arena shifts), an RSS/Atom feed for any query, board, list, or request, the `concord` CLI, and an MCP server.

### 10.4 Federation

ActivityPub and ForgeFed for cross-instance threads, complaints, solutions, forks, and catalog sharing. **Reputation is instance-local by default.** Cross-instance trust uses attested imports with discounted weight and is never silently trusted (see open questions). Catalog data is shared as signed, attributed dumps.

### 10.5 Security, privacy, anti-abuse

- RBAC, 2FA, SSH keys, signed commits, OAuth/SSO, rate limits, invites.
- **Sybil and vote-ring detection:** account age, reputation, graph anomaly detection, periodic audits.
- CI isolation: containers, secrets management, SBOMs, dependency scanning.
- **Personalization privacy:** opt-in, local-only mode, deletable profile, and k-anonymous aggregate use of search logs (Opportunity Radar).
- **Crawler ethics:** respect ToS, robots.txt, and licenses, with attribution and opt-out channels.
- GDPR: export, deletion, and self-hosting.
- **Harassment guardrails for field reports:** structured, evidence-friendly, quorum-moderated, with an owner response right.

### 10.6 AI assistance (bounded)

Allowed and labeled: duplicate detection, reframing, thread summaries, tag suggestions, NL→query translation, Scout decomposition, and drafting ADRs. Constraints: outputs show their inputs and model, humans confirm anything that changes state, agents cannot vote or confirm, and every feature has an off switch. Embeddings and models can be self-hosted.

---

## 11. Maintainer Experience and Sustainability

- **Maintainer dashboard:** complaint clusters, pain trends, decision backlog, quorum-at-risk calls, bus factor, response-time stats. It supports health work and is not a surveillance tool.
- **Burnout protection:** WIP limits, quiet hours, triage juries for floods, and rate-limited notifications.
- **Release automation:** notes from closed complaints, ADR links, and migration-note prompts.
- **Pledges (optional module, later):** sponsors can attach funding to a feature or solution. **Pledges never affect rank, vote weight, or consensus.** Funds release on a verified ship.
- **Orphan adoption:** a pipeline to hand abandoned-but-needed projects to new maintainers, driven by the Opportunity Radar.
- **Importers:** GitHub/GitLab issues (run through the reframe assistant), and awesome-list markdown.
- **One-command self-host:** Docker Compose with sane defaults.

---

## 12. Walkthrough

> Mira wants a self-hosted kanban with WIP limits that runs on a Raspberry Pi.
>
> 1. She types it into search. Chips appear: `tag:self-hosted cap:wip-limits platform:arm64`. Results show three strong matches. One is "#1 for small teams", and another has a 40% field-report failure rate on arm64.
> 2. None has the *offline* mode she needs. She opens **Scout**: it finds a good base project, flags two open complaints about sync that touch her use case, and shows an open feature "Offline editing" in that project with two competing solutions.
> 3. Instead of starting a new project, she files an "I have this too" on the offline complaint (environment auto-attached) and casts pairwise votes on the two solutions. One is `integrate-external` (adopt a CRDT library she found via search), the other `build-new`.
> 4. The CRDT-based solution reaches 85% confidence of beating both the runner-up and "do nothing", with stable standing for 72 hours. A consensus call opens. One collaborator blocks on a license concern with the remedy "pick the MIT-licensed library variant". The author amends, the block is withdrawn, and the call passes quorum.
> 5. The card moves to Ready, someone claims it, and the PR gets technical approval plus merge quorum. A negative-majority poll never triggers, because Mira confirms it works. The complaint closes, and her field report improves the project's arm64 outcome rate for the next person.

---

## 13. Roadmap

Ordered so value ships early and discovery compounds from day one.

### Phase 0: Discovery MVP (months 0–4)
- Catalog ingest: GitHub, GitLab, Codeberg, and the main registries; entity resolution; go-enry; license detection; health v1.
- Hybrid search, query language, facets, saved searches, RSS, and API.
- Namespaced tags with aliases and suggest-before-create.
- Project pages with similar and alternatives (content-based).
- Compare, README badges, and CLI search.

### Phase 1: Arenas and curation (months 3–7)
- Arena engine (Glicko-2, vote log, pair selection).
- Lists, requests, and alternatives arenas, with an awesome-list importer.
- Field reports and the capability matrix.
- Reputation basics, quorum machinery, moderation juries.

### Phase 2: Decide layer (months 5–10)
- Forgejo sync, complaints, features, **solutions with solution arenas and baselines**.
- Consensus with tiers, objections, ADRs, board with gates, and merge quorum.
- Collective charter as default; themes; what-if simulator.

### Phase 3: Intelligence (months 9–14)
- **Scout**, For You, dependency alerts, Opportunity Radar, contribution matching.
- NL→query, thread summaries, duplicate detection, and reframe assistant.
- MCP server and embeds.

### Phase 4: Scale and federation (months 12–24)
- ActivityPub/ForgeFed, code and symbol search, liquid delegation, governance templates, mobile apps, pledges, standalone hosting (optional), and portable reputation research.

---

## 14. Comparison

| | GitHub | GitLab | Forgejo | Awesome-lists | AlternativeTo / Libraries.io | **Concord** |
|---|---|---|---|---|---|---|
| Git hosting, PRs, CI | ✅ | ✅ | ✅ | — | — | ✅ (via Forgejo, then own) |
| Index of external projects | Partial | ❌ | ❌ | Manual | ✅ | ✅ |
| Search by capability, license, platform, health | Weak | Weak | Weak | ❌ | Partial | ✅ |
| Semantic and natural-language search | Limited | ❌ | ❌ | ❌ | ❌ | ✅ |
| Build-vs-adopt analysis | ❌ | ❌ | ❌ | ❌ | ❌ | **Scout** |
| Community-ranked alternatives | ❌ | ❌ | ❌ | Alphabetical | Votes | Pairwise, per use case |
| Structured user experience records | ❌ | ❌ | ❌ | ❌ | Reviews | Field reports |
| Prioritization | 👍 | 👍 | 👍 | — | — | Pairwise Glicko-2 |
| **Competing, ranked solutions** | ❌ | ❌ | ❌ | — | — | ✅ |
| Consensus process and quorum gates | ❌ | ❌ | ❌ | — | — | ✅ |
| Kanban with WIP and gates | Projects | Boards | Projects | — | — | First-class |
| Federation | ❌ | ❌ | In progress | — | ❌ | ✅ |

---

## 15. Success Metrics

**Discovery:** search success rate, time to first relevant click, zero-result rate, Scout reports that lead to adopt/extend, reduction in duplicate new projects in areas Scout covers, recommendation click-through with explanations viewed.
**Decisions:** quorum-met rate, median complaint-to-ship time, % shipped features with ≥ 2 competing solutions, % consensus calls resolved without override, reluctant-consensus rate, verified-fixed rate.
**Community:** votes per active user, field reports per active project, tag completeness, moderation appeal-overturn rate.
**Health:** maintainer-reported burnout, WIP violations, bus factor trend of native projects.

---

## 16. Open Questions

1. **Gaming:** can weighted votes and Glicko-2 uncertainty resist manipulation, or do arenas need continuous audits? Which is cheaper to run?
2. **Arena cold start:** many arenas will have few voters. How aggressively do content-based priors stand in, and how are they labeled?
3. **Context explosion:** how many use-case arenas can the community sustain before rankings fragment?
4. **Consensus at scale:** liquid delegation versus contributor-only voting for large projects.
5. **Federated trust:** how do instances trust each other's votes and reputation?
6. **Crawling and licensing:** how far can indexing go under platform ToS, and how are opt-outs honored fairly?
7. **Field-report harassment:** do owner response rights and quorum moderation suffice for third-party projects that never joined?
8. **Popularity bias:** how do we keep embeddings and rankings from favoring already-famous projects?
9. **Funding the platform:** admin-light needs money. Which models keep admins from gaining content power?
10. **Sortition juries:** are random juries workable in small communities?
11. **Complaint fatigue:** how do we keep filers' quality high without gatekeeping newcomers?

---

## Connections

- [[social-media-cross-post]]: viral content pipeline uses multiple platforms; Concord could federate content decisions across instances
- [[hermes-agent-configuration-reference]]: tooling for automating Concord interactions via CLI and MCP
- [[bluesky-feeds-ranking-and-personalization]]: ranking and explainable personalization concepts transfer to the recommendation and For You surfaces
# Concord — Complaint-Driven, Consensus-Based Forge

> A federated software forge where development is driven by real complaints, prioritized by pairwise Elo, decided through rough consensus, and executed on a kanban board.

**Status:** concept spec — nothing built yet, no code.
**Updated:** 2026-09-16 — collective-governance revision: collaborators steer by default (no single maintainer at the helm), projects get tags and computed priorities, kanban phases have gates, and both consensus and merge confirmation require quorum. Second revision same day: discovery and advanced search promoted to a first-class pillar (§15); collaborative lists added as a first-class replacement for awesome-lists (§16); user-driven/admin-light principle added (§2.10, §8).

---

## 1. Vision

GitHub, GitLab, Forgejo, and Gitea are excellent at *hosting code* and *managing pull requests*. They are weak at *deciding what to build next*. Roadmaps are opaque, feature requests are noisy, and maintainers burn out from endless "please add X" threads.

**Concord flips the model:**

- Users file **complaints** (real problems), not feature requests.
- Maintainers and contributors propose **features** that solve one or more complaints.
- Features compete in **pairwise Elo/Glicko rankings** to surface what hurts most.
- Decisions are made by **rough consensus** with transparent objections and fallbacks.
- Implementation is a **kanban board** with WIP limits, not an infinite backlog.
- Discussions are **threaded like Lemmy**, with votes, moderation, and optional federation.
- **Discovery is first-class:** projects are collaboratively and exhaustively tagged, and one general search surface finds repos by tags, quality, maintenance health, language mix, governance model, and anything else users need (§15).

The result is a forge that prioritizes pain, not hype, and makes decisions legible to everyone.

---

## 2. Principles

1. **Complaints over feature requests.** A feature must trace back to at least one validated complaint.
2. **Solutions compete.** Pairwise comparisons rank features by relative priority, not absolute upvotes.
3. **Consensus, not unanimity.** "No unresolved principled objection" is the bar. Blocking is rare and must be justified.
4. **Transparent algorithms.** Elo/Glicko, pain scores, and consensus thresholds are public and configurable.
5. **Kanban limits work.** WIP limits protect maintainers and reduce context switching.
6. **Reputation is earned.** Voting weight comes from contributions, not wealth or loudness.
7. **Fork-friendly and federated.** If consensus fails, forking is a first-class option. ActivityPub/ForgeFed enable cross-instance collaboration.
8. **Threaded, community-moderated discussion.** Lemmy-style threads keep debate organized and local.
9. **Collective by default.** The direction of the software is decided by its collaborators, not by a single maintainer at the helm. Maintainers exist and matter, but their powers are housekeeping and safety, not steering. A project may opt into maintainer-led mode in its charter.
10. **User-driven, admin-light.** The whole site runs on the same user-driven machinery — content, curation, moderation, direction. Instance admins keep the lights on (uptime, configuration, backups, legal compliance) and hold no content authority; anything that smells like a content decision goes through the same quorum processes as everyone else. Every use of admin power is logged and auditable.

---

## 3. Core Vocabulary

| Term | Meaning |
|------|---------|
| **Project** | A repo + governance charter + kanban board + community. |
| **Complaint** | A described problem: impact, frequency, workaround, environment. |
| **Feature** | A proposed solution linked to one or more complaints. |
| **Pain Score** | Aggregated severity/impact of a complaint. |
| **Priority Elo** | Glicko-2 rating for features based on pairwise comparisons. |
| **Consensus Call** | Formal decision phase with positions and objections. |
| **Objection** | A principled block with a proposed remedy. |
| **Board** | Kanban board for implementation phases. |
| **Thread** | Nested comments on a complaint, feature, PR, or release. |
| **Tag** | Project-defined label (area, platform, urgency, topic) on complaints and features; drives filters and swimlanes. |
| **Priority** | Computed, never hand-assigned: pain + Elo lower bound + strategic weight (§6.2). Priorities order the board. |

---

## 4. Roles and Reputation

### Roles
- **Guest:** read-only.
- **User:** can file complaints, comment, vote in Elo (low weight).
- **Contributor:** has merged code/docs; higher vote weight; proposes features, votes in consensus, confirms merges, moves cards.
- **Reviewer:** can review PRs; can stand aside or block in consensus.
- **Maintainer:** housekeeping and safety — merge duplicate complaints, manage tags and board columns, call and close consensus, execute merges that pass the gates, and a security/legal veto that is logged and overridable. In collective mode a maintainer cannot unilaterally accept or reject features, move cards into Ready, or merge without quorum.
- **Owner:** steward of the charter and federation settings; in collective mode the charter itself can only be changed by consensus (§8), so ownership is not a steering privilege.
- **Moderator:** manages threads, reports, bans (orthogonal to the technical roles).

### Governance models (per project charter)
- **Collective (default).** Collaborators decide the direction of the software collectively. Every direction decision — accepting a feature, moving a card into Ready, confirming a merge — requires quorum among collaborators. Maintainers and reviewers still exist and are useful; none of them can steer alone.
- **Maintainer-led (opt-in).** The classic model: a maintainer may accept features and bypass the merge quorum, with every bypass logged. Switching models requires a charter consensus, so the default cannot be quietly flipped by whoever holds the owner role.

### Reputation
Reputation is earned by:
- Accepted complaints that lead to shipped features.
- Merged PRs, reviews, documentation.
- Good-faith consensus participation.
- Moderation actions that are upheld.
- Time decay prevents permanent aristocracy.

**Vote weight** for Elo and consensus:
```
weight = min(3, 1 + log10(1 + reputation))
       * stake_multiplier
       * expertise_multiplier
       * recency_factor
```
Capped to prevent plutocracy. New accounts have low weight until they contribute.

---

## 5. Core Workflows

### 5.1 Filing a Complaint
A complaint is not a demand. It is a structured problem report:

- **Title:** short pain statement.
- **Description:** what happens, when, and why it hurts.
- **Impact:** who is affected, how often, severity (1–5).
- **Workaround:** if any.
- **Environment:** OS, version, config.
- **Evidence:** logs, screenshots, reproduction.
- **Tags:** area, platform, urgency.

Users can click **"I have this too"** to add impact. Duplicates are merged by maintainers. Status: `Open → Validated → Linked → Closed / Rejected`.

### 5.2 Proposing a Feature
A feature must link to at least one validated complaint. It includes:

- **Problem statement:** which complaints it addresses.
- **Proposed solution:** technical and UX design.
- **Alternatives considered:** why this is best.
- **Trade-offs:** cost, risk, maintenance.
- **Effort estimate:** t-shirt size or story points.
- **Dependencies:** other features, PRs, releases.

Status: `Draft → Discussion → Consensus → Ready → In Progress → Review → Shipped / Rejected`.

### 5.3 Tags and Priorities
- The **project** carries tags describing its areas; complaints and features carry their own tags (area, platform, urgency, topic).
- **Priority is computed, never hand-assigned.** The priority score below (pain + Elo lower bound + strategic weight) is the single source of truth, and anyone can see why an item ranks where it does.
- Tags drive board filters and swimlanes; priorities order the queue. In collective mode no role can pin an item to the top by hand — steering happens through the score's public inputs: complaint pain, pairwise votes, and the publicly visible strategic weight.

### 5.4 Pairwise Elo Ranking
The UI shows two features side by side:

> Which should we prioritize first?
> [Feature A] vs [Feature B]
> [Prioritize A] [Prioritize B] [Both] [Neither] [Skip]

Each vote updates both features using **Glicko-2** (better than Elo for sparse data). Features have:
- Rating `r` (default 1500)
- Rating deviation `RD` (default 350)
- Volatility `σ`

**Priority score** = `(r - 2*RD) + λ * log(1 + pain_sum) + μ * strategic_weight`

The lower confidence bound (`r - 2*RD`) prevents new, uncertain features from dominating. Pair selection uses active learning: choose pairs with close ratings and high uncertainty to maximize information gain.

### 5.5 Consensus Process
Phases:

1. **Draft:** author writes proposal.
2. **Problem Validation:** confirm complaints are real and linked.
3. **Solution Review:** technical design, alternatives, risks.
4. **Consent:** formal call for consensus.
5. **Decision:** accepted, rejected, or sent back.
6. **Implementation:** enters kanban.
7. **Post-release:** complaints closed or reopened.

During **Consent**, participants choose:
- **Consent:** I support this.
- **Abstain:** neutral.
- **Stand aside:** reservations, but I won't block.
- **Block:** principled objection with a remedy.

A block must include:
- The principle at stake.
- Why the current proposal violates it.
- A concrete remedy.

**Thresholds** (configurable per project):
- Quorum: ≥20% of eligible collaborators, never fewer than 3 (in tiny projects, effectively everyone active). Abstentions count toward quorum; the consent ratio is computed over non-abstain votes.
- Consent: ≥70% of non-abstain votes.
- Blocks: any unresolved block must be overridden by ≥80% supermajority or maintainer veto (for security/legal, logged).

If quorum is not met, the call stays open and the window extends — silence cannot decide. If consensus fails, the feature can be revised, rejected, or forked. All decisions are logged.

### 5.6 Kanban Phases and Gates
Columns are **phases**, and each advance between them is gated:

- **Inbox:** new complaints.
- **Triaged:** validated, linked to features.
- **Solution Draft:** feature being written.
- **Consensus:** under formal decision.
- **Ready:** accepted, waiting for implementer.
- **In Progress:** WIP limit enforced.
- **Review:** PR open, reviews needed.
- **Done:** shipped.
- **Rejected / Archived.**

**Phase gates** — what it takes to advance:
- **Inbox → Triaged:** a collaborator validates the complaint.
- **Solution Draft → Consensus:** a collaborator opens a formal consensus call.
- **Consensus → Ready:** the consensus call passes (quorum met, no unresolved blocks). In collective mode this gate cannot be skipped — not by a maintainer, not by anyone.
- **Review → Done:** the merge passes both technical approval and the merge quorum (§5.7). A manual move to Done is a maintainer-only, logged exception.

Swimlanes: by tag, release, or priority. Cards show Elo, pain score, tags, assignee, blockers, linked PRs. Automation:
- Consensus passed → move to **Ready**.
- PR merged (gate passed) → move to **Done**.
- Complaint closed → link to release.

Metrics: cycle time, throughput, blocked time, WIP violations.

### 5.7 Code Review and Merge Confirmation
Git hosting, PRs, CI, protected branches. A PR must link to a feature. Reviews are weighted by reputation.

Merging is itself a collective decision with its own **quorum gate**, on top of technical approval:
- **Technical approval:** at least one reviewer signs off on correctness, safety, and scope.
- **Merge quorum:** a configurable number of distinct collaborator confirmations — e.g. `max(2, 25% of eligible collaborators)`, charter-configurable per project.

The feature already passed consensus, so the merge gate asks a narrower question — "is this ready to land?" — not a re-litigation of priority. In maintainer-led mode the charter may lower or drop the merge quorum; every such merge is logged.

---

## 6. Algorithms

### 6.1 Complaint Pain Score
```
pain = severity * frequency * log(1 + affected_users) * strategic_multiplier
```
Weighted by user reputation to resist spam. Decays over time so old pain doesn't dominate forever.

### 6.2 Feature Priority (Glicko-2)
Standard Glicko-2 update per pairwise vote. Vote weight as above. Priority score:
```
priority = (r - 2*RD) + λ * log(1 + pain_sum) + μ * strategic_weight
```
`λ` and `μ` are project-configurable. Strategic weight is set by maintainers and publicly visible.

### 6.3 Consensus and Merge Thresholds
- Quorum: `min(eligible, max(3, ceil(0.2 * eligible)))` participants.
- Consent: `consent / (consent + stand_aside + block) ≥ 0.70`
- Override: `supermajority ≥ 0.80` or maintainer veto with written justification.
- Merge quorum: `max(min_confirmations, ceil(merge_ratio * eligible))` distinct collaborator confirmations, plus ≥1 reviewer technical approval. Defaults: `min_confirmations = 2`, `merge_ratio = 0.25`.

---

## 7. Threaded Conversations (Lemmy-Style)

Every complaint, feature, PR, and release has a thread. Threads are:
- **Nested:** comments can be replied to indefinitely.
- **Votable:** up/down votes affect sorting, not truth.
- **Moderated:** community moderators, reports, locks, bans.
- **Labeled:** `question`, `objection`, `support`, `evidence`, `off-topic`.
- **Federated:** ActivityPub + ForgeFed for cross-instance threads.

Sorting: hot, top, new, controversial. Users can filter by label. Objections are highlighted and linked to consensus calls.

---

## 8. Governance and Moderation

Each project has a **charter** defining:
- Roles and permissions.
- **Governance model:** `collective` (default) or `maintainer-led`.
- Decision rules and thresholds, including the merge quorum.
- Veto powers and their limits.
- Fork policy and federation settings.
- Moderation appeals process.

Amending the charter is itself a consensus decision: in collective mode the governance rules cannot be changed unilaterally, not even by the owner.

**Transparency:** all votes, consensus positions, moderation actions, and Elo updates are logged in an append-only audit log. Users can export their data.

**Instance administration:** Concord separates *platform* from *content*. Instance admins configure the platform (auth, storage, backups, federation peering) and handle legal requirements; they hold no content authority. Moderation, list curation, the tag taxonomy, and feature direction all run through the same user-driven quorum processes as everything else — an admin who wants to decide content does it as a regular member. Admin actions appear in the audit log and are subject to community review; an admin override of a user decision requires a written justification and triggers an automatic supermajority confirmation vote.

**Forking:** if consensus fails or governance breaks down, anyone can fork. Federation lets forks share complaints, features, and even code.

---

## 9. Architecture

### Recommended MVP Approach
Do not rewrite Git. Use **Forgejo/Gitea** as the Git/CI substrate for the first version. Concord becomes a decision layer on top:
- Sync repos, PRs, and users from Forgejo.
- Store complaints, features, Elo, consensus, and kanban in Concord.
- Use webhooks to keep statuses in sync.

Long-term, Concord can be a standalone forge.

### Components
- **Git Service:** bare repos, SSH/HTTP, LFS, CI runners.
- **Forge API:** GraphQL + REST + webhooks.
- **Decision Engine:** complaints, features, Elo/Glicko, consensus, kanban.
- **Social:** threads, votes, moderation.
- **Reputation Service:** earned weights, decay, anti-abuse.
- **Notification Service:** email, web push, digest.
- **Federation Service:** ActivityPub/ForgeFed.

### Stack
- Frontend: SvelteKit or React, PWA, SSR.
- Backend: Go or Rust, PostgreSQL, Redis, S3, Meilisearch.
- Git: libgit2 or embedded Forgejo.
- CI: Woodpecker/Drone or built-in runners.

### Data Model (simplified)
```
users, orgs, projects, repos
complaints(id, project_id, title, body, severity, status, merged_into)
complaint_impacts(user_id, complaint_id)
features(id, project_id, title, body, status, elo_r, elo_rd, elo_sigma)
feature_complaints(feature_id, complaint_id)
pairwise_votes(voter_id, feature_a, feature_b, outcome, weight, created_at)
consensus_calls(feature_id, phase, opens_at, closes_at, threshold)
consensus_positions(user_id, call_id, position, reason)
objections(id, call_id, user_id, reason, remedy, status)
boards, columns, cards, swimlanes
threads, comments, comment_votes
reputations, roles, charters
audit_log
```

### APIs
- GraphQL for UI.
- REST for integrations.
- Webhooks for CI/chat.
- CLI: `concord` for git, issues, voting, kanban.

---

## 10. UX Highlights

- **Project Home:** top complaints, feature ranking, kanban snapshot, activity feed.
- **Complaint Page:** thread, impact meter, linked features, status.
- **Feature Page:** Elo rating, pairwise vote button, consensus status, kanban card, spec, linked PRs.
- **Voting UI:** simple A/B choice with context; no endless lists.
- **Board:** drag-and-drop, WIP limits, filters, swimlanes.
- **Threads:** nested, collapsible, labeled, votable.
- **Mobile:** responsive PWA; push notifications.

---

## 11. Security, Privacy, Anti-Abuse

- RBAC, 2FA, SSH keys, signed commits.
- Rate limits, invite systems, OAuth/SSO.
- Sybil resistance: account age, reputation, anomaly detection.
- Elo gaming mitigation: Glicko-2 uncertainty, weighted votes, pair selection, vote audits.
- GDPR: data export, deletion, self-host.
- CI isolation: containers, secrets management, SBOM, dependency scanning.

---

## 12. Roadmap

### MVP (3–6 months)
- Forgejo backend for Git/CI.
- Complaints, features, threaded comments.
- Pairwise Elo/Glicko voting.
- Kanban board with WIP limits and phase gates.
- Consensus call with quorum.
- Merge confirmation quorum (collaborator confirmations + reviewer approval).
- Collective governance model as the default charter.

### v1 (6–12 months)
- Reputation and weighted votes.
- Full consensus phases, objections, escalation.
- Automation, API, CLI, import from GitHub/GitLab.
- Moderation tools.

### v2 (12–24 months)
- Federation via ActivityPub/ForgeFed.
- Governance templates (BDFL, meritocracy, sociocracy).
- Analytics for maintainer health.
- Mobile apps.

---

## 13. Comparison

| Feature | GitHub | GitLab | Forgejo/Gitea | Concord |
|--------|--------|--------|---------------|---------|
| Git hosting | ✅ | ✅ | ✅ | ✅ |
| Issues/PRs | ✅ | ✅ | ✅ | ✅ |
| Feature prioritization | 👍 reactions | 👍 reactions | 👍 reactions | Pairwise Elo/Glicko |
| Complaint-driven | ❌ | ❌ | ❌ | ✅ |
| Consensus process | ❌ | ❌ | ❌ | ✅ |
| Kanban with WIP | Projects | Boards | Projects | First-class |
| Threaded discussions | ❌ | ❌ | ❌ | Lemmy-style |
| Federation | ❌ | ❌ | ❌ | ActivityPub/ForgeFed |
| Reputation-weighted votes | ❌ | ❌ | ❌ | ✅ |

---

## 14. Open Questions

1. **Elo gaming:** Can weighted votes and Glicko-2 uncertainty fully prevent manipulation? Likely needs anomaly detection and audits.
2. **Consensus at scale:** For large projects, should voting be delegated (liquid democracy) or restricted to contributors?
3. **Federated votes:** How do we trust votes from other instances? Reputation portability is hard.
4. **Integration:** Should Concord always be a layer over Forgejo, or eventually replace it?
5. **Complaint fatigue:** How do we prevent users from filing low-quality complaints? Reputation and validation help, but culture matters.

---

**Concord** is not just another Git forge. It is a decision-making engine for software projects. By grounding development in real complaints, ranking solutions through pairwise comparisons, deciding through rough consensus, and executing on a kanban board, it aims to make open source more sustainable, transparent, and humane.

---

## 15. Discovery and Advanced Search (first-class)

Discovery is a core feature, not an afterthought. Every project is **collaboratively and exhaustively tagged**, and Concord exposes a single, general search surface over all of it.

### What is searchable
- **Projects/repos:** name, description, topics/tags, languages and their percentages, license, governance model (collective vs maintainer-led), maintenance health, activity, size.
- **Complaints and features:** full text, tags, status, pain and priority ranges.
- **People and teams:** later milestones, always permission-aware.

### Tag model (collaborative, exhaustive)
- Any contributor may apply tags; renames, merges, and deletions of tags are maintainer actions, logged. Tag synonyms/aliases collapse duplicates ("js" ≡ "javascript").
- The tagging UX suggests existing tags before allowing new ones — exhaustiveness is a community norm the tooling enforces, not a hope.
- Tags are the primary discovery facet; every search returns tag facets with counts.

### Search surface
- **A query language, not a box:** free text (full-text search) plus structured filters — `tag:`, `language:`, `lang_pct>=:`, `license:`, `model:collective`, `health>=:`, `active_within:`, `stars>=:` — with AND/OR/NOT and grouping. Any query can be saved, shared by URL, and subscribed to (RSS/Atom or notifications).
- **Result ranking:** relevance × quality × health with user-tunable weights; sort by relevance, recently updated, health, fewest open complaints, or newest.
- **Facets always on:** tags, languages, licenses, governance model, health buckets — returned with counts alongside every result set.
- **Quality and health are transparent, not vibes:** the health score is computed from public signals — commit and release recency, issue-triage and PR-review latency, contributor breadth, bus factor — and every component is exposed so users can reweight their own search. Metrics come from forge sync; disagreement is filed as a complaint, not emailed into a void.
- **Language percentages** come from the repo tree (linguist-style detection, e.g. go-enry), and are collaboratively correctable where detection is wrong.
- **API-first:** every search is a stable, documented, paginated API endpoint; the web UI is just a client of it. Search quality regressions are tested like any other feature.

---

## 16. Collaborative Lists (awesome-lists, first-class)

Awesome lists are a structural weakness of the git-forge model: they live as markdown files, merge wars decide what gets in, nothing ranks or classifies entries, and one list owner gatekeeps. Concord makes lists a first-class object.

- **Lists are objects, not files.** A list is a curated collection of entries (title, URL, description, category), taggable, attachable to a project or instance-wide, searchable, and fully audit-logged.
- **Anyone proposes, quorum disposes.** Adding, editing, categorizing, or removing an entry is a proposal decided by the same quorum machinery as everything else — collaborator confirmations, blocks with remedies, reputation-weighted participation. No merge wars, no benevolent list dictator.
- **Entries are ranked, not just listed.** Entries compete in pairwise comparisons (the same Glicko-2 engine as feature priority), so "best first" reflects community judgment instead of alphabetical accident. Categories group entries; ranking orders within and across them.
- **Spam dies by reputation.** Entries proposed by low-reputation accounts need more confirmations; mass-proposed links are rate-limited and audited. Removal for spam is a moderation action, itself subject to quorum and appeal.
- **Discovery integration.** Lists and their entries are first-class search citizens: find lists by tag, language, and health like projects — and find *entries across all lists* ("show me every Rust debugging tool ranked by community judgment, wherever it's curated").

---

## Connections

- [[social-media-cross-post]] — viral content pipeline uses multiple platforms; Concord could federate content decisions across instances
- [[hermes-agent-configuration-reference]] — tooling for automating Concord interactions via CLI
- [[bluesky-feeds-ranking-and-personalization]] — Elo-based ranking concepts transfer from feeds to feature prioritization

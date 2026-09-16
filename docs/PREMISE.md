# Concord — the premise

Concord is a forge: a place to host code, discuss changes, and ship software. What makes it different is what it does *between* the code. It turns the messy, political, exhausting job of deciding what to build into something structured, transparent, and shared.

*(This is the short version, written to be shared. The full spec lives in `concord-spec.md` next to this file.)*

## The problem

Every software project answers the same questions — what hurts, what to build, what to merge — and today's forges answer them badly:

- Feature requests pile up in issue trackers as competing demands. Noise wins, loudness wins, and whoever files the most tickets wins.
- Roadmaps live in a maintainer's head. Contributors can't see why something matters, so they can't help decide.
- Maintainers burn out carrying every decision alone.
- "Awesome lists" — how the ecosystem organizes its knowledge — are markdown files in git repos: no way to classify entries, no way to rank them, merge wars over what gets in.
- Search is an afterthought. Try finding "well-maintained Rust CLIs, permissive license, active in the last month" on any forge.

## The flip

Code hosting is the substrate. Decisions are the product.

1. **Complaints, not feature requests.** Work starts as a structured problem report: who is hurt, how often, what the workaround costs. A feature must trace back to validated complaints. "I have this too" is a first-class interaction.

2. **Solutions compete.** Proposed features are ranked by pairwise comparison (Glicko-2): "which should we build first, A or B?" Cheap to answer, hard to spam, mathematically transparent. Priority is computed, never hand-assigned.

3. **Consensus, not command.** Features pass a formal consent round — consent, stand aside, abstain, or block with a stated principle and remedy — with quorum. An unresolved block needs a supermajority or a logged, overridable safety veto. Maintainers exist and matter, but their powers are housekeeping and safety. Nobody steers alone; a project can opt into maintainer-led mode, but the default is collective.

4. **Kanban with teeth.** Work moves through phases, and each advance is gated: nothing reaches Ready without consensus; nothing lands without a reviewer's technical approval and a quorum of collaborator confirmations. WIP limits protect attention.

5. **Discovery is the point.** Projects are exhaustively, collaboratively tagged. Search is a query language, not a box: filter by tag, language and its percentage, license, governance model, and a transparent maintenance-health score — commit recency, review latency, contributor breadth, release cadence — where every component is exposed so you can reweight it yourself. API-first, with facets on every result.

6. **Lists are objects, not files.** Awesome lists become collaborative databases: anyone proposes entries, quorum admits them, pairwise ranking orders them, and search finds entries across every list on the instance.

7. **Users run the site.** Instance admins keep the lights on — uptime, configuration, backups, legal — and hold no content authority. Moderation, curation, tags, and direction all run through the same user-driven quorum machinery, and every use of admin power is logged and auditable.

8. **Fork-friendly.** Consensus failing is a feature, not a crisis: fork the project or the instance. Federation (ActivityPub/ForgeFed) is on the roadmap, so forks can still share complaints, lists, and code.

## What Concord is not

It is not a GitHub clone with extra buttons, and it does not replace git. It starts as a decision layer over an existing Forgejo or Gitea instance — OAuth login, webhooks, bot-mediated merges — and grows into a full forge from there. It is not raw vote-based democracy either: it is consent-based, blocks require remedies, and silence never decides anything.

## Status

The spec is written and the skeleton exists: Go, chi, SQLite, with the Glicko-2 engine, the consensus rules, the merge gate, and the search surface under test. If this sounds like your kind of forge, read `concord-spec.md` and join in — the whole point is that direction is decided by the people who show up.

# Concord — the premise

Concord is a forge: host code, discuss changes, ship software. What makes it different is
what it does *between* the code — it turns the political, exhausting job of deciding what to
build into something structured, transparent and shared.

*(The short version, written to be handed to someone. The full argument is
`concord-spec-r4.md`; the inventory of what exists today is `docs/FEATURES.md`; the status
counts live in `docs/HANDOFF.md` and this file does not repeat them.)*

## The problem

Every project answers the same three questions — what hurts, what to build, what to merge —
and today's forges answer them badly:

- Feature requests pile up as competing demands. Loudness wins; whoever files most wins.
- Roadmaps live in a maintainer's head, so contributors cannot see why anything matters and
  cannot help decide.
- Maintainers burn out carrying every decision alone.
- "Awesome lists" are markdown files: unclassifiable, unrankable, merge wars over what's in.
- Search is an afterthought. Try finding "well-maintained Rust CLIs, permissive license,
  active last month" on any forge.

## The flip

Code hosting is the substrate. **Decisions are the product.**

1. **Complaints, not feature requests.** Work starts as a structured problem report: who is
   hurt, how often, what the workaround costs. A feature traces back to validated complaints,
   and "I have this too" is a first-class act rather than a comment nobody counts.

2. **Solutions compete.** Features are ranked by pairwise comparison ("which first, A or B?")
   under Glicko-2: cheap to answer, hard to spam, and the arithmetic is inspectable. Priority
   is computed, never assigned by hand.

3. **Consensus, not command.** A feature passes a consent round — consent, stand aside,
   abstain, or block with a stated principle and remedy — with quorum. An unresolved block
   needs a supermajority, or a logged safety veto that can itself be overridden. Maintainers
   do housekeeping and safety, not direction; a project may opt into maintainer-led mode, but
   collective is the default.

4. **Planning is written down.** Features group into milestones with dates and statuses, on a
   roadmap that shows planned against shipped. A dependency that would close a loop is
   refused by name, and work cannot start on top of an unshipped blocker.

5. **Kanban with teeth.** Each phase change is gated: nothing reaches Ready without evidence
   and consensus, nothing lands without a technical approval and a quorum of collaborator
   confirmations. WIP limits protect attention.

6. **Discovery is the point.** Projects are exhaustively, collaboratively tagged. Search is a
   query language, not a box — tags, language share, license, governance, and a transparent
   maintenance-health score whose every component you can reweight yourself, with facets on
   every result. When a query is not enough, ask the forge: the request board takes plain
   language and returns community-ranked answers.

7. **Lists are objects, not files.** An awesome list is a database: entries are proposed,
   admitted by quorum, ordered by pairwise ranking, and findable from search across lists.

8. **Users run the site.** Instance admins keep the lights on — uptime, config, backups,
   legal — and hold no content authority. Moderation, curation, tagging and direction run
   through the same quorum machinery, and every act of admin power is logged and auditable.
   Taking part does not begin with an account: a visitor can file, comment and vote as a
   nameless guest until the instance reaches a configured size, and registering carries what
   they did into their account. The door closes by being asked to sign up, not by a form.

9. **Fork-friendly.** Consensus failing is not a crisis: fork the project or the instance.
   Federation (ActivityPub/ForgeFed) is on the roadmap so forks can share complaints, lists
   and code.

## What Concord is not

Not a GitHub clone with extra buttons, and not a replacement for git. It begins as a decision
layer over an existing Forgejo or Gitea instance — OAuth login, webhooks, bot-mediated merges
— and grows into a forge. Nor is it raw vote-counting democracy: it is consent-based, blocks
must carry remedies, and silence never decides anything.

## Status

Running at <https://concord.polarisocial.xyz>: the complaint board, request board, Glicko-2
pairwise ranking with per-project criteria, the full consent round (stances, stand-asides,
blocks with remedies, quorum, override window, decision record), the merge gate, milestones
and the roadmap, feature dependencies, taxonomy and tag proposals, collaborative lists,
documents, faceted search, and the moderation and audit surfaces. A visitor without an account
can take part until the instance passes its configured monthly-active threshold, and the
cookie notice states what is stored before anything is.

Everything above is verified rather than asserted: per-test in-memory databases, Go and
JavaScript mutation gates where every mutant must be killed, and browser suites that drive the
real pages. `make verify`, `make gates` and `make e2e` are the three commands.

Not built, stated plainly: federation — today a fork is a fork with no shared state; email
verification and captcha — so the guest door is rate-limited by address rather than proven
human; a retention policy for guests who never register; and a consensus call over a whole
milestone rather than one feature.

If this sounds like your kind of forge, read `concord-spec-r4.md` and join in — the point is
that direction is decided by the people who show up.

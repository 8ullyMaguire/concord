Work on concord. It is an existing, deployed project: you are continuing it, not starting it.

Read, in this order, before deciding anything:

- docs/PLAN-r4.md — the work order. Milestones R1–R8 are shipped; R9–R12 (`docs/plans/contribution-feed.md`)
  and R13–R15 (`docs/plans/instance-statistics.md`) are planned, with the file each ticket touches.
  "Where the next milestone should go" names the one to take.
- docs/concord-spec-r4.md §4.5.7 and §4.13 (the two sections under construction), plus §2, §4, §6, §9, §15.
  docs/concord-spec.md is SUPERSEDED — do not plan from it, and do not cite it.
- docs/HANDOFF.md for the next milestone and the traps; docs/REMAINING.md §21.6 for what is left
  (everything before §21.6 in that file is history); docs/KNOWN-ISSUES.md before touching a gate.
- The plan for the ticket you pick under docs/plans/, and every `docs/specs/*-spec.md` it cites.

Then load the concord-platform skill, and concord-api-client if the ticket writes to the live instance.
They carry the traps that cost a day each: the `{project_id}` path param is a SLUG; every handler must
resolve the project from the path or it leaks a private project's rows; migrations are append-only; test
DBs are `:memory:` per test; and `make verify` is the floor rather than the goal.

Take the plan's order. Do not re-rank by effort or by what is easiest to demo. If evidence contradicts the
order, say which evidence and what it changes, then proceed.

One ticket per turn. Finish it, verify it, ship it, write it down. Do not start the next one to fill the turn.

For each ticket, the order of work is fixed:

  1. Spec clause and plan first — the plan file already carries exact file paths, the verification command
     for every step, and the expected output of each. If the ticket needs a clause the spec does not have,
     write the clause before the code. No code before both exist.
  2. Migration, then store, then handler, then route, then page. Never a route with no reader: the
     reader-coverage gate fails it, and a store method with no caller fails the export-inventory gate.
  3. Tests, and watch them fail for the right reason before they pass. A test that passes on the unfixed
     code is not evidence; break the code deliberately and prove the test notices. A COMPILE-ERR mutation
     is not a kill.
  4. `make verify`, then `make gates`, then `make e2e` — SERIALLY, never in one shell. `make gates`
     rewrites internal/httpapi/finder.go in place, and running it beside `make verify` produces a failure
     that reproduces in neither target alone. Report the real counts from the output, not from memory.
  5. `make deploy`, then prove it on the RUNNING binary with a command whose output you paste. The public
     instance is thinkcentre's (concord.polarisocial.xyz); `127.0.0.1:8006` on this host is the DEV
     instance with a different database and different ids. Check which one answered before diagnosing.
  6. Docs: the milestone row in docs/PLAN-r4.md, a pass section in docs/REMAINING.md (numbered, with the
     defect and why it happened), the spec section if the shape changed, README if the front door moved.
  7. Commit, then `git push forgejo HEAD:main` and `git push github HEAD:master`. Confirm both.
  8. A note in ~/secondbrain/10-Projects/ for the session.

Evidence rules, because they are the difference between a report and a claim:

- A 200 is not proof that a surface works. The project page is a SPA: `GET /projects/<slug>` returns ~4 KB
  of shell whatever the data says. Read `document.body.innerText`, and click through the tabs.
- A green test is not proof until the mutation that should break it does.
- A compile error is not a killed mutation. Read the mutation actually applied before you count it.
- Live behaviour is checked against the running instance, not against your reading of the code.
- An empty result is a result: `min_health>0` returning zero rows, or a feed with no items, is the
  measured finding, not a reason to widen the query until something appears.
- If something cannot be verified — a host is out of memory, a forge token is missing, a repo URL has not
  been stated — say so plainly and leave it unverified. Never fill the gap with a plausible result.

Decisions that are yours: implementation shape, table and column names, where a surface lives, which of two
equivalent queries to keep, test design, and what to defer — provided the deferral is written into the plan
with its reason.

Decisions that are not yours. Stop the ticket, write the decision into docs/QUESTIONS.md with two or three
options and your recommendation, and say it is waiting:

- The upstream repository for each of the 74 catalog projects (Q5). Six resolve from a local checkout; the
  rest are a mapping only the owner has. Never guess `github.com/<slug>` — a wrong URL sends a stranger to
  somebody else's repository, and R9's whole point is that identity is stated rather than derived.
- Live corpus surgery: deleting, rewriting statuses on, or re-seeding real rows. A batch you create and
  clean up is fine; the owner's 1,874 features are not your experiment.
- Governance thresholds: quorum ratios, override windows, consent caps, eligibility rules (§6.6, §8.2).
- Licensing and attribution, and anything that changes what a published number means (§4.13's provenance
  split is a rule, not a default).
- Removing a gate, weakening a check, or deleting a failing test to get green.

Guardrails: migrations are append-only — never edit one that has been applied. SQLite writes go through a
`.backup`, never a `cp`, and the live DB is held by a running service: `lsof` before believing "database is
locked". No credentials or secrets in the repository. Unknown health is not zero health, and a seeded row
is not a contribution — a number that cannot say which it is, is not published.

End the turn with a short report: the ticket, what shipped, the exact command and output that proves it,
what you decided and why, what is blocked and on whom, and what you will take next. Nothing else — no
narration of the work.

First turn: read only. Report the order you intend to take, ticket by ticket, for the next several turns,
with one line of justification each. Start the first ticket in that same turn only if you can finish it;
otherwise end after the report.

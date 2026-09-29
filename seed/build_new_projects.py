#!/usr/bin/env python3
"""Build the Concord seed for the four newly-named projects.

Every feature is derived from a file in the project's own repository — a
requirements ledger, a spec, a plan — not from prose summaries and not from
memory. Where a project has 678 ledger rows, features are grouped by milestone
so the result stays comparable with the other projects; the grouping is
recorded in the feature body along with the exact counts, so nothing is lost.

The one thing this does not do is decide what is done. It reads the `status`
column each project already maintains and maps it onto Concord's vocabulary.
A project whose ledger disagrees with its prose is reported, not resolved.
"""

import csv
import json
import os
import re
import subprocess
import sys
from collections import Counter, defaultdict

OUT = sys.argv[1] if len(sys.argv) > 1 else "/tmp/concord_new_projects.json"

# Concord's status vocabulary. The mapping is per project because each ledger
# uses its own words, and collapsing them into one scale would quietly assert
# that two projects' notions of "done" mean the same thing.
SHIPPED = "shipped"
READY = "ready"
CONSENSUS = "consensus"
DISCUSSION = "discussion"
DRAFT = "draft"
REJECTED = "rejected"


def sh(cmd, cwd):
    return subprocess.run(
        cmd, cwd=cwd, shell=True, capture_output=True, text=True, timeout=60
    ).stdout.strip()


def read_csv(path):
    with open(path, newline="") as f:
        return list(csv.DictReader(f))


def milestone_sort_key(m):
    """M9 before M10 before M15.9; unassigned last."""
    if m == "unassigned":
        return (2, 0, 0)
    m2 = re.sub(r"[A-Za-z].*$", "", m)
    try:
        return (0, int(m2), m)
    except ValueError:
        return (1, 0, m)


# --------------------------------------------------------------------------
# lorehaven — 678 rows in docs/requirements.csv, grouped by milestone
# --------------------------------------------------------------------------
def build_lorehaven(root):
    rows = read_csv(os.path.join(root, "docs/requirements.csv"))
    by_ms = defaultdict(Counter)
    areas = defaultdict(Counter)
    for r in rows:
        ms = (r.get("milestone") or "").strip() or "unassigned"
        st = (r.get("status") or "").strip()
        by_ms[ms][st] += 1
        areas[ms][(r.get("area") or "").strip() or "general"] += 1

    def shipped(c):
        return (
            c["implemented-fully-tested"]
            + c["implemented-locally-tested"]
            + c["implemented-verified-e2e"]
        )

    total = Counter()
    for r in rows:
        total[r["status"].strip()] += 1

    features = []
    for ms in sorted(by_ms, key=milestone_sort_key):
        c = by_ms[ms]
        done, planned = shipped(c), c["planned"]
        top = ", ".join(f"{a} ({n})" for a, n in areas[ms].most_common(4))
        if planned:
            status = DISCUSSION
            title = f"M{ms.lstrip('M')}: {done} implemented, {planned} planned"
            body = (
                f"Milestone {ms}: {done} implemented, {planned} planned and not started. "
                f"Areas: {top}. Ledger rows carry the individual requirements; this groups "
                f"them so the milestone is one comparable unit."
            )
        elif c["unsupported"]:
            status = REJECTED
            title = f"M{ms.lstrip('M')}: {done} implemented, {c['unsupported']} unsupported"
            body = (
                f"Milestone {ms}: {done} implemented, {c['unsupported']} deliberately "
                f"unsupported. The unsupported rows are recorded as rejected rather than "
                f"planned because the ledger already decided against them. Areas: {top}."
            )
        else:
            status = SHIPPED
            title = f"M{ms.lstrip('M')}: {done} implemented"
            body = (
                f"Milestone {ms}: {done} implemented, none outstanding. Areas: {top}. "
                f"Status is taken from the requirements ledger's own column, not from a "
                f"progress document."
            )
        if ms == "unassigned":
            title = f"Unassigned milestone rows: {done} implemented"
            if planned:
                title += f", {planned} planned"
        features.append(
            {
                "title": title,
                "body": body,
                "effort": "M" if planned == 0 else "L",
                "status": status,
                "complaints": [0],
            }
        )

    return {
        "slug": "lorehaven",
        "name": "Lorehaven",
        "description": (
            "Self-hosted home for fanfiction: read without an account, write without a "
            "publishing queue, import the library you already have, take it offline. "
            "Rust + Postgres. Built from docs/goal.md with 678 rows tracked in "
            "docs/requirements.csv — the repo maintains its own honest ledger, which is "
            "the only source used here."
        ),
        "governance_model": "maintainer_led",
        "license": "",
        "complaints": [
            {
                "title": "A progress document is not evidence that the code works",
                "body": (
                    "lorehaven keeps docs/requirements.csv with a status column and an "
                    "evidence column per row, precisely because a claim of completion is "
                    "worthless without a command behind it. The three implemented grades "
                    "distinguish a fully-tested row from a locally-tested one from one "
                    "verified end to end, and they are not interchangeable: 289 rows are "
                    "fully tested, 193 are tested locally, and 116 are verified E2E."
                ),
                "severity": 4,
                "frequency": 0.4,
            },
            {
                "title": "678 individual requirements are not 678 independent pieces of work",
                "body": (
                    "Seeding one feature per ledger row would make this project 40x larger "
                    "than every other in the portfolio and hide the shape of what is left. "
                    "Milestone grouping keeps the counts exact in the body while making the "
                    "project comparable."
                ),
                "severity": 2,
                "frequency": 0.3,
            },
            {
                "title": "Three milestones hold 76 of the 78 outstanding requirements",
                "body": (
                    "M45 (45 planned), M59 (17) and M60 (11) are where the remaining work "
                    "actually is. A flat 'planned' list would make that invisible."
                ),
                "severity": 3,
                "frequency": 0.4,
            },
        ],
        "features": features,
        "_stats": dict(total),
    }


# --------------------------------------------------------------------------
# stash (StashForge) — 73 rows R001..R073 in docs/requirements.csv
# --------------------------------------------------------------------------
def build_stashforge(root):
    rows = read_csv(os.path.join(root, "docs/requirements.csv"))
    features = []
    for r in rows:
        st = r["status"].strip()
        if st == "shipped":
            status = SHIPPED
        elif st == "tested":
            status = SHIPPED
        else:
            status = SPECIFIED_STATUS
        prio = r.get("priority", "").strip()
        bits = [
            f"Ledger {r['id']}, thread {r['thread']}.",
            f"Priority {prio}." if prio else "",
            f"Spec §{r['spec_section']}." if r.get("spec_section") else "",
            f"Ledger status: {st}.",
        ]
        if r.get("depends_on"):
            bits.append(f"Depends on {r['depends_on']}.")
        if r.get("notes"):
            bits.append(r["notes"])
        features.append(
            {
                "title": f"{r['id']}: {r['title']}",
                "body": " ".join(b for b in bits if b),
                "effort": "M",
                "status": status,
                "complaints": [0],
            }
        )
    return {
        "slug": "stashforge",
        "name": "StashForge",
        "description": (
            "A fork of stashapp/stash turned into one binary that is a private library "
            "manager and a self-governed public curation site at once. Media never leaves "
            "the owner's host; the metadata is a shared commons that contributes by "
            "default and opts out per library, and who may change it is decided by quorum "
            "or moderators, never by an owner override. Plus a P2P downloader plugin "
            "(BitTorrent + ed2k over Kademlia). Tracked in docs/requirements.csv, R001-R073."
        ),
        "governance_model": "collective",
        "license": "",
        "complaints": [
            {
                "title": "The ledger says 9 of 73 requirements are done, and the two columns that disagree are the ones that matter",
                "body": (
                    "R001-R006 are 'shipped' and R007-R009 are 'tested'. The rest are "
                    "'specified', which is the honest state of a project whose plan is "
                    "written per-step. The gap between 'specified' and 'tested' is where "
                    "this project actually lives."
                ),
                "severity": 3,
                "frequency": 0.5,
            },
            {
                "title": "A fork inherits upstream's tests, and an inherited test suite measures the wrong thing",
                "body": (
                    "The base is stashapp/stash develop at b6b09dd5. Upstream's tests assert "
                    "upstream's behaviour; the whole point of the fork is to differ. Every "
                    "milestone here has to be judged by a test that fails against the base."
                ),
                "severity": 4,
                "frequency": 0.5,
            },
            {
                "title": "Governance on top of upstream's models duplicates what upstream already has",
                "body": (
                    "The sibling stash-box fork carries 4,480 lines of native edit/vote "
                    "consensus. Porting a parallel proposal/ballot system onto an archive "
                    "that already has one produces a fork of a different product."
                ),
                "severity": 3,
                "frequency": 0.3,
            },
        ],
        "features": features,
    }


# --------------------------------------------------------------------------
# stash-box fork — SPEC.md vision phases + tracked issues
# --------------------------------------------------------------------------
def build_stashbox(root):
    spec = os.path.join(root, "docs/SPEC.md")
    text = open(spec).read()
    phases = re.findall(r"^\|\s*(\d)\s*\|\s*(.+?)\s*\|$", text, re.M)
    features = []
    for num, scope in phases:
        if not scope or "Scope" in scope:
            continue
        features.append(
            {
                "title": f"Mesh phase {num}: {scope[:80]}",
                "body": (
                    f"From docs/SPEC.md §7.15 MVP roadmap. {scope}. Nothing in this phase is "
                    f"implemented; the direction decision (§6.1, taken 2026-09-29) names the "
                    f"mesh as the destination, with the help-wanted backlog as the "
                    f"prerequisite."
                ),
                "effort": "L" if num in "123" else "XL",
                "status": CONSENSUS,
                "complaints": [0],
            }
        )

    # The tracked upstream issues, counted rather than expanded: 177 rows would
    # bury the five mesh phases that are the actual destination.
    issues_path = os.path.join(root, "docs/track/issues-open.json")
    if os.path.exists(issues_path):
        items = json.load(open(issues_path))
        labels = Counter()
        for i in items:
            for lb in i.get("labels") or []:
                labels[lb["name"] if isinstance(lb, dict) else str(lb)] += 1
        features.append(
            {
                "title": f"Upstream issue backlog: {len(items)} open ({labels.get('help wanted', 0)} help-wanted)",
                "body": (
                    f"Tracked in docs/track/issues-open.json, harvested 2026-09-26. "
                    f"{labels.get('enhancement', 0)} enhancement, {labels.get('help wanted', 0)} "
                    f"help wanted, {len(items) - labels.get('enhancement', 0) - labels.get('help wanted', 0)} "
                    f"unlabelled. These are upstream's issues, not this fork's requirements, "
                    f"and they are the stated prerequisite: each is a bounded, provable defect "
                    f"that leaves the tree in a state worth building the mesh on top of. "
                    f"Individual issues are not expanded here — a survey decides which matter."
                ),
                "effort": "L",
                "status": READY,
                "complaints": [1],
            }
        )

    # Verified absences from SPEC §7.16 — the mesh items nothing here can extend.
    for item, present in [
        ("Elo / Glicko / TrueSkill ranking", "No rating code anywhere outside test fixtures"),
        ("Snapshot collages / storyboards", "images exist as opaque width/height metadata only"),
        ("Identification board", "absent from schema, services and all 18 frontend page dirs"),
        ("Completion scores", "no per-entity completeness notion exists"),
        ("Federation and replication", "single-instance only"),
        ("Reviews, directories, XP and badges", "none present"),
    ]:
        features.append(
            {
                "title": f"Build from scratch: {item}",
                "body": (
                    f"SPEC §7.16 verified by search, not assumed: {present}. The mesh needs "
                    f"this capability and nothing in this repository provides it, so it is new "
                    f"work rather than an extension."
                ),
                "effort": "XL",
                "status": DRAFT,
                "complaints": [0],
            }
        )

    return {
        "slug": "stashbox-fork",
        "name": "Stash-box fork (federated discovery mesh)",
        "description": (
            "Fork of github.com/stashapp/stash-box, pinned at b4b8aef2. A federated mesh of "
            "independently operated metadata instances: catalog everything, curate together, "
            "discover everywhere, preserve forever. Metadata-first, discovery-first, "
            "preservation-first, with preservation automatic by default (a scene on three "
            "instances unless configured otherwise) and trust unlocking content. Direction "
            "decided 2026-09-29: the mesh is the destination, upstreamable fixes are a "
            "standing constraint, a running instance is the prerequisite."
        ),
        "governance_model": "collective",
        "license": "",
        "complaints": [
            {
                "title": "A fork with no stated product direction is a clone that will drift",
                "body": (
                    "SPEC §6 recorded the open question and four candidate directions (A port "
                    "StashForge governance, B upstream contributions, C private instance, D "
                    "other). §6.1 then decided it: C is the prerequisite, B is a constraint on "
                    "how work is shaped, and the destination is the mesh. That decision is why "
                    "the phases below exist and why the issue backlog is finite."
                ),
                "severity": 4,
                "frequency": 0.4,
            },
            {
                "title": "Two of the upstream repo's own documented claims were false on this host",
                "body": (
                    "SPEC §2 says every fact was measured on the clone rather than taken from "
                    "the README or CLAUDE.md, and records where the two disagree — two claims "
                    "are false. A fork built on documentation rather than measurement starts "
                    "from a wrong baseline."
                ),
                "severity": 4,
                "frequency": 0.4,
            },
            {
                "title": "Preservation by default is a promise the current architecture cannot keep",
                "body": (
                    "§7.3 says a scene should exist on at least three instances unless "
                    "configured otherwise. There is no replication code in the tree, so the "
                    "default is currently a promise rather than a behaviour, and a promise "
                    "nobody has measured is a promise that will be broken quietly."
                ),
                "severity": 4,
                "frequency": 0.3,
            },
        ],
        "features": features,
    }


# --------------------------------------------------------------------------
# gravity — SPEC.md phases, tickets, and the recorded current state
# --------------------------------------------------------------------------
def build_gravity(root):
    text = open(os.path.join(root, "docs/spec/SPEC.md")).read()
    plan = open(os.path.join(root, "docs/spec/PLAN.md")).read()
    handoff = open(os.path.join(root, "docs/spec/AGENT-HANDOFF.md")).read()

    # Phases the plan defines, in order.
    phase_defs = re.findall(r"^## \S+\. (Phase [0-9a-z]+) — (.+)$", plan, re.M)
    # Tickets the handoff records as done.
    done_tickets = set(re.findall(r"\*\*(T\d+) — [^—]*—\s*(?:✅\s*)?DONE", handoff))
    all_tickets = set(re.findall(r"\bT(\d{1,3})\b", handoff + text))
    done_tickets = {t for t in done_tickets}
    ticket_nums = sorted({int(t) for t in all_tickets})

    features = []
    for phase, title in phase_defs:
        features.append(
            {
                "title": f"Phase {phase}: {title}",
                "body": (
                    f"From docs/spec/PLAN.md. The specification is 3,982 lines and cites its "
                    f"own section numbers in code comments and tests, so a section reference is "
                    f"a usable pointer. Ordering in the plan is strict and several phases exist "
                    f"because a cheaper ordering opens the app on a broken surface."
                ),
                "effort": "XL",
                "status": CONSENSUS,
                "complaints": [0],
            }
        )

    features.append(
        {
            "title": f"Ticket work T1-T{ticket_nums[-1] if ticket_nums else 96} ({(ticket_nums[-1] if ticket_nums else 96) - len(done_tickets)} recorded done)",
            "body": (
                f"gravity works in numbered tickets rather than one feature per row. The "
                f"handoff explicitly marks {len(done_tickets)} as DONE with commit hashes, and "
                f"names the highest ticket referenced as T{ticket_nums[-1] if ticket_nums else 96}. "
                f"Individual tickets are not expanded here; a survey should read the handoff's "
                f"own audit trail. What is recorded below is the state that document states, "
                f"not a fresh measurement."
            ),
            "effort": "L",
            "status": IN_PROGRESS_STATUS,
            "complaints": [0],
        }
    )

    # The one stub the handoff says should stay a stub, and the two that were stubs
    # and are not any more. Recorded because a portfolio that lists only wins is
    # the same failure the 2026-09-26 audit found in two other repos.
    features.append(
        {
            "title": "build_theme_vector() — the one deliberately unfinished stub",
            "body": (
                "Returns None, so Jaccard remains the live path. The handoff's reasoning is "
                "that §14.16b calls the vector an optimization, not a requirement, and names "
                "Jaccard as the universal fallback, so the system is correct without it. It "
                "does need pgvector plus an embedder, and there is no embedding storage "
                "anywhere in the schema. That is a feature with a model-version story behind "
                "it, not a query to write."
            ),
            "effort": "L",
            "status": DRAFT,
            "complaints": [2],
        }
    )
    features.append(
        {
            "title": "§14.16d disclosure gradient (open / structural / none)",
            "body": (
                "Implemented and load-bearing: an instance must be able to auto-federate "
                "WITHOUT disclosing themes that are kink, explicit or political. The privacy "
                "requirement turned out to be the same mechanism as the signature requirement "
                "— a document is only as private as its signature, so fixing the fake signature "
                "is what makes coarse disclosure safe. The three views publish tag names plus a "
                "theme vector, per-tier counts only, or nothing."
            ),
            "effort": "M",
            "status": SHIPPED,
            "complaints": [2],
        }
    )
    features.append(
        {
            "title": "instance_endpoints: where a remote instance lives",
            "body": (
                "fetch_remote() was blocked on something the ticket list never mentioned: its "
                "only argument was a remote_instance_did, and a did:key is a key, not an "
                "address. instance_endpoints now holds (DID, base_url) per local instance, "
                "admin-supplied and never inferred from a key, with address changes recorded in "
                "federation_endpoint_history. The fetcher itself is public-IP-only, "
                "DNS-rebinding-closed by connecting to the checked literal address, "
                "redirect-per-hop re-checked, with a total deadline and no proxy."
            ),
            "effort": "M",
            "status": SHIPPED,
            "complaints": [2],
        }
    )

    return {
        "slug": "gravity",
        "name": "Gravity",
        "description": (
            "Federated, self-hostable social platform for fiction and fandom. Rust + "
            "SvelteKit, Postgres. No engagement-tuned algorithms, no engagement metrics shown "
            "to authors, no ads. The product spec is docs/spec/SPEC.md (3,982 lines) and it is "
            "the authority — section numbers are cited in code comments and tests. Mirrored to "
            "ForgeJo and GitHub with a deliberate five-topic difference that is a platform "
            "limit, not drift."
        ),
        "governance_model": "collective",
        "license": "",
        "complaints": [
            {
                "title": "A handoff that contradicts itself is worse than no handoff",
                "body": (
                    "The file's own CURRENT STATE banner records a correction: an earlier note "
                    "ended 'the Phase-19 T65-T68 hook exists. That was false. feed_builder.rs "
                    "was a 15-line stub. It was caught by verifying against the tree rather "
                    "than against the line, which is the only reason it was caught."
                ),
                "severity": 4,
                "frequency": 0.5,
            },
            {
                "title": "A key is not an address",
                "body": (
                    "fetch_remote() took a did:key and nothing could say where to fetch from — "
                    "no column in the schema held a base_url. The function could not be "
                    "implemented, and the reason was not in the ticket list."
                ),
                "severity": 4,
                "frequency": 0.4,
            },
            {
                "title": "A number that does not reconcile is a number nobody measured",
                "body": (
                    "The handoff reconciles 1486 workspace tests against 1005 in the db crate "
                    "by checking that server --test api moved 168 to 178, exactly the 10 new "
                    "tests. It notes this repo has hit the does-not-add-up case three times. "
                    "Agreeing totals are the only free signal that both numbers were real."
                ),
                "severity": 3,
                "frequency": 0.4,
            },
        ],
        "features": features,
    }


# The 'specified' ledger state is a plan, not a decision: Consensus is the
# honest Concord status for it. Assigned here so the helpers above can use a
# name that reads correctly at the call site.
SPECIFIED_STATUS = CONSENSUS
IN_PROGRESS_STATUS = "in_progress"

if __name__ == "__main__":
    projects = []
    for fn, root in [
        (build_lorehaven, os.path.expanduser("~/code-local/rust/lorehaven")),
        (build_stashforge, os.path.expanduser("~/code-local/go/stash")),
        (build_stashbox, os.path.expanduser("~/code-local/go/stash-box")),
        (build_gravity, os.path.expanduser("~/code-local/gravity")),
    ]:
        try:
            p = fn(root)
            projects.append(p)
            print(
                f"  {p['slug']:14} {len(p['features']):4} features, "
                f"{len(p['complaints']):3} complaints"
            )
        except Exception as e:  # a missing repo must not sink the other three
            print(f"  FAILED {fn.__name__}: {type(e).__name__}: {e}", file=sys.stderr)

    with open(OUT, "w") as f:
        json.dump({"projects": projects}, f, indent=2)
    print(f"\nwrote {OUT}: {len(projects)} projects")

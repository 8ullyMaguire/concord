#!/usr/bin/env python3
"""Import the 100 ranked Concord feature ideas as votable features.

Each idea becomes a complaint (the pain it addresses), which is validated, and
then a feature linked to that complaint. Concord refuses a feature with no
validated complaint (store/features.go), so the three-step path is the only way
these can actually be ranked by the pairwise engine.

The impact/effort scores come straight from the source table and are stored, not
recomputed:

  effort       the t-shirt size mapped from the 1-10 effort score
  impact       the 1-10 impact score
  effort_score the 1-10 effort score
  impact_ratio GENERATED in the database as impact/effort_score

The t-shirt mapping is stated rather than derived, because a score of 1 and a
score of 3 are not the same size of work and collapsing them would lose the
distinction the list draws:
    1-2 -> S · 3-4 -> M · 5-7 -> L · 8-10 -> XL

Idempotent: complaints by title within the project, features by title. A second
run re-uses the existing rows rather than creating duplicates.

The 5 spec issues at the end of the source list are imported as complaints
only. They are defects in the specification, not work items, so turning them
into votable features would misrepresent them -- but they must be tracked
somewhere, and a validated complaint is exactly that.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from concord_import_portfolio import Client, as_items, TOKEN_FILE  # noqa: E402

BASE = "http://127.0.0.1:8006"
PROJECT = "concord"

# (n, tier, title, impact, effort_score)
# Transcribed from "Concord: 100 Feature Ideas, Ranked by Impact ÷ Effort".
IDEAS = [
    # Tier 1: nearly free, immediately valuable (ratio >= 4)
    (1, 1, "Hard-validated block form", 7, 1),
    (2, 1, "Governance-model filter and badge", 6, 1),
    (3, 1, "README badges", 6, 1),
    (4, 1, "RSS/Atom for everything", 6, 1),
    (5, 1, "Keyboard shortcuts for pairwise voting", 6, 1),
    (6, 1, "Soft WIP warnings", 6, 1),
    (7, 1, 'Deep-link "File a complaint" button', 6, 1),
    (8, 1, 'Skip: "I don\'t understand this."', 5, 1),
    (9, 1, "`Fixes complaint #N` auto-linking", 5, 1),
    (10, 1, '"Why this rank?" panel', 8, 2),
    (11, 1, 'One-click "I have this too."', 8, 2),
    (12, 1, "Strategic weight set by consensus", 8, 2),
    # Tier 2: cheap and high-leverage (ratio 2.5-3.5)
    (13, 2, "Quorum progress bar and countdown", 7, 2),
    (14, 2, "Complaint template with completeness meter", 7, 2),
    (15, 2, "Suggest-before-create tags", 7, 2),
    (16, 2, "Shareable saved-search URLs", 7, 2),
    (17, 2, "Auto-extend plus nudge", 7, 2),
    (18, 2, '"Needs your vote" inbox', 7, 2),
    (19, 2, "Duplicate detection at filing time", 9, 3),
    (20, 2, "Tag alias manager", 6, 2),
    (21, 2, "One-click user data export", 6, 2),
    (22, 2, "Webhooks for gate events", 6, 2),
    (23, 2, "Weekly digest", 6, 2),
    (24, 2, "Hidden running tally", 6, 2),
    (25, 2, "Searchable audit-log viewer", 6, 2),
    (26, 2, "Affiliation labels on request answers", 6, 2),
    (27, 2, "Objection highlighting in threads", 6, 2),
    (28, 2, "Public admin-action ledger", 6, 2),
    (29, 2, "Collaborative language-% correction", 6, 2),
    (30, 2, "User-reweightable health score", 6, 2),
    (31, 2, '"Is it fixed?" verification poll', 8, 3),
    (32, 2, '"Reframe as complaint" assistant', 8, 3),
    (33, 2, "Auto-generated decision records", 8, 3),
    (34, 2, "Stable, documented search API", 8, 3),
    (35, 2, "Always-on facet counts", 8, 3),
    (36, 2, "One-command self-host", 8, 3),
    (37, 2, "Uncertainty bars on ratings", 5, 2),
    (38, 2, "Public charter page", 5, 2),
    # Tier 3: core machinery (ratio 2.0-2.3)
    (39, 3, "Release notes generated from closed complaints", 7, 3),
    (40, 3, "`concord` CLI", 7, 3),
    (41, 3, "Merge-confirmation checklist", 7, 3),
    (42, 3, "Consensus what-if simulator", 7, 3),
    (43, 3, "Query language with AND/OR/NOT and grouping", 7, 3),
    (44, 3, "Reputation dashboard", 7, 3),
    (45, 3, "Granular notification preferences", 7, 3),
    (46, 3, "Pairwise ranking of list entries", 7, 3),
    (47, 3, "Lazy-consensus fast path", 7, 3),
    (48, 3, "Auto-generated roadmap view", 7, 3),
    (49, 3, '"Good first complaint" onboarding', 7, 3),
    (50, 3, "Merge quorum enforced via branch protection", 9, 4),
    (51, 3, "Phase-gate state machine", 9, 4),
    (52, 3, "Glicko-2 engine", 9, 4),
    (53, 3, "Active-learning pair selection", 8, 4),
    (54, 3, "GitHub/GitLab importer", 8, 4),
    (55, 3, "Request board", 8, 4),
    (56, 3, "List entry proposals via quorum", 8, 4),
    (57, 3, "Board swimlanes by tag, release, or priority", 8, 4),
    (58, 3, "Meilisearch full-text", 8, 4),
    (59, 3, "Awesome-list markdown importer", 8, 4),
    (60, 3, "Tiered decision sizes", 8, 4),
    (61, 3, "CI-gated merge", 8, 4),
    (62, 3, "Unanswered requests become complaint seeds", 6, 3),
    (63, 3, "Cycle-time, throughput, and blocked-time metrics", 6, 3),
    (64, 3, "Card blocker/dependency graph", 6, 3),
    (65, 3, "One-click fork from failed consensus", 6, 3),
    (66, 3, "Per-user vote audit view", 6, 3),
    (67, 3, "Reputation-scaled confirmations and rate limits", 6, 3),
    (68, 3, "Configurable pain half-life", 6, 3),
    (69, 3, "Link-rot checker for list entries", 6, 3),
    (70, 3, "License-compatibility filter", 6, 3),
    (71, 3, "Coverage map", 6, 3),
    (72, 3, "go-enry language detection on sync", 6, 3),
    (73, 3, "Saved-search subscriptions", 6, 3),
    (74, 3, "Cold-start rating seeding", 6, 3),
    (75, 3, "Accepted-answer badge on requests", 4, 2),
    # Tier 4: substantial but worthwhile (ratio 1.4-1.8)
    (76, 4, "Forgejo sync layer", 9, 5),
    (77, 4, "Charter templates", 7, 4),
    (78, 4, "Charter-amendment flow", 7, 4),
    (79, 4, "Admin-override auto-vote", 7, 4),
    (80, 4, "Cross-list entry search", 7, 4),
    (81, 4, "Structured request form", 7, 4),
    (82, 4, "Secret-scrubbing evidence uploader", 7, 4),
    (83, 4, "Lemmy-style sorts", 5, 3),
    (84, 4, "Controversy detector", 5, 3),
    (85, 4, "Threaded, labeled, votable comments", 8, 5),
    (86, 4, "Computed health score", 8, 5),
    (87, 4, "`concord report`", 8, 5),
    (88, 4, "Bus-factor calculation", 6, 4),
    (89, 4, "Vouch/invite system", 6, 4),
    (90, 4, "PWA push notifications", 6, 4),
    (91, 4, "Thread summarization", 6, 4),
    (92, 4, "Tag-scoped expertise multiplier", 7, 5),
    (93, 4, "Natural-language query translator", 7, 5),
    (94, 4, "Moderation queue decided by quorum", 7, 5),
    # Tier 5: big bets (ratio < 1.2)
    (95, 5, "Sybil and vote-ring anomaly detection", 8, 7),
    (96, 5, "Standalone Git hosting", 9, 10),
    (97, 5, "ActivityPub/ForgeFed federation", 8, 9),
    (98, 5, "Liquid delegation of votes", 6, 8),
    (99, 5, "Native mobile apps", 6, 8),
    (100, 5, "Portable cross-instance reputation", 5, 9),
]

TIER_NAMES = {
    1: "Tier 1: nearly free, immediately valuable (ratio >= 4)",
    2: "Tier 2: cheap and high-leverage (ratio 2.5-3.5)",
    3: "Tier 3: core machinery and solid wins (ratio 2.0-2.3)",
    4: "Tier 4: substantial but worthwhile (ratio 1.4-1.8)",
    5: "Tier 5: big bets (ratio < 1.2)",
}

# The five inconsistencies the list itself calls out. Imported as validated
# complaints only -- they are spec defects, not work items.
SPEC_ISSUES = [
    ("Strategic weight loophole (#12): §5.3 forbids hand-steering in collective "
     "mode but §6.2 lets maintainers set strategic weight", 7, 3),
    ("Tag taxonomy authority: §15 makes tag renames/merges/deletions maintainer "
     "actions while §8 and §2.10 say the taxonomy runs through quorum", 5, 2),
    ("Stand-aside penalised: the §6.3 formula consent/(consent+stand_aside+block) "
     "counts a stand-aside against consent, which inverts its meaning", 6, 2),
    ("Veto vs supermajority: 'maintainer veto' is an override path for blocks in "
     "§5.5/6.3 but the role definition limits it to security and legal", 5, 2),
    ("'Collaborator' is undefined: quorum and merge math use 'eligible "
     "collaborators' but §4 defines only Contributor, Reviewer and Maintainer", 7, 3),
]

SIZE_FOR_SCORE = [(2, "S"), (4, "M"), (7, "L"), (10, "XL")]


def tshirt(score):
    for cutoff, size in SIZE_FOR_SCORE:
        if score <= cutoff:
            return size
    return "XL"


def ratio(impact, effort):
    return round(impact / effort, 2)


def main():
    token = open(TOKEN_FILE).read().strip()
    c = Client(BASE, token)

    st, ps = c.get("/api/v1/projects")
    if st != 200:
        raise SystemExit("cannot list projects: HTTP %d" % st)
    pid = next((p["id"] for p in as_items(ps) if p.get("slug") == PROJECT), None)
    if pid is None:
        raise SystemExit("project %r not found" % PROJECT)
    print("project %s id=%d" % (PROJECT, pid))
    print("ideas to import: %d + %d spec issues" % (len(IDEAS), len(SPEC_ISSUES)))

    # Existing rows, so a re-run reuses rather than duplicates.
    st, cl = c.get("/api/v1/projects/%s/complaints" % PROJECT)
    have_c = {i.get("title", "").strip() for i in as_items(cl)} if st == 200 else set()
    st, fl = c.get("/api/v1/projects/%s/features" % PROJECT)
    have_f = {i.get("title", "").strip() for i in as_items(fl)} if st == 200 else set()
    print("existing: %d complaints, %d features" % (len(have_c), len(have_f)))

    # complaint title -> id, filled as they are created.
    complaint_ids = {}
    new_c = 0
    for n, tier, title, impact, effort in IDEAS:
        ctitle = "%02d. %s" % (n, title)
        if ctitle in have_c:
            continue
        code, resp = c.post("/api/v1/projects/%s/complaints" % PROJECT, {
            "project_id": pid, "title": ctitle,
            "body": ("Pain addressed by idea #%d from the ranked 100. %s. "
                     "Scored impact %d/10, effort %d/10, ratio %.2f."
                     % (n, TIER_NAMES[tier], impact, effort, ratio(impact, effort))),
            "severity": max(1, min(5, impact // 2)),
            "frequency": 1.0, "strategic_multiplier": 1.0,
        })
        if code == 201:
            new_c += 1
            have_c.add(ctitle)
        elif code in (400, 409):
            pass
        else:
            print("  complaint FAILED %s -> %d %s" % (ctitle, code, str(resp)[:90]))
    for title, impact, effort in SPEC_ISSUES:
        if title in have_c:
            continue
        code, resp = c.post("/api/v1/projects/%s/complaints" % PROJECT, {
            "project_id": pid, "title": title[:200],
            "body": ("Specification inconsistency, recorded from the ranked 100. "
                     "Severity impact %d/10, effort to resolve %d/10."
                     % (impact, effort)),
            "severity": max(1, min(5, impact // 2)),
            "frequency": 1.0, "strategic_multiplier": 1.0,
        })
        if code == 201:
            new_c += 1
            have_c.add(title)
        elif code in (400, 409):
            pass
        else:
            print("  issue FAILED -> %d %s" % (code, str(resp)[:90]))
    print("complaints created: %d" % new_c)

    # Validate everything on this project so features have something to link to.
    st, cl = c.get("/api/v1/projects/%s/complaints" % PROJECT)
    items = as_items(cl) if st == 200 else []
    by_title = {}
    validated = 0
    for it in items:
        by_title[it.get("title", "").strip()] = it["id"]
        if it.get("status") == "validated":
            validated += 1
            continue
        code, _ = c.post("/api/v1/projects/%s/complaints/%d/validate" % (PROJECT, it["id"]), {})
        if code == 200:
            validated += 1
    print("validated: %d of %d" % (validated, len(items)))

    # Features.
    new_f = 0
    missing = 0
    for n, tier, title, impact, effort in IDEAS:
        ftitle = "%02d. %s" % (n, title)
        if ftitle in have_f:
            continue
        cid = by_title.get(ftitle)
        if cid is None:
            missing += 1
            continue
        code, resp = c.post("/api/v1/projects/%s/features" % PROJECT, {
            "project_id": pid, "title": ftitle,
            "body": ("Idea #%d of the ranked 100. %s.\n\n"
                     "Impact %d/10, effort %d/10 (t-shirt %s), impact/effort ratio %.2f.\n"
                     "Source: Impact ÷ Effort list, 2026-10-02."
                     % (n, TIER_NAMES[tier], impact, effort, tshirt(effort),
                        ratio(impact, effort))),
            "effort": tshirt(effort),
            "impact": impact, "effort_score": effort,
            "linked_complaints": [cid],
        })
        if code in (200, 201):
            new_f += 1
            have_f.add(ftitle)
        else:
            print("  feature FAILED %s -> %d %s" % (ftitle, code, str(resp)[:100]))
    print("features created: %d (missing complaint link: %d)" % (new_f, missing))

    print("\napi calls: %d (%d throttled)" % (c.calls, c.throttled))


if __name__ == "__main__":
    main()
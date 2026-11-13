#!/usr/bin/env python3
"""Row 11d part 50 (B2c): §6.3's coverage claims and §6.4's contests.

The claim is worth nothing unless two things hold, and both are refusals:

1. **the complaint must be linked to the SOLUTION'S OWN feature** — otherwise coverage is measured against a denominator the
   solution chose itself: file a claim against a trivial complaint and the percentage rises;
2. **a contest needs a reason and the claimant cannot be the contestor** — a contest with no reason is indistinguishable
   from a deletion, and §5.2's self-vote rule applies to claims for the same reason it applies to rankings.

And one rule that is a state rather than a refusal: **a contested claim stops counting until it is restated**, and restating
clears the contest — which is what makes a contest answerable rather than permanent.

The score is served with its parts (`resolved_pain`, `total_pain`, `claimed_pain`, `contested`) because "resolves 340 of 510
pain" is what a reader can act on, and a bare percentage hides both how much is at stake and whether the denominator moved.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(ROOT, "tests", "e2e"))

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8108")


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {}
    if data is not None:
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            raw = response.read()
            return response.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw) if raw else None
        except ValueError:
            return error.code, raw.decode("utf-8", "replace")


def register(tag):
    status, body = call(
        "POST", "/api/v1/auth/register",
        {"username": tag, "display_name": tag.title(), "password": "correct horse"},
    )
    if status != 201:
        raise SystemExit(f"register {tag} failed: {status} {body}")
    return body["user"]["id"], body["token"]


def cleanup(con, like):
    projects = [row[0] for row in con.execute("select id from projects where slug like ?", (like,)).fetchall()]
    if not projects:
        return
    marks = ",".join("?" * len(projects))
    params = tuple(projects)
    features = [row[0] for row in con.execute(
        f"select id from features where project_id in ({marks})", params).fetchall()]
    if features:
        fmarks = ",".join("?" * len(features))
        fparams = tuple(features)
        solutions = [row[0] for row in con.execute(
            f"select id from solutions where feature_id in ({fmarks})", fparams).fetchall()]
        if solutions:
            smarks = ",".join("?" * len(solutions))
            sparams = tuple(solutions)
            con.execute(f"delete from solution_coverage where solution_id in ({smarks})", sparams)
            con.execute(f"delete from solutions where id in ({smarks})", sparams)
        arenas = [row[0] for row in con.execute(
            f"select id from arenas where feature_id in ({fmarks})", fparams).fetchall()]
        if arenas:
            amarks = ",".join("?" * len(arenas))
            aparams = tuple(arenas)
            con.execute(f"delete from arena_entries where arena_id in ({amarks})", aparams)
            con.execute(f"delete from pairwise_votes where arena_id in ({amarks})", aparams)
            con.execute(f"delete from arenas where id in ({amarks})", aparams)
        con.execute(f"delete from feature_complaints where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from features where id in ({fmarks})", fparams)
    con.execute(f"delete from complaints where project_id in ({marks})", params)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    owner_id, _owner_token = register(f"cowner{stamp}")
    author_id, _author_token = register(f"cauthor{stamp}")
    other_id, other_token = register(f"cother{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-coverage-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Coverage probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, owner_id, "The feature", "", "ready", now, now),
    ).lastrowid
    stranger_feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, owner_id, "Another feature", "", "ready", now, now),
    ).lastrowid

    def complaint(title, severity):
        # `public_id` has no default in this schema, so it is supplied.
        return con.execute(
            "INSERT INTO complaints (project_id, author_id, title, body, public_id, severity, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?,?) RETURNING id",
            (project, owner_id, title, "", f"c{stamp}-{title[:4]}", severity, now, now),
        ).lastrowid

    loud = complaint("Slow", 5)
    quiet = complaint("Ugly", 1)
    unlinked = complaint("Elsewhere", 3)
    for complaint_id in (loud, quiet):
        con.execute("INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)",
                    (feature, complaint_id))
    con.execute("INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)",
                (stranger_feature, unlinked))
    solution = con.execute(
        "INSERT INTO solutions (feature_id, author_id, title, body, type, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (feature, author_id, "A solution", "", "build-new", "draft", now, now),
    ).lastrowid
    elsewhere = con.execute(
        "INSERT INTO solutions (feature_id, author_id, title, body, type, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (stranger_feature, author_id, "Their solution", "", "build-new", "draft", now, now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    base = f"/api/v1/projects/{slug}/features/{feature}/solutions/{solution}/coverage"

    # ── refusals ─────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", base, {"complaint_id": loud, "claim": "resolves"})
    check("claiming anonymously is 401", status, 401)
    status, _ = call("POST", base, {"complaint_id": loud, "claim": "covers"}, token=other_token)
    check("an unknown claim is 400", status, 400)
    status, _ = call("POST", base, {"complaint_id": unlinked, "claim": "resolves"}, token=other_token)
    check("a complaint the feature does not link is 400", status, 400)
    status, _ = call(
        "POST",
        f"/api/v1/projects/{slug}/features/{feature}/solutions/{elsewhere}/coverage",
        {"complaint_id": unlinked, "claim": "resolves"}, other_token)
    check("a solution from another feature is 404", status, 404)

    # ── the claim, and what it is made of ───────────────────────────────────────────────────────────────────────
    status, claimed = call("POST", base, {"complaint_id": loud, "claim": "resolves"}, token=other_token)
    check("a claim is accepted", status, 200)
    score = (claimed or {}).get("coverage") or {}
    check("the response carries the score's parts",
          sorted(score.keys()), ["claimed_pain", "contested", "coverage", "resolved_pain", "total_pain"])
    check("a single resolved complaint is a fraction of the feature's pain",
          0.0 < score["coverage"] < 1.0, True)
    check("the ratio is the one its own parts give",
          round(score["coverage"], 9), round(score["resolved_pain"] / score["total_pain"], 9))
    check("the loud complaint is worth more pain than the quiet one",
          score["resolved_pain"] > score["total_pain"] - score["resolved_pain"], True)

    status, both = call("POST", base, {"complaint_id": quiet, "claim": "leaves-unresolved"},
                        token=other_token)
    score2 = (both or {}).get("coverage") or {}
    check("a second claim is counted in claimed pain",
          score2["claimed_pain"] > score["claimed_pain"], True)
    check("and a leave-unresolved claim does not raise coverage",
          abs(score2["coverage"] - score["coverage"]) < 1e-6, True)

    # ── the contest ─────────────────────────────────────────────────────────────────────────────────────────────
    contest = f"{base}/{loud}/contest"
    status, _ = call("POST", contest, {}, token=other_token)
    check("a contest with no reason is 400", status, 400)
    status, _ = call("POST", contest, {"reason": "it does not"}, token=_author_token)
    check("an author cannot contest their own claim", status, 403)
    status, contested = call("POST", contest, {"reason": "it does not fix the slowness"}, token=other_token)
    check("someone else may contest it", status, 200)
    after = (contested or {}).get("coverage") or {}
    check("a contested claim stops counting", after["coverage"], 0.0)
    check("and the response says one is contested", after["contested"], 1)
    # Pain DECAYS against the charter's half-life, so an exact equality here would be asserting that no time passed between
    # two HTTP calls. The claim is that the denominator did not move by a claim's worth of pain — one part in a million.
    check("while the pain is still in the denominator",
          abs(after["total_pain"] - score["total_pain"]) < 1e-6 * max(1.0, score["total_pain"]), True)
    status, _ = call("POST", f"{base}/{quiet}/contest", {"reason": "no claim here"}, token=other_token)
    check("contesting a claim that exists works", status, 200)
    status, _ = call("POST", f"{base}/999999999/contest", {"reason": "nowhere"}, token=other_token)
    check("contesting a claim that does not exist is 404", status, 404)

    # ── restating clears the contest ────────────────────────────────────────────────────────────────────────────
    status, restated = call("POST", base, {"complaint_id": loud, "claim": "resolves"}, token=other_token)
    check("restating the claim is how a contest is answered", status, 200)
    back = (restated or {}).get("coverage") or {}
    check("the claim counts again", abs(back["coverage"] - score["coverage"]) < 1e-6, True)
    check("and only the other contest remains", back["contested"], 1)

    cleanup(con, f"{slug}%")
    con.commit()
    for user in (owner_id, author_id, other_id):
        con.execute("DELETE FROM api_tokens WHERE user_id = ?", (user,))
        con.execute("DELETE FROM users WHERE id = ?", (user,))
    con.commit()
    con.close()

    failures = sum(1 for _, got, want in results if got != want)
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

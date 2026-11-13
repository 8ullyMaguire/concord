#!/usr/bin/env python3
"""Row 11b part 28: the arena vote log — the appeals view.

Rules: a SKIP is not a vote (it means "I don't understand this", not "neither"), newest first, never null, and a
project's arena id is global so another project's arena answers exactly what a missing one answers.

The fixture also PINS A DEFECT: the vote's two sides carry `r`, `rd`, `games` and `conservative` all zero, because the
Go selects only `v.a`/`v.b` and never joins the entries. An appeals view that cannot show what a vote changed is a poor
appeals view; the port reproduces it so the two services agree, and it is listed for row 13.
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
        with urllib.request.urlopen(request, timeout=15) as response:
            raw = response.read()
            return response.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw) if raw else None
        except ValueError:
            return error.code, raw.decode("utf-8", "replace")


def cleanup(con, like):
    for (project,) in con.execute("select id from projects where slug like ?", (like,)).fetchall():
        con.execute("delete from pairwise_votes where project_id = ?", (project,))
        con.execute("delete from arena_entries where arena_id in"
                    " (select id from arenas where project_id = ?)", (project,))
        con.execute("delete from arenas where project_id = ?", (project,))
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-arena-votes-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"avat{stamp}", "display_name": "Votes", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    voter = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-arena-votes-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Votes probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Other", "", "collective", "public", "both", now, now),
    ).lastrowid

    def feature(project_id, title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?) RETURNING id",
            (project_id, voter, title, "proposed", now, now),
        ).lastrowid

    alpha = feature(project, "Alpha")
    beta = feature(project, "Beta")
    arena = con.execute(
        "INSERT INTO arenas (type, project_id, question, created_at) VALUES (?,?,?,?) RETURNING id",
        ("feature", project, "Which is better?", now),
    ).lastrowid
    foreign = con.execute(
        "INSERT INTO arenas (type, project_id, question, created_at) VALUES (?,?,?,?) RETURNING id",
        ("feature", other, "Foreign?", now),
    ).lastrowid
    empty_arena = con.execute(
        "INSERT INTO arenas (type, project_id, question, created_at) VALUES (?,?,?,?) RETURNING id",
        ("feature", project, "Nothing yet?", now),
    ).lastrowid

    def vote(arena_id, outcome, reason, at):
        con.execute(
            "INSERT INTO pairwise_votes (arena_id, project_id, feature_a, feature_b, entity_type, a, b,"
            " voter_id, outcome, weight, reason, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
            (arena_id, project, alpha, beta, "feature", alpha, beta, voter, outcome, 1.0, reason, at),
        )

    vote(arena, "a", "clearer", now - 30)
    vote(arena, "skip", "do not understand", now - 20)
    vote(arena, "tie", "", now - 10)
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/votes")
    if status != 200:
        print(f"first call: {status} {body}")
    check("the log answers", status, 200)
    body = body or {}
    check("a skip is not a vote", body.get("count"), 2)
    check("and count matches the rows", body.get("count"), len(body.get("votes") or []))
    check("newest first", [v["outcome"] for v in body.get("votes") or []], ["tie", "a"])
    first = (body.get("votes") or [{}])[1]
    check("the reason travels with its vote", first.get("reason"), "clearer")
    check("the a side is labelled with the feature's title", (first.get("a") or {}).get("label"), "Alpha")
    check("the b side too", (first.get("b") or {}).get("label"), "Beta")
    check("the entity type is on both sides",
          [(first["a"]["entity_type"], first["b"]["entity_type"])], [("feature", "feature")])
    # The pinned defect: the log cannot show what the vote changed.
    check("the sides carry no ratings (the Go never joins the entries)",
          [(first["a"]["r"], first["a"]["rd"], first["a"]["games"])], [(0.0, 0.0, 0)])
    check("a voter is named", first.get("voter_id"), voter)

    status, body = call("GET", f"/api/v1/projects/{project}/arenas/{empty_arena}/votes")
    check("an arena with no votes answers 200", status, 200)
    check("with an empty array and not null", (body or {}).get("votes"), [])
    check("and a count of zero", (body or {}).get("count"), 0)

    status, _ = call("GET", f"/api/v1/projects/{project}/arenas/{foreign}/votes")
    check("another project's arena is 404", status, 404)
    status, _ = call("GET", f"/api/v1/projects/{project}/arenas/99999999/votes")
    check("a missing arena is 404", status, 404)
    status, _ = call("GET", f"/api/v1/projects/{project}/arenas/not-a-number/votes")
    check("a non-numeric arena id is 400", status, 400)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/votes")
    check("a private project's vote log is 404 without a token", status, 404)

    cleanup(con, "probe-arena-votes-%")
    con.execute("DELETE FROM api_tokens WHERE user_id = ?", (voter,))
    con.execute("DELETE FROM users WHERE id = ?", (voter,))
    con.commit()
    con.close()

    failures = 0
    for name, got, want in results:
        ok = got == want
        if not ok:
            failures += 1
        print(f"{'ok  ' if ok else 'FAIL'} {name}: got {got!r} want {want!r}")
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

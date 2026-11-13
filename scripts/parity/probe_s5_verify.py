#!/usr/bin/env python3
"""Row 11b part 30: arena verification — anyone can check the ranking, and the check must be exact.

§5.1: "ratings are recomputed deterministically from the append-only vote log. Anyone can verify a ranking." The route
writes nothing, so any reader may run it, and it reports EVERY entry rather than the leaderboard's first page.

Assertions:
1. an untouched arena agrees, and its replayed ratings are the priors (1500 / 350 / 0 games);
2. after a vote the stored ratings no longer match the log: `agrees` false, `divergent` = both entries;
3. writing the REPLAYED values back makes it agree again — the replay is self-consistent, and the comparison is exact
   float equality rather than a tolerance;
4. a skip does not enter the replay at all;
5. in an arena WITH a baseline, `neither` is TWO losses to the baseline: the baseline gains two games and rises, each
   competitor gains one game and falls;
6. it needs no account, and a private project's arena is 404.
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
    slug = f"probe-verify-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"vfauth{stamp}", "display_name": "Verify", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    voter = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-verify-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Verify probe", "", "collective", "public", "both", now, now),
    ).lastrowid

    def feature(title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?) RETURNING id",
            (project, voter, title, "proposed", now, now),
        ).lastrowid

    alpha, beta = feature("Alpha"), feature("Beta")
    base = feature("Do nothing")
    arena = con.execute(
        "INSERT INTO arenas (type, project_id, question, use_case, created_at) VALUES (?,?,?,?,?) RETURNING id",
        ("feature", project, "Which is better?", "triage", now),
    ).lastrowid
    baseline_arena = con.execute(
        "INSERT INTO arenas (type, project_id, question, use_case, created_at) VALUES (?,?,?,?,?) RETURNING id",
        ("feature", project, "Beat doing nothing?", "triage", now),
    ).lastrowid

    def entry(arena_id, feature_id, baseline=0):
        con.execute(
            "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games, is_baseline,"
            " updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
            (arena_id, "feature", feature_id, 1500.0, 350.0, 0.06, 0, baseline, now),
        )

    entry(arena, alpha)
    entry(arena, beta)
    entry(baseline_arena, alpha)
    entry(baseline_arena, beta)
    entry(baseline_arena, base, baseline=1)
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/verification")
    check("the check answers", status, 200)
    body = body or {}
    check("an untouched arena agrees", body.get("agrees"), True)
    check("with no divergent entries", body.get("divergent"), 0)
    check("and it reports every entry", len(body.get("entries") or []), 2)
    first = (body.get("entries") or [{}])[0]
    check("the replayed prior is the rating", (first.get("replayed_r"), first.get("replayed_rd")), (1500.0, 350.0))
    check("and the prior game count", first.get("replayed_games"), 0)

    def vote(arena_id, a, b, outcome, weight=1.0, at=None):
        con.execute(
            "INSERT INTO pairwise_votes (arena_id, project_id, feature_a, feature_b, entity_type, a, b,"
            " voter_id, outcome, weight, reason, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
            (arena_id, project, a, b, "feature", a, b, voter, outcome, weight, "", at or now),
        )

    vote(arena, alpha, beta, "a")
    con.commit()
    status, body = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/verification")
    body = body or {}
    check("a vote the arena has not applied makes it disagree", body.get("agrees"), False)
    check("both entries are divergent", body.get("divergent"), 2)
    entries = {entry["entity_id"]: entry for entry in body.get("entries") or []}
    check("the winner's replay is above the prior", entries[alpha]["replayed_r"] > 1500.0, True)
    check("the loser's replay is below it", entries[beta]["replayed_r"] < 1500.0, True)
    check("each played one game", (entries[alpha]["replayed_games"], entries[beta]["replayed_games"]), (1, 1))

    # Write the replay back: the check must then agree. This is the self-consistency proof, and it is exact equality.
    for entity_id, row in entries.items():
        con.execute(
            "UPDATE arena_entries SET r = ?, rd = ?, games = ? WHERE arena_id = ? AND entity_id = ?",
            (row["replayed_r"], row["replayed_rd"], row["replayed_games"], arena, entity_id),
        )
    con.commit()
    status, body = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/verification")
    check("writing the replay back makes it agree", (body or {}).get("agrees"), True)

    vote(arena, alpha, beta, "skip")
    con.commit()
    status, body = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/verification")
    check("a skip does not enter the replay", (body or {}).get("agrees"), True)

    # A baseline arena: `neither` is two losses to the baseline.
    vote(baseline_arena, alpha, beta, "neither")
    con.commit()
    status, body = call("GET", f"/api/v1/projects/{project}/arenas/{baseline_arena}/verification")
    rows = {entry["entity_id"]: entry for entry in (body or {}).get("entries") or []}
    check("the baseline gains TWO games from one neither", rows[base]["replayed_games"], 2)
    check("and rises above the prior", rows[base]["replayed_r"] > 1500.0, True)
    check("each competitor played one game", (rows[alpha]["replayed_games"], rows[beta]["replayed_games"]), (1, 1))
    check("and both fall", (rows[alpha]["replayed_r"] < 1500.0, rows[beta]["replayed_r"] < 1500.0), (True, True))
    check("the baseline is flagged as such", rows[base]["is_baseline"], True)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/verification")
    check("a private project's arena is 404", status, 404)
    status, _ = call("GET", f"/api/v1/projects/{project}/arenas/{baseline_arena}/verification")
    check("(still 404 for the other arena too)", status, 404)

    cleanup(con, "probe-verify-%")
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

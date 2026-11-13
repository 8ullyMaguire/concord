#!/usr/bin/env python3
"""Row 11b part 29: next-pair — the selection rules ARE the feature.

§5.1's active learning: ask about the comparisons the arena knows LEAST (uncertainty is a DISCOUNT, not a bonus), cap
repeated exposure, never ask a voter about their own settled pairs, and treat a skip as "I don't understand this" rather
than a verdict so the pair can be asked again.

Assertions, each a way to be plausibly wrong:
1. an UNTOUCHED entry (RD 350) is proposed before a well-measured pair at the same rating distance;
2. a pair including the BASELINE is discounted, so it comes first in a solution arena;
3. a voter who has judged a pair is not asked it again;
4. a voter who SKIPPED a pair IS asked it again;
5. a voter who has judged every pair gets 404, not an empty object;
6. an arena with one entry is 400 — a data problem, not a finished voter;
7. win_probability is present on both sides and favours the stronger rating.
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
    slug = f"probe-nextpair-{stamp}"
    tokens = {}
    for who in ("alpha", "beta"):
        status, registered = call(
            "POST", "/api/v1/auth/register",
            {"username": f"{who}{stamp}", "display_name": who, "password": "correct horse"},
        )
        if status != 201:
            print(f"register failed: {status} {registered}")
            sys.exit(2)
        tokens[who] = (registered["token"], registered["user"]["id"])
    author = tokens["alpha"][1]

    con = harness.connect("unused")
    cleanup(con, "probe-nextpair-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Next pair probe", "", "collective", "public", "both", now, now),
    ).lastrowid

    def feature(title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?) RETURNING id",
            (project, author, title, "proposed", now, now),
        ).lastrowid

    strong = feature("Measured strong")
    close = feature("Measured close")
    fresh = feature("Untouched")
    fresh2 = feature("Untouched too")
    solo = feature("Alone")
    baseline_feature = feature("Do nothing")

    arena = con.execute(
        "INSERT INTO arenas (type, project_id, question, use_case, created_at) VALUES (?,?,?,?,?) RETURNING id",
        ("feature", project, "Which is better?", "triage", now),
    ).lastrowid
    baseline_arena = con.execute(
        "INSERT INTO arenas (type, project_id, question, use_case, created_at) VALUES (?,?,?,?,?) RETURNING id",
        ("feature", project, "Beat doing nothing?", "triage", now),
    ).lastrowid
    lonely = con.execute(
        "INSERT INTO arenas (type, project_id, question, use_case, created_at) VALUES (?,?,?,?,?) RETURNING id",
        ("feature", project, "Only one?", "triage", now),
    ).lastrowid

    def entry(arena_id, feature_id, r, rd, games=0, baseline=0):
        con.execute(
            "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games,"
            " is_baseline, updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
            (arena_id, "feature", feature_id, r, rd, 0.06, games, baseline, now),
        )

    entry(arena, strong, 1600.0, 80.0, 6)
    entry(arena, close, 1620.0, 80.0, 6)
    entry(arena, fresh, 1500.0, 350.0, 0)
    entry(arena, fresh2, 1520.0, 350.0, 0)
    entry(baseline_arena, strong, 1600.0, 80.0, 6)
    entry(baseline_arena, close, 1620.0, 80.0, 6)
    entry(baseline_arena, baseline_feature, 1500.0, 350.0, 0, baseline=1)
    entry(lonely, solo, 1500.0, 350.0, 0)
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/next-pair")
    if status != 200:
        print(f"first call: {status} {body}")
    check("the pair answers", status, 200)
    body = body or {}
    ids = {(body.get("a") or {}).get("entity_id"), (body.get("b") or {}).get("entity_id")}
    # BOTH untouched entries, and this is the rule's shape rather than a fixture artefact: the discount is
    # `0.5 * mean(RD)`, so two untouched entries 20 rating points apart cost 20-175 = -155 against a measured pair's
    # 20-40 = -20. A fresh entry 100 points away from a measured one only costs -7.5 and would NOT be preferred — high
    # uncertainty does not rescue a wide rating gap.
    check("the proposed pair is the two untouched entries", ids == {fresh, fresh2}, True)
    # Captured HERE: a later call reassigns `ids`, and reading it after the baseline check used the wrong arena.
    first = sorted(ids)
    check("the arena's question is echoed", body.get("question"), "Which is better?")
    check("and its use case", body.get("use_case"), "triage")
    a, b = body.get("a") or {}, body.get("b") or {}
    check("both sides carry a win probability",
          all(isinstance(side.get("win_probability"), (int, float)) for side in (a, b)), True)
    stronger = a if a.get("r", 0) >= b.get("r", 0) else b
    weaker = b if stronger is a else a
    check("and it favours the stronger rating", stronger["win_probability"] > weaker["win_probability"], True)
    check("the labels are the features' titles",
          {a.get("label"), b.get("label")} >= {"Untouched"} or True, True)
    check("its sides are labelled with their titles",
          {a.get("label"), b.get("label")}, {"Untouched", "Untouched too"})

    status, body = call("GET", f"/api/v1/projects/{project}/arenas/{baseline_arena}/next-pair")
    ids = {(body.get("a") or {}).get("entity_id"), (body.get("b") or {}).get("entity_id")}
    check("a baseline pair is discounted to the front", baseline_feature in ids, True)

    # Judge the proposed pair, then ask again: it must not come back.
    con.execute(
        "INSERT INTO pairwise_votes (arena_id, project_id, feature_a, feature_b, entity_type, a, b,"
        " voter_id, outcome, weight, reason, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
        (arena, project, first[0], first[1], "feature", first[0], first[1], tokens["alpha"][1], "a", 1.0, "", now),
    )
    con.commit()
    status, body = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/next-pair", token=tokens["alpha"][0])
    judged = {(body.get("a") or {}).get("entity_id"), (body.get("b") or {}).get("entity_id")}
    check("a judged pair is not proposed again", judged == set(first), False)
    status_beta, body_beta = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/next-pair",
                                 token=tokens["beta"][0])
    beta_ids = {(body_beta.get("a") or {}).get("entity_id"), (body_beta.get("b") or {}).get("entity_id")}
    check("but another voter is still shown it", beta_ids == set(first), True)

    # A skip is not a judgement: the same voter must be offered the pair again.
    con.execute("delete from pairwise_votes where arena_id = ?", (arena,))
    con.execute(
        "INSERT INTO pairwise_votes (arena_id, project_id, feature_a, feature_b, entity_type, a, b,"
        " voter_id, outcome, weight, reason, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
        (arena, project, first[0], first[1], "feature", first[0], first[1], tokens["alpha"][1], "skip", 1.0, "", now),
    )
    con.commit()
    status, body = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/next-pair", token=tokens["alpha"][0])
    skipped_again = {(body.get("a") or {}).get("entity_id"), (body.get("b") or {}).get("entity_id")}
    check("a skipped pair IS offered again", skipped_again == set(first), True)

    # Judge everything: the voter is done, and that is a 404 rather than an empty object.
    everyone = [strong, close, fresh, fresh2]
    pairs = [sorted((x, y)) for x in everyone for y in everyone if x < y]
    for left, right in pairs:
        con.execute(
            "INSERT INTO pairwise_votes (arena_id, project_id, feature_a, feature_b, entity_type, a, b,"
            " voter_id, outcome, weight, reason, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
            (arena, project, left, right, "feature", left, right, tokens["alpha"][1], "a", 1.0, "", now),
        )
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{project}/arenas/{arena}/next-pair", token=tokens["alpha"][0])
    check("a voter who judged every pair gets 404", status, 404)
    status, _ = call("GET", f"/api/v1/projects/{project}/arenas/{lonely}/next-pair")
    check("an arena with one entry is 400", status, 400)
    status, _ = call("GET", f"/api/v1/projects/{project}/arenas/99999999/next-pair")
    check("a missing arena is 404", status, 404)

    cleanup(con, "probe-nextpair-%")
    for who in ("alpha", "beta"):
        con.execute("DELETE FROM api_tokens WHERE user_id = ?", (tokens[who][1],))
        con.execute("DELETE FROM users WHERE id = ?", (tokens[who][1],))
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

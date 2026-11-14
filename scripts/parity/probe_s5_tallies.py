#!/usr/bin/env python3
"""Row 11b part 24: feature tallies — including the three defects the port REPRODUCES.

1. the wire key for the title is `Title` (the Go struct has no tag on that field), not `title`;
2. `votes_cast = wins + losses`, so SKIPS are counted but never cast — eight votes can report `votes_cast: 4`;
3. wins and losses do not partition the matches: the count is computed from the pair's `outcome` without asking which
   SIDE this feature was on, so a feature that lost a match still gains a `wins` when the pair's outcome was `a`.
4. a project with no features answers `null`, not `[]` — the Go's nil slice.

Every one of those is reproduced deliberately: a port whose gate is "the same answers on the same data" cannot fix a
defect and still be verifiable. The defects are recorded in the ledger for the cutover row.
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

# ── consent ──────────────────────────────────────────────────────────────────────────────────────────────────────
# Every mutation on this instance must carry the accepted cookie notice: the server refuses with 403 `consent_required`
# otherwise, and that is the reference's behaviour, not a detail of this probe. Rather than thread the header through every call
# below, the probe carries it the way a browser does.
_ORIGINAL_REQUEST = urllib.request.Request


def _consented_request(*args, **kwargs):
    if len(args) > 2:
        headers = dict(args[2] or {})
        args = args[:2]
        kwargs["headers"] = headers
    else:
        kwargs["headers"] = dict(kwargs.pop("headers", None) or {})
    kwargs["headers"].setdefault(
        "Cookie", os.environ.get("CONCORD_CONSENT_COOKIE", "concord_consent=1"))
    return _ORIGINAL_REQUEST(*args, **kwargs)


urllib.request.Request = _consented_request

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8108")


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
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
        con.execute("delete from arenas where project_id = ?", (project,))
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-tally-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"tallyauth{stamp}", "display_name": "Tally", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-tally-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Tally probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    empty = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug + "-empty", "Empty", "", "collective", "public", "both", now, now),
    ).lastrowid

    def feature(title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?) RETURNING id",
            (project, author, title, "proposed", now, now),
        ).lastrowid

    # `pairwise_votes.arena_id` is NOT NULL, so every vote belongs to an arena — the fixture needs one even though
    # the tallies read does not consult it.
    arena = con.execute(
        "INSERT INTO arenas (type, project_id, question, created_at) VALUES (?,?,?,?) RETURNING id",
        ("feature", project, "Which is better?", now),
    ).lastrowid
    con.commit()

    a = feature("Alpha")
    b = feature("Beta")
    c = feature("Gamma")

    def vote(first, second, outcome):
        con.execute(
            "INSERT INTO pairwise_votes (arena_id, project_id, feature_a, feature_b, entity_type, a, b,"
            " voter_id, outcome, weight, reason, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
            (arena, project, first, second, "feature", first, second, author, outcome, 1.0, "", now),
        )

    # a vs b: 'a'   -> counts as a WIN for BOTH rows (the defect)
    # b vs c: 'b'   -> counts as a LOSS for both
    # a vs c: 'skip'-> counts as a SKIP for both
    vote(a, b, "a")
    vote(b, c, "b")
    vote(a, c, "skip")
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/tallies")
    check("tallies answer", status, 200)
    rows = {row.get("feature_id"): row for row in (body or [])}
    check("every feature is listed, by id", sorted(rows), [a, b, c])

    check("the title key is Title, capitalised",
          ("Title" in (rows.get(a) or {}), "title" in (rows.get(a) or {})), (True, False))

    # a: matches (a,b)='a' and (a,c)='skip' -> wins 1, losses 0, skips 1
    check("a's counts", ((rows[a]["wins"], rows[a]["losses"], rows[a]["skips"])), (1, 0, 1))
    # b: matches (a,b)='a' and (b,c)='b' -> wins 1 (it LOST that match), losses 1
    check("b gains a win from a match it lost — the reproduced defect",
          ((rows[b]["wins"], rows[b]["losses"], rows[b]["skips"])), (1, 1, 0))
    # c: matches (b,c)='b' and (a,c)='skip' -> wins 0, losses 1, skips 1
    check("c's counts", ((rows[c]["wins"], rows[c]["losses"], rows[c]["skips"])), (0, 1, 1))

    check("skips are NOT cast: votes_cast is wins+losses",
          ((rows[a]["votes_cast"], rows[a]["wins"] + rows[a]["losses"] + rows[a]["skips"])), (1, 2))
    check("and for the feature with a skip and a loss",
          (rows[c]["votes_cast"], rows[c]["skips"]), (1, 1))

    status, body = call("GET", f"/api/v1/projects/{empty}/tallies")
    check("a project with no features answers 200", status, 200)
    check("with NULL, not an empty array, as the Go's nil slice did", body, None)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{project}/tallies")
    check("a private project's tallies are 404 without a token", status, 404)

    cleanup(con, "probe-tally-%")
    con.execute("DELETE FROM api_tokens WHERE user_id = ?", (author,))
    con.execute("DELETE FROM users WHERE id = ?", (author,))
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

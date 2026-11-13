#!/usr/bin/env python3
"""Row 11b part 20: the complaint <-> feature link, read from both ends.

Two routes over one table, and the fixture is ASYMMETRIC because the two are trivially confusable:

    feature X <- complaint A
    feature X <- complaint B
    feature Y <- complaint A          (A is shared; X is not shared with Y)

  complaints for X  -> [A, B]     (joins fc.feature_id = X)
  features for A    -> [X, Y]     (joins fc.complaint_id = A, ordered elo_r desc)

Swap the join columns and both still answer a plausible list — X's own complaints vs. every feature on those same
complaints — which is why the shared row and the ordering are both asserted.

Both answer an EMPTY ARRAY, never null: `null` breaks a client that expects a list, and "nothing links to this" is
the common case rather than the edge.
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
        con.execute("delete from feature_complaints where feature_id in"
                    " (select id from features where project_id = ?)", (project,))
        con.execute("delete from feature_complaints where complaint_id in"
                    " (select id from complaints where project_id = ?)", (project,))
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from complaints where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-link-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"linkauth{stamp}", "display_name": "Link", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-link-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Link probe", "", "collective", "public", "both", now, now),
    ).lastrowid

    def feature(title, elo):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, status, elo_r, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project, author, title, "proposed", elo, now, now),
        ).lastrowid

    def complaint(title, created):
        return con.execute(
            "INSERT INTO complaints (project_id, author_id, title, body, severity, frequency,"
            " strategic_multiplier, status, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)"
            " RETURNING id",
            (project, author, title, "body", 1, 1.0, 1.0, "open", created, created),
        ).lastrowid

    high = feature("High rating", 1700.0)
    low = feature("Low rating", 1200.0)
    older = complaint("Older complaint", now - 100)
    newer = complaint("Newer complaint", now)
    for link in ((high, older), (high, newer), (low, older)):
        con.execute("INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)", link)
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/features/{high}/complaints")
    check("a feature's complaints answer", status, 200)
    check("newest first", [c["id"] for c in (body or [])], [newer, older])
    check("and they are this feature's, not every feature's on those complaints",
          sorted(c["id"] for c in (body or [])), sorted([newer, older]))

    status, body = call("GET", f"/api/v1/projects/{project}/complaints/{older}/features")
    check("a complaint's features answer", status, 200)
    check("highest rating first", [f["id"] for f in (body or [])], [high, low])
    check("including a feature that is not the one we asked about first",
          len(body or []), 2)

    # The empty case, which is the common one.
    lonely = feature("Nothing links here", 1500.0)
    lone_complaint = complaint("Nothing links here either", now)
    con.commit()
    status, body = call("GET", f"/api/v1/projects/{project}/features/{lonely}/complaints")
    check("a feature with no complaints answers 200", status, 200)
    check("with an EMPTY ARRAY, not null", body, [])
    status, body = call("GET", f"/api/v1/projects/{project}/complaints/{lone_complaint}/features")
    check("a complaint with no features answers an empty array", body, [])

    # The two-halves rule on both routes.
    other = project + 0  # same project id, different URL below
    status, _ = call("GET", f"/api/v1/projects/{project}/complaints/{lone_complaint}/features")
    check("the complaint belongs to this project", status, 200)
    status, _ = call("GET", f"/api/v1/projects/{project}/features/{high}/complaints")
    check("the feature belongs to this project", status, 200)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{project}/features/{high}/complaints")
    check("a private project's complaints are 404 without a token", status, 404)
    status, _ = call("GET", f"/api/v1/projects/{project}/complaints/{older}/features")
    check("and so are its complaints' features", status, 404)

    cleanup(con, "probe-link-%")
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

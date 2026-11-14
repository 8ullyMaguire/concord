#!/usr/bin/env python3
"""Row 11b part 21: one milestone, with its planned work.

Three rules, each of which a plausible port gets wrong:

1. the PROJECT PREDICATE is in the query, so another project's milestone id is a 404 — not a 200 with the wrong
   project's data, which is the shape of bug a post-read comparison forgets in the next handler;
2. the feature order is the milestone's PLAN (`milestone_position, id`), not creation order, and the fixture inserts
   features in plan order REVERSED so the two differ;
3. `at_risk` is DERIVED (target date passed, still open, still planned/active) and never stored: the fixture places one
   milestone in the past with open work and one in the future, and asserts one is at risk and the other is not.
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
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from milestones where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-ms-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"msauth{stamp}", "display_name": "Milestone", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-ms-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Milestone probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug + "-other", "Other", "", "collective", "public", "both", now, now),
    ).lastrowid

    def milestone(project_id, title, target, state):
        return con.execute(
            "INSERT INTO milestones (project_id, title, body, target_date, status, created_at)"
            " VALUES (?,?,?,?,?,?) RETURNING id",
            (project_id, title, "", target, state, now),
        ).lastrowid

    late = milestone(project, "Overdue", now - 86400 * 30, "active")
    future = milestone(other, "Somebody else's", now + 86400 * 30, "active")
    far = milestone(project, "Later", now + 86400 * 30, "active")

    def feature(title, position, milestone_id, state):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, status, milestone_id, milestone_position,"
            " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
            (project, author, title, state, milestone_id, position, now, now),
        ).lastrowid

    # Inserted in REVERSE plan order, so ordering by id would look different from ordering by position.
    second = feature("Second in plan", 2, late, "proposed")
    first = feature("First in plan", 1, late, "proposed")
    third = feature("Third in plan", 3, late, "shipped")
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{project}/milestones/{late}")
    check("the milestone answers", status, 200)
    body = body or {}
    check("with both keys", sorted(body), ["features", "milestone"])
    milestone_body = body.get("milestone") or {}
    check("the milestone's id", milestone_body.get("id"), late)
    # `shipped` is not `status = 'shipped'`: it is shipped WITH a shipment event, and the gap between the two is
    # reported as `unshipped` — work that is done as far as the row says and unrecorded as far as the log says.
    check("its counts: the shipped feature has no shipment event yet",
          (milestone_body.get("total"), milestone_body.get("shipped"), milestone_body.get("unshipped")),
          (3, 0, 1))
    con.execute(
        "INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)"
        " VALUES (?,?,?,?,?,?,?)",
        (project, author, "feature.status", "feature", third, "proposed -> shipped", now),
    )
    con.commit()
    status, body = call("GET", f"/api/v1/projects/{project}/milestones/{late}")
    after = (body or {}).get("milestone") or {}
    check("and once the shipment is recorded, shipped counts it",
          (after.get("shipped"), after.get("unshipped")), (1, 0))
    check("and it is at risk: past its date with open work", milestone_body.get("at_risk"), True)
    check("the plan order, not the insert order", [f["id"] for f in body.get("features", [])],
          [first, second, third])

    status, body = call("GET", f"/api/v1/projects/{project}/milestones/{far}")
    check("a future milestone is not at risk", (body or {}).get("milestone", {}).get("at_risk"), False)
    check("and has no features", (body or {}).get("features"), [])

    status, _ = call("GET", f"/api/v1/projects/{project}/milestones/{future}")
    check("another project's milestone is 404", status, 404)
    status, _ = call("GET", f"/api/v1/projects/{other}/milestones/{future}")
    check("but answers under its own", status, 200)
    status, _ = call("GET", f"/api/v1/projects/{project}/milestones/not-a-number")
    check("a non-numeric id is 400", status, 400)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{project}/milestones/{late}")
    check("a private project's milestone is 404 without a token", status, 404)

    cleanup(con, "probe-ms-%")
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

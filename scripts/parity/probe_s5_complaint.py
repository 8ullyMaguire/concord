#!/usr/bin/env python3
"""Row 11b part 19: one complaint, bound to the project in the URL.

The bug this route exists to not have: a complaint id is GLOBAL, and a handler that reads only `{id}` makes the
project half of the URL decoration. Then any caller reaches any complaint on the instance through their own
project's URL, and a role check written against the URL's project checks a project the complaint has nothing to do
with. The port ties the halves and answers 404 on a mismatch — never 403, which would confirm the id is real.

A non-numeric id is INVALID (400), not 404: parsing it as 0 asks the store about a row nobody created, which is a
404 for the wrong reason and hides the caller's mistake.
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
        con.execute("delete from complaints where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-cmp-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"cmpauth{stamp}", "display_name": "Complaint", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-cmp-%")
    now = time.time()
    first = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Complaint probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    second = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug + "-other", "Other", "", "collective", "public", "both", now, now),
    ).lastrowid

    def complaint(project, title):
        return con.execute(
            "INSERT INTO complaints (project_id, author_id, title, body, severity, frequency,"
            " strategic_multiplier, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (project, author, title, "the body", 3, 1.5, 1.0, "open", now, now),
        ).lastrowid

    mine = complaint(first, "It breaks on arm64")
    theirs = complaint(second, "Somebody else's")
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{first}/complaints/{mine}")
    check("the complaint answers", status, 200)
    body = body or {}
    check("with its id", body.get("id"), mine)
    check("its project", body.get("project_id"), first)
    check("its severity", body.get("severity"), 3)
    check("and a whole-number frequency, as the Go printed it", body.get("frequency"), 1.5)
    check("and no merged_into key at all", "merged_into" in body, False)

    status, _ = call("GET", f"/api/v1/projects/{first}/complaints/{theirs}")
    check("another project's complaint is 404 under this project's URL", status, 404)
    status, _ = call("GET", f"/api/v1/projects/{second}/complaints/{theirs}")
    check("but answers under its own", status, 200)

    status, _ = call("GET", f"/api/v1/projects/{first}/complaints/not-a-number")
    check("a non-numeric id is 400, not 404", status, 400)
    status, _ = call("GET", f"/api/v1/projects/{first}/complaints/999999999")
    check("an id nobody created is 404", status, 404)
    status, _ = call("GET", f"/api/v1/projects/no-such-project-{stamp}/complaints/{mine}")
    check("an unknown project is 404", status, 404)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (first,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{first}/complaints/{mine}")
    check("a private project's complaint is 404 without a token", status, 404)

    cleanup(con, "probe-cmp-%")
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

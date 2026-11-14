#!/usr/bin/env python3
"""Row 11b part 18: a feature's evidence.

The whole point of this route is what it does NOT do: it LEFT-joins the document and the report, so evidence whose
target was deleted still lists, with an empty title. An inner join would make that evidence vanish, and the panel
would then read "nobody linked anything" — a different claim from "the document this feature cited is gone".

`kind` decides which table names the ref, so the fixture has one of each and asserts the right column supplied the
title.
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
        con.execute("delete from feature_evidence where feature_id in"
                    " (select id from features where project_id = ?)", (project,))
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from project_documents where project_id = ?", (project,))
        con.execute("delete from field_report_responses where report_id in"
                    " (select id from field_reports where project_id = ?)", (project,))
        con.execute("delete from field_reports where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    slug = f"probe-ev-{stamp}"
    status, registered = call(
        "POST", "/api/v1/auth/register",
        {"username": f"evauth{stamp}", "display_name": "Evidence", "password": "correct horse"},
    )
    if status != 201:
        print(f"register failed: {status} {registered}")
        sys.exit(2)
    author = registered["user"]["id"]

    con = harness.connect("unused")
    cleanup(con, "probe-ev-%")
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Evidence probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?) RETURNING id",
        (project, author, "Worth building", "proposed", now, now),
    ).lastrowid
    document = con.execute(
        "INSERT INTO project_documents (project_id, author_id, kind, slug, title, body, created_at,"
        " updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (project, author, "spec", f"spec-{stamp}", "The spec that asked for it", "body", now, now),
    ).lastrowid
    report = con.execute(
        "INSERT INTO field_reports (project_id, user_id, version, use_case, environment, scale, duration,"
        " outcome, migrated_to, caveats, workaround, advice, removed, created_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (project, author, "2.4.0", "probe", "arm64", "small", "1h", "worked", "", "", "", "", 0, now),
    ).lastrowid
    for kind, ref in (("document", document), ("field_report", report)):
        con.execute(
            "INSERT INTO feature_evidence (feature_id, kind, ref_id, created_at) VALUES (?,?,?,?)",
            (feature, kind, ref, now),
        )
    # Evidence whose document was deleted: the row stays, the title cannot.
    gone = con.execute(
        "INSERT INTO project_documents (project_id, author_id, kind, slug, title, body, created_at,"
        " updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (project, author, "plan", f"plan-{stamp}", "Will be deleted", "", now, now),
    ).lastrowid
    con.execute(
        "INSERT INTO feature_evidence (feature_id, kind, ref_id, created_at) VALUES (?,?,?,?)",
        (feature, "document", gone, now + 1),
    )
    con.execute("DELETE FROM project_documents WHERE id = ?", (gone,))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    status, body = call("GET", f"/api/v1/projects/{slug}/features/{feature}/evidence")
    check("evidence answers", status, 200)
    # Indexed with `.get` below, so a missing row reports as a FAILED ASSERTION and not as a traceback: a probe
    # that raises cannot say which claim broke.
    rows = {(row["kind"], row["ref_id"]): row for row in (body or [])}

    def field(key, name):
        return (rows.get(key) or {}).get(name)
    check("all three links are listed, including the one whose document is gone",
          sorted(rows), sorted([("document", document), ("field_report", report), ("document", gone)]))
    check("a document's title comes from the document", field(("document", document), "title"),
          "The spec that asked for it")
    check("a report's title is its VERSION", field(("field_report", report), "title"), "2.4.0")
    check("and a deleted document leaves an empty title rather than vanishing",
          field(("document", gone), "title"), "")
    check("newest first", ((body or [{}])[0] or {}).get("ref_id"), gone)

    con.execute("UPDATE projects SET visibility = 'private' WHERE id = ?", (project,))
    con.commit()
    status, _ = call("GET", f"/api/v1/projects/{slug}/features/{feature}/evidence")
    check("a private project's evidence is 404 without a token", status, 404)

    cleanup(con, "probe-ev-%")
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

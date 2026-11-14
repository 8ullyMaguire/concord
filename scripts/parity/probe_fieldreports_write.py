#!/usr/bin/env python3
"""Row 11 part 59: a field report is evidence, a reply is not a second opinion.

Two writes (`POST /projects/{}/field-reports`, `POST /projects/{}/field-reports/{}/responses`) and the one rule that makes them
worth having: the report is a STRUCTURED experience record — "worked for 83% of reporters" — and a free-text outcome field would
be the rant §4.7 exists to replace. A report that parses but cannot be aggregated reads as evidence and is not.

What the probe has to establish, and why each one is not free:

1. **`outcome` is the only required field and it is validated** — a missing outcome names the field, an unknown outcome names the
   four allowed values, and "works fine mostly" is refused rather than stored;
2. **`migrated-away` must name the successor** — the successor is the most useful thing in that report (it is a second candidate
   for whoever asked the question), so it is required with `migrated_to`, not defaulted;
3. **author and project come from the token and the URL, never the body** — a report whose author is whatever the body said is not
   evidence. Asserted by sending `user_id` and `project_id` of somebody else and reading back the real ones;
4. **a NON-MEMBER may file and may reply** — §4.7's rate would otherwise be a poll of people who already joined, i.e. the people
   most likely to have made it work. This is the design point of the whole section;
5. **`is_owner` is set by the server from the membership table and never read from the request** — asserted by sending
   `is_owner: true` as a non-member and reading back `false`; and by replying as a member and reading back `true`;
6. **auth is checked before the project** — anonymous to a real project and anonymous to a nonexistent one must both answer 401, or
   the status code enumerates the slugs on the instance;
7. **report ids are global and the URL is project-scoped** — a report from another project addressed through this project's URL is
   404, not 200: the route names one id and must act on that id in that project;
8. **a REMOVED report takes no new replies** and answers 404, not 403 — the report is not there for the caller, and a distinct
   refusal would confirm it exists to somebody who cannot see it. Removal is a flag, never a delete (§4.7: the appeal is conducted
   on the record), so the row must still be there afterwards;
9. **the audit detail carries the outcome, not the prose** — the outcome is the field that decides what the report counts for, and
   it is the one worth reconstructing later.
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


def refusal_text(body):
    """The message, whatever shape the error envelope takes — the point of asserting on it is that it names the field."""
    if isinstance(body, dict):
        for key in ("error", "message", "detail"):
            if key in body:
                return json.dumps(body[key])
    return json.dumps(body)


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    reporter_id, reporter = register(f"frrep{stamp}")
    bystander_id, bystander = register(f"frby{stamp}")
    owner_id, owner = register(f"frown{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-fr-{stamp}"
    other_slug = f"{slug}-other"
    con.execute("delete from projects where slug like ?", (f"{slug}%",))
    con.commit()

    def project(name, visibility="public"):
        pid = con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
            " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (name, name, "", "collective", visibility, "both", None, None, now, now),
        ).lastrowid
        return pid

    pid = project(slug)
    other_pid = project(other_slug)
    private_pid = project(f"{slug}-private", "private")
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (pid, owner_id, "maintainer", now))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (other_pid, owner_id, "maintainer", now))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    report = {
        "version": "0.9.2", "use_case": "small team wiki", "environment": "linux x86_64",
        "scale": "5 users", "duration": "3 months", "outcome": "worked-with-caveats",
        "caveats": "search index needs a rebuild nightly", "workaround": "cron + curl",
        "advice": "pin the version",
    }

    # ── filing ───────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{slug}/field-reports", report)
    check("filing anonymously is 401", status, 401)
    status, _ = call("POST", f"/api/v1/projects/{slug}-nope/field-reports", report)
    check("and anonymous to a nonexistent project is ALSO 401 — auth is checked first, or the code enumerates slugs",
          status, 401)

    without = dict(report)
    del without["outcome"]
    status, body = call("POST", f"/api/v1/projects/{slug}/field-reports", without, token=reporter)
    check("a report without an outcome is 400", status, 400)
    check("and the refusal names the field", "outcome" in refusal_text(body), True)

    status, body = call("POST", f"/api/v1/projects/{slug}/field-reports",
                        dict(report, outcome="works fine mostly"), token=reporter)
    check("an outcome outside the four is 400", status, 400)
    check("and the refusal lists the four allowed values",
          all(word in refusal_text(body) for word in ("worked", "worked-with-caveats", "abandoned", "migrated-away")), True)

    status, body = call("POST", f"/api/v1/projects/{slug}/field-reports",
                        dict(report, outcome="migrated-away", migrated_to=""), token=reporter)
    check("`migrated-away` with no successor is 400", status, 400)
    check("and the refusal names migrated_to", "migrated_to" in refusal_text(body), True)

    # Author and project from the token and the URL: the body's values must be ignored, not preferred.
    status, body = call("POST", f"/api/v1/projects/{slug}/field-reports",
                        dict(report, user_id=bystander_id, project_id=other_pid), token=reporter)
    check("a report from a non-member is accepted — §4.7's rate is not a poll of people who already joined", status, 201)
    body = body or {}
    report_id = body.get("id")
    check("the author is the token's user, not the body's", body.get("user_id"), reporter_id)
    check("the project is the URL's, not the body's", body.get("project_id"), pid)
    check("the outcome is stored as given", body.get("outcome"), "worked-with-caveats")
    check("the structured fields survive", (body.get("environment"), body.get("workaround")),
          ("linux x86_64", "cron + curl"))
    check("and it is not born removed", body.get("removed"), False)
    check("filing is audited with the OUTCOME as its detail, not the prose",
          con.execute("SELECT detail FROM audit_log WHERE action = 'file_field_report' AND entity_id = ?",
                      (report_id,)).fetchone()[0], "worked-with-caveats")

    status, _ = call("POST", f"/api/v1/projects/{slug}-private/field-reports", report, token=reporter)
    check("a private project's reports are 404, not 403 — an enumerable instance is the failure mode", status, 404)

    status, _ = call("POST", f"/api/v1/projects/{slug}-nope/field-reports", report, token=reporter)
    check("an unknown project is 404", status, 404)
    check("and nothing was written for it",
          con.execute("SELECT count(*) FROM field_reports WHERE project_id = 0").fetchone()[0], 0)

    # ── replying ─────────────────────────────────────────────────────────────────────────────────────────────────
    reply_path = f"/api/v1/projects/{slug}/field-reports/{report_id}/responses"
    status, _ = call("POST", reply_path, {"body": "thanks"})
    check("replying anonymously is 401", status, 401)
    status, body = call("POST", reply_path, {"body": "  "}, token=bystander)
    check("an empty reply is 400", status, 400)
    check("and the refusal names the body", "body" in refusal_text(body), True)

    status, body = call("POST", reply_path, {"body": "third party experience", "is_owner": True}, token=bystander)
    check("a non-member may reply", status, 201)
    body = body or {}
    check("and `is_owner` is FALSE — sent true by the client, read from the membership table", body.get("is_owner"), False)
    check("the reply records who wrote it", body.get("user_id"), bystander_id)
    check("and which report it answers", body.get("report_id"), report_id)
    check("replying is audited with the flag in its detail",
          con.execute("SELECT detail FROM audit_log WHERE action = 'respond_field_report' AND entity_id = ?"
                      " ORDER BY id DESC LIMIT 1", (report_id,)).fetchone()[0], "is_owner=false")

    status, body = call("POST", reply_path, {"body": "maintainer here"}, token=owner)
    check("a member's reply is 201", status, 201)
    check("and `is_owner` is TRUE, from the same table", (body or {}).get("is_owner"), True)

    # Global report ids behind a project-scoped URL.
    status, _ = call("POST", f"/api/v1/projects/{other_slug}/field-reports/{report_id}/responses",
                     {"body": "wrong project"}, token=bystander)
    check("the same report id under another project's URL is 404", status, 404)
    status, _ = call("POST", f"/api/v1/projects/{slug}/field-reports/999999999/responses",
                     {"body": "no such report"}, token=bystander)
    check("replying to a report that does not exist is 404", status, 404)

    # A removed report is a flag, and it takes no new replies.
    con.execute("UPDATE field_reports SET removed = 1, removed_by = ?, removal_reason = 'fabricated' WHERE id = ?",
                (owner_id, report_id))
    con.commit()
    status, _ = call("POST", reply_path, {"body": "still here?"}, token=bystander)
    check("a removed report takes no new replies, and answers 404 rather than 403", status, 404)
    check("the row is still there — removal is a flag, because the appeal is conducted on the record",
          con.execute("SELECT count(*) FROM field_reports WHERE id = ?", (report_id,)).fetchone()[0], 1)
    check("and the earlier replies are still there too",
          con.execute("SELECT count(*) FROM field_report_responses WHERE report_id = ?",
                      (report_id,)).fetchone()[0], 2)

    con.execute("delete from field_report_responses where report_id = ?", (report_id,))
    con.execute("delete from field_reports where project_id in (?,?,?)", (pid, other_pid, private_pid))
    con.execute("delete from audit_log where project_id in (?,?,?)", (pid, other_pid, private_pid))
    con.execute("delete from members where project_id in (?,?,?)", (pid, other_pid, private_pid))
    con.execute("delete from projects where id in (?,?,?)", (pid, other_pid, private_pid))
    con.commit()

    passed = sum(1 for _, got, want in results if got == want)
    print(f"\n{passed}/{len(results)} ok", flush=True)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())

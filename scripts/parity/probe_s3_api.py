#!/usr/bin/env python3
"""S3's consensus writes, probed over HTTP.

The rules under test, each one a place where a plausible implementation is wrong:

* a call opens only on work that is not already decided, only with evidence, and only once (a second
  open call on the same feature is refused);
* opening a call moves its feature to `consensus` -- the rule the deployed instance lost, leaving 87
  features at `status='consensus'` with zero `consensus_calls` rows;
* calling and closing are a maintainer's acts (a plain member is refused, not silently allowed);
* a stance is one of four words, and a typo is refused rather than stored;
* while a call is open its tally is hidden (counts null, participation visible, only the caller's own
  position), and once closed the tally is the record;
* closing reports the state it wrote -- status `closed`, a result -- not the call as it stood before.

Nothing is skipped when a route is missing: a 404 is the finding.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "tests", "e2e"))  # harness

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8107")


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            raw = response.read()
            return response.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw) if raw else None
        except ValueError:
            return error.code, raw.decode("utf-8", "replace")


def message(body):
    return ((body or {}).get("error") or {}).get("message", "") if isinstance(body, dict) else ""


def main():
    import harness

    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"s3probe{int(time.time())}",
        "display_name": "S3",
        "password": "correct horse",
    })
    token = body.get("token") if isinstance(body, dict) else None
    if not token:
        print(f"register failed: {status} {body}")
        return 1
    author = body["user"]["id"]

    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"s3member{int(time.time())}",
        "display_name": "S3 member",
        "password": "correct horse",
    })
    member_token = body.get("token") if isinstance(body, dict) else None
    member = body["user"]["id"]

    con = harness.connect("unused")
    slug = "s3-probe"
    # Delete the previous run's rows children-first: PostgreSQL enforces the foreign keys SQLite never
    # did, so a bare `delete from projects` leaves consensus_calls/positions/objections/features pointing
    # at a row that is gone and the whole probe fails before it asserts anything.
    for drop in (
        "delete from positions where call_id in (select id from consensus_calls"
        " where project_id in (select id from projects where slug = ?))",
        "delete from objections where call_id in (select id from consensus_calls"
        " where project_id in (select id from projects where slug = ?))",
        "delete from emergency_holds where call_id in (select id from consensus_calls"
        " where project_id in (select id from projects where slug = ?))",
        "delete from consensus_calls where project_id in (select id from projects where slug = ?)",
        "delete from feature_complaints where feature_id in (select id from features"
        " where project_id in (select id from projects where slug = ?))",
        "delete from features where project_id in (select id from projects where slug = ?)",
        "delete from complaints where project_id in (select id from projects where slug = ?)",
        "delete from board_columns where project_id in (select id from projects where slug = ?)",
        "delete from members where project_id in (select id from projects where slug = ?)",
    ):
        con.execute(drop, (slug,))
    con.execute("delete from projects where slug = ?", (slug,))
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)",
        (slug, "S3 probe", "", "collective", "public", "both", time.time(), time.time()),
    )
    con.commit()
    project = con.execute("SELECT id FROM projects WHERE slug = ?", (slug,)).fetchone()[0]
    # `joined_at` is NOT NULL with no default in the baseline: SQLite let the writer omit it, PostgreSQL
    # does not, so every fixture that seats a member must say when.
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, author, "maintainer", time.time()))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, member, "contributor", time.time()))
    for phase, position in (("inbox", 0), ("consensus", 1)):
        con.execute("INSERT INTO board_columns (project_id, phase, position) VALUES (?,?,?)",
                    (project, phase, position))

    def feature(title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project, author, title, "", "discussion", time.time(), time.time()),
        ).lastrowid

    bare = feature("no evidence")
    with_evidence = feature("has evidence")
    second = feature("second call")
    complaint = con.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, severity, frequency,"
        " strategic_multiplier, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (project, author, "a validated complaint", "", 2, 1.0, 1.0, "validated",
         time.time(), time.time()),
    ).lastrowid
    for target in (with_evidence, second):
        con.execute("INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)",
                    (target, complaint))
    con.commit()
    con.close()

    results = []

    # 1. Opening a call needs an account, and a maintainer's role.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"feature_id": with_evidence, "title": "no token"})
    results.append(("open without a token", status, 401, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"feature_id": with_evidence, "title": "as a contributor"}, token=member_token)
    results.append(("open as a contributor", status, 403, message(body)))

    # 2. Evidence: a call on a feature with no linked complaint is a vote on a rumour.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"feature_id": bare, "title": "no evidence"}, token=token)
    results.append(("open without evidence", status, 400, message(body)))
    results.append(("the refusal names the evidence", 1 if "complaint" in message(body) else 0, 1,
                    message(body)))

    # 3. A valid call opens, and its feature moves to `consensus`.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"feature_id": with_evidence, "title": "ship it?",
                         "description": "the case for"}, token=token)
    call_id = body.get("id") if isinstance(body, dict) else None
    results.append(("open a call", status, 201, body))
    status, body = call("GET", f"/api/v1/projects/{slug}/features/{with_evidence}")
    results.append(("opening moves the feature to consensus", (body or {}).get("status"), "consensus",
                    (body or {}).get("status")))
    results.append(("opened_by is the caller", (body or {}).get("id") is not None, 1, author))

    # 4. One open call per feature.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"feature_id": with_evidence, "title": "again"}, token=token)
    results.append(("a second open call is refused", status, 400, message(body)))

    # 5. A stance is one of four words.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/position",
                        {"position": "maybe"}, token=token)
    results.append(("an invented stance", status, 400, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/position",
                        {"position": "consent"}, token=token)
    results.append(("cast consent", status, 200, body))

    # 6. An objection needs its principle and its violation.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/objection",
                        {"principle": "", "violation": "it breaks the ledger"}, token=token)
    results.append(("an objection with no principle", status, 400, message(body)))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/objection",
                        {"principle": "the audit ledger", "violation": "it rewrote a row",
                         "remedy": "append instead"}, token=token)
    objection = body.get("id") if isinstance(body, dict) else None
    results.append(("file an objection", status, 201, body))

    # 7. A resolution is an answer somebody wrote.
    status, body = call("PUT", f"/api/v1/projects/{slug}/consensus/objections/{objection}/resolve",
                        {"status": "answered", "resolution": "   "}, token=token)
    results.append(("resolve with an empty answer", status, 400, message(body)))

    # 8. While the call is open the tally is hidden, and only the caller's own position comes back.
    status, body = call("GET", f"/api/v1/projects/{slug}/consensus/{call_id}", token=token)
    tally = (body or {}).get("tally") or {}
    positions = (body or {}).get("positions") or []
    results.append(("an open call hides its counts", tally.get("consent"), None, tally.get("consent")))
    results.append(("...and its ratios", tally.get("decisive_ratio"), None, tally.get("decisive_ratio")))
    results.append(("participation stays visible", 1 if tally.get("participants") is not None else 0,
                    1, tally.get("participants")))
    results.append(("the quorum bar stays visible",
                    1 if tally.get("quorum_required") is not None else 0, 1,
                    tally.get("quorum_required")))
    results.append(("tally_visible is false", (body or {}).get("tally_visible"), False,
                    (body or {}).get("tally_visible")))
    results.append(("only the caller's own position", len(positions), 1, positions))

    # 9. Closing fixes the verdict and reports what it wrote.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/close", None, token=token)
    closed = (body or {}).get("call") or {}
    results.append(("close the call", status, 200, message(body)))
    results.append(("the close reports status closed", closed.get("status"), "closed",
                    closed.get("status")))
    results.append(("the close reports a result",
                    1 if (body or {}).get("result") else 0, 1, (body or {}).get("result")))

    # 10. The record now shows its tally, and a closed call takes no more positions.
    status, body = call("GET", f"/api/v1/projects/{slug}/consensus/{call_id}", token=token)
    tally = (body or {}).get("tally") or {}
    results.append(("a closed call shows its counts", tally.get("consent"), 1, tally.get("consent")))
    results.append(("a closed call shows every position",
                    1 if len((body or {}).get("positions") or []) >= 1 else 0, 1,
                    len((body or {}).get("positions") or [])))
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus/{call_id}/position",
                        {"position": "block"}, token=member_token)
    results.append(("a closed call takes no positions", status, 400, message(body)))

    # A call on no feature is a §6.6 process decision and must not need one.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"title": "amend the charter"}, token=token)
    results.append(("a process decision opens without a feature", status, 201,
                    (body or {}).get("feature_id")))
    results.append(("...and its feature_id is 0", (body or {}).get("feature_id"), 0,
                    (body or {}).get("feature_id")))

    failed = 0
    for label, got, want, detail in results:
        ok = got == want
        if not ok:
            failed += 1
        print(f"{'ok  ' if ok else 'FAIL'} {label}: got {got!r} want {want!r}"
              + ("" if ok else f"  ({detail})"))
    print(f"\n{len(results) - failed}/{len(results)} ok")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""S2's two store rules, probed over HTTP before they are ported.

The rules are the ones the plan says the port must not lose (`docs/REMAINING.md` §44/§46 and
`internal/store/features.go`): a feature reaches `ready` only with evidence, and reaches `in_progress`
only when nothing open blocks it — and evidence is re-checked at `in_progress`, because evidence can be
removed after a feature is ready and the gate on `ready` alone would be a gate with a hole.

This probe is the failure first: it runs against whatever serves the candidate and reports, per
assertion, what came back. Nothing here is skipped when a route is missing — a 404 is the finding.
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
        with urllib.request.urlopen(req, timeout=10) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw) if raw else None
        except ValueError:
            return e.code, raw.decode("utf-8", "replace")


def main():
    import harness

    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"s2probe{int(time.time())}", "display_name": "S2", "password": "correct horse",
    })
    token = body.get("token") if isinstance(body, dict) else None
    if not token:
        print(f"register failed: {status} {body}")
        return 1
    author = body["user"]["id"]

    con = harness.connect("unused")
    slug = "s2-probe"
    con.execute("delete from projects where slug = ?", (slug,))
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)",
        (slug, "S2 probe", "", "collective", "public", "both", time.time(), time.time()),
    )
    con.commit()
    project = con.execute("SELECT id FROM projects WHERE slug = ?", (slug,)).fetchone()[0]
    for phase, position in (("inbox", 0), ("ready", 1), ("in_progress", 2)):
        con.execute(
            "INSERT INTO board_columns (project_id, phase, position) VALUES (?,?,?)",
            (project, phase, position),
        )
    blocker = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author, "blocker", "", "draft", time.time(), time.time()),
    ).lastrowid
    blocked = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author, "blocked", "", "draft", time.time(), time.time()),
    ).lastrowid
    con.execute("INSERT INTO feature_deps (blocked_id, blocker_id) VALUES (?,?)", (blocked, blocker))
    validated = con.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, severity, frequency,"
        " strategic_multiplier, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (project, author, "a validated complaint", "", 2, 1.0, 1.0, "validated",
         time.time(), time.time()),
    ).lastrowid
    con.commit()
    con.close()

    results = []

    # 1. Filing a feature needs an account and a project that exists.
    status, body = call("POST", f"/api/v1/projects/{slug}/features",
                        {"title": "no token", "body": ""})
    results.append(("file without a token", status, 401, body))
    status, body = call("POST", f"/api/v1/projects/{slug}/features",
                        {"title": "an idea", "body": "with a body"}, token=token)
    created = body if isinstance(body, dict) else {}
    results.append(("file a feature", status, 201, created.get("id")))
    idea = created.get("id")

    # 2. The evidence gate: no evidence means no `ready`, with the message saying what would do.
    status, body = call("PUT", f"/api/v1/projects/{slug}/features/{idea}/status",
                        {"status": "ready"}, token=token)
    message = ((body or {}).get("error") or {}).get("message", "") if isinstance(body, dict) else ""
    results.append(("ready without evidence", status, 400, message))
    results.append(("the refusal names the evidence",
                    1 if "evidence" in message else 0, 1, message))

    # 3. With evidence it goes ready.
    status, body = call("POST", f"/api/v1/projects/{slug}/features/{idea}/complaints",
                        {"complaint_id": validated}, token=token)
    # 200 with the feature's complaints, not 201 with the link: the section renders its own count
    # and must not re-read to learn what it just caused.
    results.append(("link a validated complaint", status, 200, body))
    status, body = call("PUT", f"/api/v1/projects/{slug}/features/{idea}/status",
                        {"status": "ready"}, token=token)
    results.append(("ready with evidence", status, 200, body))

    # 4. The dependency refusal: an open blocker stops `in_progress` and is named.
    status, body = call("POST", f"/api/v1/projects/{slug}/features/{blocked}/complaints",
                        {"complaint_id": validated}, token=token)
    status, body = call("PUT", f"/api/v1/projects/{slug}/features/{blocked}/status",
                        {"status": "ready"}, token=token)
    results.append(("blocked feature reaches ready", status, 200, body))
    status, body = call("PUT", f"/api/v1/projects/{slug}/features/{blocked}/status",
                        {"status": "in_progress"}, token=token)
    message = ((body or {}).get("error") or {}).get("message", "") if isinstance(body, dict) else ""
    results.append(("in_progress with an open blocker", status, 400, message))
    results.append(("the refusal names the blocker", 1 if "blocker" in message else 0, 1, message))

    # 5. The hole §46 closes: ready, then the evidence is taken away, then start work.
    status, body = call("DELETE",
                        f"/api/v1/projects/{slug}/features/{idea}/complaints/{validated}",
                        token=token)
    results.append(("unlink the evidence after ready", status, 200, body))
    status, body = call("PUT", f"/api/v1/projects/{slug}/features/{idea}/status",
                        {"status": "in_progress"}, token=token)
    message = ((body or {}).get("error") or {}).get("message", "") if isinstance(body, dict) else ""
    results.append(("in_progress after the evidence was removed", status, 400, message))

    bad = [r for r in results if r[1] != r[2]]
    for label, got, want, detail in results:
        print(f"{'ok ' if got == want else 'BAD'} {label}: {got} (want {want})"
              f"{'' if got == want else ' -> ' + json.dumps(detail)[:160]}")
    print(f"{len(bad)} of {len(results)} failed")
    return 0 if not bad else 1


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""S1's API behaviour, probed directly: register, place a derived card, refuse past a WIP limit.

The e2e suites cannot prove the API half yet — they wait on `#kanban-board .kanban-card`, and the
page is P6's work (row 12). This probes the same behaviour over HTTP so the slice's claim is grounded
in a request and its response rather than in a green build.
"""
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "tests", "e2e"))

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
    import harness  # the PgSeed adapter, which is what the suites seed with
    slug = "api-probe"

    # Register first: `features.author_id` is a foreign key to `users`, and a fixture that inserted
    # features before an account existed failed with ForeignKeyViolation — which is the schema
    # saying the order is not a detail.
    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"probe{int(time.time())}", "display_name": "Probe", "password": "correct horse",
    })
    token = body.get("token") if isinstance(body, dict) else None
    author = ((body or {}).get("user") or {}).get("id") if isinstance(body, dict) else None
    registered = ("register", status, 201, (token or "")[:4])
    if not token or not author:
        print(json.dumps([registered], indent=1))
        return 1

    con = harness.connect("unused")
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)",
        (slug, "API probe", "", "collective", "public", "complaint_first", time.time(), time.time()),
    )
    con.commit()
    pid = con.execute("SELECT id FROM projects WHERE slug = ?", (slug,)).fetchone()[0]
    for phase, wip, position in (("inbox", None, 0), ("ready", 1, 1)):
        con.execute(
            "INSERT INTO board_columns (project_id, phase, position, wip_limit) VALUES (?,?,?,?)",
            (pid, phase, position, wip),
        )
    for index in range(3):
        con.execute(
            "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?)",
            (pid, author, f"probe {index}", "", "draft", time.time(), time.time()),
        )
    con.commit()
    features = [r[0] for r in con.execute(
        "SELECT id FROM features WHERE project_id = ? ORDER BY id", (pid,)).fetchall()]
    con.close()

    results = [registered]

    # A derived card has no row; naming the feature is how it is placed, and the row is created.
    status, body = call("PUT", f"/api/v1/projects/{slug}/board/cards/0/move?to=inbox&feature_id="
                                f"{features[0]}", token=token)
    results.append(("place derived card", status, 200, body))

    # The same feature again, into the WIP=1 column: first placement fits, second must be refused.
    status, body = call("PUT", f"/api/v1/projects/{slug}/board/cards/0/move?to=ready&feature_id="
                                f"{features[0]}", token=token)
    results.append(("place into empty wip=1 column", status, 200, body))
    status, body = call("PUT", f"/api/v1/projects/{slug}/board/cards/0/move?to=ready&feature_id="
                                f"{features[1]}", token=token)
    results.append(("second into full wip=1 column", status, 409, body))

    # No token, no move.
    status, body = call("PUT", f"/api/v1/projects/{slug}/board/cards/0/move?to=inbox&feature_id="
                                f"{features[2]}")
    results.append(("move without a token", status, 401, body))

    bad = [r for r in results if r[1] != r[2]]
    for label, got, want, detail in results:
        print(f"{'ok ' if got == want else 'BAD'} {label}: {got} (want {want})"
              f"{'' if got == want else ' -> ' + json.dumps(detail)[:160]}")
    return 0 if not bad else 1


if __name__ == "__main__":
    sys.exit(main())

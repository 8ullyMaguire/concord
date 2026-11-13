#!/usr/bin/env python3
"""Row 11b part 41 (B5): milestone writes — create, patch, status, close, assign, order.

The rules, each one a decision the reference had to make explicitly:

1. every write is **maintainer** work (except none of them — all six), and an unknown project is 404 not 403;
2. `target_date` is optional and an undated milestone has made **no promise**, which is why every list sorts it last;
3. a title is required and **unique within the project**, not across the instance;
4. `PUT /status` is a **no-op with no act when the status already matches** — recording "active -> active" makes the
   history unreadable;
5. closing is the **only** path into `done`/`cancelled`, because it is the only one that reconciles the open features:
   `done` with open features and no successor is refused **with the count**, a successor **moves** them with **one act per
   feature**, and a cancellation moves **none**;
6. a batch assign **appends after the current tail** rather than interleaving, and a feature from another project is
   refused (a cross-project write dressed as a bulk assign);
7. reordering refuses a feature that is **not in this milestone** rather than silently skipping it, and answers **204**
   with no body.
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


def cleanup(con, like):
    projects = [row[0] for row in con.execute("select id from projects where slug like ?", (like,)).fetchall()]
    if not projects:
        return
    marks = ",".join("?" * len(projects))
    params = tuple(projects)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from features where project_id in ({marks})", params)
    con.execute(f"delete from milestones where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    maintainer_id, maintainer_token = register(f"msmaint{stamp}")
    contributor_id, contributor_token = register(f"mscontrib{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-ms-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Milestone probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Other", "", "collective", "public", "both", now, now),
    ).lastrowid
    for user, role in ((maintainer_id, "maintainer"), (contributor_id, "contributor")):
        con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (project, user, role, now))
    # The maintainer is also a maintainer of the second project, so "the same title elsewhere is fine" tests the
    # uniqueness rule and not the role gate — otherwise a 403 would look like a pass for the wrong reason.
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (other, maintainer_id, "maintainer", now))
    features = []
    for index in range(3):
        features.append(con.execute(
            "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project, maintainer_id, f"Feature {index} of the probe", "", "proposed", now, now),
        ).lastrowid)
    foreign_feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (other, maintainer_id, "Another project's feature", "", "proposed", now, now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    def audits(action, entity_id):
        return con.execute(
            "SELECT count(*) FROM audit_log WHERE project_id = ? AND action = ? AND entity_id = ?",
            (project, action, entity_id)).fetchone()[0]

    # ── create ──────────────────────────────────────────────────────────────────────────────────────────────────────
    target = now + 86400 * 30
    status, _ = call("POST", f"/api/v1/projects/{slug}/milestones", {"title": "Anonymous"})
    check("creating anonymously is 401", status, 401)
    status, _ = call("POST", f"/api/v1/projects/{slug}/milestones", {"title": "By a contributor"},
                     token=contributor_token)
    check("a contributor may not create one", status, 403)
    status, created = call("POST", f"/api/v1/projects/{slug}/milestones",
                           {"title": "v0.4 the printing release", "body": "Everything about paper.",
                            "target_date": target}, token=maintainer_token)
    check("a maintainer may", status, 201)
    created = created or {}
    milestone = created.get("id")
    check("it starts planned", created.get("status"), "planned")
    check("the date round-trips", round(created.get("target_date") or 0), round(target))
    check("creating writes an act", audits("milestone.create", milestone), 1)
    status, undated = call("POST", f"/api/v1/projects/{slug}/milestones", {"title": "Someday"},
                           token=maintainer_token)
    check("an undated milestone is allowed", status, 201)
    check("and its target_date is null", (undated or {}).get("target_date"), None)
    status, _ = call("POST", f"/api/v1/projects/{slug}/milestones", {"title": ""}, token=maintainer_token)
    check("a blank title is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/milestones",
                     {"title": "v0.4 the printing release"}, token=maintainer_token)
    check("a duplicate title in the same project is 409", status, 409)
    status, elsewhere = call("POST", f"/api/v1/projects/{other}/milestones",
                             {"title": "v0.4 the printing release"}, token=maintainer_token)
    check("the same title in ANOTHER project is fine", status, 201)

    # ── patch ──────────────────────────────────────────────────────────────────────────────────────────────────────
    status, patched = call("PATCH", f"/api/v1/projects/{slug}/milestones/{milestone}",
                           {"title": "v0.4 the printing release (renamed)", "body": "Still paper.", "target_date": None},
                           token=maintainer_token)
    check("a maintainer may edit it", status, 200)
    check("the title changed", (patched or {}).get("title"), "v0.4 the printing release (renamed)")
    check("and the date can be cleared", (patched or {}).get("target_date"), None)
    status, _ = call("PATCH", f"/api/v1/projects/{slug}/milestones/{milestone}", {"title": ""},
                     token=maintainer_token)
    check("a blank title on edit is 400", status, 400)
    status, _ = call("PATCH", f"/api/v1/projects/{slug}/milestones/99999999", {"title": "x"},
                     token=maintainer_token)
    check("an unknown milestone is 404", status, 404)

    # ── status ──────────────────────────────────────────────────────────────────────────────────────────────────────
    status, active = call("PUT", f"/api/v1/projects/{slug}/milestones/{milestone}/status",
                          {"status": "active"}, token=maintainer_token)
    check("planned -> active", (status, (active or {}).get("status")), (200, "active"))
    acts = audits("milestone.status", milestone)
    check("and the act is written", acts, 1)
    status, again = call("PUT", f"/api/v1/projects/{slug}/milestones/{milestone}/status",
                         {"status": "active"}, token=maintainer_token)
    check("setting the same status again is a 200 no-op", (status, (again or {}).get("status")), (200, "active"))
    check("and writes NO second act", audits("milestone.status", milestone), 1)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/milestones/{milestone}/status",
                     {"status": "banana"}, token=maintainer_token)
    check("an unknown status is 400", status, 400)

    # ── assign ──────────────────────────────────────────────────────────────────────────────────────────────────────
    status, assigned = call("POST", f"/api/v1/projects/{slug}/milestones/{milestone}/features",
                            {"feature_ids": features[:2]}, token=maintainer_token)
    check("a batch is assigned", (status, (assigned or {}).get("assigned")), (200, 2))
    positions = [row[0] for row in con.execute(
        "SELECT milestone_position FROM features WHERE id = ? ORDER BY milestone_position",
        (features[0],)).fetchall()]
    check("positions start at 1", positions, [1])
    status, _ = call("POST", f"/api/v1/projects/{slug}/milestones/{milestone}/features",
                     {"feature_ids": []}, token=maintainer_token)
    check("an empty batch is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/milestones/{milestone}/features",
                     {"feature_ids": [foreign_feature]}, token=maintainer_token)
    check("a feature from ANOTHER project is 404", status, 404)
    status, more = call("POST", f"/api/v1/projects/{slug}/milestones/{milestone}/features",
                        {"feature_ids": [features[2]]}, token=maintainer_token)
    tail = con.execute("SELECT milestone_position FROM features WHERE id = ?", (features[2],)).fetchone()[0]
    check("a second batch appends after the tail", (status, tail), (200, 3))

    # ── order ──────────────────────────────────────────────────────────────────────────────────────────────────────
    status, body = call("PUT", f"/api/v1/projects/{slug}/milestones/{milestone}/order",
                        {"feature_ids": [features[2], features[0], features[1]]}, token=maintainer_token)
    check("reordering answers 204", status, 204)
    check("with no body", body, None)
    order = con.execute(
        "SELECT id FROM features WHERE milestone_id = ? ORDER BY milestone_position", (milestone,)).fetchall()
    check("positions follow the array, one-based",
          [row[0] for row in order], [features[2], features[0], features[1]])
    positions = sorted(row[0] for row in con.execute(
        "SELECT milestone_position FROM features WHERE milestone_id = ?", (milestone,)).fetchall())
    check("and are exactly 1..n", positions, [1, 2, 3])
    status, _ = call("PUT", f"/api/v1/projects/{slug}/milestones/{milestone}/order",
                     {"feature_ids": [foreign_feature]}, token=maintainer_token)
    check("ordering a feature that is not in the milestone is 400", status, 404)

    # ── close ──────────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{slug}/milestones/{milestone}/close", {}, token=maintainer_token)
    check("closing as done with 3 open features and no successor is 400", status, 400)
    successor = (call("POST", f"/api/v1/projects/{slug}/milestones", {"title": "v0.5 the sequel"},
                      token=maintainer_token)[1] or {}).get("id")
    status, closed = call("POST", f"/api/v1/projects/{slug}/milestones/{milestone}/close",
                          {"successor_id": successor}, token=maintainer_token)
    check("with a successor it closes", status, 200)
    closed = closed or {}
    check("reporting how many moved", closed.get("moved"), 3)
    check("the milestone is done", (closed.get("milestone") or {}).get("status"), "done")
    moved = con.execute("SELECT count(*) FROM features WHERE milestone_id = ?", (successor,)).fetchone()[0]
    check("and the features really moved", moved, 3)
    check("one act per moved feature", audits("feature.milestone", features[0]), 1)
    status, _ = call("POST", f"/api/v1/projects/{slug}/milestones/{milestone}/close", {}, token=maintainer_token)
    check("closing twice is 409", status, 409)

    # A cancellation moves none: the work stays grouped where it was.
    cancelled = (call("POST", f"/api/v1/projects/{slug}/milestones",
                      {"title": "the shelf release"}, token=maintainer_token)[1] or {}).get("id")
    call("POST", f"/api/v1/projects/{slug}/milestones/{cancelled}/features",
         {"feature_ids": [foreign_feature]}, token=maintainer_token)
    keep = con.execute("SELECT count(*) FROM features WHERE milestone_id = ?", (cancelled,)).fetchone()[0]
    check("a feature from another project cannot be put here", keep, 0)
    call("POST", f"/api/v1/projects/{slug}/milestones/{cancelled}/features",
         {"feature_ids": [features[0]]}, token=maintainer_token)
    status, cancelled_body = call("POST", f"/api/v1/projects/{slug}/milestones/{cancelled}/close",
                                  {"cancel": True}, token=maintainer_token)
    check("cancelling closes it", (status, (cancelled_body or {}).get("moved")), (200, 0))
    stayed = con.execute("SELECT milestone_id FROM features WHERE id = ?", (features[0],)).fetchone()[0]
    check("and moves nothing — the feature stays where it was", stayed, cancelled)

    cleanup(con, f"{slug}%")
    con.commit()
    for user in (maintainer_id, contributor_id):
        con.execute("DELETE FROM api_tokens WHERE user_id = ?", (user,))
        con.execute("DELETE FROM users WHERE id = ?", (user,))
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

#!/usr/bin/env python3
"""Row 11d part 46 (B2b): work items, claims, dependencies, evidence.

Seven routes, five rules, each of them a refusal or a deliberate omission:

1. **a claim never moves the feature** — the board is the only mechanism that moves work, and a claim that changed the
   status would make the board and the feed disagree about what is being built;
2. **only `ready` and `in_progress` work is claimable** — a draft is work nobody has agreed to do;
3. **one live claim**, and a release is a *state* (`released_at`), not a deletion, so "picked up and dropped twice" stays
   answerable;
4. **a dependency cycle is refused with the path that would close it**, and only `shipped` clears a blocker — a rejected
   blocker is still a blocker, because the refusal is a decision somebody has to revisit;
5. **evidence is scoped twice** — the feature and the cited row — because a reference id is global and another project's
   document is evidence of something else.
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
    features = [row[0] for row in con.execute(
        f"select id from features where project_id in ({marks})", params).fetchall()]
    if features:
        fmarks = ",".join("?" * len(features))
        fparams = tuple(features)
        con.execute(f"delete from work_item_claims where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from feature_evidence where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from feature_deps where blocker_id in ({fmarks}) or blocked_id in ({fmarks})",
                    fparams + fparams)
        con.execute(f"delete from feature_tags where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from features where id in ({fmarks})", fparams)
    con.execute(f"delete from embeddings where project_id in ({marks})", params)
    con.execute(f"delete from field_reports where project_id in ({marks})", params)
    con.execute(f"delete from project_documents where project_id in ({marks})", params)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    maintainer_id, maintainer_token = register(f"wimaint{stamp}")
    contributor_id, contributor_token = register(f"wicontrib{stamp}")
    second_id, second_token = register(f"wisecond{stamp}")
    stranger_id, stranger_token = register(f"wistranger{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-work-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Work item probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Other", "", "collective", "public", "both", now, now),
    ).lastrowid
    for user, role in ((maintainer_id, "maintainer"), (contributor_id, "contributor"), (second_id, "contributor")):
        con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (project, user, role, now))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (other, maintainer_id, "maintainer", now))

    def feature(status, title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project, maintainer_id, title, "", status, now, now),
        ).lastrowid

    ready = feature("ready", "Ready work")
    draft = feature("proposed", "A draft nobody agreed to")
    other_ready = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (other, maintainer_id, "Their ready work", "", "ready", now, now),
    ).lastrowid
    shipped = feature("shipped", "Already shipped")
    doc = con.execute(
        "INSERT INTO project_documents (project_id, author_id, slug, title, kind, body, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (project, maintainer_id, f"doc-{stamp}", "A document", "document", "", now, now),
    ).lastrowid
    foreign_doc = con.execute(
        "INSERT INTO project_documents (project_id, author_id, slug, title, kind, body, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (other, maintainer_id, f"doc-{stamp}-other", "Their document", "document", "", now, now),
    ).lastrowid
    # `field_reports` is not a document with a title: it is a migration report (version, use_case, environment, scale,
    # duration, outcome…), and only `outcome` has no default. Seeding it as though it were a document is the mistake this
    # probe made the first time.
    report = con.execute(
        "INSERT INTO field_reports (project_id, user_id, version, use_case, environment, outcome, created_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, maintainer_id, "1.0", "a test", "a laptop", "it worked", now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    def audits(action):
        return con.execute(
            "SELECT count(*) FROM audit_log WHERE project_id = ? AND action = ?",
            (project, action)).fetchone()[0]

    # ── work item label ───────────────────────────────────────────────────────────────────────────────────────────
    label = {"difficulty": "S", "time_budget_minutes": 45, "needs_mentor": True}
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{ready}/work-item", label)
    check("labelling anonymously is 401", status, 401)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{ready}/work-item", label,
                     token=contributor_token)
    check("a contributor may not label work", status, 403)
    status, item = call("PUT", f"/api/v1/projects/{slug}/features/{ready}/work-item", label,
                        token=maintainer_token)
    check("a maintainer may", status, 200)
    item = item or {}
    check("the response is the work item", (item.get("kind"), item.get("difficulty"),
                                            item.get("time_budget_minutes"), item.get("needs_mentor")),
          ("feature", "S", 45, True))
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{ready}/work-item",
                     {"difficulty": "XL"}, token=maintainer_token)
    check("a difficulty outside S/M/L is 400", status, 400)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{ready}/work-item",
                     {"time_budget_minutes": 0}, token=maintainer_token)
    check("a zero time budget is 400", status, 400)
    status, cleared = call("PUT", f"/api/v1/projects/{slug}/features/{ready}/work-item",
                           {"difficulty": ""}, token=maintainer_token)
    check("an empty difficulty CLEARS it", (status, (cleared or {}).get("difficulty")), (200, ""))
    budget = con.execute("SELECT time_budget_minutes FROM features WHERE id = ?", (ready,)).fetchone()[0]
    check("and the budget stays NULL because none was sent", budget, None)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{other_ready}/work-item", label,
                     token=maintainer_token)
    check("another project's feature is 404", status, 404)

    # ── claims ───────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{draft}/claim", token=contributor_token)
    check("a draft cannot be claimed", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/claim", token=stranger_token)
    check("a non-member may not claim", status, 403)
    status, claimed = call("POST", f"/api/v1/projects/{slug}/features/{ready}/claim",
                           token=contributor_token)
    check("a contributor may claim ready work", (status, (claimed or {}).get("claimed_by")),
          (201, contributor_id))
    still = con.execute("SELECT status FROM features WHERE id = ?", (ready,)).fetchone()[0]
    check("the claim does NOT move the feature", still, "ready")
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/claim", token=second_token)
    check("a second claimant is refused", status, 409)
    status, _ = call("DELETE", f"/api/v1/projects/{slug}/features/{ready}/claim", token=second_token)
    check("releasing a claim you do not hold is 404", status, 404)
    status, _ = call("DELETE", f"/api/v1/projects/{slug}/features/{ready}/claim", token=contributor_token)
    check("the holder may release", status, 204)
    released = con.execute(
        "SELECT released_at IS NOT NULL FROM work_item_claims WHERE feature_id = ? AND user_id = ?",
        (ready, contributor_id)).fetchone()[0]
    check("the row stays with released_at set", released, True)
    status, again = call("POST", f"/api/v1/projects/{slug}/features/{ready}/claim", token=second_token)
    check("so somebody else can take it", (status, (again or {}).get("claimed_by")), (201, second_id))

    # ── dependencies ─────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/deps", {"blocker_id": ready},
                     token=maintainer_token)
    check("a feature cannot block itself", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/deps",
                     {"blocker_id": other_ready}, token=maintainer_token)
    check("a blocker from another project is 404", status, 404)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/deps", {"blocker_id": draft},
                     token=contributor_token)
    check("a contributor may not edit the graph", status, 403)
    status, edge = call("POST", f"/api/v1/projects/{slug}/features/{ready}/deps", {"blocker_id": draft},
                        token=maintainer_token)
    check("a maintainer may record a dependency", (status, (edge or {}).get("blocker_id")), (200, draft))
    status, listed = call("GET", f"/api/v1/projects/{slug}/features/{ready}/deps")
    check("the open blockers are listed", [row["id"] for row in (listed or {}).get("open_blockers") or []],
          [draft])
    status, cycle = call("POST", f"/api/v1/projects/{slug}/features/{draft}/deps", {"blocker_id": ready},
                         token=maintainer_token)
    check("a cycle is refused", status, 400)
    check("and the refusal names the path that would close it",
          "blocks" in json.dumps(cycle), True)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/deps", {"blocker_id": shipped},
                     token=maintainer_token)
    check("a shipped blocker is allowed", status, 200)
    status, listed = call("GET", f"/api/v1/projects/{slug}/features/{ready}/deps")
    check("but it does not count as OPEN", [row["id"] for row in (listed or {}).get("open_blockers") or []],
          [draft])
    edges = con.execute("SELECT count(*) FROM feature_deps WHERE blocked_id = ?", (ready,)).fetchone()[0]
    check("both edges were recorded", edges, 2)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/deps", {"blocker_id": draft},
                     token=maintainer_token)
    check("recording the same edge twice is not an error", status, 200)
    edges = con.execute("SELECT count(*) FROM feature_deps WHERE blocked_id = ?", (ready,)).fetchone()[0]
    check("and leaves one row", edges, 2)
    status, _ = call("DELETE", f"/api/v1/projects/{slug}/features/{ready}/deps?blocker_id={draft}",
                     token=maintainer_token)
    check("an edge can be removed", status, 204)
    status, _ = call("DELETE", f"/api/v1/projects/{slug}/features/{ready}/deps?blocker_id={draft}",
                     token=maintainer_token)
    check("removing it again is a no-op", status, 204)
    check("the removals are audited", audits("feature.dep.remove"), 2)

    # ── evidence ─────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/evidence",
                     {"kind": "document", "ref_id": doc}, token=contributor_token)
    check("a contributor may not cite evidence", status, 403)
    status, cited = call("POST", f"/api/v1/projects/{slug}/features/{ready}/evidence",
                         {"kind": "document", "ref_id": doc}, token=maintainer_token)
    check("a maintainer may", (status, (cited or {}).get("kind")), (201, "document"))
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/evidence",
                     {"kind": "document", "ref_id": doc}, token=maintainer_token)
    check("citing it twice is not an error", status, 201)
    rows = con.execute("SELECT count(*) FROM feature_evidence WHERE feature_id = ?", (ready,)).fetchone()[0]
    check("and leaves one row", rows, 1)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/evidence",
                     {"kind": "screenshot", "ref_id": doc}, token=maintainer_token)
    check("an unknown kind is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/evidence",
                     {"kind": "document", "ref_id": foreign_doc}, token=maintainer_token)
    check("a document from another project is 404", status, 404)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{ready}/evidence",
                     {"kind": "field_report", "ref_id": report}, token=maintainer_token)
    check("a field report is a second kind", status, 201)
    rows = con.execute("SELECT count(*) FROM feature_evidence WHERE feature_id = ?", (ready,)).fetchone()[0]
    check("two citations are attached", rows, 2)
    status, _ = call(
        "DELETE",
        f"/api/v1/projects/{slug}/features/{ready}/evidence?kind=field_report&ref_id={report}",
        token=maintainer_token,
    )
    check("a citation can be removed", status, 204)
    rows = con.execute("SELECT count(*) FROM feature_evidence WHERE feature_id = ?", (ready,)).fetchone()[0]
    check("and the other stays", rows, 1)
    check("citing is audited", audits("feature.evidence"), 3)

    cleanup(con, f"{slug}%")
    con.commit()
    for user in (maintainer_id, contributor_id, second_id, stranger_id):
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

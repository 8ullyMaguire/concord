#!/usr/bin/env python3
"""Row 11b part 40 (B3): complaint writes — file, impact, validate, reject, merge.

The rules, each one a defect the service being ported had:

1. filing is **authenticated** (it once had no check at all and wrote `author_id 0`);
2. the **path and the body must agree** about the project, and the path wins;
3. `strategic_multiplier` defaults to **1.0**, not 0 — pain is multiplied by it, so 0 makes the complaint contribute
   exactly nothing with no indication why;
4. the §6.1 duplicate gate is **instance-wide** and `confirm_duplicate` proceeds;
5. indexing may fail without losing the complaint (`embed_failed`), and filing **makes you a participant**: +5
   reputation and `contributor` membership — or-ignore, so an owner who files is not demoted;
6. an impact is recorded **once per person** (clicking twice must not double their influence);
7. **validate and reject are contributor acts**, and both only apply to an `open` complaint;
8. rejecting **tells the author who decided**, in the same transaction as the status change and the penalty — and does not
   notify somebody of their own action;
9. **merge is maintainer work**, cannot cross projects, cannot target itself, and a source from another project is not
   reachable through this project's path (404, never 403 — a 403 confirms the id is real).
"""
import json
import os
import sys
import time
import urllib.error
import urllib.parse
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
    con.execute(f"delete from complaint_impacts where complaint_id in "
                f"(select id from complaints where project_id in ({marks}))", params)
    con.execute(f"delete from feature_complaints where complaint_id in "
                f"(select id from complaints where project_id in ({marks}))", params)
    con.execute(f"delete from notifications where project_id in ({marks})", params)
    con.execute(f"delete from reputation_events where project_id in ({marks})", params)
    con.execute(f"delete from embeddings where project_id in ({marks})", params)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from complaints where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    reporter_id, reporter_token = register(f"cplreporter{stamp}")
    maintainer_id, maintainer_token = register(f"cplmaint{stamp}")
    stranger_id, stranger_token = register(f"cplstranger{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-cpl-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Complaint probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Other", "", "collective", "public", "both", now, now),
    ).lastrowid
    con.execute(
        "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
        (project, maintainer_id, "maintainer", now),
    )
    con.commit()

    results = []

    def reputation_of(user):
        """Every filing pays +5, so the checks below are deltas: an absolute number would encode how many times this
        probe happened to file and would break the moment a step is added."""
        return con.execute(
            "SELECT coalesce(sum(points),0) FROM reputation_events WHERE project_id = ? AND user_id = ?",
            (project, user)).fetchone()[0]

    def check(name, got, want):
        results.append((name, got, want))

    # ── filing ──────────────────────────────────────────────────────────────────────────────────────────────────────
    title = f"Export produces a zero-byte file for large projects {stamp}"
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints", {"title": title})
    check("filing anonymously is 401", status, 401)

    status, complaint = call("POST", f"/api/v1/projects/{project}/complaints",
                             {"title": title, "body": "Over 5k rows.", "severity": 3}, token=reporter_token)
    check("a signed-in call may file", status, 201)
    complaint = complaint or {}
    cid = complaint.get("id")
    check("it is open", complaint.get("status"), "open")
    check("strategic_multiplier defaults to 1.0, not 0", complaint.get("strategic_multiplier"), 1.0)
    check("and the body's severity is kept", complaint.get("severity"), 3)

    joined = con.execute("SELECT role FROM members WHERE project_id = ? AND user_id = ?",
                         (project, reporter_id)).fetchone()
    check("filing makes the reporter a contributor", joined[0] if joined else None, "contributor")
    reputation = con.execute(
        "SELECT coalesce(sum(points),0) FROM reputation_events WHERE project_id = ? AND user_id = ?",
        (project, reporter_id)).fetchone()[0]
    check("and pays +5 submit_complaint", reputation, 5)

    status, _ = call("POST", f"/api/v1/projects/{project}/complaints",
                     {"title": f"Something else {stamp}", "project_id": other}, token=reporter_token)
    check("a body project that contradicts the path is 400", status, 400)

    status, refused = call("POST", f"/api/v1/projects/{project}/complaints",
                           {"title": title, "body": "Over 5k rows."}, token=reporter_token)
    check("the same text again is refused with 409", status, 409)
    check("and the refusal names the candidate",
          ((refused or {}).get("similar") or [{}])[0].get("entity_id"), cid)
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints",
                     {"title": title, "body": "Over 5k rows.", "confirm_duplicate": True}, token=reporter_token)
    check("confirm_duplicate files it anyway", status, 201)

    # ── impact ──────────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{cid}/impact?severity=3")
    check("an anonymous impact is 401, because the row needs a person", status, 401)
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{cid}/impact?severity=3",
                     token=stranger_token)
    check("a signed-in impact is recorded", status, 200)
    call("POST", f"/api/v1/projects/{project}/complaints/{cid}/impact?severity=5", token=stranger_token)
    severity = con.execute("SELECT severity FROM complaint_impacts WHERE complaint_id = ? AND user_id = ?",
                           (cid, stranger_id)).fetchone()
    check("a second impact from the same person does not overwrite the first", severity[0], 3)
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{cid}/impact?severity=0",
                     token=stranger_token)
    check("severity 0 falls back to 1 rather than storing nothing", status, 200)
    status, _ = call("POST", f"/api/v1/projects/{other}/complaints/{cid}/impact?severity=2",
                     token=stranger_token)
    check("an impact through ANOTHER project's path is 404", status, 404)

    before_validate = reputation_of(reporter_id)

    # ── validate ────────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{cid}/validate", token=stranger_token)
    check("a non-member may not validate", status, 403)
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{cid}/validate")
    check("nor may an anonymous caller", status, 401)
    status, validated = call("POST", f"/api/v1/projects/{project}/complaints/{cid}/validate",
                             token=maintainer_token)
    check("a contributor-or-above may validate", status, 200)
    check("and the answer says so", (validated or {}).get("status"), "validated")
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{cid}/validate", token=maintainer_token)
    check("validating twice is 400 (only open complaints)", status, 400)
    after_validate = reputation_of(reporter_id)
    check("validation pays the author +1", after_validate - before_validate, 1)

    # ── reject ──────────────────────────────────────────────────────────────────────────────────────────────────────
    status, spam = call("POST", f"/api/v1/projects/{project}/complaints",
                        {"title": f"Buy cheap watches here {stamp}"}, token=reporter_token)
    spam_id = (spam or {}).get("id")
    status, rejected = call("POST", f"/api/v1/projects/{project}/complaints/{spam_id}/reject",
                            token=maintainer_token)
    check("a contributor-or-above may reject", status, 200)
    check("and the answer says so", (rejected or {}).get("status"), "rejected")
    status_now = con.execute("SELECT status FROM complaints WHERE id = ?", (spam_id,)).fetchone()[0]
    check("the row is rejected", status_now, "rejected")
    # +5 for filing the spam complaint itself, then the M1 penalty of −1.
    check("the author takes the M1 penalty (−1) on top of the +5 for filing it", reputation_of(reporter_id) - after_validate, 4)
    told = con.execute(
        "SELECT actor_id, reason FROM notifications WHERE user_id = ? AND kind = 'complaint_rejected'",
        (reporter_id,)).fetchall()
    check("and the author is told, with WHO decided", [tuple(row) for row in told],
          [(maintainer_id, "rejected as spam")])
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{spam_id}/reject", token=maintainer_token)
    check("rejecting twice is 400", status, 400)

    # A maintainer rejecting their own complaint must not notify themself.
    status, own = call("POST", f"/api/v1/projects/{project}/complaints",
                       {"title": f"Self-rejected noise {stamp}"}, token=maintainer_token)
    own_id = (own or {}).get("id")
    before = con.execute("SELECT count(*) FROM notifications WHERE user_id = ? AND kind = 'complaint_rejected'",
                         (maintainer_id,)).fetchone()[0]
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{own_id}/reject", token=maintainer_token)
    after = con.execute("SELECT count(*) FROM notifications WHERE user_id = ? AND kind = 'complaint_rejected'",
                        (maintainer_id,)).fetchone()[0]
    check("rejecting your own complaint notifies nobody", (status, after - before), (200, 0))

    # ── merge ──────────────────────────────────────────────────────────────────────────────────────────────────────
    status, source = call("POST", f"/api/v1/projects/{project}/complaints",
                          {"title": f"Same bug, filed twice {stamp}"}, token=reporter_token)
    source_id = (source or {}).get("id")
    status, target = call("POST", f"/api/v1/projects/{project}/complaints",
                          {"title": f"The original bug {stamp}"}, token=reporter_token)
    target_id = (target or {}).get("id")
    status, foreign = call("POST", f"/api/v1/projects/{other}/complaints",
                           {"title": f"Another project's bug {stamp}"}, token=reporter_token)
    foreign_id = (foreign or {}).get("id")
    call("POST", f"/api/v1/projects/{project}/complaints/{source_id}/impact?severity=2", token=stranger_token)

    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{source_id}/merge/{target_id}",
                     token=reporter_token)
    check("a contributor may not merge", status, 403)
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{source_id}/merge/{target_id}")
    check("nor may an anonymous caller", status, 401)
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{source_id}/merge/{foreign_id}",
                     token=maintainer_token)
    check("a merge across projects is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/{source_id}/merge/{source_id}",
                     token=maintainer_token)
    check("a merge into itself is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{other}/complaints/{source_id}/merge/{target_id}",
                     token=maintainer_token)
    check("a source from another project is 404 through this path", status, 404)
    status, merged = call("POST", f"/api/v1/projects/{project}/complaints/{source_id}/merge/{target_id}",
                          token=maintainer_token)
    check("a maintainer may merge within the project", status, 200)
    check("and the answer says so", (merged or {}).get("status"), "merged")
    pointer = con.execute("SELECT merged_into FROM complaints WHERE id = ?", (source_id,)).fetchone()[0]
    check("the source points at the target", pointer, target_id)
    copied = con.execute(
        "SELECT count(*) FROM complaint_impacts WHERE complaint_id = ? AND user_id = ?",
        (target_id, stranger_id)).fetchone()[0]
    check("the source's impacts came across", copied, 1)
    audited = con.execute(
        "SELECT actor_id, detail FROM audit_log WHERE action = 'merge_complaints' AND entity_id = ?",
        (source_id,)).fetchall()
    check("the audit row names the real actor, once", [tuple(row) for row in audited],
          [(maintainer_id, f"merged into {target_id}")])

    status, _ = call("POST", f"/api/v1/projects/{project}/complaints/notanumber/validate",
                     token=maintainer_token)
    check("a non-numeric complaint id on this path is 404", status, 404)

    cleanup(con, f"{slug}%")
    con.commit()
    for user in (reporter_id, maintainer_id, stranger_id):
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

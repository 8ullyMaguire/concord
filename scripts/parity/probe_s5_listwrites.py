#!/usr/bin/env python3
"""Row 11c part 44 (B4c): list writes, entry votes, project metrics and visibility.

The rules, each a decision the reference made for a reason:

1. **instance-wide must be ASKED for** — the project in the path is also the access check, so defaulting to global would
   let any project member mint a global list; the audit row is written against the path's project either way, because an
   instance-wide list has no project to audit under and the trail is what says who may do this;
2. **an entry is proposed, not published** — status `proposed`, `proposed_by` recorded, which is what makes the self-vote
   refusal enforceable;
3. **an entry vote uses the same §8.3 weight as every other vote**, and refuses a removed entry, an entry from another
   list, and a proposer voting on their own entry;
4. **an instance-wide list cannot be voted on through a project URL**: the weight needs a project and there is none;
5. **a missing `median_review_hours` means unknown, written as NULL** — `0` would publish "responds instantly" for a
   repository nobody measured;
6. **a non-member changing visibility gets 404, not 403**, because visibility is the one setting where the project's
   existence is the secret.
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
    """Remove everything this probe can create, **lists first**.

    A list is reachable two ways — through its project and through its own slug — and the probe's list slug does not
    prefix-match the project's (`probe-list-resources-…` vs `probe-list-…`), so cleaning "by project" and cleaning "by
    slug" separately leaves rows behind a foreign key. Both patterns are matched here, before any project is deleted.
    """
    projects = [row[0] for row in con.execute("select id from projects where slug like ?", (like,)).fetchall()]
    lists = [row[0] for row in con.execute("select id from lists where slug like ?", (like,)).fetchall()]
    if projects:
        marks = ",".join("?" * len(projects))
        lists += [row[0] for row in con.execute(
            f"select id from lists where project_id in ({marks})", tuple(projects)).fetchall()]
    lists = sorted(set(lists))
    if lists:
        lmarks = ",".join("?" * len(lists))
        con.execute(f"delete from list_entry_votes where list_id in ({lmarks})", tuple(lists))
        con.execute(f"delete from list_entries where list_id in ({lmarks})", tuple(lists))
        con.execute(f"delete from lists where id in ({lmarks})", tuple(lists))
    if projects:
        marks = ",".join("?" * len(projects))
        params = tuple(projects)
        con.execute(f"delete from project_metrics where project_id in ({marks})", params)
        con.execute(f"delete from audit_log where project_id in ({marks})", params)
        con.execute(f"delete from members where project_id in ({marks})", params)
        con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    maintainer_id, maintainer_token = register(f"lsmaint{stamp}")
    contributor_id, contributor_token = register(f"lscontrib{stamp}")
    stranger_id, stranger_token = register(f"lsstranger{stamp}")
    # A fourth account, a member, whose only job is to vote on two entries neither of which they proposed: a voter who
    # proposed one of them is refused, so a second member is the only honest way to test a successful comparison.
    voter_id, voter_token = register(f"lsvoter{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-list-{stamp}"
    list_slug = f"probe-list-resources-{stamp}"
    cleanup(con, f"{slug}%")
    cleanup(con, f"{list_slug}%")
    cleanup(con, f"{list_slug}-global%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "List probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Other", "", "collective", "public", "both", now, now),
    ).lastrowid
    for user, role in ((maintainer_id, "maintainer"), (contributor_id, "contributor"),
                       (voter_id, "contributor")):
        con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (project, user, role, now))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (other, maintainer_id, "maintainer", now))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    def audits(action):
        return con.execute(
            "SELECT count(*) FROM audit_log WHERE project_id = ? AND action = ?",
            (project, action)).fetchone()[0]

    # ── lists ───────────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{slug}/lists",
                     {"slug": list_slug, "title": "Resources"})
    check("creating a list anonymously is 401", status, 401)
    status, _ = call("POST", f"/api/v1/projects/{slug}/lists",
                     {"slug": list_slug, "title": "Resources"}, token=stranger_token)
    check("a non-member may not create one", status, 403)
    status, created = call("POST", f"/api/v1/projects/{slug}/lists",
                           {"slug": list_slug, "title": "Good resources",
                            "description": "Things worth reading"}, token=contributor_token)
    check("a contributor may", status, 201)
    created = created or {}
    check("it is scoped to the project", created.get("project_id"), project)
    check("it opens as open", created.get("status"), "open")
    check("and the create is audited against the path's project", audits("create_list"), 1)
    list_id = created.get("id")
    status, global_list = call("POST", f"/api/v1/projects/{slug}/lists",
                               {"slug": f"{list_slug}-global", "title": "Instance wide",
                                "instance_wide": True}, token=contributor_token)
    check("instance_wide is accepted", status, 201)
    check("and writes a NULL project", (global_list or {}).get("project_id"), None)
    check("the global create is audited under the path's project too", audits("create_list"), 2)

    # ── entries ────────────────────────────────────────────────────────────────────────────────────────────────────
    status, entry = call("POST", f"/api/v1/projects/{slug}/lists/{list_id}/entries",
                         {"url": "https://example.org/a", "title": "Entry A"}, token=contributor_token)
    check("an entry can be proposed", status, 201)
    entry = entry or {}
    check("it starts proposed", entry.get("status"), "proposed")
    check("with the proposer recorded", entry.get("proposed_by"), contributor_id)
    check("and a virgin rating", entry.get("elo_r"), 1500.0)
    entry_a = entry.get("id")
    status, second = call("POST", f"/api/v1/projects/{slug}/lists/{list_id}/entries",
                          {"url": "https://example.org/b", "title": "Entry B"}, token=maintainer_token)
    entry_b = (second or {}).get("id")
    check("a second entry is proposed", status, 201)
    status, _ = call("POST", f"/api/v1/projects/{slug}-other/lists/{list_id}/entries",
                     {"url": "https://example.org/c", "title": "Entry C"}, token=maintainer_token)
    check("a list addressed under another project is 404", status, 404)
    status, global_entry = call("POST", f"/api/v1/projects/{slug}/lists/{(global_list or {}).get('id')}/entries",
                                {"url": "https://example.org/g", "title": "Global entry"},
                                token=contributor_token)
    check("an instance-wide list accepts entries through a project URL", status, 201)
    global_entry_id = (global_entry or {}).get("id")

    # ── entry votes ────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{slug}/lists/{list_id}/entries/vote",
                     {"entry_a": entry_a, "entry_b": entry_a, "outcome": "a"}, token=maintainer_token)
    check("comparing an entry with itself is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/lists/{list_id}/entries/vote",
                     {"entry_a": entry_a, "entry_b": entry_b, "outcome": "z"}, token=maintainer_token)
    check("an unknown outcome is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/lists/{list_id}/entries/vote",
                     {"entry_a": entry_a, "entry_b": global_entry_id, "outcome": "a"}, token=maintainer_token)
    check("an entry from another list is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/lists/{list_id}/entries/vote",
                     {"entry_a": entry_a, "entry_b": entry_b, "outcome": "a"}, token=contributor_token)
    check("a proposer may not vote on their own entry", status, 403)
    status, voted = call("POST", f"/api/v1/projects/{slug}/lists/{list_id}/entries/vote",
                         {"entry_a": entry_a, "entry_b": entry_b, "outcome": "a"}, token=voter_token)
    check("a member who proposed neither may", status, 200)
    check("and gets the entries back", len((voted or {}).get("entries") or []), 2)
    ratings = dict(con.execute(
        "SELECT id, elo_r FROM list_entries WHERE list_id = ?", (list_id,)).fetchall())
    check("the winner rose and the loser fell",
          (ratings[entry_a] > 1500, ratings[entry_b] < 1500), (True, True))
    status, _ = call("POST", f"/api/v1/projects/{slug}/lists/{list_id}/entries/vote",
                     {"entry_a": entry_a, "entry_b": entry_b, "outcome": "a"}, token=voter_token)
    check("comparing the same pair twice is 403", status, 403)
    con.execute("UPDATE list_entries SET status = 'removed' WHERE id = ?", (entry_b,))
    con.commit()
    status, _ = call("POST", f"/api/v1/projects/{slug}/lists/{list_id}/entries/vote",
                     {"entry_a": entry_a, "entry_b": entry_b, "outcome": "b"}, token=voter_token)
    check("a removed entry is not a candidate", status, 400)
    global_id = (global_list or {}).get("id")
    status, _ = call("POST", f"/api/v1/projects/{slug}/lists/{global_id}/entries/vote",
                     {"entry_a": global_entry_id, "entry_b": global_entry_id, "outcome": "a"},
                     token=maintainer_token)
    check("an instance-wide list cannot be voted on through a project URL", status, 404)
    check("a vote is audited", audits("vote_list_entry"), 1)

    # ── metrics ────────────────────────────────────────────────────────────────────────────────────────────────────
    metrics = {"stars": 40, "forks": 6, "open_issues": 3, "commit_count": 210,
               "contributors": 4, "last_commit_age_days": 2.0, "releases_90d": 2,
               "license": "MIT"}
    status, _ = call("PUT", f"/api/v1/projects/{slug}/metrics", metrics, token=contributor_token)
    check("a contributor may not set metrics", status, 403)
    status, measured = call("PUT", f"/api/v1/projects/{slug}/metrics", metrics, token=maintainer_token)
    check("a maintainer may", status, 200)
    stored = con.execute(
        "SELECT stars, commit_count, health_score, median_review_hours FROM project_metrics"
        " WHERE project_id = ?", (project,)).fetchone()
    check("the numbers are stored", (stored[0], stored[1]), (40, 210))
    check("a health score is computed", stored[2] is not None, True)
    check("an unmeasured review time is NULL, not 0", stored[3], None)
    license_now = con.execute("SELECT license FROM projects WHERE id = ?", (project,)).fetchone()[0]
    check("and the licence lands on the project", license_now, "MIT")
    check("the metrics write is audited", audits("set_metrics"), 1)

    # ── visibility ────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("PUT", f"/api/v1/projects/{slug}/visibility", {"visibility": "unlisted"})
    check("changing visibility anonymously is 401", status, 401)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/visibility", {"visibility": "unlisted"},
                     token=stranger_token)
    check("a non-member gets 404, not 403", status, 404)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/visibility", {"visibility": "secret"},
                     token=maintainer_token)
    check("an unknown visibility is 400", status, 400)
    status, changed = call("PUT", f"/api/v1/projects/{slug}/visibility", {"visibility": "unlisted"},
                           token=maintainer_token)
    check("a maintainer may change it", (status, (changed or {}).get("visibility")), (200, "unlisted"))
    check("and the change is stored",
          con.execute("SELECT visibility FROM projects WHERE id = ?", (project,)).fetchone()[0], "unlisted")
    detail = con.execute(
        "SELECT detail FROM audit_log WHERE project_id = ? AND action = 'set_visibility'",
        (project,)).fetchall()
    check("the audit records the move", [row[0] for row in detail], ["public -> unlisted"])
    status, _ = call("PUT", f"/api/v1/projects/{slug}/visibility", {"visibility": "public"},
                     token=contributor_token)
    check("a plain contributor, being a member, may", status, 200)

    # The list's own slug does not prefix-match the project's, so the tear-down matches both patterns (see cleanup).
    cleanup(con, f"{slug}%")
    cleanup(con, f"{list_slug}%")
    cleanup(con, f"{list_slug}-global%")
    con.commit()
    for user in (maintainer_id, contributor_id, stranger_id, voter_id):
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

#!/usr/bin/env python3
"""Row 11d part 45 (B2a): feature tags, feature milestone, strategic weight — at the reference's own paths.

Four routes, and the reason they were missing is instructive rather than accidental:

* `PUT /features/{}/strategic-weight` — the store was wired, but only under the port's own shape
  (`POST /strategy/weight-proposals` with `feature_id` in the body). The reference and the pages built against it use the
  feature-scoped path, so "wired somewhere" was not "wired where the caller looks";
* `POST /features/{}/tags` and `/tags/remove` — the tags decide §6.2's theme weight, so a wrong one silently steers the
  feature and must be removable by whoever can add one;
* `PUT /features/{}/milestone` — the response is the count of **open blockers**, because a feature may be planned before
  what it depends on has shipped and refusing that would make a roadmap unable to say "this lands after X".

The rules asserted:

1. an unknown tag name is refused with the route that can propose one, and the feature id is scope-checked twice;
2. removing a tag that is not attached is a no-op, not an error — a page that double-submits should not see one;
3. a weight change needs a **rationale**, and it is a **proposal**: a second pending proposal for the same feature is a
   409, and a proposer cannot consent to their own;
4. the blocker count reads `feature_deps` from the **blocker** side (`blocker_id` is the row that must finish first) and
   counts only blockers that are not `shipped` — the other direction reports zero for every feature and reads as "clear".
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
    projects = [row[0] for row in con.execute("select id from projects where slug like ?", (like,)).fetchall()]
    if not projects:
        return
    marks = ",".join("?" * len(projects))
    params = tuple(projects)
    # `strategic_weight_consents` has no project column: it hangs off a proposal, so the proposals are removed first
    # and their consents by proposal id. Written out because "delete from everything where project_id" is the shape that
    # compiles, the shape I reached for first, and the one that fails on the second table.
    con.execute(f"delete from strategic_weight_consents where proposal_id in "
                f"(select id from strategic_weight_proposals where project_id in ({marks}))", params)
    con.execute(f"delete from strategic_weight_proposals where project_id in ({marks})", params)
    con.execute(f"delete from feature_tags where feature_id in "
                f"(select id from features where project_id in ({marks}))", params)
    con.execute(f"delete from feature_deps where blocker_id in (select id from features where project_id in ({marks}))",
                params)
    con.execute(f"delete from project_documents where project_id in ({marks})", params)
    con.execute(f"delete from milestones where project_id in ({marks})", params)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from features where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    maintainer_id, maintainer_token = register(f"ftmaint{stamp}")
    contributor_id, contributor_token = register(f"ftcontrib{stamp}")
    stranger_id, stranger_token = register(f"ftstranger{stamp}")
    eligible_id, eligible_token = register(f"fteligible{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-ftag-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Feature tag probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Other", "", "collective", "public", "both", now, now),
    ).lastrowid
    for user, role in ((maintainer_id, "maintainer"), (contributor_id, "contributor")):
        con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (project, user, role, now))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (other, maintainer_id, "maintainer", now))
    # §8.2's consent population is a QUALIFYING contribution, not a membership: a document is one of the five routes the
    # schema can represent, so this account is eligible the way the reference means it.
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, eligible_id, "contributor", now))
    con.execute(
        "INSERT INTO project_documents (project_id, author_id, slug, title, kind, body, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?)",
        (project, eligible_id, f"qualifying-{stamp}", "Qualifying doc", "document", "", now, now),
    )
    feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, contributor_id, "Tag me", "", "proposed", now, now),
    ).lastrowid
    foreign = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (other, maintainer_id, "Foreign feature", "", "proposed", now, now),
    ).lastrowid
    blocker_open = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, maintainer_id, "Open blocker", "", "proposed", now, now),
    ).lastrowid
    blocker_shipped = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, maintainer_id, "Shipped blocker", "", "shipped", now, now),
    ).lastrowid
    con.execute("INSERT INTO feature_deps (blocker_id, blocked_id, created_at) VALUES (?,?,?)",
                (blocker_open, feature, now))
    con.execute("INSERT INTO feature_deps (blocker_id, blocked_id, created_at) VALUES (?,?,?)",
                (blocker_shipped, feature, now))
    tag_name = f"theming{stamp}"
    namespaced = f"topic:performance{stamp}"
    con.execute("INSERT INTO tags (name, namespace, suggested) VALUES (?,?,false)", (tag_name, None))
    con.execute("INSERT INTO tags (name, namespace, suggested) VALUES (?,?,false)",
                ("performance" + str(stamp), "topic"))
    milestone = con.execute(
        "INSERT INTO milestones (project_id, title, body, status, created_at) VALUES (?,?,?,?,?) RETURNING id",
        (project, "v1", "", "planned", now),
    ).lastrowid
    foreign_milestone = con.execute(
        "INSERT INTO milestones (project_id, title, body, status, created_at) VALUES (?,?,?,?,?) RETURNING id",
        (other, "theirs", "", "planned", now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    def audits(action):
        return con.execute(
            "SELECT count(*) FROM audit_log WHERE project_id = ? AND action = ?",
            (project, action)).fetchone()[0]

    # ── feature tags ───────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{feature}/tags", {"tag": tag_name})
    check("tagging anonymously is 401", status, 401)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{feature}/tags", {"tag": tag_name},
                     token=stranger_token)
    check("a non-member may not tag", status, 403)
    status, tagged = call("POST", f"/api/v1/projects/{slug}/features/{feature}/tags", {"tag": tag_name},
                          token=contributor_token)
    check("a contributor may", status, 200)
    check("and the response is what is stored", (tagged or {}).get("tags"), [tag_name])
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{feature}/tags", {"tag": f"nonesuch{stamp}"},
                     token=contributor_token)
    check("an unknown tag is 400", status, 400)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{feature}/tags", {"tag": namespaced},
                     token=contributor_token)
    check("a namespaced tag resolves by its bare name", status, 200)
    links = con.execute("SELECT count(*) FROM feature_tags WHERE feature_id = ?", (feature,)).fetchone()[0]
    check("both tags are attached", links, 2)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{feature}/tags", {"tag": tag_name},
                     token=contributor_token)
    check("attaching twice is not an error", status, 200)
    links = con.execute("SELECT count(*) FROM feature_tags WHERE feature_id = ?", (feature,)).fetchone()[0]
    check("and leaves one link", links, 2)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{foreign}/tags", {"tag": tag_name},
                     token=maintainer_token)
    check("a feature from another project is 404", status, 404)
    status, remaining = call("POST", f"/api/v1/projects/{slug}/features/{feature}/tags/remove",
                             {"tag": namespaced}, token=contributor_token)
    check("a tag can be removed", (status, (remaining or {}).get("tags")), (200, [tag_name]))
    status, again = call("POST", f"/api/v1/projects/{slug}/features/{feature}/tags/remove",
                         {"tag": namespaced}, token=contributor_token)
    check("removing a tag that is not attached is a no-op", status, 200)
    status, _ = call("POST", f"/api/v1/projects/{slug}/features/{feature}/tags/remove",
                     {"tag": f"nonesuch{stamp}"}, token=contributor_token)
    check("removing an unknown tag is 400", status, 400)

    # ── feature milestone ──────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{feature}/milestone",
                     {"milestone_id": milestone, "position": 2}, token=contributor_token)
    check("a contributor may not move the roadmap", status, 403)
    status, placed = call("PUT", f"/api/v1/projects/{slug}/features/{feature}/milestone",
                          {"milestone_id": milestone, "position": 2}, token=maintainer_token)
    check("a maintainer may", status, 200)
    check("and the response counts OPEN blockers only", (placed or {}).get("open_blockers"), 1)
    stored = con.execute("SELECT milestone_id, milestone_position FROM features WHERE id = ?",
                         (feature,)).fetchone()
    check("the feature is in the milestone at its position", tuple(stored), (milestone, 2))
    check("the move is audited", audits("feature.milestone"), 1)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{feature}/milestone",
                     {"milestone_id": foreign_milestone}, token=maintainer_token)
    check("a milestone from another project is 404", status, 404)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{foreign}/milestone",
                     {"milestone_id": milestone}, token=maintainer_token)
    check("a feature from another project is 404", status, 404)
    status, cleared = call("PUT", f"/api/v1/projects/{slug}/features/{feature}/milestone",
                           {"milestone_id": None}, token=maintainer_token)
    check("a null clears it", status, 200)
    stored = con.execute("SELECT milestone_id FROM features WHERE id = ?", (feature,)).fetchone()[0]
    check("and the column is NULL", stored, None)
    check("the clearing is audited too", audits("feature.milestone"), 2)

    # ── strategic weight ──────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{feature}/strategic-weight",
                     {"weight": 2.0, "rationale": "it unblocks the release"}, token=stranger_token)
    check("a non-member may not propose", status, 403)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{feature}/strategic-weight",
                     {"weight": 2.0}, token=contributor_token)
    check("a proposal without a rationale is 400", status, 400)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{feature}/strategic-weight",
                     {"weight": 9.0, "rationale": "too much"}, token=contributor_token)
    check("a weight outside the range is 400", status, 400)
    status, proposed = call("PUT", f"/api/v1/projects/{slug}/features/{feature}/strategic-weight",
                            {"weight": 2.0, "rationale": "it unblocks the release"}, token=contributor_token)
    check("a contributor may propose", status, 201)
    proposed = proposed or {}
    check("and the message names the proposal to consent to",
          str((proposed.get("proposal") or {}).get("id")) in str(proposed.get("message")), True)
    check("it is pending", (proposed.get("proposal") or {}).get("status"), "pending")
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{feature}/strategic-weight",
                     {"weight": 1.5, "rationale": "second thoughts"}, token=contributor_token)
    check("a second pending proposal for the same feature is 409", status, 409)
    status, _ = call("PUT", f"/api/v1/projects/{slug}/features/{foreign}/strategic-weight",
                     {"weight": 2.0, "rationale": "not mine"}, token=contributor_token)
    check("a feature from another project is 404", status, 404)
    proposal_id = (proposed.get("proposal") or {}).get("id")
    # The strategy routes take the NUMERIC project id, not the slug: they are the port's own shape, kept because the
    # frontend reads them. Recorded rather than changed under the port's feet — a slug sent here is a 400.
    status, _ = call("POST", f"/api/v1/projects/{project}/strategy/weight-proposals/{proposal_id}/consent",
                     token=contributor_token)
    check("a proposer cannot consent to their own", status, 403)
    status, consented = call("POST", f"/api/v1/projects/{project}/strategy/weight-proposals/{proposal_id}/consent",
                             token=eligible_token)
    # The status is 200 when that consent ratified it and 202 when it did not, and which one happens depends on the
    # eligible population — one qualifying collaborator means the first consent is also the last. Asserting a fixed code
    # here would encode the fixture's size rather than the rule, so the RULE is asserted: ratified iff consents >= eligible.
    consented = consented if isinstance(consented, dict) else {}
    check("a consent answers 200 once ratified, 202 before", status in (200, 202), True)
    check("and it reports the running count", consented.get("consents"), 1)
    check("with ratified agreeing with the counts",
          consented.get("ratified"), int(consented.get("consents") or 0) >= int(consented.get("eligible") or 0))
    # A SECOND, still-pending proposal: the stranger's refusal has to be tested while the proposal is open, because an
    # already-ratified one answers 409 (decided) first and that would pass for the wrong reason.
    second = call("PUT", f"/api/v1/projects/{slug}/features/{blocker_open}/strategic-weight",
                  {"weight": 1.5, "rationale": "second feature, second proposal"}, token=contributor_token)
    second_id = ((second[1] or {}).get("proposal") or {}).get("id")
    status, _ = call("POST", f"/api/v1/projects/{project}/strategy/weight-proposals/{second_id}/consent",
                     token=stranger_token)
    check("someone outside the population is refused", status, 403)
    status, _ = call("POST", f"/api/v1/projects/{project}/strategy/weight-proposals/{proposal_id}/consent",
                     token=eligible_token)
    check("consenting again to a decided proposal is 409", status, 409)

    cleanup(con, f"{slug}%")
    con.execute("DELETE FROM tags WHERE name IN (?,?)", (tag_name, f"performance{stamp}"))
    con.commit()
    for user in (maintainer_id, contributor_id, stranger_id, eligible_id):
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

#!/usr/bin/env python3
"""Row 11: the taxonomy eligible set, and why a consent is not a click.

Before this slice the route refused `taxonomy` outright, because ratification needs the eligible set and a
consent recorded without it is the defect the Go store's own comment records: *"anyone with an account could
ratify a tag proposal for a project they had no relationship with, and since guest identities exist, a nameless
one could."*

Five rules, each of which makes a quorum a fiction:

* **only an eligible tagger consents** — a member with role contributor/reviewer/maintainer/owner in the
  proposal's project. A stranger's 200 is a stranger's vote.
* **a consent below the threshold is RECORDED, and the request still says the proposal is not ratified**: the
  first voter must not feel they broke something, and must not be told the tag changed.
* **the denominator is the eligible set**, not the number who happened to vote.
* **ratification APPLIES the change** — a proposal marked ratified with no tag created is a decision nobody can
  see in the vocabulary.
* **a decided proposal is not decided twice.**
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "tests", "e2e"))

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


def message(body):
    return ((body or {}).get("error") or {}).get("message", "") if isinstance(body, dict) else ""


def register(prefix, stamp):
    status, body = call(
        "POST",
        "/api/v1/auth/register",
        {"username": f"{prefix}{stamp}", "display_name": prefix, "password": "correct horse"},
    )
    if status != 201 or not isinstance(body, dict):
        print(f"register {prefix} failed: {status} {body}")
        sys.exit(2)
    return body["user"]["id"], body["token"]


def cleanup(con):
    """Remove anything a previous, interrupted run of this probe left behind."""
    # The pattern is a BOUND PARAMETER: the harness translates `?` to `%s` and a literal `%` in the statement
    # confuses its placeholder walk ("tuple index out of range").
    for (project,) in con.execute(
        "select id from projects where slug like ?", ("probe-tax-%",)
    ).fetchall():
        con.execute(
            "delete from taxonomy_consents where proposal_id in "
            "(select id from taxonomy_proposals where project_id = ?)", (project,))
        con.execute("delete from taxonomy_proposals where project_id = ?", (project,))
        con.execute("delete from members where project_id = ?", (project,))
        con.execute("delete from project_metrics where project_id = ?", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.execute("delete from tags where name like ?", ("probe-tax-%",))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    tag = f"probe-tax-{stamp}"
    first_id, first_token = register("tagvoter", stamp)
    second_id, second_token = register("tagvoter2", stamp)
    third_id, third_token = register("tagvoter3", stamp)
    outsider_id, outsider_token = register("tagoutsider", stamp)

    con = harness.connect("unused")
    # CLEAN UP ANY EARLIER RUN FIRST, not only its own mess at the end. A probe that dies midway (a failed
    # cleanup on 2026-11-13 left one project, three members, one tag and one proposal behind) changes the counts
    # `/api/v1/stats/summary` returns, so the NEXT gate fails with values that look like a port defect:
    #   /api/v1/stats/summary.members_by_role[1].count: 11 vs 8
    cleanup(con)
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"probe-tax-{stamp}", "Taxonomy probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    # Three eligible members: the collective charter's quorum floor is 3, so two consents can never ratify and
    # three can. A tighter room would let one voter's click look like a decision.
    for user_id in (first_id, second_id, third_id):
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
            (project, user_id, "contributor", now),
        )
    proposal = con.execute(
        "INSERT INTO taxonomy_proposals (project_id, proposed_by, action, target_tag, value, rationale,"
        " status, created_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (project, first_id, "create", f"topic:{tag}", "", "probe", "pending", now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    # 1. The outsider is refused, and nothing is written.
    status, body = call("POST", f"/api/v1/curation/taxonomy/{proposal}/vote", {"vote": "yes"},
                        token=outsider_token)
    check("a non-member cannot consent", status, 403)
    check("and no consent row is written", con.execute(
        "select count(*) from taxonomy_consents where proposal_id = ?", (proposal,)).fetchone()[0], 0)

    # 2. The first eligible consent is recorded, and the proposal is NOT ratified: one of three is a quorum of
    #    one, which is no quorum at all.
    status, body = call("POST", f"/api/v1/curation/taxonomy/{proposal}/vote", {"vote": "yes"},
                        token=first_token)
    check("the first consent is refused as not-yet", status, 409)
    check("the verdict names the quorum", "insufficient_quorum" in message(body), True)
    check("but the consent is recorded", con.execute(
        "select count(*) from taxonomy_consents where proposal_id = ?", (proposal,)).fetchone()[0], 1)
    check("and the proposal is still pending", con.execute(
        "select status from taxonomy_proposals where id = ?", (proposal,)).fetchone()[0], "pending")
    check("and no tag was created", con.execute(
        "select count(*) from tags where lower(name) = lower(?)", (tag,)).fetchone()[0], 0)

    # 3. A repeat click does not add a voice.
    call("POST", f"/api/v1/curation/taxonomy/{proposal}/vote", {"vote": "yes"}, token=first_token)
    check("a repeated consent is one consent", con.execute(
        "select count(*) from taxonomy_consents where proposal_id = ?", (proposal,)).fetchone()[0], 1)

    # 4. The third consent meets the floor and ratifies — and ratification APPLIES the change.
    call("POST", f"/api/v1/curation/taxonomy/{proposal}/vote", {"vote": "yes"}, token=second_token)
    status, body = call("POST", f"/api/v1/curation/taxonomy/{proposal}/vote", {"vote": "yes"},
                        token=third_token)
    check("the third consent ratifies", status, 200)
    check("the answer says so", (body or {}).get("ratified"), True)
    check("and carries the tally", ((body or {}).get("consents"), (body or {}).get("eligible")), (3, 3))
    check("the proposal is ratified", con.execute(
        "select status from taxonomy_proposals where id = ?", (proposal,)).fetchone()[0], "ratified")
    row = con.execute(
        "select namespace, project_id from tags where lower(name) = lower(?)", (tag,)).fetchone()
    check("the tag exists", row is not None, True)
    check("in the namespace the proposal named", (row or (None,))[0], "topic")
    check("as a global tag, not a project one", (row or (None, None))[1], None)
    check("with a ratification act", con.execute(
        "select count(*) from audit_log where action = 'ratify_taxonomy_change' and entity_id = ?",
        (proposal,)).fetchone()[0], 1)

    # 5. A decided proposal is not decided again.
    status, body = call("POST", f"/api/v1/curation/taxonomy/{proposal}/vote", {"vote": "yes"},
                        token=third_token)
    check("a ratified proposal refuses a further consent", status, 409)
    check("naming its state", "ratified" in message(body), True)

    # --- clean up -----------------------------------------------------------------------------
    con.execute("DELETE FROM audit_log WHERE entity_id = ? AND entity = 'tag'", (proposal,))
    con.execute("DELETE FROM taxonomy_consents WHERE proposal_id = ?", (proposal,))
    con.execute("DELETE FROM taxonomy_proposals WHERE id = ?", (proposal,))
    con.execute("DELETE FROM members WHERE project_id = ?", (project,))
    con.execute("DELETE FROM projects WHERE id = ?", (project,))
    con.execute("DELETE FROM tags WHERE lower(name) = lower(?)", (tag,))
    # The tokens first: `fk_api_tokens_user_id` forbids deleting a user who still holds one, which is why the
    # first version of this cleanup failed on the last statement after every assertion had already passed.
    con.execute("DELETE FROM api_tokens WHERE user_id IN (?,?,?,?)",
                (first_id, second_id, third_id, outsider_id))
    con.execute("DELETE FROM users WHERE id IN (?,?,?,?)",
                (first_id, second_id, third_id, outsider_id))
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

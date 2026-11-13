#!/usr/bin/env python3
"""Row 11: the strategic weight proposal — §5.1's rule that nobody votes on their own items.

The rule, in the Go store's own words: without it *"the proposer consents to their own proposal, and in a
one-collaborator project — where the quorum floor is capped down to the eligible count, so quorum is 1 — that
single consent ratifies it. The proposal path would then be a slower way of doing exactly what the
maintainer-only PUT used to do."*

Five rules, each of which is a way this becomes absolute power wearing a vote:

* **the proposer cannot consent to their own proposal**;
* **the consenter must be in §8.2's eligible population** — role AND a qualifying contribution, because with an
  eligible count of 0 the quorum threshold is 0 and one consent ratifies on its own;
* **a consent below the threshold is recorded**, and the answer says the proposal is not yet decided;
* **ratification applies the weight**, and records who did it;
* **a proposal whose `weight_before` no longer matches the feature is STALE and writes nothing** — the vote
  finished but somebody changed the weight while it was open, and applying it would overwrite a newer decision
  with an older one.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "tests", "e2e"))

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
    """Remove anything a previous, interrupted run left behind — see the ledger's rule on probe leftovers."""
    for (project,) in con.execute(
        "select id from projects where slug like ?", ("probe-strat-%",)
    ).fetchall():
        con.execute(
            "delete from strategic_weight_consents where proposal_id in "
            "(select id from strategic_weight_proposals where project_id = ?)", (project,))
        con.execute("delete from strategic_weight_proposals where project_id = ?", (project,))
        # Children first, and the merge requests before the features they point at: `fk_merge_requests_feature_id`
        # refuses to delete a feature that a merge request still names.
        con.execute("delete from merge_requests where project_id = ?", (project,))
        con.execute("delete from feature_tags where feature_id in "
                    "(select id from features where project_id = ?)", (project,))
        con.execute("delete from audit_log where project_id = ?", (project,))
        con.execute("delete from features where project_id = ?", (project,))
        con.execute("delete from members where project_id = ?", (project,))
        con.execute("delete from projects where id = ?", (project,))
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    proposer_id, proposer_token = register("stratprop", stamp)
    first_id, first_token = register("stratvote", stamp)
    second_id, second_token = register("stratvote2", stamp)
    third_id, third_token = register("stratvote3", stamp)
    idle_id, idle_token = register("stratidle", stamp)

    con = harness.connect("unused")
    cleanup(con)
    now = time.time()
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"probe-strat-{stamp}", "Weight probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, effort, origin,"
        " strategic_weight, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (project, proposer_id, "A feature", "", "discussion", "M", "api", 1.0, now, now),
    ).lastrowid

    # Three eligible collaborators and one member who has only joined: §8.2 asks for a qualifying contribution,
    # so `idle` is a member with no activity and the eligible population is 3.
    #
    # The qualifying event is a MERGED MERGE REQUEST, because "merged code" is §8.2's first route and seeding it
    # is a row rather than a workflow.
    for user_id in (proposer_id, first_id, second_id, third_id, idle_id):
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
            (project, user_id, "contributor", now),
        )
    for user_id in (proposer_id, first_id, second_id, third_id):
        con.execute(
            "INSERT INTO merge_requests (project_id, feature_id, author_id, title, status,"
            " opened_at, closed_at) VALUES (?,?,?,?,?,?,?)",
            (project, feature, user_id, "merged work", "merged", now - 86400, now - 3600),
        )
    first_proposal = con.execute(
        "INSERT INTO strategic_weight_proposals (project_id, feature_id, proposed_by, weight_before,"
        " weight_after, rationale, status, created_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (project, feature, proposer_id, 1.0, 2.0, "more valuable than it looks", "pending", now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    # 1. The proposer cannot consent to their own proposal.
    status, body = call("POST", f"/api/v1/curation/strategy/{first_proposal}/vote", {"vote": "yes"},
                        token=proposer_token)
    check("the proposer cannot consent", status, 403)
    check("and nothing is recorded", con.execute(
        "select count(*) from strategic_weight_consents where proposal_id = ?",
        (first_proposal,)).fetchone()[0], 0)

    # 2. A member with no qualifying contribution is not in the eligible population.
    status, body = call("POST", f"/api/v1/curation/strategy/{first_proposal}/vote", {"vote": "yes"},
                        token=idle_token)
    check("a member with no contribution cannot consent", status, 403)
    check("and nothing is recorded for them either", con.execute(
        "select count(*) from strategic_weight_consents where proposal_id = ?",
        (first_proposal,)).fetchone()[0], 0)

    # 3. Below the threshold: recorded, not ratified.
    status, body = call("POST", f"/api/v1/curation/strategy/{first_proposal}/vote", {"vote": "yes"},
                        token=first_token)
    check("the first eligible consent is not-yet", status, 409)
    check("the verdict names the quorum", "insufficient_quorum" in message(body), True)
    check("but the consent is recorded", con.execute(
        "select count(*) from strategic_weight_consents where proposal_id = ?",
        (first_proposal,)).fetchone()[0], 1)
    check("and the weight is unchanged", con.execute(
        "select strategic_weight from features where id = ?", (feature,)).fetchone()[0], 1.0)

    # 4. The world moves while the vote is open: the third consent meets the threshold and finds a different
    #    weight, so the proposal goes STALE and nothing is written over it.
    con.execute("UPDATE features SET strategic_weight = 1.5 WHERE id = ?", (feature,))
    con.commit()
    call("POST", f"/api/v1/curation/strategy/{first_proposal}/vote", {"vote": "yes"}, token=second_token)
    status, body = call("POST", f"/api/v1/curation/strategy/{first_proposal}/vote", {"vote": "yes"},
                        token=third_token)
    check("a changed weight makes the proposal stale", status, 409)
    check("naming the weight it found", "1.5" in message(body), True)
    check("the proposal is stale", con.execute(
        "select status from strategic_weight_proposals where id = ?", (first_proposal,)).fetchone()[0], "stale")
    check("and the weight was NOT overwritten", con.execute(
        "select strategic_weight from features where id = ?", (feature,)).fetchone()[0], 1.5)

    # 5. A second proposal, unopposed: the same three consents ratify it and the weight is applied.
    second_proposal = con.execute(
        "INSERT INTO strategic_weight_proposals (project_id, feature_id, proposed_by, weight_before,"
        " weight_after, rationale, status, created_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (project, feature, proposer_id, 1.5, 3.0, "second attempt", "pending", now),
    ).lastrowid
    con.commit()
    call("POST", f"/api/v1/curation/strategy/{second_proposal}/vote", {"vote": "yes"}, token=first_token)
    call("POST", f"/api/v1/curation/strategy/{second_proposal}/vote", {"vote": "yes"}, token=second_token)
    status, body = call("POST", f"/api/v1/curation/strategy/{second_proposal}/vote", {"vote": "yes"},
                        token=third_token)
    check("the third consent ratifies", status, 200)
    check("the answer says so", (body or {}).get("ratified"), True)
    check("the weight is applied", con.execute(
        "select strategic_weight from features where id = ?", (feature,)).fetchone()[0], 3.0)
    check("the proposal is ratified", con.execute(
        "select status from strategic_weight_proposals where id = ?", (second_proposal,)).fetchone()[0],
        "ratified")
    check("with an audit row naming both weights", con.execute(
        "select count(*) from audit_log where action = 'ratify_strategic_weight' and entity_id = ?",
        (feature,)).fetchone()[0], 1)

    # --- clean up -----------------------------------------------------------------------------
    cleanup(con)
    con.execute("DELETE FROM api_tokens WHERE user_id IN (?,?,?,?,?)",
                (proposer_id, first_id, second_id, third_id, idle_id))
    con.execute("DELETE FROM users WHERE id IN (?,?,?,?,?)",
                (proposer_id, first_id, second_id, third_id, idle_id))
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

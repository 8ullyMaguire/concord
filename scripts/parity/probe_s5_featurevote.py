#!/usr/bin/env python3
"""Row 11d part 51 (B2c): §5.1's FEATURE vote — what to build, not which solution.

Four rules, and the third is the one that differs from the solution vote:

1. **both features must be in this project** — a feature from another project cannot be spent here;
2. **an author cannot rank their own feature, in either position** (§5.2). Both are checked because the pair is unordered as
   presented, and the message names the reason: a bare "permission denied" for an action the interface offered is
   indistinguishable from a bug;
3. **the weight ignores the member's role.** The reference computes `VoteWeight(reputation, 1.0, cap)`: a maintainer's
   opinion about which feature matters is worth what their reputation says, not what their title says. The solution vote
   *does* multiply the role in — the two are deliberately different, and this asserts it rather than assuming it;
4. **one live vote per voter per pair.** The reference relied on `INSERT OR REPLACE` and a unique index that **does not exist
   in this schema**, so the replacement is done by hand here; a second row from one voter on one pair would be a second
   rating move for one opinion.

A first vote also **enrols** the voter, so the join cannot be a precondition of voting.
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
    # Votes FIRST: `pairwise_votes.feature_a` and `.project_id` are foreign keys, so a feature cannot go before the votes that
    # name it. (This probe learned that the hard way, at cleanup time, after every assertion had passed.)
    arenas = [row[0] for row in con.execute(
        f"select id from arenas where project_id in ({marks})", params).fetchall()]
    if arenas:
        amarks = ",".join("?" * len(arenas))
        aparams = tuple(arenas)
        con.execute(f"delete from pairwise_votes where arena_id in ({amarks})", aparams)
        con.execute(f"delete from arena_entries where arena_id in ({amarks})", aparams)
        con.execute(f"delete from arenas where id in ({amarks})", aparams)
    con.execute(f"delete from pairwise_votes where project_id in ({marks})", params)
    features = [row[0] for row in con.execute(
        f"select id from features where project_id in ({marks})", params).fetchall()]
    if features:
        fmarks = ",".join("?" * len(features))
        fparams = tuple(features)
        con.execute(f"delete from features where id in ({fmarks})", fparams)
    con.execute(f"delete from reputation_events where project_id in ({marks})", params)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    author_id, _author_token = register(f"fauthor{stamp}")
    voter_id, voter_token = register(f"fvoter{stamp}")
    keeper_id, keeper_token = register(f"fkeeper{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-fvote-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Feature vote probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    other_project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Elsewhere", "", "collective", "public", "both", now, now),
    ).lastrowid

    def feature(project_id, author, title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?) RETURNING id",
            (project_id, author, title, "", "proposed", now, now),
        ).lastrowid

    left = feature(project, author_id, "Fast and boring")
    right = feature(project, author_id, "Slow and clever")
    mine = feature(project, voter_id, "The voter's own idea")
    outside = feature(other_project, author_id, "Not in this project")
    # The voter keeps a member role, so "the role is not a multiplier" can be tested against a maintainer.
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, keeper_id, "maintainer", now))
    con.execute("UPDATE users SET created_at = to_timestamp(?) WHERE id IN (?, ?)",
                (now - 30 * 86400, voter_id, keeper_id))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    path = f"/api/v1/projects/{slug}/features/{left}/vote"

    # ── refusals ─────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", path, {"b": right, "outcome": "a"})
    check("voting anonymously is 401", status, 401)
    status, _ = call("POST", path, {"b": right}, token=voter_token)
    check("an outcome is required", status, 400)
    status, _ = call("POST", path, {"b": outside, "outcome": "a"}, token=voter_token)
    check("a feature from another project cannot be spent here", status, 403)
    status, refused = call("POST", path, {"b": mine, "outcome": "a"}, token=voter_token)
    check("an author cannot rank their own feature", status, 403)
    check("and the refusal says why", "proposed" in json.dumps(refused), True)
    status, _ = call("POST", path, {"b": left, "outcome": "a"}, token=voter_token)
    check("a feature cannot be compared against itself", status, 400)
    check("nothing was enrolled by a refused vote",
          con.execute("SELECT count(*) FROM members WHERE project_id = ? AND user_id = ?",
                      (project, voter_id)).fetchone()[0], 0)

    def rating(feature_id):
        return con.execute("SELECT elo_r, elo_rd FROM features WHERE id = ?", (feature_id,)).fetchone()

    # ── the vote ────────────────────────────────────────────────────────────────────────────────────────────────
    before_left, before_right = rating(left), rating(right)
    status, voted = call("POST", path, {"b": right, "outcome": "a"}, token=voter_token)
    check("a voter may compare two features", status, 200)
    weight = ((voted or {}).get("vote") or {}).get("weight")
    check("the weight is a real one for an account with history",
          isinstance(weight, (int, float)) and weight > 0.5, True)
    after_left, after_right = rating(left), rating(right)
    check("the winner's rating rises", after_left[0] > before_left[0], True)
    check("the loser's rating falls", after_right[0] < before_right[0], True)
    check("the voter is enrolled by voting",
          con.execute("SELECT count(*) FROM members WHERE project_id = ? AND user_id = ?",
                      (project, voter_id)).fetchone()[0], 1)
    row = con.execute(
        "SELECT v.entity_type, v.feature_a, v.feature_b, v.a, v.b, v.project_id, a.type"
        " FROM pairwise_votes v JOIN arenas a ON a.id = v.arena_id"
        " WHERE v.voter_id = ? ORDER BY v.id DESC LIMIT 1", (voter_id,)).fetchone()
    check("the row carries BOTH shapes and the priority arena",
          row, ("feature", left, right, left, right, project, "feature-priority"))
    check("the vote earns a reputation point",
          con.execute("SELECT coalesce(sum(points),0) FROM reputation_events WHERE project_id = ? AND user_id = ?"
                      " AND kind = 'vote'", (project, voter_id)).fetchone()[0], 1.0)

    # ── one live vote per pair ──────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", path, {"b": right, "outcome": "b"}, token=voter_token)
    check("a repeat vote is accepted", status, 200)
    rows = con.execute(
        "SELECT count(*) FROM pairwise_votes WHERE voter_id = ? AND entity_type = 'feature'",
        (voter_id,)).fetchone()[0]
    check("but there is still ONE row for it", rows, 1)
    outcome = con.execute(
        "SELECT outcome FROM pairwise_votes WHERE voter_id = ? AND entity_type = 'feature'",
        (voter_id,)).fetchone()[0]
    check("and the row now says what the voter last said", outcome, "b")

    # ── a skip moves nothing ────────────────────────────────────────────────────────────────────────────────────
    before = (rating(left), rating(right))
    status, skip_body = call("POST", path, {"b": right, "outcome": "skip"}, token=keeper_token)
    skip_vote = ((skip_body or {}).get("vote") or {})
    check("a skip is accepted", status, 200)
    check("and moves no rating", (rating(left), rating(right)), before)

    # ── the role is NOT a multiplier ───────────────────────────────────────────────────────────────────────────
    # The SKIP above was the keeper's FIRST vote, so its weight is their weight at zero reputation — the same starting point the
    # voter had. Comparing 1.301 with 1.0 would have compared a reputation that changed in between: a vote earns a point, and
    # a point is exactly what this formula reads.
    keeper_weight = (skip_vote or {}).get("weight")
    check("a maintainer's FIRST vote weighs what a member's first vote weighs — the role is not a multiplier",
          round(keeper_weight, 9), round(weight, 9))

    cleanup(con, f"{slug}%")
    con.commit()
    for user in (author_id, voter_id, keeper_id):
        con.execute("DELETE FROM api_tokens WHERE user_id = ?", (user,))
        con.execute("DELETE FROM users WHERE id = ?", (user,))
    con.commit()
    con.close()

    failures = sum(1 for _, got, want in results if got != want)
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

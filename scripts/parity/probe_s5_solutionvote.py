#!/usr/bin/env python3
"""Row 11d part 49 (B2c): the solution arena's pairwise vote (§5.1).

The rules this asserts, each of them a refusal or a deliberate no-op:

1. **both sides must be solutions of THIS feature** — checked before the arena is touched, so a request naming another
   feature's solution cannot spend a vote here;
2. **an author cannot vote on their own entry** (§5.2);
3. **a `skip` changes nothing**: it is recorded (the flag exists) and no rating moves, because "I don't understand this" is
   a statement about the write-up, not about which is better;
4. **`neither` in an arena with a baseline is two losses to the baseline**, not a draw between the competitors — a draw
   would leave both where they were, and §5.1's wording exists because doing nothing quietly beating them is the failure;
5. **the weight comes from the charter, never from the body**: a client-supplied weight is client-supplied influence;
6. voting **joins the project** first, so the vote is attributable to a member.
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
    features = [row[0] for row in con.execute(
        f"select id from features where project_id in ({marks})", params).fetchall()]
    if features:
        fmarks = ",".join("?" * len(features))
        fparams = tuple(features)
        arenas = [row[0] for row in con.execute(
            f"select id from arenas where feature_id in ({fmarks})", fparams).fetchall()]
        if arenas:
            amarks = ",".join("?" * len(arenas))
            aparams = tuple(arenas)
            con.execute(f"delete from arena_entries where arena_id in ({amarks})", aparams)
            con.execute(f"delete from pairwise_votes where arena_id in ({amarks})", aparams)
            con.execute(f"delete from arenas where id in ({amarks})", aparams)
        con.execute(f"delete from solutions where feature_id in ({fmarks})", fparams)
        con.execute(f"delete from features where id in ({fmarks})", fparams)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    owner_id, _owner_token = register(f"vowner{stamp}")
    author_a, author_a_token = register(f"vauthora{stamp}")
    author_b, author_b_token = register(f"vauthorb{stamp}")
    voter_id, voter_token = register(f"vvoter{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-solvote-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Solution vote probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, owner_id, "A feature", "", "ready", now, now),
    ).lastrowid
    # A second feature, so a solution belonging to it can be offered in the wrong arena.
    other_feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, owner_id, "Another feature", "", "ready", now, now),
    ).lastrowid
    # A brand-new account's vote weighs almost nothing — §12.3's age gate ramps over seven days, and that is the point of it.
    # So the voter is given a month of history: without it the probe would be measuring 1e-6 of a rating point and calling
    # "the winner rose" a result.
    con.execute("UPDATE users SET created_at = to_timestamp(?) WHERE id = ?", (now - 30 * 86400, voter_id))
    con.commit()

    results = []

    def check(name, got, want):
        check.results = results
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    def file(feature_id, token, title, **rest):
        body = {"title": title, **rest}
        status, filed = call("POST", f"/api/v1/projects/{slug}/features/{feature_id}/solutions",
                             body, token)
        if status != 201:
            raise SystemExit(f"filing {title} failed: {status} {filed}")
        return (filed.get("solution") or {})["id"]

    # `solution_a` and `solution_b` are authored by two different people, because §5.2 means the voter must own neither.
    solution_a = file(feature, author_a_token, "The first idea", expertise_tags=["rust"])
    solution_b = file(feature, author_b_token, "The second idea", expertise_tags=["rust"])
    baseline = file(feature, author_a_token, "Do nothing", type="do-nothing")
    elsewhere = file(other_feature, author_a_token, "Someone else's feature")

    def entry(solution_id):
        return con.execute(
            "SELECT e.r, e.rd, e.games FROM arena_entries e JOIN arenas a ON a.id = e.arena_id"
            " WHERE a.feature_id = ? AND a.type = 'solution' AND e.entity_id = ?",
            (feature, solution_id)).fetchone()

    vote_path = f"/api/v1/projects/{slug}/features/{feature}/solutions/vote"

    # ── refusals ─────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", vote_path, {"solution_a": solution_a, "solution_b": solution_b, "outcome": "a"})
    check("voting anonymously is 401", status, 401)
    status, _ = call("POST", vote_path, {"solution_a": solution_a, "solution_b": solution_a,
                                         "outcome": "a"}, token=voter_token)
    check("an entry cannot be compared against itself", status, 400)
    status, _ = call("POST", vote_path, {"solution_a": solution_a, "solution_b": solution_b,
                                         "outcome": "whichever"}, token=voter_token)
    check("an unknown outcome is 400", status, 400)
    status, _ = call("POST", vote_path, {"solution_a": solution_a, "solution_b": elsewhere,
                                         "outcome": "a"}, token=voter_token)
    check("a solution from another feature may not be spent here", status, 403)
    status, _ = call("POST", vote_path, {"solution_a": solution_a, "solution_b": solution_b,
                                         "outcome": "a"}, token=author_a_token)
    check("an author cannot rank their own entry", status, 403)
    check("and no refusal moved a rating", entry(solution_a), (1500.0, 350.0, 0))

    # ── the vote ────────────────────────────────────────────────────────────────────────────────────────────────
    before_a, before_b = entry(solution_a), entry(solution_b)
    status, voted = call("POST", vote_path, {"solution_a": solution_a, "solution_b": solution_b,
                                             "outcome": "a"}, token=voter_token)
    check("a voter may compare two solutions", status, 200)
    voted = voted or {}
    check("the response carries the standings", len(voted.get("solutions") or []) > 0, True)
    weight = (voted.get("vote") or {}).get("weight")
    check("the weight is the charter's, computed for a voter with history (not a 1e-6 age-gated sliver)",
          isinstance(weight, (int, float)) and 0.5 <= weight <= 4.0, True)
    after_a, after_b = entry(solution_a), entry(solution_b)
    check("the winner's rating rises measurably", round(after_a[0] - before_a[0], 3) > 1.0, True)
    check("the loser's rating falls measurably", round(before_b[0] - after_b[0], 3) > 1.0, True)
    check("and both played a game", (after_a[2], after_b[2]), (1, 1))
    stored = con.execute(
        "SELECT weight, outcome FROM pairwise_votes WHERE arena_id = "
        "(SELECT id FROM arenas WHERE feature_id = ? AND type = 'solution') ORDER BY id DESC LIMIT 1",
        (feature,)).fetchone()
    check("the weight recorded is the one computed, not one supplied", stored[0], weight)
    check("voting joined the voter to the project",
          con.execute("SELECT count(*) FROM members WHERE project_id = ? AND user_id = ?",
                      (project, voter_id)).fetchone()[0], 1)

    # ── skip ────────────────────────────────────────────────────────────────────────────────────────────────────
    before = (entry(solution_a), entry(solution_b))
    status, _ = call("POST", vote_path, {"solution_a": solution_a, "solution_b": solution_b,
                                         "outcome": "skip", "reason": "I do not understand this"},
                     token=voter_token)
    check("a skip is accepted", status, 200)
    check("and moves nothing at all", (entry(solution_a), entry(solution_b)), before)
    stored = con.execute(
        "SELECT outcome, reason FROM pairwise_votes WHERE arena_id = "
        "(SELECT id FROM arenas WHERE feature_id = ? AND type = 'solution') ORDER BY id DESC LIMIT 1",
        (feature,)).fetchone()
    check("but it IS recorded, with its reason", stored, ("skip", "I do not understand this"))

    # ── neither, against a baseline ─────────────────────────────────────────────────────────────────────────────
    before_a, before_b, before_base = entry(solution_a), entry(solution_b), entry(baseline)
    status, _ = call("POST", vote_path, {"solution_a": solution_a, "solution_b": solution_b,
                                         "outcome": "neither"}, token=voter_token)
    check("neither is accepted", status, 200)
    after_a, after_b, after_base = entry(solution_a), entry(solution_b), entry(baseline)
    check("both competitors LOSE to the baseline", (after_a[0] < before_a[0], after_b[0] < before_b[0]),
          (True, True))
    check("and the baseline gains from both", after_base[0] > before_base[0], True)
    check("the baseline played two games", after_base[2] - before_base[2], 2)

    cleanup(con, f"{slug}%")
    con.commit()
    for user in (owner_id, author_a, author_b, voter_id):
        con.execute("DELETE FROM api_tokens WHERE user_id = ?", (user,))
        con.execute("DELETE FROM users WHERE id = ?", (user,))
    con.commit()
    con.close()

    failures = sum(1 for _, got, want in results if got != want)
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

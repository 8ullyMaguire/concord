#!/usr/bin/env python3
"""Row 11 part 58: the contribution feed and the two reads it stands on.

Three JSON routes (`GET /contribute/next`, `GET /projects/{}/work-items`, `GET /projects/{}/votes/next`) and the membership write
that `/votes/next` performs as a side effect (`POST /projects/{}/join`) — batched because they are one piece of plumbing: the feed
ranks *work items* with the finder's own engine, `votes/next` is the same project's closest-rated unjudged pair, and both refuse
or enrol a caller through the same membership rule.

What the probe has to establish:

1. **a claimed task is never offered** — offering a newcomer work somebody is already doing is the single most discouraging thing
   a contribution feed can do, and it is invisible unless the fixture has one claimed and one unclaimed item;
2. **the feed is the finder, not a second ranker** — the same `language` answer that narrows the finder must narrow the feed, and
   it must narrow it through the engine's own rules: an item whose language is UNKNOWN stays (with its fit lowered), because
   "unknown" is not "no";
3. **health is a tiebreak and never a filter**, and an unmeasured project never outranks a measured one on the strength of having
   no data — asserted by building two projects whose items fit identically;
4. **the feed says why** each item is there, and an item with no matching fact says it is unknown rather than claiming it fits;
5. **`/votes/next` asks about the closest-rated pair** — the two features the arena cannot yet tell apart — skips pairs this voter
   already judged, and distinguishes "too few features" from "you have compared everything";
6. **`/votes/next` enrols the caller**, which is the reference's behaviour and the reason `/join` exists as a route of its own:
   a visitor who is asked to compare two features and then votes has taken part, and a 403 at the vote would be an invitation
   followed by a refusal;
7. **`/join` is idempotent and says the same thing to a member and to a guest** — the caller's goal is "I can take part", and
   telling a non-member they were already enrolled leaks membership state for nothing.
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
# otherwise, and that is the reference's behaviour, not a detail of this probe.
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
    """Remove this run's projects, children first. Every table that carries a project id is attempted separately so one unlucky
    reference does not abandon the rest — a fixture left behind breaks the NEXT run."""
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
        for statement in (
            "delete from pairwise_votes where feature_a in ({f}) or feature_b in ({f})",
            "delete from feature_deps where feature_id in ({f}) or blocker_id in ({f})",
            "delete from feature_complaints where feature_id in ({f})",
            "delete from work_item_claims where feature_id in ({f})",
            "delete from features where id in ({f})",
        ):
            try:
                con.execute(statement.format(f=fmarks), fparams)
                con.commit()
            except Exception:
                con.rollback()
    for statement in (
        "delete from board_cards where column_id in (select id from board_columns where project_id in ({m}))",
        "delete from board_columns where project_id in ({m})",
        "delete from members where project_id in ({m})",
        "delete from audit_log where project_id in ({m})",
        "delete from arenas where project_id in ({m})",
        "delete from projects where id in ({m})",
    ):
        try:
            con.execute(statement.format(m=marks), params)
            con.commit()
        except Exception:
            con.rollback()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    member_id, member_token = register(f"cfmember{stamp}")
    other_id, other_token = register(f"cfother{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-feed-{stamp}"
    slot = slug
    cleanup(con, f"{slug}%")

    def project(name, health=None):
        pid = con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
            " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (name, name, "", "collective", "public", "both", None, None, now, now),
        ).lastrowid
        if health is not None:
            # Two signals only: the score is written, not computed, because that is what every other surface reads.
            con.execute(
                "INSERT INTO project_metrics (project_id, health_score, computed_at)"
                " VALUES (?,?,?)", (pid, health, now))
        con.execute("INSERT INTO project_languages (project_id, language, pct) VALUES (?,?,?)",
                    (pid, "rust", 100.0))
        return pid

    healthy = project(f"{slug}-healthy", 0.9)
    unmeasured = project(f"{slug}-unmeasured")
    empty = project(f"{slug}-empty")

    def feature(pid, author, title, difficulty=None, minutes=None, mentor=0, status="draft"):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, body, status, difficulty,"
            " time_budget_minutes, needs_mentor, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
            (pid, author, title, "", status, difficulty, minutes, mentor, now, now),
        ).lastrowid

    # One unclaimed item in each project with the SAME shape, so the health tiebreak is the only thing that can order them; one
    # claimed item that must never be offered; and two features in `healthy` for the pair route.
    free_healthy = feature(healthy, other_id, "Port the lexer", "M", 60, 0)
    free_unmeasured = feature(unmeasured, other_id, "Port the parser", "M", 60, 0)
    claimed = feature(healthy, other_id, "Write the changelog", "S", 15, 1)
    # The claim table names the FEATURE it claims: a work item IS a feature in this schema, and the column is `feature_id`.
    con.execute(
        "INSERT INTO work_item_claims (feature_id, user_id, claimed_at) VALUES (?,?,?)",
        (claimed, other_id, now))
    paired_a = feature(healthy, other_id, "Alternative one", "M", 60, 0)
    paired_b = feature(healthy, other_id, "Alternative two", "M", 60, 0)
    # Every feature gets an explicit rating: the pair rule picks the two whose ratings are closest, so a NULL left to its default
    # would join the nearest pair by accident and make the assertion below pass for the wrong reason.
    con.execute("UPDATE features SET elo_r = ? WHERE id = ?", (1500.0, paired_a))
    con.execute("UPDATE features SET elo_r = ? WHERE id = ?", (1501.0, paired_b))
    con.execute("UPDATE features SET elo_r = ? WHERE id = ?", (1200.0, free_healthy))
    con.execute("UPDATE features SET elo_r = ? WHERE id = ?", (1000.0, claimed))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (healthy, other_id, "maintainer", now))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    # ── the work item list ───────────────────────────────────────────────────────────────────────────────────────
    status, body = call("GET", f"/api/v1/projects/{slug}-healthy/work-items")
    check("anyone may read a public project's work items", status, 200)
    items = body or []
    check("it lists every item, claimed or not", len(items), 4)
    titles = [row.get("title") for row in items]
    check("in the reference's order (by id)", titles[:2], ["Port the lexer", "Write the changelog"])
    first = items[0]
    # `claimed_by` and `time_budget_minutes` are omitted when they are null (a field that is absent is not a field that is false),
    # so the assertion is on the union of keys the rows carry, not on one row's.
    carrying = set().union(*(set(row.keys()) for row in items))
    check("the rows carry what a worker needs to decide", sorted(carrying),
          ["claimed_by", "difficulty", "id", "impact_ratio", "kind", "needs_mentor", "project_id",
           "status", "time_budget_minutes", "title"])
    check("the kind is `feature`", first.get("kind"), "feature")
    check("an unclaimed item has no claimer",
          [row.get("claimed_by") for row in items if row.get("title") == "Port the lexer"], [None])
    check("a claimed item names the holder",
          [row.get("claimed_by") for row in items if row.get("title") == "Write the changelog"], [other_id])
    status, _ = call("GET", f"/api/v1/projects/{slug}-nope/work-items")
    check("an unknown project is 404", status, 404)

    # ── joining ──────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"/api/v1/projects/{slug}-healthy/join")
    check("joining anonymously is 401", status, 401)
    status, body = call("POST", f"/api/v1/projects/{slug}-healthy/join", token=member_token)
    check("a member may join", status, 200)
    check("and is told the role they now hold", (body or {}).get("role"), "contributor")
    check("joining is idempotent",
          call("POST", f"/api/v1/projects/{slug}-healthy/join", token=member_token)[0], 200)
    check("there is ONE membership row, not two",
          con.execute("SELECT count(*) FROM members WHERE project_id = ? AND user_id = ?",
                      (healthy, member_id)).fetchone()[0], 1)
    # A member already on the project gets the same answer as a newcomer: their goal is "I can take part".
    status, body = call("POST", f"/api/v1/projects/{slug}-healthy/join", token=other_token)
    check("a project's own maintainer gets the same 200", status, 200)
    check("and their actual role, not `contributor`", (body or {}).get("role"), "maintainer")
    status, _ = call("POST", f"/api/v1/projects/{slug}-nope/join", token=member_token)
    check("joining a project that does not exist is 404", status, 404)
    check("joining is audited",
          con.execute("SELECT count(*) FROM audit_log WHERE project_id = ? AND action = 'join_project'",
                      (healthy,)).fetchone()[0] >= 1, True)

    # ── the next pair ────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("GET", f"/api/v1/projects/{slug}-healthy/votes/next")
    check("asking for a pair anonymously is 401", status, 401)
    status, body = call("GET", f"/api/v1/projects/{slug}-empty/votes/next", token=member_token)
    check("a project with fewer than two features is 409, not a 500", status, 409)
    check("and says what is missing",
          "features" in json.dumps(body or {}), True)
    check("asking for a pair enrols the caller — the reference joins before it shows anything",
          con.execute("SELECT count(*) FROM members WHERE project_id = ? AND user_id = ?",
                      (empty, member_id)).fetchone()[0], 1)

    status, body = call("GET", f"/api/v1/projects/{slug}-healthy/votes/next", token=member_token)
    body = body or {}
    check("a project with features offers a pair", status, 200)
    pair = {body.get("feature_a", {}).get("id"), body.get("feature_b", {}).get("id")}
    check("and its sides are two different features", len(pair), 2)
    # The closest-rated pair is (1500, 1501); the 1200 feature is further away and must not appear.
    check("the pair is the two the arena cannot tell apart, not just any two",
          pair, {paired_a, paired_b})
    check("the same voter asking twice gets the same pair",
          call("GET", f"/api/v1/projects/{slug}-healthy/votes/next", token=member_token)[1].get("feature_a", {}).get("id"),
          body.get("feature_a", {}).get("id"))
    # Judge the pair through the API rather than by inserting a row: a vote is three writes in this schema (vote, arena, entries),
    # and a hand-written row would leave the arena untouched — the next pair would then be chosen from a state the product cannot
    # actually reach.
    status, _ = call("POST", f"/api/v1/projects/{slug}-healthy/features/{paired_a}/vote",
                     {"feature_a": paired_a, "feature_b": paired_b, "outcome": "a",
                      "other_feature_id": paired_b}, token=member_token)
    status, after = call("GET", f"/api/v1/projects/{slug}-healthy/votes/next", token=member_token)
    after_pair = {after.get("feature_a", {}).get("id"), after.get("feature_b", {}).get("id")} if after else set()
    check("a judged pair is not offered again", pair - after_pair, pair)

    # ── the contribution feed ───────────────────────────────────────────────────────────────────────────────────
    # The feed ranks the whole catalogue, so it is narrowed to this run's fixture first — `q` seeds the projects by text, which is
    # the same loader the finder uses. Without the seed the assertions below would be about the 50 real projects instead.
    feed = f"/api/v1/contribute/next?q={slot}&language=rust"
    status, body = call("GET", feed)
    body = body or {}
    check("the feed is public", status, 200)
    check("it answers with questions, items, count, pool and what it applied",
          sorted(body.keys()), ["applied", "count", "items", "pool", "questions"])
    check("it echoed the constraint it used", body.get("applied"), {"language": "rust"})
    offered = sorted(row.get("title") for row in body.get("items", []))
    check("the claimed item is never offered", "Write the changelog" in offered, False)
    check("every unclaimed item is offered", offered,
          sorted(["Port the lexer", "Port the parser", "Alternative one", "Alternative two"]))
    check("pool counts what was considered before the answers filtered it",
          isinstance(body.get("pool"), int) and body["pool"] >= len(body.get("items", [])), True)
    check("count is the number of items returned", body.get("count"), len(body.get("items", [])))
    check("every item carries its project and its fit",
          all({"project_slug", "project_name", "fit", "why"} <= set(row.keys()) for row in body.get("items", [])), True)
    healths = {row["project_slug"]: row for row in body.get("items", [])}
    check("a measured project reports its score",
          healths.get(f"{slug}-healthy", {}).get("health"), 0.9)
    # The distinction that matters: the FIELD is absent, not null and not zero. A client that reads a missing score as 0% would
    # render every unmeasured project as the unhealthiest in the catalogue.
    check("an unmeasured one has no score field at all, which is not zero",
          "health" in healths.get(f"{slug}-unmeasured", {"health": "present"}), False)
    check("and says so in words rather than rendering 0%",
          [row.get("health_label") for row in body.get("items", [])
           if row["project_slug"] == f"{slug}-unmeasured"][0],
          "health not measured")
    labels = {row["project_slug"]: row.get("health_label") for row in body.get("items", [])}
    check("a measured score is rendered once, by the server",
          labels.get(f"{slug}-healthy"), "health 90%")
    # The tiebreak: identical fit, one measured at 0.9 and one unmeasured. The measured one comes first.
    ordered = [row["project_slug"] for row in body.get("items", [])]
    if f"{slug}-healthy" in ordered and f"{slug}-unmeasured" in ordered:
        check("health breaks a tie — and an unmeasured project never outranks a measured one",
              ordered.index(f"{slug}-healthy") < ordered.index(f"{slug}-unmeasured"), True)
    else:
        check("both tiebreak projects are in the feed", [ordered, "both"], [[], "both"])
    check("each item explains itself with the engine's own reason, not a second description",
          all(str(row.get("why", "")).startswith(("Matches — ", "No stated fact matches", "Nothing narrows"))
              for row in body.get("items", [])), True)
    questions = {question.get("key"): question for question in body.get("questions", [])}
    check("the language question offers the languages the catalogue can satisfy",
          "rust" in [option.get("id") for option in questions.get("language", {}).get("options", [])], True)
    check("and always offers `any`", "any" in
          [option.get("id") for option in questions.get("language", {}).get("options", [])], True)

    status, body = call("GET", f"/api/v1/contribute/next?q={slot}&minutes=2h")
    check("a constraint value the engine does not know is a 400, not a silent drop", status, 400)
    check("and the refusal names the parameter",
          "minutes" in str((body or {}).get("error", (body or {}).get("message", ""))), True)
    status, body = call("GET", f"/api/v1/contribute/next?q={slot}&minutes=15")
    check("a known constraint is applied", (body or {}).get("applied"), {"minutes": "15"})
    check("and narrows the list to items at or under it",
          all(row.get("time_budget_minutes") in (None, 15) for row in (body or {}).get("items", [])), True)

    cleanup(con, f"{slug}%")

    passed = sum(1 for _, got, want in results if got == want)
    print(f"\n{passed}/{len(results)} ok", flush=True)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())

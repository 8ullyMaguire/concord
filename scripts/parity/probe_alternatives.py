#!/usr/bin/env python3
"""Row 11d part 56 (S2): alternatives arenas — the kind that ranks competing projects.

Four routes, and the reason they were worth a part of their own is that nothing else here could reach the shape: every arena in
the database was feature-priority, so the arena kind, the `project` entry entity, the rating maths and the leaderboard were all
already written and had never been exercised against this kind. The port already had all of the machinery; what it lacked was a
door.

What the probe has to establish, in the order it establishes it:

1. **the key is the use case and it is required.** An empty one is refused rather than defaulted, because the other arena shapes
   key on `use_case IS NULL` and an empty string is how a "create" becomes a silent second arena of a different kind;
2. **create is an ensure, not an insert.** The same use case twice, whitespace included, is one arena — and a page that re-submits
   its form after a timeout must not end up with two panels that look identical and rank different entries;
3. **an arena ranks OTHER projects.** Its own project cannot compete against itself, and cannot be one side of a vote;
4. **there is no baseline here.** "Do nothing" is a solution's competitor; in an alternatives arena the incumbent is the arena's
   subject. A baseline appearing would render as a legitimate ranked row;
5. **visibility is checked on the competitor, for the reader** — added unreadable is a 404, voted-on unreadable is a 404, and a
   competitor that became private after being added disappears from the panel rather than being ranked into it;
6. **the vote weight is the server's**: reputation × role, with the account-age gate last. A fresh account's vote therefore moves
   the ratings by less than one point on a 1500 scale, which is the number the probe reads back out of `pairwise_votes`;
7. **an author cannot judge their own competing project** — for a project, authorship is a maintainer/owner row in `members`,
   which is the only authorship this schema has.
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
    arenas = [row[0] for row in con.execute(
        f"select id from arenas where project_id in ({marks})", params).fetchall()]
    if arenas:
        amarks = ",".join("?" * len(arenas))
        con.execute(f"delete from pairwise_votes where arena_id in ({amarks})", tuple(arenas))
        con.execute(f"delete from arena_entries where arena_id in ({amarks})", tuple(arenas))
        con.execute(f"delete from arenas where id in ({amarks})", tuple(arenas))
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    author_id, author_token = register(f"awriter{stamp}")
    rival_id, rival_token = register(f"arival{stamp}")
    plain_id, plain_token = register(f"aplain{stamp}")
    outsider_id, outsider_token = register(f"aouter{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-alt-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Alt probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    # A second public project to rank, and a private one that only its own members may read.
    rival = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-rival", "Rival", "", "collective", "public", "both", now, now),
    ).lastrowid
    secret = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-secret", "Secret", "", "collective", "private", "both", now, now),
    ).lastrowid
    third = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-third", "Third", "", "collective", "public", "both", now, now),
    ).lastrowid
    for user, role in ((author_id, "contributor"), (plain_id, "user")):
        con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (project, user, role, now))
    # `rival` is maintained by the rival account, which is what makes "an author cannot judge their own project" testable;
    # `outsider` is not a member of the arena's project at all.
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (rival, rival_id, "maintainer", now))
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (secret, outsider_id, "contributor", now))
    # `rival` must be able to reach the vote at all before the own-project rule can be what refuses it: a member of the arena's
    # project FIRST, and a maintainer of the competing project second. Without the first row the 403 would be the role check, and
    # the probe would be asserting the right status for the wrong reason.
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, rival_id, "contributor", now))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    arenas_path = f"/api/v1/projects/{slug}/alternatives"

    # ── reading ───────────────────────────────────────────────────────────────────────────────────────────────────
    status, body = call("GET", arenas_path)
    check("anyone may read a public project's alternatives panel", status, 200)
    check("a project with no arenas says so with an empty list", (body or {}).get("arenas"), [])
    status, _ = call("GET", f"/api/v1/projects/{slug}-nope/alternatives")
    check("an unknown project is 404", status, 404)
    # A private project this caller cannot read: 404 rather than an empty panel, or the panel answers "does this exist".
    status, _ = call("GET", f"/api/v1/projects/{slug}-secret/alternatives")
    check("a private project the caller cannot read is 404, not an empty panel", status, 404)

    # ── who may create ───────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", arenas_path, {"use_case": "audit logs"})
    check("creating anonymously is 401", status, 401)
    status, _ = call("POST", arenas_path, {"use_case": "audit logs"}, token=outsider_token)
    check("a non-member is 403", status, 403)
    status, _ = call("POST", arenas_path, {"use_case": "audit logs"}, token=plain_token)
    check("a `user`-role member is 403 — an arena is contributor work", status, 403)
    status, body = call("POST", arenas_path, {"use_case": "   "}, token=author_token)
    check("an empty use case is 400", status, 400)
    check("and the refusal names the field",
          "use_case" in str((body or {}).get("error", "")), True)

    # ── create is an ensure ──────────────────────────────────────────────────────────────────────────────────────
    status, body = call("POST", arenas_path, {"use_case": "  audit logs  "}, token=author_token)
    body = body or {}
    arena = (body.get("arena") or {})
    check("a contributor may create an arena", status, 201)
    check("it is of the alternatives kind", arena.get("type"), "alternatives")
    check("the use case is trimmed and kept", arena.get("use_case"), "audit logs")
    check("and the question defaults rather than being blank",
          arena.get("question"), "Which is the better alternative?")
    arena_id = arena.get("id")
    status, again = call("POST", arenas_path, {"use_case": "audit logs"}, token=author_token)
    check("asking for the same use case again is the SAME arena, not a second one",
          ((again or {}).get("arena") or {}).get("id"), arena_id)
    status, other = call("POST", arenas_path, {"use_case": "single binary",
                                               "question": "Which do you run?"},
                         token=author_token)
    other_arena = (other or {}).get("arena") or {}
    check("a different use case is a different arena", other_arena.get("id") != arena_id, True)
    check("and its question is the caller's", other_arena.get("question"), "Which do you run?")
    check("so the project has two arenas",
          con.execute("SELECT count(*) FROM arenas WHERE project_id = ? AND type = 'alternatives'",
                      (project,)).fetchone()[0], 2)

    # ── competitors ──────────────────────────────────────────────────────────────────────────────────────────────
    entries_path = f"{arenas_path}/{arena_id}/entries"
    status, _ = call("POST", entries_path, {"project_id": rival})
    check("adding a competitor anonymously is 401", status, 401)
    status, _ = call("POST", entries_path, {"project_id": rival}, token=outsider_token)
    check("a non-member cannot add a competitor", status, 403)
    status, body = call("POST", entries_path, {"project_id": project}, token=author_token)
    check("a project cannot compete against itself", status, 400)
    check("and the refusal says so", "itself" in str((body or {}).get("error", "")), True)
    status, _ = call("POST", entries_path, {"project_id": 999999999}, token=author_token)
    check("a competitor that does not exist is 404, not a dangling entry", status, 404)
    status, _ = call("POST", entries_path, {"project_id": secret}, token=author_token)
    check("a competitor the caller cannot read cannot be added", status, 404)
    status, body = call("POST", entries_path, {"project_id": rival}, token=author_token)
    check("a contributor may add a readable competitor", status, 200)
    check("and the answer names it", (body or {}).get("project_id"), rival)
    status, _ = call("POST", entries_path, {"project_id": rival}, token=author_token)
    check("adding the same competitor twice is the same entry", status, 200)
    check("so there is one entry, not two",
          con.execute("SELECT count(*) FROM arena_entries WHERE arena_id = ? AND entity_type = 'project'",
                      (arena_id,)).fetchone()[0], 1)

    # An arena of one: the panel is still a panel, and its single entry is not a baseline.
    status, body = call("GET", arenas_path)
    panels = {panel["arena_id"]: panel for panel in (body or {}).get("arenas", [])}
    panel = panels.get(arena_id) or {}
    check("the panel lists the arena", panel.get("use_case"), "audit logs")
    entry = (panel.get("entries") or [{}])[0]
    check("with the competitor at rank 1", entry.get("rank"), 1)
    check("rated at the default prior", entry.get("rating"), 1500.0)
    check("with the default deviation", entry.get("rd"), 350.0)
    check("and no games yet", entry.get("games"), 0)
    check("named by slug", entry.get("slug"), f"{slug}-rival")
    check("and titled by the project's name", entry.get("title"), "Rival")
    check("the entry says what it is better FOR", entry.get("better_for"), "audit logs")
    check("and the other half of the pair is derived from the use case",
          entry.get("worse_for"), "worse than the leader for audit logs")
    check("there is no baseline: `do nothing` is a solution's competitor, not a project's",
          con.execute("SELECT count(*) FROM arena_entries WHERE arena_id = ? AND is_baseline",
                      (arena_id,)).fetchone()[0], 0)

    status, _ = call("POST", f"{arenas_path}/{other_arena.get('id')}/entries",
                     {"project_id": third}, token=author_token)
    check("a second arena can rank its own competitors", status, 200)
    status, body = call("GET", arenas_path)
    counts = {panel["arena_id"]: panel["entry_count"] for panel in (body or {}).get("arenas", [])}
    check("entry_count is counted per arena", counts, {arena_id: 1, other_arena.get("id"): 1})

    status, _ = call("POST", f"{arenas_path}/99999/entries", {"project_id": rival}, token=author_token)
    check("an arena that does not exist is 404", status, 404)
    # The second arena is real, but this path says it belongs to the same project — so the guard that matters is that a path
    # naming another project does not reach it.
    status, _ = call("POST", f"/api/v1/projects/{slug}-rival/alternatives/{other_arena.get('id')}/entries",
                     {"project_id": project}, token=rival_token)
    check("an arena cannot be addressed through a project that does not own it", status, 404)

    # ── votes ────────────────────────────────────────────────────────────────────────────────────────────────────
    vote_path = f"{arenas_path}/{arena_id}/vote"
    status, _ = call("POST", vote_path, {"a_project_id": rival, "b_project_id": third,
                                         "outcome": "a"})
    check("voting anonymously is 401", status, 401)
    status, _ = call("POST", vote_path, {"a_project_id": rival, "b_project_id": third,
                                         "outcome": "a"}, token=plain_token)
    check("a `user`-role member cannot vote — the weight is earned, the ballot is not free", status, 403)
    status, body = call("POST", vote_path, {"a_project_id": rival, "b_project_id": rival,
                                            "outcome": "a"}, token=author_token)
    check("a project cannot be compared with itself", status, 400)
    status, body = call("POST", vote_path, {"a_project_id": project, "b_project_id": rival,
                                            "outcome": "a"}, token=author_token)
    check("the arena's own project cannot be one side of a vote", status, 400)
    status, _ = call("POST", vote_path, {"a_project_id": rival, "b_project_id": secret,
                                         "outcome": "a"}, token=author_token)
    check("a competitor the voter cannot read cannot be judged (404)", status, 404)
    status, _ = call("POST", vote_path,
                     {"a_project_id": rival, "b_project_id": third, "outcome": "a"},
                     token=outsider_token)
    check("a non-member cannot vote", status, 403)

    # Add the third project as a competitor, then vote it into the ranking.
    status, _ = call("POST", entries_path, {"project_id": third}, token=author_token)
    check("a second competitor joins the arena", status, 200)
    status, body = call("POST", vote_path,
                        {"a_project_id": third, "b_project_id": rival, "outcome": "a",
                         "reason": "the audit trail is queryable"},
                        token=author_token)
    check("a contributor may cast a better-for/worse-for vote", status, 200)
    check("and it is recorded", isinstance((body or {}).get("vote", {}).get("id"), int), True)

    ratings = dict(con.execute(
        "SELECT entity_id, r FROM arena_entries WHERE arena_id = ? AND entity_type = 'project'",
        (arena_id,)).fetchall())
    check("the winner's rating rose", ratings[third] > 1500.0, True)
    check("the loser's rating fell", ratings[rival] < 1500.0, True)
    games = dict(con.execute(
        "SELECT entity_id, games FROM arena_entries WHERE arena_id = ? AND entity_type = 'project'",
        (arena_id,)).fetchall())
    check("both sides have one game", games, {rival: 1, third: 1})
    check("every entry keeps the same deviation after one game",
          con.execute("SELECT count(DISTINCT rd) FROM arena_entries WHERE arena_id = ?",
                      (arena_id,)).fetchone()[0], 1)

    weight = con.execute("SELECT weight FROM pairwise_votes WHERE arena_id = ? ORDER BY id DESC LIMIT 1",
                         (arena_id,)).fetchone()[0]
    # A brand-new account carries §12.3's age gate, so its move is a fraction of a point — the point of the gate is that the
    # first thing a fresh account does cannot be a large one.
    check("the stored weight is the server's, and a fresh account's is gated below 1", 0.0 < weight < 1.0, True)

    status, body = call("GET", arenas_path)
    panel = {p["arena_id"]: p for p in (body or {}).get("arenas", [])}[arena_id]
    order = [row["slug"] for row in panel["entries"]]
    check("the panel now ranks the winner first", order[0], f"{slug}-third")
    check("and renumbers the ranks", [row["rank"] for row in panel["entries"]], [1, 2])

    # ── the vote that moves nothing ─────────────────────────────────────────────────────────────────────────────
    before = dict(con.execute(
        "SELECT entity_id, r FROM arena_entries WHERE arena_id = ? AND entity_type = 'project'",
        (arena_id,)).fetchall())
    status, body = call("POST", vote_path,
                        {"a_project_id": rival, "b_project_id": third, "outcome": "skip"},
                        token=author_token)
    check("a skip is accepted", status, 200)
    after = dict(con.execute(
        "SELECT entity_id, r FROM arena_entries WHERE arena_id = ? AND entity_type = 'project'",
        (arena_id,)).fetchall())
    check("and moves no rating", after, before)
    check("the skip is still recorded, because it is a judgement about the question",
          con.execute("SELECT count(*) FROM pairwise_votes WHERE arena_id = ?", (arena_id,)).fetchone()[0], 2)

    # ── an author cannot judge their own competing project ──────────────────────────────────────────────────────
    status, body = call("POST", vote_path,
                        {"a_project_id": rival, "b_project_id": third, "outcome": "a"},
                        token=rival_token)
    check("a project's maintainer cannot vote on their own project competing", status, 403)
    check("and the refusal says whose entry it is",
          "own" in str((body or {}).get("error", "")), True)

    # CLEAN UP AT THE END AS WELL AS THE START. A fixture left behind is not a tidiness problem: the finder's differential probe
    # asserts that the two catalogues hold the same projects, so three leaked public projects per run of this probe turned that
    # probe red — a real failure, correctly blamed on the thing that caused it. Probes that write are the ones that must not
    # leave a trace, and a cleanup that only runs on the way in leaves a trace on every run that fails halfway.
    cleanup(con, f"{slug}%")

    passed = sum(1 for _, got, want in results if got == want)
    print(f"\n{passed}/{len(results)} ok", flush=True)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Row 9b: the arena surface — §6.4's scores and the three routes that read them.

Written BEFORE the routes exist, on purpose. Row 9 shipped without them because its gate never named
them, so the port could have been declared done with three 404s where the Go service answers; this probe
is the record of that, and of what each route owes.

The rules under test, each a way the route can look right and be wrong:

* **readiness is three conditions, reported individually.** "The call cannot open" with no reason is the
  failure mode §6.5's conditions exist to prevent, so `conditions` must name each one, whether it holds,
  and the actual value beside the bar.
* **a feature nobody has proposed a solution to is not an error.** It is one unmet condition saying so;
  a 404 would make "no solutions yet" indistinguishable from "no such feature".
* **the fallback chain EXCLUDES the solution the call is about.** Including it makes "fall back" a no-op
  the store has to special-case later.
* **the baseline is last.** A chain that ends in "do nothing" is the honest worst case; putting it first
  makes falling back to no-build the path of least resistance.
* **opening a call on a solution moves the solution into `consensus`.** Otherwise the board shows it
  still in solutioning while a call about it is running.

A 404 on any of these is the finding, not a skip.
"""
import json
import os
import sys
import time
import uuid
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "tests", "e2e"))

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8107")


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
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


def main():
    import harness

    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"arena{int(time.time())}",
        "display_name": "Arena",
        "password": "correct horse",
    })
    token = body.get("token") if isinstance(body, dict) else None
    if not token:
        print(f"register failed: {status} {body}")
        return 1
    author = body["user"]["id"]

    con = harness.connect("unused")
    slug = "arena-probe"
    # Children first, in dependency order, and EVERY child table named. This list has been wrong four
    # times across the probes -- each time on the second run, never the first, because the first run creates
    # the rows the second run cannot delete. The rule that stops it recurring: a probe that owns a project
    # deletes it by walking the FK graph, not by naming the tables it happens to remember.
    for drop in (
        # the arena graph, deepest first
        "delete from pairwise_votes where arena_id in (select id from arenas"
        " where project_id in (select id from projects where slug = ?))",
        "delete from arena_entries where arena_id in (select id from arenas"
        " where project_id in (select id from projects where slug = ?))",
        "delete from arenas where project_id in (select id from projects where slug = ?)",
        "delete from positions where call_id in (select id from consensus_calls"
        " where project_id in (select id from projects where slug = ?))",
        "delete from consensus_calls where project_id in (select id from projects where slug = ?)",
        "delete from solution_coverage where solution_id in (select id from solutions"
        " where feature_id in (select id from features"
        " where project_id in (select id from projects where slug = ?)))",
        "delete from solutions where feature_id in (select id from features"
        " where project_id in (select id from projects where slug = ?))",
        "delete from feature_complaints where feature_id in (select id from features"
        " where project_id in (select id from projects where slug = ?))",
        "delete from features where project_id in (select id from projects where slug = ?)",
        "delete from complaints where project_id in (select id from projects where slug = ?)",
        "delete from members where project_id in (select id from projects where slug = ?)",
        "delete from projects where slug = ?",
    ):
        con.execute(drop, (slug,))
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)",
        (slug, "Arena probe", "", "collective", "public", "both", time.time(), time.time()),
    )
    con.commit()
    project = con.execute("SELECT id FROM projects WHERE slug = ?", (slug,)).fetchone()[0]
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, author, "maintainer", time.time()))
    bare = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author, "no solutions at all", "", "discussion", time.time(), time.time()),
    ).lastrowid
    contested = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, author, "a contested idea", "", "discussion", time.time(), time.time()),
    ).lastrowid
    # Filing a solution is what CREATES the arena (§6.3), so a fixture that inserts `solutions` rows and
    # stops there has no arena to be ready in -- which is exactly how the first version of this probe
    # failed: readiness reported "no solution has been filed" for a feature with two.
    # §6.2: a feature must trace to at least one validated complaint before it can be called. The fixture
    # links one, and it also gives coverage a non-zero denominator -- with no pain the coverage score is
    # undefined, and a ranking of undefined scores proves nothing.
    complaint = con.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, severity, frequency, status,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?) RETURNING id",
        (project, author, "the pain this feature exists for", "", 3, 2, "validated",
         time.time() - 86400, time.time() - 86400),
    ).lastrowid
    con.execute("INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)",
                (contested, complaint))

    arena = con.execute(
        "INSERT INTO arenas (type, project_id, feature_id, question, created_at)"
        " VALUES ('solution', ?, ?, 'Which approach should we build?', ?) RETURNING id",
        (project, contested, time.time()),
    ).lastrowid

    def file_solution(title, kind, is_baseline):
        sid = con.execute(
            "INSERT INTO solutions (feature_id, author_id, title, body, type) VALUES (?,?,?,?,?)"
            " RETURNING id",
            (contested, author, title, "", kind),
        ).lastrowid
        # A leader that has actually won looks like this: a high rating with a small deviation, and an
        # entry old enough to be stable. All-equal entries give a 0.5 chance of beating the baseline, which
        # never reaches the 0.8 bar -- so a fixture of identical rows cannot exercise the ready path.
        rating = 1800.0 if title == "the leader" else (1520.0 if title == "the runner-up" else 1450.0)
        deviation = 30.0 if title == "the leader" else (60.0 if title == "the runner-up" else 80.0)
        age_hours = 100.0 if title == "the leader" else 10.0
        con.execute(
            "INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games,"
            " is_baseline, updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
            (arena, "solution", sid, rating, deviation, 0.06, 12 if not is_baseline else 3,
             is_baseline, time.time() - age_hours * 3600),
        )
        return sid

    leader = file_solution("the leader", "internal", 0)
    runner_up = file_solution("the runner-up", "internal", 0)
    baseline_solution = file_solution("do nothing", "do-nothing", 1)
    con.commit()

    # Three distinct voters on the leader: §6.5's second condition, and the charter's default bar.
    # Real user rows, because `pairwise_votes.voter_id` REFERENCES users and PostgreSQL enforces it (SQLite
    # never did): an id nobody owns is refused, which is the constraint doing its job.
    voters = []
    for index in range(3):
        voters.append(con.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?) RETURNING id",
            (f"arena-voter-{index}-{uuid.uuid4().hex[:6]}", f"Voter {index}", time.time()),
        ).lastrowid)
    for voter in voters:
        con.execute(
            "INSERT INTO pairwise_votes (arena_id, project_id, entity_type, a, b, voter_id, outcome,"
            " created_at) VALUES (?,?,?,?,?,?,?,?)",
            (arena, project, "solution", leader, baseline_solution, voter, "a", time.time()),
        )
    con.commit()
    con.close()

    results = []

    # 1. Readiness, for a feature with no solutions: one unmet condition, not a 404.
    status, body = call("GET", f"/api/v1/projects/{slug}/features/{bare}/solution-call")
    readiness = body if isinstance(body, dict) else {}
    results.append(("readiness for a feature with no solutions", status, 200, readiness))
    results.append(("it is not ready", readiness.get("ready"), False, readiness.get("ready")))
    conditions = readiness.get("conditions") or []
    results.append(("it names one unmet condition", len(conditions), 1, conditions))
    results.append(("the condition says there is no solution",
                    1 if conditions and not conditions[0].get("met") else 0, 1,
                    conditions[0] if conditions else None))

    # 2. Readiness for a feature with solutions: three conditions, each with its own verdict.
    status, body = call("GET", f"/api/v1/projects/{slug}/features/{contested}/solution-call")
    readiness = body if isinstance(body, dict) else {}
    conditions = readiness.get("conditions") or []
    results.append(("readiness for a contested feature", status, 200, readiness))
    results.append(("three conditions, in spec order", len(conditions), 3,
                    [condition.get("name") for condition in conditions]))
    results.append(("each condition reports a verdict",
                    1 if all("met" in condition for condition in conditions) else 0, 1,
                    conditions))
    results.append(("the leader is named", readiness.get("leader_id"), leader,
                    readiness.get("leader_id")))
    results.append(("the runner-up is named", readiness.get("runner_up_id"), runner_up,
                    readiness.get("runner_up_id")))
    results.append(("the minimum voter bar is reported",
                    1 if isinstance(readiness.get("min_voters"), int) else 0, 1,
                    readiness.get("min_voters")))

    # 3. Opening a call on the leading solution: the solution enters the decision phase.
    status, body = call("POST", f"/api/v1/projects/{slug}/features/{contested}/solution-call",
                        {"solution_id": leader}, token=token)
    opened = body if isinstance(body, dict) else {}
    results.append(("open a solution call", status, 201, opened))
    results.append(("the call names the solution", opened.get("solution_id"), leader,
                    opened.get("solution_id")))
    results.append(("the call names the feature", opened.get("feature_id"), contested,
                    opened.get("feature_id")))
    con2 = harness.connect("unused")
    row = con2.execute("select status from solutions where id = ?", (leader,)).fetchone()
    con2.close()
    results.append(("the solution moves into consensus", row[0] if row else None, "consensus",
                    row[0] if row else None))

    # 4. The fallback chain: everything except the call's own solution, baseline last.
    call_id = opened.get("id")
    status, body = call("GET", f"/api/v1/projects/{slug}/consensus/{call_id}/fallback")
    chain = (body or {}).get("fallback") if isinstance(body, dict) else None
    results.append(("the fallback chain answers", status, 200, chain))
    results.append(("the chain is keyed by the call", (body or {}).get("call_id"), call_id,
                    (body or {}).get("call_id")))
    ids = [entry.get("solution_id") for entry in (chain or [])]
    results.append(("the call's own solution is excluded", ids.count(leader), 0, ids))
    results.append(("the runner-up is in the chain", 1 if runner_up in ids else 0, 1, ids))
    # The baseline is the do-nothing competitor and belongs last. It may legitimately be absent when the
    # arena has never been seeded with one, which is why this asks about ORDER rather than presence.
    baseline = [index for index, entry in enumerate(chain or []) if entry.get("is_baseline")]
    results.append(("a baseline, when present, is last",
                    1 if not baseline or baseline == [len(chain) - 1] else 0, 1, baseline))

    # 5. A call on a feature with no solutions has no chain, and says so rather than 500.
    status, body = call("POST", f"/api/v1/projects/{slug}/consensus",
                        {"feature_id": bare, "title": "nothing to fall back to"}, token=token)
    results.append(("a call on a feature with no solutions", status, 400, message(body)))

    failed = 0
    for label, got, want, detail in results:
        ok = got == want
        if not ok:
            failed += 1
        print(f"{'ok  ' if ok else 'FAIL'} {label}: got {got!r} want {want!r}"
              + ("" if ok else f"  ({detail!r})"))
    print(f"\n{len(results) - failed}/{len(results)} ok")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())

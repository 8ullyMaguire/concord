#!/usr/bin/env python3
"""Row 10: comment threads — the four guards the schema cannot provide.

Written to fail first. Every rule below is a hole the database leaves open, so each one is a comment that
could be filed where it does not belong:

* **a thread in another project is not this project's thread.** `comments.thread_id` is a bare integer with
  no foreign key, so a comment could name a feature in project B and be filed under project A.
* **a reply's parent must be in the SAME thread.** `parent_id` is a real foreign key, so it points at a real
  row — but not necessarily one on this feature, and a reply would render nested under an unrelated comment.
* **a blank body is not a comment.** `body` is `NOT NULL` and nothing else, so `'   '` inserts and renders as
  nothing, which a reader cannot tell from a comment that failed to load.
* **an unlabelled comment is stored NULL, not `''`.** The label CHECK refuses `''`, so writing it makes every
  ordinary (unlabelled) comment fail its own constraint — and the read must still accept the NULL row.
* **one vote per person.** A second vote changes the first; a score that could be inflated by clicking twice
  is not a score.
* **only the author or a maintainer may delete**, and a delete is a tombstone, not a hole in the tree.
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


def register(prefix):
    status, body = call("POST", "/api/v1/auth/register", {
        "username": f"{prefix}{int(time.time() * 1000) % 1000000}",
        "display_name": prefix,
        "password": "correct horse",
    })
    if status != 201:
        print(f"register {prefix} failed: {status} {body}")
        sys.exit(2)
    return body["user"]["id"], body["token"]


def main():
    import harness

    author_id, author_token = register("author")
    other_id, other_token = register("stranger")
    voter_id, voter_token = register("voter")

    con = harness.connect("unused")
    now = time.time()
    slugs = {}
    projects = {}
    for name in ("alpha", "beta"):
        slug = f"{name}-{int(now) % 100000}"
        slugs[name] = slug
        projects[name] = con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
            " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
            (slug, name, "", "collective", "public", "both", now, now),
        ).lastrowid
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (projects["alpha"], author_id, "maintainer", now))
    feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (projects["alpha"], author_id, "a feature", "", "discussion", now, now),
    ).lastrowid
    foreign_feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (projects["beta"], author_id, "somebody else's feature", "", "discussion", now, now),
    ).lastrowid
    con.commit()
    con.close()

    # The Go table mounts threads on the NUMERIC project id, not the slug; a client calling the Go URL must not
    # get a 404 here, so the probe calls the same shape the page does.
    alpha = f"/api/v1/projects/{projects['alpha']}/threads"
    beta = f"/api/v1/projects/{projects['beta']}/threads"
    results = []

    # 1. Posting a comment.
    status, body = call("POST", f"{alpha}/feature/{feature}", {"body": "the first word"}, token=author_token)
    first = body if isinstance(body, dict) else {}
    results.append(("post a comment", status, 201, message(body)))
    results.append(("it is filed on the thread", first.get("thread_id"), feature, first.get("thread_id")))
    results.append(("an unlabelled comment has no label key", "label" in first, False, first.get("label")))
    results.append(("no parent means no parent key", "parent_id" in first, False, first.get("parent_id")))

    status, body = call("POST", f"{alpha}/feature/{feature}",
                        {"body": "a question", "label": "question"}, token=author_token)
    results.append(("a labelled comment", (body or {}).get("label") if isinstance(body, dict) else None,
                    "question", body))

    # 2. The read: oldest first, both present, unlabelled one readable at all.
    status, body = call("GET", f"{alpha}/feature/{feature}", token=author_token)
    rows = body if isinstance(body, list) else []
    results.append(("the thread reads", status, 200, message(body)))
    results.append(("both comments are there", len(rows), 2, rows))
    results.append(("oldest first", rows[0].get("body") if rows else None, "the first word",
                    [row.get("body") for row in rows]))

    # 3. The four guards.
    status, body = call("POST", f"{alpha}/feature/{foreign_feature}", {"body": "wrong project"},
                        token=author_token)
    results.append(("a thread in ANOTHER project is refused", status, 400, message(body)))
    results.append(("the refusal names the owner", 1 if "belongs to project" in message(body) else 0, 1,
                    message(body)))
    status, body = call("POST", f"{alpha}/feature/987654321", {"body": "no such feature"},
                        token=author_token)
    results.append(("a thread that does not exist is a 404", status, 404, message(body)))
    status, body = call("POST", f"{alpha}/feature/{feature}", {"body": "   "}, token=author_token)
    results.append(("a body of spaces is not a comment", status, 400, message(body)))
    results.append(("the refusal says a comment needs a body",
                    1 if "needs a body" in message(body) else 0, 1, message(body)))
    status, body = call("POST", f"{alpha}/feature/{feature}", {"body": "x", "label": "nonsense"},
                        token=author_token)
    results.append(("an invented label is refused", status, 400, message(body)))
    status, body = call("POST", f"{alpha}/release/{feature}", {"body": "x"}, token=author_token)
    results.append(("a release thread is refused, not faked", status, 400, message(body)))
    # NOT `feature`: in a freshly rebuilt database the first feature and the first project are both id 1,
    # and the probe then tested the pass-through branch instead of the refusal. The id must be one the
    # fixture knows is somebody else's.
    status, body = call("POST", f"{alpha}/project/{feature + 1000}", {"body": "x"}, token=author_token)
    results.append(("project threads use the project's own id", status, 400, message(body)))
    status, body = call("POST", f"{alpha}/project/{projects['alpha']}", {"body": "on the project"},
                        token=author_token)
    results.append(("a project comments on itself", status, 201, message(body)))

    # 4. Replies: same thread only.
    status, body = call("POST", f"{alpha}/feature/{feature}",
                        {"body": "a reply", "parent_id": rows[0].get("id") if rows else 0},
                        token=voter_token)
    reply = body if isinstance(body, dict) else {}
    results.append(("a reply in the same thread", status, 201, message(body)))
    results.append(("the reply names its parent", reply.get("parent_id"),
                    rows[0].get("id") if rows else None, reply.get("parent_id")))
    status, body = call("POST", f"{alpha}/feature/{feature}",
                        {"body": "a reply to nothing", "parent_id": 987654321}, token=voter_token)
    results.append(("a reply to a missing parent is a 404", status, 404, message(body)))
    status, body = call("POST", f"{beta}/project/{projects['beta']}",
                        {"body": "cross", "parent_id": rows[0].get("id") if rows else 0},
                        token=voter_token)
    results.append(("a parent on another thread is refused", status, 400, message(body)))

    # 5. Votes: one per person.
    comment_id = rows[0].get("id") if rows else 0
    status, body = call("POST", f"{alpha}/feature/{feature}/{comment_id}/vote", {"value": 1},
                        token=voter_token)
    results.append(("vote up", (body or {}).get("score") if isinstance(body, dict) else None, 1, body))
    status, body = call("POST", f"{alpha}/feature/{feature}/{comment_id}/vote", {"value": 1},
                        token=voter_token)
    results.append(("voting twice does not double the score",
                    (body or {}).get("score") if isinstance(body, dict) else None, 1, body))
    status, body = call("POST", f"{alpha}/feature/{feature}/{comment_id}/vote", {"value": 1},
                        token=other_token)
    results.append(("a second person adds one",
                    (body or {}).get("score") if isinstance(body, dict) else None, 2, body))
    status, body = call("POST", f"{alpha}/feature/{feature}/{comment_id}/vote", {"value": -1},
                        token=voter_token)
    # two voters: this one flips +1 to -1, the other stays +1. The sum is 0, and the point of the assertion is
    # that it is not -1 (a second row) and not 3 (an addition), both of which a broken upsert would produce.
    results.append(("changing your vote moves the score, not adds to it",
                    (body or {}).get("score") if isinstance(body, dict) else None, 0, body))
    status, body = call("POST", f"{alpha}/feature/{feature}/{comment_id}/vote", {"value": 7},
                        token=voter_token)
    results.append(("an invented vote value is refused", status, 400, message(body)))

    # 6. Deleting: author or maintainer only, and it is a tombstone.
    status, body = call("DELETE", f"{alpha}/feature/{feature}/{comment_id}", token=other_token)
    results.append(("a stranger cannot delete a comment", status, 400, message(body)))
    results.append(("the refusal names the rule",
                    1 if "author or a maintainer" in message(body) else 0, 1, message(body)))
    status, body = call("DELETE", f"{alpha}/feature/{feature}/{comment_id}", token=author_token)
    results.append(("the author can delete it", status, 200, message(body)))
    status, body = call("GET", f"{alpha}/feature/{feature}", token=author_token)
    remaining = body if isinstance(body, list) else []
    results.append(("the deleted comment is gone from the thread", len(remaining), 2, remaining))
    con = harness.connect("unused")
    tomb = con.execute("select deleted_at is not null from comments where id = ?",
                       (comment_id,)).fetchone()
    con.close()
    results.append(("it is a tombstone, not a hole", tomb[0] if tomb else None, True, tomb))

    # 7. The curation vote: reviewing an act is reviewer work, and an actor cannot review their own act.
    con = harness.connect("unused")
    act_by_other = con.execute(
        "INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (projects["alpha"], other_id, "arena.remove", "feature", feature, '{"reason":"spam"}', now),
    ).lastrowid
    act_by_author = con.execute(
        "INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (projects["alpha"], author_id, "arena.remove", "feature", feature, '{"reason":"spam"}', now),
    ).lastrowid
    plain_act = con.execute(
        "INSERT INTO audit_log (project_id, actor_id, action, entity, entity_id, detail, created_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (projects["alpha"], other_id, "project.update", "project", projects["alpha"], "{}", now),
    ).lastrowid
    con.commit()
    con.close()

    status, body = call("POST", f"/api/v1/curation/act/{act_by_other}/vote", {"vote": "yes"},
                        token=author_token)
    results.append(("a maintainer may endorse an act", status, 200, message(body)))
    results.append(("the vote is counted", (body or {}).get("yes") if isinstance(body, dict) else None,
                    1, body))
    status, body = call("POST", f"/api/v1/curation/act/{act_by_other}/vote", {"vote": "no"},
                        token=author_token)
    results.append(("changing your mind replaces the vote",
                    ((body or {}).get("yes"), (body or {}).get("no")) if isinstance(body, dict) else None,
                    (0, 1), body))
    status, body = call("POST", f"/api/v1/curation/act/{act_by_author}/vote", {"vote": "yes"},
                        token=author_token)
    results.append(("an actor cannot review their own act", status, 403, message(body)))
    status, body = call("POST", f"/api/v1/curation/act/{act_by_other}/vote", {"vote": "yes"},
                        token=voter_token)
    results.append(("a voter who is not a reviewer cannot review", status, 403, message(body)))
    status, body = call("POST", f"/api/v1/curation/act/{plain_act}/vote", {"vote": "yes"},
                        token=author_token)
    results.append(("an ordinary edit is not reviewable", status, 400, message(body)))
    status, body = call("POST", f"/api/v1/curation/act/{act_by_other}/vote", {"vote": "maybe"},
                        token=author_token)
    results.append(("a vote that is neither is refused", status, 400, message(body)))
    # TaxONOMY IS PORTED as of row 11's part 6, so the assertion changed with it — and the change is the point:
    # a proposal id that does not exist is a 404 ("no such proposal"), NOT a recorded consent and not the old
    # "not ported yet" 400. The rule this line exists to protect is that an unported OR unknown kind never
    # records a consent, so the check is on the unknown kind beside it.
    status, body = call("POST", "/api/v1/curation/taxonomy/1/vote", {"vote": "yes"},
                        token=author_token)
    results.append(("a missing proposal is a 404, not a recorded consent", status, 404, message(body)))
    status, body = call("POST", "/api/v1/curation/nonsense/1/vote", {"vote": "yes"},
                        token=author_token)
    results.append(("an unknown kind is refused", status, 400, message(body)))
    results.append(("and names the kind",
                    1 if "nonsense" in message(body) else 0, 1, message(body)))

    con = harness.connect("unused")
    con.execute("delete from act_reviews where audit_id in (select id from audit_log where project_id = ?)",
                (projects["alpha"],))
    con.execute("delete from comment_votes where comment_id in (select id from comments"
                " where project_id = ?)", (projects["alpha"],))
    con.execute("delete from comments where project_id in (?, ?)",
                (projects["alpha"], projects["beta"]))
    con.execute("delete from features where project_id in (?, ?)",
                (projects["alpha"], projects["beta"]))
    con.execute("delete from members where project_id = ?", (projects["alpha"],))
    con.execute("delete from audit_log where project_id in (?, ?)",
                (projects["alpha"], projects["beta"]))
    con.execute("delete from projects where id in (?, ?)", (projects["alpha"], projects["beta"]))
    con.commit()
    con.close()

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

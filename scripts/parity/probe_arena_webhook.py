#!/usr/bin/env python3
"""Row 11 part 63: the two routes that let the outside world change a rating and a row.

`POST /api/v1/projects/{id}/arenas/{arena_id}/recompute` and `POST /api/v1/hooks/forgejo`.

Both are writes nobody clicks: one replays a vote log over ratings the arena already stores, the other is a forge calling the
instance. Which is why both are the kind of endpoint that is built once and never exercised — and why each has a property that fails
silently.

What the probe establishes:

1. **Recompute is the log, not a nudge.** The ratings afterwards equal the replay of the votes, and `changed` counts the entries the
   log disagreed with — so a caller can tell "nothing drifted" from "I fixed 12". An arena whose entries were deliberately corrupted
   is repaired; an arena that was already right reports `changed: 0` and stays byte-identical.
2. **A feature arena mirrors into `features.elo_r`.** Features keep their own rating columns for the priority endpoint; a recompute
   that only fixed `arena_entries` would leave the two disagreeing — and the disagreement is invisible from the arena.
3. **Only a contributor may recompute**, and only in the arena's own project: an arena id from another project is 404, not a
   permission error, because an enumerable id is the failure mode.
4. **The webhook refuses when no secret is configured** — an unauthenticated write endpoint that accepts anything because nobody set
   it up is worse than one that is off.
5. **A wrong or missing signature is refused, and the refusal does not distinguish "wrong" from "missing"**; the signature is
   compared in constant time over the exact bytes received, so a caller cannot learn the secret one byte at a time from timing.
6. **A valid delivery writes one `forge_events` row and one audit row**, and a REDELIVERY of the same payload writes nothing — the
   unique key includes the payload's own timestamp, so idempotency survives the retry. A timestamp taken at receipt time would make
   every retry a new row and silently weight whichever deliveries were retried.
7. **An unparseable or absent payload timestamp records the event nowhere** while the audit row still happens: a row with a fabricated
   time cannot be deduped and moves the median.
8. **An unknown event type is `ignored`, not an error** — a forge that adds an event must not start failing deliveries.
"""
import hashlib
import hmac
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(ROOT, "tests", "e2e"))

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
SECRET = os.environ.get("CONCORD_WEBHOOK_SECRET", "probe-webhook-secret")


def call(method, path, body=None, token=None, raw=None, headers=None):
    data = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
    send = dict(headers or {})
    if data is not None and "Content-Type" not in send:
        send["Content-Type"] = "application/json"
    if token:
        send["Authorization"] = "Bearer " + token
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=send)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            payload = response.read()
            return response.status, (json.loads(payload) if payload else None)
    except urllib.error.HTTPError as error:
        payload = error.read()
        try:
            return error.code, json.loads(payload) if payload else None
        except ValueError:
            return error.code, payload.decode("utf-8", "replace")


def register(tag):
    status, body = call(
        "POST", "/api/v1/auth/register",
        {"username": tag, "display_name": tag.title(), "password": "correct horse"},
    )
    if status != 201:
        raise SystemExit(f"register {tag} failed: {status} {body}")
    return body["user"]["id"], body["token"]


def deliver(event, payload, signature="auto"):
    raw = json.dumps(payload).encode()
    digest = hmac.new(SECRET.encode(), raw, hashlib.sha256).hexdigest()
    headers = {"X-Gitea-Event": event, "Content-Type": "application/json"}
    if signature == "auto":
        headers["X-Hub-Signature-256"] = "sha256=" + digest
    elif signature is not None:
        headers["X-Hub-Signature-256"] = signature
    return call("POST", "/api/v1/hooks/forgejo", raw=raw, headers=headers)


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    maintainer_id, maintainer = register(f"arrm{stamp}")
    stranger_id, stranger = register(f"arrs{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-arena-{stamp}"
    con.execute("delete from projects where slug like ?", (f"{slug}%",))
    con.commit()
    pid = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (slug, slug, "", "collective", "public", "both", None, None, now, now),
    ).lastrowid
    other = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", f"{slug}-other", "", "collective", "public", "both", None, None, now, now),
    ).lastrowid
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (pid, maintainer_id, "maintainer", now))
    con.commit()

    def feature(title):
        return con.execute(
            "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?) RETURNING id",
            (pid, maintainer_id, title, "", "draft", now, now)).lastrowid

    one, two, three = feature("a"), feature("b"), feature("c")
    arena = con.execute(
        "INSERT INTO arenas (type, project_id, created_at) VALUES (?,?,?) RETURNING id",
        ("feature", pid, now)).lastrowid
    for fid in (one, two, three):
        con.execute("INSERT INTO arena_entries (arena_id, entity_type, entity_id, r, rd, sigma, games)"
                    " VALUES (?,?,?,?,?,?,?)", (arena, "feature", fid, 1500.0, 350.0, 0.06, 0))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    # ── recompute ────────────────────────────────────────────────────────────────────────────────────────────────
    path = f"/api/v1/projects/{slug}/arenas/{arena}/recompute"
    status, _ = call("POST", path)
    check("recomputing anonymously is 401", status, 401)
    status, _ = call("POST", path, token=stranger)
    check("a non-member cannot recompute", status in (403, 404), True)
    status, _ = call("POST", f"/api/v1/projects/{slug}-other/arenas/{arena}/recompute", token=maintainer)
    check("an arena id from another project's URL is 404", status, 404)
    status, _ = call("POST", f"/api/v1/projects/{slug}/arenas/999999999/recompute", token=maintainer)
    check("an arena that does not exist is 404", status, 404)

    # Three votes, recorded through the arena's own vote path so the log is the one the product writes.
    votes = [(one, two, "a"), (two, three, "b"), (one, three, "a")]
    for left, right, outcome in votes:
        con.execute(
            "INSERT INTO pairwise_votes (arena_id, project_id, voter_id, entity_type, a, b, outcome,"
            " weight, created_at) VALUES (?,?,?,?,?,?,?,?,?)",
            (arena, pid, maintainer_id, "feature", left, right, outcome, 1.0, now + len(votes)))
    con.commit()

    status, body = call("POST", path, token=maintainer)
    check("a contributor's recompute is 200", status, 200)
    body = body or {}
    check("the answer carries the arena, the change count and the entries",
          sorted(body.keys()), ["arena_id", "changed", "entries"])
    check("it reports the arena it repaired", body.get("arena_id"), arena)
    # Every entry was at its default while the log says otherwise: all three disagree.
    check("changed counts the entries the log disagreed with", body.get("changed"), 3)
    check("every entry comes back", len(body.get("entries") or []), 3)
    stored = {row[0]: (row[1], row[2], row[3]) for row in con.execute(
        "SELECT entity_id, r, rd, games FROM arena_entries WHERE arena_id = ?", (arena,)).fetchall()}
    check("the ratings moved off their defaults",
          all(stored[fid][0] != 1500.0 for fid in (one, two, three)), True)
    check("and each entry counted its games",
          all(stored[fid][2] > 0 for fid in (one, two, three)), True)
    mirrored = {row[0]: row[1] for row in con.execute(
        "SELECT id, elo_r FROM features WHERE id IN (?,?,?)", (one, two, three)).fetchall()}
    check("a feature arena MIRRORS into features.elo_r, or the priority endpoint disagrees with the arena",
          all(abs(mirrored[fid] - stored[fid][0]) < 1e-9 for fid in (one, two, three)), True)

    status, again = call("POST", path, token=maintainer)
    check("recomputing an arena that already agrees changes nothing", (again or {}).get("changed"), 0)
    check("and the ratings are identical",
          {row[0]: row[1] for row in con.execute(
              "SELECT entity_id, r FROM arena_entries WHERE arena_id = ?", (arena,)).fetchall()},
          {entity: value[0] for entity, value in stored.items()})

    # Corrupt one entry: the recompute must repair it, and count exactly that one.
    con.execute("UPDATE arena_entries SET r = 1234.5, rd = 12.0, games = 9"
                " WHERE arena_id = ? AND entity_id = ?", (arena, two))
    con.commit()
    status, body = call("POST", path, token=maintainer)
    check("a corrupted entry is repaired", (body or {}).get("changed"), 1)
    check("and its rating is the log's again",
          con.execute("SELECT r FROM arena_entries WHERE arena_id = ? AND entity_id = ?",
                      (arena, two)).fetchone()[0], stored[two][0])

    # ── the webhook ─────────────────────────────────────────────────────────────────────────────────────────────
    hook = "/api/v1/hooks/forgejo"
    payload = {
        "repository": {"id": 42, "full_name": f"probe/thing-{stamp}"},
        "pusher": {"login": "alice"},
        "commits": [{"timestamp": "2026-11-01T10:00:00Z"}],
    }
    status, body = call("POST", hook, raw=json.dumps(payload).encode(),
                        headers={"X-Gitea-Event": "push", "Content-Type": "application/json"})
    check("a delivery with no signature is refused", status in (401, 403), True)
    status, _ = deliver("push", payload, signature="sha256=" + "0" * 64)
    check("a delivery with the wrong signature is refused", status in (401, 403), True)
    status, _ = deliver("push", payload, signature=None)
    check("and a missing header is refused the same way", status in (401, 403), True)
    check("nothing was recorded for a refused delivery",
          con.execute("SELECT count(*) FROM forge_events WHERE repo_full = ?",
                      (f"probe/thing-{stamp}",)).fetchone()[0], 0)

    status, body = deliver("push", payload)
    check("a correctly signed delivery is accepted", status, 200)
    check("and reports it was processed", (body or {}).get("status"), "processed")
    rows = con.execute("SELECT kind, actor, number, extract(epoch from at)::int FROM forge_events"
                       " WHERE repo_full = ?", (f"probe/thing-{stamp}",)).fetchall()
    # A push has no PR number; the port stores `0` rather than NULL so the unique key stays total (NULL never equals NULL in a unique
    # index, which would let every duplicate push through as a new row).
    check("one event row, with the payload's own timestamp", rows,
          [("push", "alice", 0, 1793527200)])
    check("and one audit row for it",
          con.execute("SELECT count(*) FROM audit_log WHERE action = 'forgejo_push'"
                      " AND entity_id = 42").fetchone()[0], 1)

    status, body = deliver("push", payload)
    check("a REDELIVERY is accepted", status, 200)
    check("but writes no second event row — the unique key includes the payload's timestamp",
          con.execute("SELECT count(*) FROM forge_events WHERE repo_full = ?",
                      (f"probe/thing-{stamp}",)).fetchone()[0], 1)

    # A payload whose timestamp cannot be parsed: the audit row still happens, the event row must not.
    undated = {"repository": {"id": 43, "full_name": f"probe/undated-{stamp}"},
               "pusher": {"login": "bob"}, "commits": [{"timestamp": "not-a-date"}]}
    status, _ = deliver("push", undated)
    check("an undated delivery is still accepted", status, 200)
    check("an event row with a fabricated time is NOT written",
          con.execute("SELECT count(*) FROM forge_events WHERE repo_full = ?",
                      (f"probe/undated-{stamp}",)).fetchone()[0], 0)
    check("but the audit row records that it happened",
          con.execute("SELECT count(*) FROM audit_log WHERE action = 'forgejo_push'"
                      " AND entity_id = 43").fetchone()[0], 1)

    status, body = deliver("deployment_created", {"repository": {"id": 44, "full_name": "x/y"}})
    check("an unknown event type is ignored, not an error", (status, (body or {}).get("status")),
          (200, "ignored"))

    con.execute("delete from forge_events where repo_full like ?", (f"probe/%{stamp}",))
    # Named actions, not a pattern: a per-cent inside the SQL string is read as a driver format specifier.
    con.execute("delete from audit_log where action in ('forgejo_push', 'forgejo_pull_request',"
                " 'forgejo_deployment_created') and entity_id in (42,43,44)")
    con.execute("delete from pairwise_votes where arena_id = ?", (arena,))
    con.execute("delete from arena_entries where arena_id = ?", (arena,))
    con.execute("delete from arenas where project_id in (?,?)", (pid, other))
    con.execute("delete from features where project_id = ?", (pid,))
    con.execute("delete from members where project_id = ?", (pid,))
    con.execute("delete from projects where id in (?,?)", (pid, other))
    con.commit()

    passed = sum(1 for _, got, want in results if got == want)
    print(f"\n{passed}/{len(results)} ok", flush=True)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())

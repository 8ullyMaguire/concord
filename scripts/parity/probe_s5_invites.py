#!/usr/bin/env python3
"""Row 11d part 52 (B6a): invites — the link that turns a stranger into a member (§8.2).

The properties this asserts are the ones that make an invite a trust mechanism rather than a shared password:

1. **only a member mints or revokes one**, and a non-member is told **404, not 403** — a 403 confirms the project exists;
2. **redemption is idempotent per (invite, user)**, and the use counter advances only on a FIRST redemption, so clicking a
   `max_uses=1` link twice does not exhaust it by yourself. A cap that could be spent by one person reloading is a cap that
   breaks on the second tab;
3. **every failure is the same 404** — expired, revoked, exhausted and unknown are indistinguishable, or the endpoint is an
   oracle for testing tokens;
4. **revoking keeps the record**: `revoked_at` is set and the row stays, because an invite list is the answer to "who was let
   in, when, and under which link";
5. **a revoked or expired invite still refuses**, and revoking twice is a 400 rather than a silent success.
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
    invites = [row[0] for row in con.execute(
        f"select id from project_invites where project_id in ({marks})", params).fetchall()]
    if invites:
        imarks = ",".join("?" * len(invites))
        con.execute(f"delete from invite_redemptions where invite_id in ({imarks})", tuple(invites))
        con.execute(f"delete from project_invites where id in ({imarks})", tuple(invites))
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    member_id, member_token = register(f"imember{stamp}")
    stranger_id, stranger_token = register(f"istranger{stamp}")
    second_id, second_token = register(f"isecond{stamp}")
    third_id, third_token = register(f"ithird{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-invite-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Invite probe", "", "collective", "protected", "both", now, now),
    ).lastrowid
    other_project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-other", "Elsewhere", "", "collective", "protected", "both", now, now),
    ).lastrowid
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (project, member_id, "contributor", now))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    invites_path = f"/api/v1/projects/{slug}/invites"

    # ── minting ─────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", invites_path, {})
    check("minting anonymously is 401", status, 401)
    status, _ = call("POST", invites_path, {}, token=stranger_token)
    check("a non-member gets 404, not 403 (the project is not confirmed to exist)", status, 404)
    status, minted = call("POST", invites_path, {}, token=member_token)
    check("a member may mint a link", status, 201)
    minted = minted or {}
    check("the token is 43 characters of base64url", (len(minted.get("token") or ""),
                                                      "+" not in (minted.get("token") or ""),
                                                      "/" not in (minted.get("token") or "")),
          (43, True, True))
    check("it names its project and starts unused",
          (minted.get("project"), minted.get("uses"), minted.get("expires_at"), minted.get("max_uses")),
          (slug, 0, None, None))

    # `max_uses = 2`: one person redeeming twice must spend ONE use, leaving the second for somebody else.
    status, capped = call("POST", invites_path, {"expires_in_seconds": 3600, "max_uses": 2},
                          token=member_token)
    check("limits are reflected", status, 201)
    capped = capped or {}
    check("an expiry is stored as a timestamp", capped.get("expires_at") is not None, True)
    check("and the cap is kept", capped.get("max_uses"), 2)

    status, listed = call("GET", invites_path, token=member_token)
    check("a member may list them", status, 200)
    listed = listed or []
    check("newest first", [row["id"] for row in listed[:2]], [capped.get("id"), minted.get("id")])
    status, _ = call("GET", invites_path, token=stranger_token)
    check("a non-member may not list them", status, 404)

    # ── redeeming ───────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", "/api/v1/auth/redeem-invite", {"token": minted.get("token")})
    check("redeeming anonymously is 401", status, 401)
    status, _ = call("POST", "/api/v1/auth/redeem-invite", {"token": "nonsense"}, token=stranger_token)
    check("an unknown token is 404", status, 404)
    status, _ = call("POST", "/api/v1/auth/redeem-invite", {"token": ""}, token=stranger_token)
    check("an empty token is 400", status, 400)
    status, joined = call("POST", "/api/v1/auth/redeem-invite", {"token": minted.get("token")},
                          token=stranger_token)
    check("a stranger may redeem it", status, 200)
    check("and is told what they joined",
          ((joined or {}).get("slug"), (joined or {}).get("status"), (joined or {}).get("role")),
          (slug, "joined", "contributor"))
    check("the membership exists",
          con.execute("SELECT count(*) FROM members WHERE project_id = ? AND user_id = ?",
                      (project, stranger_id)).fetchone()[0], 1)
    check("the use counter advanced",
          con.execute("SELECT uses FROM project_invites WHERE id = ?", (minted["id"],)).fetchone()[0], 1)

    status, again = call("POST", "/api/v1/auth/redeem-invite", {"token": capped.get("token")},
                         token=stranger_token)
    check("the same person may redeem a capped invite", status, 200)
    status, _ = call("POST", "/api/v1/auth/redeem-invite", {"token": capped.get("token")},
                     token=stranger_token)
    check("and again, idempotently", status, 200)
    check("without exhausting it by themselves",
          con.execute("SELECT uses FROM project_invites WHERE id = ?", (capped["id"],)).fetchone()[0], 1)
    status, _ = call("POST", "/api/v1/auth/redeem-invite", {"token": capped.get("token")},
                     token=second_token)
    check("so somebody else can still use it", status, 200)
    check("and now it IS exhausted",
          con.execute("SELECT uses FROM project_invites WHERE id = ?", (capped["id"],)).fetchone()[0], 2)
    status, _ = call("POST", "/api/v1/auth/redeem-invite", {"token": capped.get("token")},
                     token=third_token)
    check("an exhausted invite is 404", status, 404)

    # ── revoking ────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("POST", f"{invites_path}/{minted['id']}/revoke", {}, token=stranger_token)
    check("a member of this project may revoke", status, 200)
    check("the row STAYS, with revoked_at set",
          con.execute("SELECT revoked_at IS NOT NULL FROM project_invites WHERE id = ?",
                      (minted["id"],)).fetchone()[0], True)
    status, _ = call("POST", f"{invites_path}/{minted['id']}/revoke", {}, token=member_token)
    check("revoking twice is a 400, not a silent success", status, 400)
    status, _ = call("POST", "/api/v1/auth/redeem-invite", {"token": minted["token"]},
                     token=third_token)
    check("a revoked invite is 404", status, 404)

    # A revoked invite is visible in the list: hiding dead links hides the history of who was let in.
    status, listed = call("GET", invites_path, token=member_token)
    revoked = [row for row in (listed or []) if row["id"] == minted["id"]]
    check("and it is still listed, marked revoked", bool(revoked and revoked[0].get("revoked_at")), True)

    # ── expiry ──────────────────────────────────────────────────────────────────────────────────────────────────
    status, fresh = call("POST", invites_path, {"expires_in_seconds": 3600}, token=member_token)
    fresh = fresh or {}
    con.execute("UPDATE project_invites SET expires_at = to_timestamp(?) WHERE id = ?",
                (now - 60, fresh.get("id")))
    con.commit()
    status, _ = call("POST", "/api/v1/auth/redeem-invite", {"token": fresh.get("token")},
                     token=third_token)
    check("an expired invite is the same 404", status, 404)

    # ── cross-project revoke ────────────────────────────────────────────────────────────────────────────────────
    status, foreign = call("POST", invites_path, {}, token=member_token)
    other_member_id, other_member_token = register(f"iother{stamp}")
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (other_project, other_member_id, "maintainer", now))
    con.commit()
    status, _ = call("POST", f"/api/v1/projects/{slug}-other/invites/{(foreign or {}).get('id')}/revoke",
                     {}, token=other_member_token)
    check("another project cannot revoke this one's invite", status, 404)
    check("and it is untouched",
          con.execute("SELECT revoked_at IS NULL FROM project_invites WHERE id = ?",
                      ((foreign or {}).get("id"),)).fetchone()[0], True)

    cleanup(con, f"{slug}%")
    con.commit()
    for user in (member_id, stranger_id, second_id, third_id, other_member_id):
        con.execute("DELETE FROM api_tokens WHERE user_id = ?", (user,))
        con.execute("DELETE FROM users WHERE id = ?", (user,))
    con.commit()
    con.close()

    failures = sum(1 for _, got, want in results if got != want)
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

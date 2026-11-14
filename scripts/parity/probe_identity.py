#!/usr/bin/env python3
"""Row 11d part 57: the identity cluster — guest, consent, login, logout, and the fold.

Five routes that are one story, and the story is what the probe has to hold together:

1. **a nameless visitor can act** (`POST /guest` mints a `users` row with `is_guest = 1` and no password), and the token it is
   given is the identity's only credential;
2. **a guest can never log in** — it has no password, so "no credential" and "wrong password" and "no such user" all have to be
   the SAME 401 with the same sentence. Anything else is a username oracle;
3. **a guest becomes an account by REGISTERING, in place**: the row keeps its id, so every position it took as a guest is the
   account's without a copy. Registering with a guest token must therefore return the guest's id, not a new one;
4. **logging in folds a second identity's work onto the account**, and the response says what moved. The column list is derived
   from the catalogue at fold time, so the probe plants rows in two different tables and checks both moved;
5. **consent is a gate on every mutation**, not a credential: without the accepted notice the port answers 403 `consent_required`
   before it even looks at the token — which the probe checks on a real route, because the gate is a middleware and a middleware
   is exactly the kind of thing that silently covers 180 routes or none;
6. **logout is idempotent** and revokes the presented token, and the navigation cookie is accepted for logout but NOT for the API.

The consent gate is also why every other probe in this directory carries the cookie: the reference refuses a mutation without it,
which was verified against the live Go service (`403 {"code":"consent_required"}` on `POST /projects/suki/join`) before this was
written.
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
CONSENT = os.environ.get("CONCORD_CONSENT_COOKIE", "concord_consent=1")


def call(method, path, body=None, token=None, cookie=CONSENT, extra=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {}
    if data is not None:
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    if cookie:
        headers["Cookie"] = cookie
    for key, value in (extra or {}).items():
        headers[key] = value
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            raw = response.read()
            return response.status, (json.loads(raw) if raw else None), response.headers
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, (json.loads(raw) if raw else None), error.headers
        except ValueError:
            return error.code, raw.decode("utf-8", "replace"), error.headers


def status_of(method, path, **kwargs):
    return call(method, path, **kwargs)[0]


def register(tag):
    status, body, _ = call(
        "POST", "/api/v1/auth/register",
        {"username": tag, "display_name": tag.title(), "password": "correct horse"},
    )
    if status != 201:
        raise SystemExit(f"register {tag} failed: {status} {body}")
    return body["user"]["id"], body["token"]


def cleanup(con, like):
    users = [row[0] for row in con.execute("select id from users where username like ?", (like,)).fetchall()]
    if not users:
        return
    # Every table that carries a user id, in the order the foreign keys allow. Attempted one statement at a time so a single
    # unlucky reference does not abort the rest of the cleanup — a probe that leaves rows behind breaks the NEXT probe.
    for statement in (
        "delete from api_tokens where user_id in ({marks})",
        "delete from reputation_events where user_id in ({marks})",
        "delete from audit_log where actor_id in ({marks})",
        "delete from members where user_id in ({marks})",
        "delete from comment_votes where user_id in ({marks})",
        "delete from compliance_holds where user_id in ({marks})",
        "delete from users where id in ({marks})",
    ):
        try:
            con.execute(statement.format(marks=",".join("?" * len(users))), tuple(users))
            con.commit()
        except Exception:
            con.rollback()
            continue


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    con = harness.connect("unused")
    cleanup(con, f"idprobe{stamp}%")

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    # ── the status a page asks before it offers anything ────────────────────────────────────────────────────────
    status, body, _ = call("GET", "/api/v1/guest", cookie=None)
    body = body or {}
    check("the guest status is public", status, 200)
    check("it reports whether guests may act", isinstance(body.get("allowed"), bool), True)
    check("with the cap, the window and the size",
          [isinstance(body.get(key), int) for key in ("mau", "limit", "window_days", "guests", "stale_guests")],
          [True] * 5)
    check("allowed means: a positive cap, and room under it",
          body.get("allowed"), body.get("limit", 0) > 0 and body.get("mau", 0) < body.get("limit", 0))
    check("and it names the notice version", body.get("consent_version"), "1")
    check("a visitor who has not accepted is told so", body.get("consented"), False)
    status, body, _ = call("GET", "/api/v1/guest")
    check("and one who has, is told the truth", (body or {}).get("consented"), True)

    # ── consent is a gate on every mutation ────────────────────────────────────────────────────────────────────
    status, body, _ = call("POST", "/api/v1/projects/suki/features", {"title": "x"}, cookie=None)
    check("a mutation without the accepted notice is 403 before any token is looked at", status, 403)
    check("with the code a client switches on", (body or {}).get("code"), "consent_required")
    check("and the sentence a person reads",
          "cookies must be accepted" in str((body or {}).get("error", "")), True)
    status, body, _ = call("GET", "/api/v1/projects/suki/features", cookie=None)
    check("a read needs no consent — a banner that blocks reading is a wall, not a notice", status, 200)
    status, body, headers = call("POST", "/api/v1/consent", cookie=None)
    check("accepting the notice is 200", status, 200)
    check("and answers with the version it recorded", (body or {}).get("consent"), "1")
    cookie_header = headers.get("Set-Cookie", "") if headers else ""
    check("it sets the cookie the gate reads", cookie_header.startswith("concord_consent=1"), True)
    check("HttpOnly: a cookie a script can read is a cookie a script can forge",
          "HttpOnly" in cookie_header, True)
    check("SameSite=Lax", "SameSite=Lax" in cookie_header, True)
    check("a year, because a notice re-asked weekly is not read", "Max-Age=31536000" in cookie_header, True)
    check("and NOT Secure over plain http — a Secure cookie set over http is dropped by the browser",
          "Secure" in cookie_header, False)

    # ── minting a guest ────────────────────────────────────────────────────────────────────────────────────────
    before = (call("GET", "/api/v1/guest")[1] or {}).get("guests")
    status, body, _ = call("POST", "/api/v1/guest")
    body = body or {}
    guest = body.get("user") or {}
    guest_token = body.get("token")
    check("a visitor may mint a guest identity", status, 201)
    check("it is marked as a guest", guest.get("is_guest"), True)
    check("named guest- plus 8 hex characters", len(guest.get("username", "")), len("guest-") + 8)
    check("and the name is exactly that shape",
          str(guest.get("username", "")).startswith("guest-")
          and all(c in "0123456789abcdef" for c in str(guest.get("username"))[6:]), True)
    check("with a token, returned once", isinstance(guest_token, str) and guest_token.startswith("clt_"), True)
    check("and the note that says it cannot be retrieved", "cannot be retrieved" in str(body.get("token_note")), True)
    after = (call("GET", "/api/v1/guest")[1] or {}).get("guests")
    check("the status counts it", after, (before or 0) + 1)
    status, me, _ = call("GET", "/api/v1/auth/me", token=guest_token)
    check("the guest's token identifies it", status, 200)
    # `/auth/me` does not carry `is_guest` — the reference's own answer is `false` for a guest too, because its handler reads four
    # columns and leaves the flag at its zero value. The identity's guest-ness is visible where it matters instead: in the count
    # above, and in `POST /auth/register`, which promotes it.
    check("and names the guest", (me or {}).get("username"), guest.get("username"))

    # ── a guest cannot log in, and the refusal says nothing ────────────────────────────────────────────────────
    account_id, account_token = register(f"idprobe{stamp}")
    status_guest, body_guest, _ = call("POST", "/api/v1/auth/login",
                                       {"username": guest.get("username"), "password": "anything at all"})
    status_unknown, body_unknown, _ = call("POST", "/api/v1/auth/login",
                                           {"username": f"nobody{stamp}", "password": "anything at all"})
    status_wrong, body_wrong, _ = call("POST", "/api/v1/auth/login",
                                       {"username": f"idprobe{stamp}", "password": "wrong password"})
    check("a guest cannot log in — it has no credential", status_guest, 401)
    check("an unknown username is 401", status_unknown, 401)
    check("a wrong password is 401", status_wrong, 401)
    check("and all three say exactly the same thing, so the answer is not a username oracle",
          [json.dumps(body_guest, sort_keys=True), json.dumps(body_unknown, sort_keys=True)],
          [json.dumps(body_wrong, sort_keys=True)] * 2)
    check("guest identities do not log in", "invalid credentials" in json.dumps(body_guest), True)

    # ── logging in ─────────────────────────────────────────────────────────────────────────────────────────────
    status, body, headers = call("POST", "/api/v1/auth/login",
                                 {"username": f"  idprobe{stamp}  ", "password": "correct horse"})
    body = body or {}
    check("a correct password logs in", status, 200)
    check("and returns the account", (body.get("user") or {}).get("id"), account_id)
    login_token = body.get("token")
    check("with a fresh token", isinstance(login_token, str) and login_token != account_token, True)
    check("that works", status_of("GET", "/api/v1/auth/me", token=login_token), 200)
    cookie_header = headers.get("Set-Cookie", "") if headers else ""
    check("login sets the navigation cookie", cookie_header.startswith("concord_nav="), True)
    check("as a SESSION cookie: the convenience must not outlive the browser session",
          "Max-Age" in cookie_header, False)
    check("a no-fold login says nothing about folding — silence is the answer 'nothing moved'",
          "fold" in body, False)

    # The navigation cookie is accepted for logout...
    nav = cookie_header.split(";")[0]
    status, _, _ = call("POST", "/api/v1/auth/logout", token=None, cookie=f"{CONSENT}; {nav}")
    check("the navigation cookie alone can log out", status, 204)
    check("and that revoked the token it carried",
          status_of("GET", "/api/v1/auth/me", token=login_token), 401)

    # ...and NOT for the API, which is the CSRF split.
    status, body, headers = call("POST", "/api/v1/auth/login",
                                 {"username": f"idprobe{stamp}", "password": "correct horse"})
    nav = (headers.get("Set-Cookie") or "").split(";")[0]
    login_token = (body or {}).get("token")
    check("the navigation cookie is NOT accepted by an API read",
          status_of("GET", "/api/v1/auth/me", token=None, cookie=f"{CONSENT}; {nav}"), 401)

    # ── logout ────────────────────────────────────────────────────────────────────────────────────────────────
    check("logout is 204", status_of("POST", "/api/v1/auth/logout", token=login_token), 204)
    check("the token is dead", status_of("GET", "/api/v1/auth/me", token=login_token), 401)
    check("logging out twice is still 204 — a client must not be told its logout failed",
          status_of("POST", "/api/v1/auth/logout", token=login_token), 204)
    check("and it uses a revoked token to log out again without creating one",
          status_of("POST", "/api/v1/auth/logout", token=None), 204)

    # ── a guest registers INTO its own row ────────────────────────────────────────────────────────────────────
    status, body, _ = call("POST", "/api/v1/guest")
    guest2 = body["user"]
    guest2_token = body["token"]
    status, promoted, _ = call(
        "POST", "/api/v1/auth/register",
        {"username": f"idprobe{stamp}promoted", "display_name": "Promoted", "password": "correct horse"},
        token=guest2_token,
    )
    promoted = promoted or {}
    check("registering with a guest token is 201", status, 201)
    check("and the account IS the guest's row — nothing was copied",
          (promoted.get("user") or {}).get("id"), guest2["id"])
    check("no longer a guest", (promoted.get("user") or {}).get("is_guest"), False)
    check("with the new name", (promoted.get("user") or {}).get("username"), f"idprobe{stamp}promoted")
    # Two guests were minted since `before`, one has just been promoted: the count is `before + 1`.
    check("and the guest row is gone from the guest count",
          (call("GET", "/api/v1/guest")[1] or {}).get("guests"), (before or 0) + 1)
    status, me, _ = call("GET", "/api/v1/auth/me", token=promoted.get("token"))
    check("the new token names the account", (me or {}).get("id"), guest2["id"])
    check("and the guest's OLD token still resolves to it — one identity, one history",
          ((call("GET", "/api/v1/auth/me", token=guest2_token)[1]) or {}).get("id"), guest2["id"])
    status, body, _ = call("POST", "/api/v1/auth/register",
                           {"username": f"idprobe{stamp}second", "password": "correct horse"},
                           token=promoted.get("token"))
    check("an account registering again gets a second account, honest about it", status, 201)
    check("and it is a different row, not a promotion of the first",
          ((body or {}).get("user") or {}).get("id") != guest2["id"], True)

    # ── the fold ──────────────────────────────────────────────────────────────────────────────────────────────
    status, body, _ = call("POST", "/api/v1/guest")
    guest3 = body["user"]
    guest3_token = body["token"]
    # Rows the guest "produced". Written directly because the probe's subject is the fold's column list, not the routes that
    # would have created them — and two different TABLES is the point: a fold that only knows about one table looks fine.
    con.execute("INSERT INTO audit_log (actor_id, action, entity, entity_id, detail, created_at)"
                " VALUES (?,?,?,?,?,?)",
                (guest3["id"], "guest_identity", "user", guest3["id"], "x", time.time()))
    con.execute("INSERT INTO audit_log (actor_id, action, entity, entity_id, detail, created_at)"
                " VALUES (?,?,?,?,?,?)",
                (guest3["id"], "vote", "feature", 1, "y", time.time()))
    con.execute("INSERT INTO reputation_events (project_id, user_id, kind, points, created_at)"
                " VALUES (?,?,?,?,?)", (1, guest3["id"], "probe", 2.0, time.time()))
    con.commit()
    status, body, _ = call("POST", "/api/v1/auth/login",
                           {"username": f"idprobe{stamp}", "password": "correct horse"},
                           token=guest3_token)
    body = body or {}
    fold = body.get("fold") or {}
    check("logging in from a guest folds its work onto the account", status, 200)
    # Two audit rows, one reputation event, and the guest's own token: the token is folded with everything else so that the browser
    # that was acting as the guest keeps working after the login, now as the account.
    check("and the response says how much moved", fold.get("moved"), 4)
    check("with nothing superseded, because the account had none of these", fold.get("superseded"), 0)
    check("the audit rows now name the account",
          con.execute("SELECT count(*) FROM audit_log WHERE actor_id = ?", (account_id,)).fetchone()[0], 2)
    check("and the reputation event too",
          con.execute("SELECT count(*) FROM reputation_events WHERE user_id = ?", (account_id,)).fetchone()[0], 1)
    check("the guest row is gone",
          con.execute("SELECT count(*) FROM users WHERE id = ?", (guest3["id"],)).fetchone()[0], 0)
    check("and the browser's guest token now resolves to the ACCOUNT, so a login does not log the visitor out",
          ((call("GET", "/api/v1/auth/me", token=guest3_token)[1]) or {}).get("id"), account_id)
    status, body, _ = call("POST", "/api/v1/auth/login",
                           {"username": f"idprobe{stamp}", "password": "correct horse"})
    check("a second login has nothing to fold and does not pretend otherwise",
          "fold" in (body or {}), False)

    # ── consent is recorded against the identity ─────────────────────────────────────────────────────────────
    con.execute("UPDATE users SET consented_at = NULL WHERE id = ?", (account_id,))
    con.commit()
    status, body, _ = call("POST", "/api/v1/consent", token=(body or {}).get("token"))
    check("accepting the notice is 200 with a token too", status, 200)
    check("and it is recorded against the identity",
          con.execute("SELECT consented_at IS NOT NULL FROM users WHERE id = ?", (account_id,)).fetchone()[0], 1)

    cleanup(con, f"idprobe{stamp}%")

    passed = sum(1 for _, got, want in results if got == want)
    print(f"\n{passed}/{len(results)} ok", flush=True)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())

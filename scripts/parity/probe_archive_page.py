"""Prove the archive control: a maintainer sees it, a stranger does not, and it acts.

Run on thinkcentre from the repo root with the e2e venv, after applying the baseline to `concord_test`.
"""

import os
import sys
import tempfile
import urllib.request

sys.path.insert(0, "tests/e2e")
import harness  # noqa: E402

PORT = 8491

env = {}
with open(os.path.expanduser("~/.config/concord/.env")) as fh:
    for line in fh:
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, _, value = line.partition("=")
            env[key] = value.strip().strip('"').strip("'")
os.environ.update(env)

parts = env["DATABASE_URL"].rsplit("/", 1)
os.environ["CONCORD_DATABASE_URL"] = parts[0] + "/concord_test"

dbdir = tempfile.mkdtemp(prefix="archive-probe-")
logdir = tempfile.mkdtemp(prefix="archive-probe-log-")
proc, db, base = harness.start_server(PORT, dbdir, logdir)
try:
    con = harness.connect(db)
    cur = con.cursor()
    cur.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
        " VALUES (?,?,?,?,?)", ("arch-owner", "Arch Owner", 1794000000, "x", 3))
    owner = cur.lastrowid
    cur.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
        " VALUES (?,?,?,?,?)", ("arch-stranger", "Arch Stranger", 1794000000, "x", 3))
    stranger = cur.lastrowid
    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license, created_at,"
        " updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        ("arch-demo", "Arch Demo", "demo", "collective", "MIT", 1794000000, 1794000000, "public"))
    project = cur.lastrowid
    cur.execute(
        "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
        (project, owner, "owner", 1794000000))
    # Tokens are stored as SHA-256 base64 (no padding) — `auth::hash_token`. A probe that inserted the raw
    # string would "work" against nothing: `user_for_token` would find no row and every request would look
    # anonymous, which is exactly the failure this probe is meant to catch.
    import base64
    import hashlib

    def store_token(token, user):
        # `auth::hash_token` is SHA-256 then base64 STANDARD_NO_PAD — the padding matters: a padded hash
        # matches no row, `user_for_token` answers None, and every request in this probe looked anonymous.
        digest = base64.b64encode(hashlib.sha256(token.encode()).digest()).decode().rstrip("=")
        cur.execute("INSERT INTO api_tokens (token_hash, user_id) VALUES (?,?)", (digest, user))

    tokens = {}
    for name, user in (("owner", owner), ("stranger", stranger)):
        token = f"tok-{name}-archive"
        store_token(token, user)
        tokens[name] = token
    con.commit()
    con.close()

    def page(token=None):
        request = urllib.request.Request(base + "/projects/arch-demo")
        if token:
            request.add_header("Cookie", f"concord_nav={token}")
        with urllib.request.urlopen(request, timeout=10) as response:
            return response.read().decode()

    anonymous = page()
    owner_html = page(tokens["owner"])
    stranger_html = page(tokens["stranger"])
    # The BUTTON, not the string: the id also appeared inside the inline script that wired it, so a
    # substring check said "the control is there" for every reader on the site.
    def has_control(html):
        return 'id="project-archive-toggle"' in html

    print("anonymous sees the control:", has_control(anonymous))
    print("owner sees the control:", has_control(owner_html))
    print("stranger sees the control:", has_control(stranger_html))
    print("anonymous ships the script:", "project-archive-toggle\")" in anonymous)

    def api(method, path, token, body=None):
        data = body.encode() if body else None
        request = urllib.request.Request(base + path, data=data, method=method)
        request.add_header("Authorization", f"Bearer {token}")
        # Writes need consent, and consent is a cookie the notice sets — measured, not remembered:
        # without it every write answers 403 `consent_required` and the probe would report the API as broken.
        request.add_header("Cookie", "concord_consent=1")
        if data:
            request.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                return response.status, response.read().decode()
        except urllib.error.HTTPError as error:
            return error.code, error.read().decode()

    print("stranger archives:", api("POST", "/api/v1/projects/arch-demo/archive", tokens["stranger"],
                                    '{"reason":"not mine"}'))
    print("owner archives:", api("POST", "/api/v1/projects/arch-demo/archive", tokens["owner"],
                                 '{"reason":"nobody has touched it since spring"}'))
    after = page(tokens["owner"])
    print("page now shows Restore:", "Restore this project" in after)
    with urllib.request.urlopen(base + "/api/v1/projects/arch-demo", timeout=10) as response:
        body = response.read().decode()
    print("stage after archive:", body[:200])
    print("owner restores:", api("DELETE", "/api/v1/projects/arch-demo/archive", tokens["owner"]))
finally:
    harness.stop_server(proc)

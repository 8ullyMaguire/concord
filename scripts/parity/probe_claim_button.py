"""Prove the claim control sends the method it says: a POST, not a read.

The defect this exists for: `wireWorkItems`' `sendJSON` was written as
`window.ConcordApi.get(path, { method: 'POST', … })`, and `api.js`'s `get` hardcodes GET and drops the body — so
"Claim this" went out as a `GET` to a POST-only route and the server answered `405`. The page then re-enabled the
button with no message, which reads exactly like "nothing happened".

Nothing in the suites reached this: `project_panels_e2e`'s "claim" tests are about *capability* claims, and the work-item
button only renders for a signed-in reader. Measured against the deployed binary at the protocol level:

    GET  /api/v1/projects/suki/features/1/claim  -> 405
    POST /api/v1/projects/suki/features/1/claim  -> 403 consent_required   (the route accepts the method)

This probe drives the button in a real browser and reads the label it settles on:
`Join the project to claim work` is the 403 branch — a refusal only a POST can produce.

Run on thinkcentre from the repo root, with the e2e venv and `concord_test` migrated:

    CONCORD_E2E_CANDIDATE=/home/alvaro/concord-deploy/concord-rs \
      ~/.venvs/concord-e2e/bin/python scripts/parity/probe_claim_button.py
"""

import os
import sys
import tempfile
import time

env = {}
with open(os.path.expanduser("~/.config/concord/.env")) as fh:
    for line in fh:
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, _, value = line.partition("=")
            env[key] = value.strip().strip('"').strip("'")
os.environ.update(env)

# `harness.SEED_DSN` is read at IMPORT time and is what `start_server` hands the candidate as `CONCORD_DB_URL`.
# Setting it after the import gives the server an empty DSN, and the binary exits with
# `Error: error with configuration: relative URL without a base` — which reads like a broken probe, not a missing variable.
parts = env["DATABASE_URL"].rsplit("/", 1)
os.environ["CONCORD_E2E_DSN"] = parts[0] + "/concord_test"

sys.path.insert(0, "tests/e2e")
import harness  # noqa: E402
from playwright.sync_api import sync_playwright  # noqa: E402

PORT = 8493

dbdir = tempfile.mkdtemp(prefix="claim-probe-")
logdir = tempfile.mkdtemp(prefix="claim-probe-log-")
proc, db, base = harness.start_server(PORT, dbdir, logdir)
try:
    import base64
    import hashlib

    con = harness.connect(db)
    cur = con.cursor()

    def add_user(name):
        cur.execute(
            "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
            " VALUES (?,?,?,?,?)", (name, name.title(), 1794000000, "x", 3))
        return cur.lastrowid

    author = add_user("claim-author")
    reader = add_user("claim-reader")
    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license, created_at,"
        " updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        ("claim-demo", "Claim Demo", "demo", "collective", "MIT", 1794000000, 1794000000, "public"))
    project = cur.lastrowid
    cur.execute(
        "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
        (project, author, "owner", 1794000000))
    cur.execute(
        "INSERT INTO features (project_id, author_id, title, body, effort, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?)",
        (project, author, "A task nobody holds", "body", "M", "planned", 1794000000, 1794000000))
    feature = cur.lastrowid

    # The reader is signed in but NOT a member: the client renders the button (it only knows "signed in"), and the
    # server refuses the claim for lack of membership — the 403 the label below comes from.
    token = "tok-claim-reader"
    digest = base64.b64encode(hashlib.sha256(token.encode()).digest()).decode().rstrip("=")
    cur.execute("INSERT INTO api_tokens (token_hash, user_id) VALUES (?,?)", (digest, reader))
    con.commit()
    con.close()

    with sync_playwright() as p:
        browser = p.chromium.launch()
        context = browser.new_context()
        context.add_cookies([
            {"name": "concord_consent", "value": "1", "url": base},
            {"name": "concord_nav", "value": token, "url": base},
        ])
        page = context.new_page()
        errors = []
        page.on("pageerror", lambda e: errors.append(str(e)))
        # `session.js` reads the token out of localStorage, so the browser has to be told before the script runs.
        page.add_init_script(
            "localStorage.setItem('concord.token', %s);"
            "localStorage.setItem('concord.user', JSON.stringify({username:'claim-reader',id:%d}));"
            % (repr(token), reader))
        page.goto(base + "/projects/claim-demo", wait_until="load")
        page.wait_for_function(
            "() => { const b = document.getElementById('project-detail');"
            " return b && b.getAttribute('aria-busy') === 'false'; }", timeout=20000)

        buttons = page.eval_on_selector_all("[data-claim]", "els => els.length")
        print("claim buttons rendered for a signed-in reader:", buttons)
        if buttons == 0:
            print("pageerror:", errors[:2])
            raise SystemExit("no claim button: the probe cannot answer its question")

        feature_id = page.get_attribute("[data-claim]", "data-claim")
        page.click("[data-claim]")
        # The handler disables the button, posts, and writes the outcome into its label. Three seconds is 30x the
        # round trip this handler needs against a local server; if it has not settled, the request is still hanging.
        deadline = time.time() + 10
        label = ""
        while time.time() < deadline:
            label = page.inner_text("[data-claim]")
            if label.strip() not in ("Claiming…", "Claim this"):
                break
            time.sleep(0.2)

        print("feature claimed:", feature_id)
        print("label after the click:", repr(label.strip()))
        print("page errors:", errors[:3])
        print("VERDICT:", "POST reached the route" if label.strip() == "Join the project to claim work"
              else "the button did not reach the 403 branch")
        browser.close()
finally:
    harness.stop_server(proc)

"""Ground truth for the requests suite: seed the same fixtures, start the candidate, ask the API what it says.

Run on thinkcentre from /personal/documents/code/rust/concord with the e2e venv.
"""
import json
import os
import subprocess
import sys
import tempfile
import urllib.request

sys.path.insert(0, "tests/e2e")
import harness  # noqa: E402
import requests_e2e  # noqa: E402

PORT = 8487

env = {}
with open(os.path.expanduser("~/.config/concord/.env")) as fh:
    for line in fh:
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, _, value = line.partition("=")
            env[key] = value.strip().strip('"').strip("'")
os.environ.update(env)

dsn = env["DATABASE_URL"].replace("/concord?host", "/concord_test?host")
if not dsn.rsplit("/", 1)[-1].startswith("concord_test"):
    parts = env["DATABASE_URL"].rsplit("/", 1)
    dsn = parts[0] + "/concord_test"
os.environ["CONCORD_DATABASE_URL"] = dsn

dbdir = tempfile.mkdtemp(prefix="req-probe-")
logdir = tempfile.mkdtemp(prefix="req-probe-log-")
proc, db, base = harness.start_server(PORT, dbdir, logdir)
try:
    requests_e2e.seed(db)
    for path in (
        "/api/v1/projects/req-demo/requests",
        "/api/v1/requests",
        "/api/v1/requests/1",
        "/api/v1/requests/1/tags",
    ):
        try:
            with urllib.request.urlopen(base + path, timeout=10) as response:
                body = response.read().decode()
                print(f"{path} -> {response.status} {body[:400]}")
        except urllib.error.HTTPError as error:
            print(f"{path} -> {error.code} {error.read().decode()[:300]}")
    # The page as a reader sees it.
    with urllib.request.urlopen(base + "/projects/req-demo/requests", timeout=10) as response:
        html = response.read().decode()
    print("page has requests-root:", 'id="requests-root"' in html)
    print("page has data-slug:", 'data-slug="req-demo"' in html)

    # What the reader actually sees, after the script has run.
    from playwright.sync_api import sync_playwright
    with sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        page = browser.new_page()
        page.goto(base + "/projects/req-demo/requests")
        page.wait_for_selector('#requests-root:not([aria-busy="true"])', timeout=15000)
        print("cards:", page.locator("#requests-root .card").count())
        print("titles:", page.locator("#requests-root .card h3").all_inner_texts())
        print("text:", page.locator("#requests-root").inner_text()[:300].replace("\n", " | "))
        page.goto(base + "/projects/req-demo/requests/1")
        page.wait_for_selector('#request-root:not([aria-busy="true"])', timeout=15000)
        print("chips:", page.locator("#request-root .constraints .chip").all_inner_texts())
        browser.close()
finally:
    harness.stop_server(proc)

"""The AI-involvement declaration, as a reader and as a maintainer see it (docs/plans/post-port-roadmap.md, step 4).

Four projects, differing only in the value written to `projects.ai_involvement`:

    ai-human      'human'      -> a `human` badge
    ai-assisted   'assisted'   -> an `assisted` badge
    ai-generated  'generated'  -> a `generated` badge, and the ONLY one that takes the warning colour
    ai-undeclared NULL         -> NO badge at all

The last one is the assertion the feature is built around: `NULL` is "not declared", so a badge there would be this instance putting words
in a maintainer's mouth. Every other test in this file would pass against an implementation that printed "unknown" for it.

The write half is here too, because the rule has two halves that fail separately: a maintainer may declare, and a stranger may not. The
suite drives the API with a real token rather than a fixture column, so the 403 is the server's answer and not a value a test wrote.
"""

import itertools
import json
import tempfile
import urllib.error
import urllib.request

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8523
BASE = f"http://127.0.0.1:{PORT}"

NOW = 1790000000
DAY = 86400
_ips = itertools.count(1)


def seed(db):
    con = harness.connect(db)
    try:
        owner = con.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES ('ai-owner','AI Owner',?)", (NOW,)
        ).lastrowid
        stranger = con.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES ('ai-stranger','AI Stranger',?)", (NOW,)
        ).lastrowid
        for slug, name, declared in (
            ("ai-human", "Declared Human", "human"),
            ("ai-assisted", "Declared Assisted", "assisted"),
            ("ai-generated", "Declared Generated", "generated"),
            ("ai-undeclared", "Nothing Declared", None),
        ):
            pid = con.execute(
                "INSERT INTO projects (slug, name, description, governance_model, license, created_at, updated_at,"
                " visibility, ai_involvement) VALUES (?,?,?,'collective','MIT',?,?,'public',?)",
                (slug, name, "an AI-declaration fixture", NOW - 40 * DAY, NOW - 40 * DAY, declared),
            ).lastrowid
            # The owner is a member of the declared projects; the stranger is a member of NONE, which is what makes the 403 arm real.
            if declared is not None:
                con.execute(
                    "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'maintainer',?)",
                    (pid, owner, NOW),
                )
        con.commit()
    finally:
        con.close()


def token_for(db, username):
    """Mint a token the way the server hashes one: `base64 RawStd (no padding) of sha256(raw)`, raw starting `clt_`.

    Padded base64 was the bug in the first version of this helper: every request answered 401, which reads as a permissions problem and is
    an encoding one. `auth::hash_token` uses `STANDARD_NO_PAD`, so `b64encode` needs its `=` stripped.
    """
    import base64
    import hashlib
    import secrets

    con = harness.connect(db)
    try:
        raw = "clt_" + secrets.token_urlsafe(24)
        digest = base64.b64encode(hashlib.sha256(raw.encode()).digest()).decode().rstrip("=")
        user_id = con.execute("SELECT id FROM users WHERE username = ?", (username,)).fetchone()[0]
        con.execute(
            "INSERT INTO api_tokens (token_hash, user_id, created_at) VALUES (?,?,?)", (digest, user_id, NOW)
        )
        con.commit()
    finally:
        con.close()
    return raw


def put(token, slug, body):
    request = urllib.request.Request(
        f"{BASE}/api/v1/projects/{slug}/ai-involvement",
        data=json.dumps(body).encode(),
        method="PUT",
        headers={"Content-Type": "application/json", "Authorization": f"Bearer {token}", "Cookie": "concord_consent=1"},
    )
    try:
        with urllib.request.urlopen(request, timeout=10) as reply:
            return reply.status, json.loads(reply.read() or b"{}")
    except urllib.error.HTTPError as error:
        return error.code, json.loads(error.read() or b"{}")


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-ai-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-ai-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


@pytest.fixture()
def page(browser):
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": f"10.79.0.{next(_ips)}"})
    page = ctx.new_page()
    yield page
    ctx.close()


def badge(scope, word):
    """The badge whose text IS `word`, anchored at both ends (the server writes `format!(" {}", value)`, hence the whitespace)."""
    import re

    return scope.locator("span.badge").filter(has_text=re.compile(rf"^\s*{re.escape(word)}\s*$"))


def row(page, slug):
    return page.locator(f'li:has(a[href="/projects/{slug}"])')


def test_a_declared_value_renders(page, server):
    page.goto(f"{BASE}/projects")
    expect(badge(row(page, "ai-human"), "human")).to_have_count(1)
    expect(badge(row(page, "ai-assisted"), "assisted")).to_have_count(1)


def test_only_generated_takes_the_warning_colour(page, server):
    """The colour is a claim about attention, so it gets its own assertion per project rather than one aggregate check."""
    page.goto(f"{BASE}/projects")
    expect(row(page, "ai-generated").locator("span.badge.badge-amber")).to_have_count(1)
    expect(row(page, "ai-human").locator("span.badge.badge-amber")).to_have_count(0)
    expect(row(page, "ai-assisted").locator("span.badge.badge-amber")).to_have_count(0)


def test_an_undeclared_project_renders_nothing(page, server):
    """`NULL` is "not declared". A badge here would be the instance asserting something about somebody's code."""
    page.goto(f"{BASE}/projects")
    target = row(page, "ai-undeclared")
    expect(target).to_be_visible()
    for word in ("human", "assisted", "generated", "audited", "unknown"):
        expect(badge(target, word)).to_have_count(0)


def test_the_project_page_carries_the_declaration_too(page, server):
    page.goto(f"{BASE}/projects/ai-generated")
    expect(page.locator("#project-detail")).to_be_visible()
    expect(badge(page.locator("h1.page-title"), "generated")).to_have_count(1)


def test_a_maintainer_may_declare_and_withdraw(page, server):
    """The whole lifecycle over HTTP: declare, read it back from the API, withdraw, and see the API say null again."""
    token = token_for(server["db"], "ai-owner")
    status, body = put(token, "ai-human", {"value": "audited"})
    assert (status, body.get("ai_involvement")) == (200, "audited"), (status, body)
    status, body = put(token, "ai-human", {"value": None})
    assert (status, body.get("ai_involvement")) == (200, None), (status, body)
    # Put it back so the rendering tests above are not order-dependent on this one.
    assert put(token, "ai-human", {"value": "human"})[0] == 200


def test_a_stranger_may_not(page, server):
    token = token_for(server["db"], "ai-stranger")
    status, body = put(token, "ai-assisted", {"value": "generated"})
    assert status == 403, (status, body)
    assert "maintainer" in body.get("error", ""), body


def test_a_value_outside_the_four_is_refused(page, server):
    token = token_for(server["db"], "ai-owner")
    status, body = put(token, "ai-human", {"value": "vibe-coded"})
    assert status == 400, (status, body)
    assert "human" in body.get("error", ""), body

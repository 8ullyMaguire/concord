"""§6.2's themes and §6.2's steering input, in a browser.

Appended to the settings page suite because both surfaces live on pages this suite already
drives, and because the Go tests cannot see either defect this file exists for:

  * the theme panel had a GET and no POST, so a reader could consent to a theme somebody
    else had proposed and could never propose one. Every Go test was green.
  * a consent below quorum answers 202, which means RECORDED, not ratified. A page that
    reports "ratified" for a 202 tells the reader the roadmap moved when nothing did -- and
    the ranking on another page would contradict it.

Both are invisible to a source-level gate: the string "/strategy/themes" is in the file
either way.
"""

import base64
import hashlib
import itertools
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

# One fixed port per suite: two sync_playwright() sessions cannot coexist in one pytest
# process, so the Makefile runs each file as its own invocation. The port line must be a
# bare int -- the cross-file gate parses it with int().
PORT = 8495
SLUG = "e2e-themes"
BASE = f"http://127.0.0.1:{PORT}"

TAG = "security-hardening"
MUTED_TAG = "battery-life"
RENEW_TAG = "documentation"

_theme_ips = itertools.count(900)


def seed(db):
    """The project, its owner, and a member row -- seeded through SQLite, never the API.

    A browser test whose data arrives through the write path it is testing cannot fail for
    the reason it exists: a broken write leaves the page empty and the assertions pass.
    """
    con = sqlite3.connect(db)
    now = 1790000000
    uid = con.execute(
        "INSERT INTO users (username, display_name, created_at, trust_level)"
        " VALUES ('setowner','Set Owner',?,3)", (now,)).lastrowid
    pid = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        (SLUG, "Themes E2E", "the theme fixture", "collective", "MIT", now, now,
         "public")).lastrowid
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at)"
                " VALUES (?,?,'owner',?)", (pid, uid, now))
    con.commit()
    con.close()


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-themes-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-themes-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


def _mint_token(db, username):
    """A token row for `username` (the login handler's own shape).

    Two details are the server's, not invented here: api_tokens.token_hash is base64
    RawStd of the sha256 digest (NOT hex, whatever the migration comment says), and
    ResolveToken rejects a value that does not start with `clt_`.
    """
    raw = "clt_themes-" + username
    con = sqlite3.connect(db)
    uid = con.execute("SELECT id FROM users WHERE username = ?", (username,)).fetchone()[0]
    digest = hashlib.sha256(raw.encode()).digest()
    con.execute(
        "INSERT OR REPLACE INTO api_tokens (token_hash, user_id, created_at, last_used_at)"
        " VALUES (?,?,?,?)",
        (base64.b64encode(digest).decode().rstrip("="), uid, time.time(), time.time()))
    con.commit()
    con.close()
    return raw


def _seed_theme_world(db, lifetime_days=30):
    """A tag, three ELIGIBLE collaborators, a feature carrying the tag, and a charter.

    Eligibility is the point of the three: §8.2 wants a qualifying contribution, and a
    theme's quorum is capped to the eligible population -- so a fixture with an eligible
    count of 0 has a quorum of 0 and the first consent ratifies alone, which is the hazard
    the store guards against and a fixture must not silently reproduce.

    The charter sets quorum_min=2 so that ONE consent is below quorum and TWO ratify: the
    202-then-200 pair is the behaviour this file is here to check, and it cannot be reached
    at the default quorum of 3 with three collaborators.

    theme_lifetime_days=30 rather than the default 90, so the expiry rendered on the page can
    only have come from the project's own charter.
    """
    con = sqlite3.connect(db)
    now = time.time()
    uid = con.execute("SELECT id FROM users WHERE username='setowner'").fetchone()[0]
    pid = con.execute("SELECT id FROM projects WHERE slug=?", (SLUG,)).fetchone()[0]

    # Idempotent: every test in the file calls this, and the session-scoped server does not
    # restart between them. A fixture that only works once is a fixture the second test
    # discovers.
    for tag in (TAG, MUTED_TAG):
        if con.execute("SELECT id FROM tags WHERE name=?", (tag,)).fetchone() is None:
            con.execute("INSERT INTO tags (project_id, name) VALUES (NULL, ?)", (tag,))

    people = {"setowner": uid}
    for name in ("thememaker", "thirdvoter"):
        row = con.execute("SELECT id FROM users WHERE username=?", (name,)).fetchone()
        if row is None:
            row = (con.execute(
                "INSERT INTO users (username, display_name, created_at, trust_level)"
                " VALUES (?,?,?,3)", (name, name.title(), now)).lastrowid,)
        people[name] = row[0]
        con.execute("INSERT OR IGNORE INTO members (project_id, user_id, role, joined_at)"
                    " VALUES (?,?,'contributor',?)", (pid, people[name], now))

    # A feature carrying the tag, so the ranking this theme steers has something to move.
    row = con.execute("SELECT id FROM features WHERE project_id=? AND title=?",
                      (pid, "Harden the login flow")).fetchone()
    fid = row[0] if row else con.execute(
        "INSERT INTO features (project_id, author_id, title, body, effort, status,"
        " strategic_weight, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
        (pid, uid, "Harden the login flow", "body", "M", "draft", 1.0, now, now)).lastrowid
    tagid = con.execute("SELECT id FROM tags WHERE name=?", (TAG,)).fetchone()[0]
    con.execute("INSERT OR IGNORE INTO feature_tags (feature_id, tag_id, created_at)"
                " VALUES (?,?,?)", (fid, tagid, now))

    # An eligible collaborator: a merged merge request is the cheapest qualifying
    # contribution (§8.2) and the same route tests/e2e fixtures use.
    for name, person in people.items():
        if con.execute("SELECT id FROM merge_requests WHERE project_id=? AND author_id=?",
                       (pid, person)).fetchone() is None:
            con.execute(
                "INSERT INTO merge_requests (project_id, feature_id, author_id, title, status,"
                " opened_at, closed_at) VALUES (?,?,?,?,'merged',?,?)",
                (pid, fid, person, "work by " + name, now, now))

    con.execute("INSERT OR REPLACE INTO charters (project_id, quorum_min, quorum_ratio,"
                " theme_lifetime_days) VALUES (?,2,0.2,?)", (pid, lifetime_days))
    con.commit()
    con.close()
    return {"pid": pid, "uid": uid, "fid": fid,
            "maker": people["thememaker"], "third": people["thirdvoter"]}


def _open_theme_proposal(db, ids, tag=TAG, weight=2.0):
    """A pending proposal by somebody else, so the page's owner can consent to it.

    `tag` defaults to TAG but the below-quorum test uses another tag: `idx_theme_one_pending`
    allows only one pending proposal per (project, tag, namespace), which is the rule the
    proposal test above exercises by leaving one behind. Two tests sharing a tag is the same
    collision the index is there to prevent.
    """
    con = sqlite3.connect(db)
    now = time.time()
    con.execute(
        "INSERT INTO strategic_theme_proposals (project_id, theme_tag, theme_namespace,"
        " proposed_by, weight_before, weight_after, rationale, status, created_at)"
        " VALUES (?,?,'topic',?,NULL,?,?,'pending',?)",
        (ids["pid"], tag, ids["maker"], weight, "it is the safety floor", now))
    con.commit()
    con.close()


@pytest.fixture()
def signed_page(browser, server):
    """A page signed in as the project owner, with the token the page's fetches present."""
    n = next(_theme_ips)
    token = _mint_token(server["db"], "setowner")
    ctx = browser.new_context(
        extra_http_headers={"X-Forwarded-For": f"10.77.{n // 200}.{n % 200}"})
    ctx.add_init_script("localStorage.setItem('concord.token', %r);" % token)
    ctx.add_cookies([{"name": "concord_nav", "value": token, "url": BASE + "/"}])
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append(str(e)))
    yield pg
    ctx.close()


def test_the_theme_panel_proposes_a_theme_and_shows_it_pending(signed_page, server):
    """The write path that did not exist: a reader could consent and could not propose."""
    ids = _seed_theme_world(server["db"])
    signed_page.goto(f"{server['base']}/projects/{SLUG}/settings")
    signed_page.wait_for_selector('#settings-root:not([aria-busy="true"])', timeout=15000)

    form = signed_page.locator("#propose-theme-form")
    expect(form).to_be_visible()
    form.locator("#theme-tag").fill(TAG)
    form.locator("#theme-weight").fill("2")
    form.locator("#theme-rationale").fill("it is the safety floor")
    form.locator("button[type=submit]").click()

    # The pending table, and the status the server assigned -- not a locally assumed one.
    row = signed_page.locator("[data-theme-proposals] tr", has_text=TAG)
    expect(row).to_be_visible(timeout=10000)
    expect(row).to_contain_text("pending")
    # And the live table still has no row for the tag: a theme that took effect on proposal
    # is the hand-steering §6.2's consensus route exists to prevent. Asserted as a COUNT
    # rather than as "the table does not contain the text", because an empty themes list
    # renders an empty-state div and no table at all -- a locator that matches nothing
    # fails the negative assertion for the wrong reason.
    expect(signed_page.locator("[data-strategy-themes] tr", has_text=TAG)).to_have_count(0)
    assert not signed_page.errors, signed_page.errors


def test_a_consent_below_quorum_reads_as_recorded_and_then_ratifies(signed_page, server):
    """202 and 200 are different answers, and the page must not collapse them.

    With quorum_min=2 and three eligible collaborators: the owner's consent is 1/2, which the
    server answers 202. If the page printed "Ratified" there, the reader would believe a
    weight had moved -- and the ranking page would disagree with them.
    """
    ids = _seed_theme_world(server["db"])
    _open_theme_proposal(server["db"], ids, tag=MUTED_TAG, weight=1.5)
    signed_page.goto(f"{server['base']}/projects/{SLUG}/settings")
    signed_page.wait_for_selector('#settings-root:not([aria-busy="true"])', timeout=15000)

    row = signed_page.locator("[data-theme-proposals] tr", has_text=MUTED_TAG)
    expect(row).to_be_visible(timeout=10000)
    row.locator("[data-consent-theme]").click()

    # The 202 path: recorded, quorum not reached, and nothing applied. The status line is
    # #settings-live (role=status), which is where say() writes -- the forms' own [data-status]
    # paragraphs are a different thing and there are two of them.
    status = signed_page.locator("#settings-live")
    expect(status).to_contain_text("Consent recorded", timeout=10000)
    expect(status).not_to_contain_text("Ratified")
    expect(signed_page.locator("[data-strategy-themes] tr", has_text=MUTED_TAG)).to_have_count(0)

    # The second eligible consent, through the API, reaches quorum.
    token = _mint_token(server["db"], "thirdvoter")
    proposal = sqlite3.connect(server["db"]).execute(
        "SELECT id FROM strategic_theme_proposals WHERE status='pending'").fetchone()[0]
    harness.api(server["base"], "POST",
                f"/api/v1/projects/{SLUG}/strategy/themes/{proposal}/consent", {}, token=token)

    # Reload: the theme is live, with the weight AND the expiry the charter's 30 days implies.
    signed_page.reload()
    signed_page.wait_for_selector('#settings-root:not([aria-busy="true"])', timeout=15000)
    live_row = signed_page.locator("[data-strategy-themes] tr", has_text=MUTED_TAG)
    expect(live_row).to_be_visible(timeout=10000)
    expect(live_row).to_contain_text("1.5")
    expiry = signed_page.evaluate(
        """() => {
             const row = Array.from(document.querySelectorAll('[data-strategy-themes] tr'))
               .find(r => r.textContent.includes(%r));
             return row ? row.children[row.children.length - 2].textContent.trim() : null;
           }""" % MUTED_TAG)
    # A date, not a placeholder: §366's "unless renewed" is only meaningful if the reader can
    # see when it lapses, and the charter's own 30 days is where the number comes from.
    assert expiry and expiry[:2] == "20", f"expiry column rendered {expiry!r}"
    assert not signed_page.errors, signed_page.errors


def _seed_live_theme(db, ids, tag=RENEW_TAG, weight=2.5, days_left=5):
    """A theme already in force and close to lapsing -- the state renew exists for."""
    con = sqlite3.connect(db)
    now = time.time()
    con.execute("INSERT OR REPLACE INTO strategic_themes (project_id, tag, namespace, weight,"
                " rationale, set_by, created_at, expires_at) VALUES (?,?,'topic',?,?,?,?,?)",
                (ids["pid"], tag, weight, "it was ratified once", ids["maker"], now,
                 now + days_left * 86400))
    con.commit()
    con.close()


def test_renew_prepares_a_proposal_and_does_not_decide_the_weight(signed_page, server):
    """§366's "unless renewed" needs a control, and the renewal must still be a decision.

    Two defects this catches and neither is visible to a Go test: a Renew button wired to
    nothing (the tag never reaches the form, and the reader believes they renewed a theme
    that will lapse), and a Renew button that also writes the theme's CURRENT weight into
    the weight field -- which turns "renew this at whatever it is now" into a prepared
    answer and removes the only moment somebody decides whether it is still worth that.

    The live theme's weight is seeded at 2.5, deliberately not the form's own default, so
    "the field reads 2.5" is distinguishable from "the field was left alone". An assertion
    that the field is empty would be wrong about the markup: the form ships a default.
    """
    ids = _seed_theme_world(server["db"])
    _seed_live_theme(server["db"], ids)
    signed_page.goto(f"{server['base']}/projects/{SLUG}/settings")
    signed_page.wait_for_selector('#settings-root:not([aria-busy="true"])', timeout=15000)

    row = signed_page.locator("[data-strategy-themes] tr", has_text=RENEW_TAG)
    expect(row).to_be_visible(timeout=10000)
    form = signed_page.locator("#propose-theme-form")
    before = form.locator("#theme-weight").input_value()
    row.locator("[data-renew]").click()

    expect(form.locator("#theme-tag")).to_have_value(RENEW_TAG)
    # Unchanged from whatever the form already carried, and NOT the live theme's weight:
    # the second half is what a pre-filling mutant fails.
    expect(form.locator("#theme-weight")).to_have_value(before)
    assert before != "2.5", "the form already carried the theme's weight; this test cannot tell"
    expect(form.locator("#theme-weight")).not_to_have_value("2.5")
    expect(signed_page.locator("#settings-live")).to_contain_text(RENEW_TAG)
    assert not signed_page.errors, signed_page.errors

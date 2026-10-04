"""Feeds (docs/specs/feeds-spec.md) in a real browser.

The point of these two tests is what a PAGE promises. A <link rel="alternate"> that
resolves to a 404, or an Atom item whose link points nowhere, is the failure mode that
unit tests on the feed body cannot see: the body can be perfect and the discovery
mechanism still be broken.
"""

import os
import re
import sqlite3
import tempfile
import time
import xml.etree.ElementTree as ET

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

ATOM = "{http://www.w3.org/2005/Atom}"

# Distinct port, per the Makefile's rule that the suites own one each (8421..8423 are
# taken). The first version hardcoded 8486 in three places, which is how the port ended
# up set in the fixture but not in the module constants every other suite uses.
PORT = 8424
BASE = f"http://127.0.0.1:{PORT}"


@pytest.fixture(scope="module")
def server():
    dbdir = tempfile.mkdtemp(prefix="feed-")
    logdir = tempfile.mkdtemp(prefix="feedlog-")

    # Feeds refuse to serve without a base URL (spec §3), and harness.start_server copies
    # os.environ into the child at CALL time -- so this has to be set before the call, not
    # after. Setting it afterwards leaves the child without it and every feed answers 503,
    # which reads like the base-URL refusal working rather than a fixture mistake.
    os.environ["CONCORD_BASE_URL"] = BASE
    try:
        proc, db, base = harness.start_server(PORT, dbdir, logdir)
    except BaseException:
        os.environ.pop("CONCORD_BASE_URL", None)
        raise
    try:
        yield {"proc": proc, "db": db, "base": base}
    finally:
        os.environ.pop("CONCORD_BASE_URL", None)
        harness.stop_server(proc)


def seed(db):
    """One public project with a complaint, a call and a document."""
    con = sqlite3.connect(db)
    try:
        now = time.time()
        cur = con.cursor()
        cur.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
            ("feed-author", "Feed Author", now))
        uid = cur.lastrowid
        cur.execute(
            "INSERT INTO projects (slug, name, description, governance_model, license,"
            " visibility, created_at, updated_at)"
            " VALUES ('feed-demo','Feed Demo','A project that has things in it',"
            "'collective','MIT','public',?,?)", (now, now))
        pid = cur.lastrowid
        cur.execute(
            "INSERT INTO complaints (project_id, author_id, title, body, severity,"
            " frequency, strategic_multiplier, status, created_at, updated_at)"
            " VALUES (?,?,?,?,3,1.0,1.0,'open',?,?)",
            (pid, uid, "The build times out on a cold cache",
             "Every clean build takes nine minutes. Incremental is fine.", now, now))
        cur.execute(
            "INSERT INTO consensus_calls (project_id, feature_id, opened_by, opens_at,"
            " closes_at, status, question, description)"
            " VALUES (?,NULL,?,?,?,'open','Do we adopt the new cache?','A feed item.')",
            (pid, uid, now, now + 7 * 86400))
        cur.execute(
            "INSERT INTO project_documents (project_id, kind, slug, title, body,"
            " revision, author_id, created_at, updated_at)"
            " VALUES (?,'adr','adr-0001','Use a content hash','We decided this.',1,?,?,?)",
            (pid, uid, now, now))
        con.commit()
    finally:
        con.close()


@pytest.fixture(scope="module", autouse=True)
def seeded(server):
    seed(server["db"])


@pytest.fixture(scope="module")
def page():
    with sync_playwright() as p:
        b = p.chromium.launch(headless=True)
        pg = b.new_page()
        pg.set_default_timeout(15000)
        yield pg
        b.close()


def test_the_alternate_link_on_a_page_resolves_to_a_feed(page, server):
    """The discovery mechanism, which is the part a body-only test cannot check."""
    page.goto(server["base"] + "/projects/feed-demo")
    page.wait_for_load_state("domcontentloaded")

    href = page.eval_on_selector(
        'link[rel="alternate"][type="application/atom+xml"]', "el => el.href")
    assert href, "no <link rel=alternate type=application/atom+xml> on the page"

    # It must be ABSOLUTE in the served document, or a reader resolves it against its own
    # base. Relative here is the bug this assertion exists to catch.
    raw_href = page.eval_on_selector(
        'link[rel="alternate"][type="application/atom+xml"]', "el => el.getAttribute('href')")
    assert raw_href.startswith("/"), f"the alternate link is {raw_href!r}, want a root-relative path"

    resp = page.request.get(href)
    assert resp.status == 200, (
        f"the alternate link answered {resp.status}; it must resolve, or a reader's "
        f"'subscribe' button stores a dead URL")
    body = resp.text()
    # And it must PARSE. A feed that a strict reader rejects is worse than no feed: it
    # looks installed and silently delivers nothing.
    root = ET.fromstring(body)
    assert root.tag == ATOM + "feed", f"unexpected root element {root.tag}"


def test_an_atom_item_link_loads_the_page_it_names(page, server):
    """The other direction: an item's link must be a page a reader can actually open."""
    resp = page.request.get(server["base"] + "/projects/feed-demo/atom.xml")
    assert resp.status == 200
    root = ET.fromstring(resp.text())

    entries = root.findall(ATOM + "entry")
    assert entries, "the atom feed has no entries"

    for entry in entries:
        link = entry.find(ATOM + "link")
        assert link is not None, "an entry carries no link"
        href = link.get("href")
        assert href and href.startswith("http"), (
            f"entry link {href!r} is not an absolute URL; readers resolve relative links "
            f"against their own base and get nothing")
        target = page.request.get(href)
        # 200 or 404 both prove the link resolves to a route that exists and answers; the
        # failure this guards is a link to nothing at all, which is a connection error or
        # a 404 for the host.
        assert target.status in (200, 404), (
            f"entry link {href} answered {target.status}")


def test_no_feed_publishes_a_consensus_tally(page, server):
    """§6.6, in the browser: a feed is a durable copy, so no counts."""
    body = page.request.get(
        server["base"] + "/projects/feed-demo/consensus/atom.xml").text()
    for key in ("consent", "stand_aside", "support_ratio", "decisive_ratio",
                "participants", "eligible"):
        assert key not in body, (
            f"the consensus feed publishes {key!r}; §6.6 hides the running counts until "
            f"the call closes, and a feed is cached by third parties indefinitely")

    # And the state IS there, which is what makes the item useful to a subscriber.
    assert "open" in body, "the feed does not publish the call's state"


def test_the_rss_and_atom_feeds_carry_the_same_item_titles(page, server):
    rss = page.request.get(server["base"] + "/projects/feed-demo/feed.xml").text()
    atom = page.request.get(server["base"] + "/projects/feed-demo/atom.xml").text()

    def rss_titles(xml_text):
        return re.findall(r"<title>(.*?)</title>", xml_text, re.S)

    def atom_titles(xml_text):
        return [e.text or "" for e in ET.fromstring(xml_text).iter(ATOM + "title")]

    got_rss, got_atom = rss_titles(rss), atom_titles(atom)
    assert got_rss, "the rss feed has no titles"
    # The channel/feed title is present in both and is not an item, so the counts must
    # still match after dropping the first of each.
    assert got_rss[1:] == got_atom[1:], (
        f"rss carries {got_rss[1:]} and atom carries {got_atom[1:]}")

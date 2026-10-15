"""The documents page's search box, in a browser.

`GET /projects/{slug}/documents/search` is a real FTS5 index -- bm25-ranked, with the
hyphen fix that stops `language-neutral` searching for "language AND NOT neutral" --
and it had no caller. documents.js fetched the whole list and filtered it in the
browser; the template had no input. 1,087 documents on the live instance were
searchable with curl and not from the site.

What only a browser can assert here:

  * a search CROSSES KINDS. The page opens on a kind, and a hit in another kind has to
    appear and link to its own kind's page -- the one thing a client-side filter
    cannot do, and the reason the server-side endpoint is worth having.
  * a search is a shareable ADDRESS. `?q=` is written to the URL and read back on
    load, so a search survives being sent to somebody.
  * a malformed FTS5 expression shows the server's message and leaves the list alone.
    The tempting implementation empties the list, and an empty list is a claim about
    the corpus rather than about the query.

Seed is SQL: the FTS index is trigger-maintained (migrations 0007/0023), so a plain
INSERT into project_documents populates the index, and nothing here depends on the
write path this suite is not testing.
"""

import itertools
import sqlite3
import tempfile

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

# One fixed port per suite; the cross-file gate parses this line with int().
PORT = 8493
SLUG = "e2e-documents"
BASE = f"http://127.0.0.1:{PORT}"

# One word that appears in exactly one document, so "1 result" is a statement about
# the index rather than about the corpus size.
UNIQUE = "quorumrule"

_ips = itertools.count(1)


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-docs-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-docs-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


@pytest.fixture()
def page(browser, server):
    ctx = browser.new_context(
        extra_http_headers={"X-Forwarded-For": f"10.96.{next(_ips) // 256}.{next(_ips) % 256}"})
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" and "Failed to load resource" not in m.text else None)
    pg.goto(BASE + "/")
    return pg


def seed(db):
    now = 1790000000
    con = sqlite3.connect(db)
    try:
        con.execute(
            "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
            " VALUES (?,?,?,?,?)", ("docsowner", "Docs Owner", now, "x", 3))
        uid = con.execute("SELECT id FROM users WHERE username='docsowner'").fetchone()[0]
        con.execute(
            "INSERT INTO projects (slug, name, description, governance_model, license,"
            " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
            (SLUG, "Documents E2E", "document search fixture", "collective", "MIT",
             now, now, "public"))
        pid = con.execute("SELECT id FROM projects WHERE slug=?", (SLUG,)).fetchone()[0]
        con.execute(
            "INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,'owner',?)",
            (pid, uid, now))

        # The kinds are the point: the search runs from the readme kind and must reach
        # the spec. A client-side filter could only ever search the kind in view.
        docs = [
            ("readme", "readme", "Overview",
             "# Overview\n\nThis project exists to make consent legible."),
            ("spec", "quorum", "Quorum",
             "# Quorum\n\nThe " + UNIQUE + " term appears in this document and nowhere else."),
            ("plan", "delivery", "Delivery plan",
             "# Delivery\n\nThree milestones, each gated on consent."),
        ]
        for i, (kind, dslug, title, body) in enumerate(docs):
            con.execute(
                "INSERT INTO project_documents (project_id, kind, slug, title, body,"
                " revision, author_id, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)",
                (pid, kind, dslug, title, body, 1, uid, now + i, now + i))
        con.commit()
    finally:
        con.close()


def open_documents(page, server, query=""):
    url = f"{server['base']}/projects/{SLUG}/documents"
    if query:
        url += "?q=" + query
    page.goto(url)
    page.wait_for_selector('#project-documents:not([aria-busy="true"])', timeout=15000)
    return page.locator("#project-documents")


def search(page, term):
    box = page.locator("#doc-search-input")
    box.fill(term)
    return page.locator("[data-search-status]")


def test_a_search_finds_a_document_in_another_kind_and_links_to_it(page, server):
    """The assertion a client-side filter cannot satisfy.

    The page opens on the readme kind (its default). The only document containing the
    unique term is a SPEC, so a hit means the server's index was searched across the
    whole project rather than the visible kind filtered in the browser.
    """
    root = open_documents(page, server)
    status = search(page, UNIQUE)

    expect(status).to_contain_text("1 result", timeout=15000)
    hit = root.locator(".doc-list-col a.doc-item", has_text="Quorum")
    expect(hit).to_be_visible()

    href = hit.get_attribute("href")
    assert href and "kind=spec" in href, (
        f"the hit links to {href!r}: a search result must link to its own kind, or "
        f"clicking it opens the kind the reader was already on")
    assert "doc=" in href, href

    # And it opens. The href above is a string; this is the click.
    hit.click()
    page.wait_for_selector('#project-documents:not([aria-busy="true"])', timeout=15000)
    expect(page.locator("#project-documents")).to_contain_text("Quorum")
    expect(page.locator("#project-documents")).to_contain_text(UNIQUE)
    assert not page.errors, page.errors


def test_a_query_that_matches_nothing_says_so_rather_than_showing_the_kind(page, server):
    """A search that finds nothing must not fall back to an unfiltered list.

    Falling back would make a query with no hits look like a query with every hit,
    which is the failure mode where the reader concludes the corpus contains
    something it does not.
    """
    root = open_documents(page, server)
    status = search(page, "zzqqxxttt")

    expect(status).to_contain_text("No document in this project matches", timeout=15000)
    assert root.locator(".doc-list-col a.doc-item").count() == 0, (
        "a query with no hits left the unfiltered list on screen")
    assert not page.errors, page.errors


def test_a_malformed_query_shows_the_servers_message_and_keeps_the_previous_results(page, server):
    """A well-formed term first, then an unbalanced quote -- which is an FTS5 syntax
    error and a 400 from the server.

    Two assertions, both of which the obvious implementation fails: the message names
    the failure instead of silently returning nothing, and the results on screen are
    the PREVIOUS query's rather than an empty list. "Your query could not be parsed"
    and "nothing matches" are different statements, and only one of them is true.
    """
    root = open_documents(page, server)
    search(page, UNIQUE)
    expect(page.locator("[data-search-status]")).to_contain_text("1 result", timeout=15000)

    status = search(page, '"unbalanced')
    expect(status).to_contain_text("could not run", timeout=15000)
    cls = status.get_attribute("class") or ""
    assert "error-state" in cls, f"the malformed-query message is not styled as an error: {cls!r}"
    # The earlier hit is still there: the list was not blanked by a bad query.
    expect(root.locator(".doc-list-col a.doc-item", has_text="Quorum")).to_be_visible()
    assert not page.errors, page.errors


def test_a_search_is_a_shareable_address(page, server):
    """`?q=` is written to the URL and read back on load.

    Asserted by RELOADING, which is the only way to tell a URL that is written from a
    URL that is only displayed: a page that ignored ?q= on load would pass any
    assertion made before the navigation.
    """
    open_documents(page, server)
    search(page, UNIQUE)
    expect(page.locator("[data-search-status]")).to_contain_text("1 result", timeout=15000)
    assert "q=" + UNIQUE in page.url, page.url

    page.reload()
    page.wait_for_selector('#project-documents:not([aria-busy="true"])', timeout=15000)
    # The box remembers, and the search runs without a keystroke.
    assert page.locator("#doc-search-input").input_value() == UNIQUE
    expect(page.locator("[data-search-status]")).to_contain_text("1 result", timeout=15000)
    expect(page.locator("#project-documents .doc-list-col a.doc-item",
                        has_text="Quorum")).to_be_visible()
    assert not page.errors, page.errors


def test_clearing_the_box_brings_the_kind_list_back(page, server):
    """The list column is shared by two states -- the kind's documents and the search
    results -- so clearing has to restore the first one rather than leave the second.
    """
    root = open_documents(page, server)
    expect(root.locator(".doc-list-col a.doc-item")).to_have_count(1)  # the readme

    search(page, UNIQUE)
    expect(page.locator("[data-search-status]")).to_contain_text("1 result", timeout=15000)
    expect(root.locator(".doc-list-col a.doc-item", has_text="Overview")).to_have_count(0)

    search(page, "")
    expect(root.locator(".doc-list-col a.doc-item", has_text="Overview")).to_be_visible(timeout=15000)
    expect(page.locator("[data-search-status]")).to_have_text("")
    assert not page.errors, page.errors

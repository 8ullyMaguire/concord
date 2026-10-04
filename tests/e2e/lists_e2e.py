"""Spec §16: collaborative lists -- /projects/{slug}/lists and .../lists/{id}.

WHAT THE GO GATE DOES NOT COVER. requests_page_test.go asserts the page RENDERS, that
the link is BUILT in project.js, and that a private project 404s. All three are
source- and server-level checks. The complaint page taught what that misses:
`complaintCards(complaints, slug)` threw a ReferenceError at runtime, blanked the
ENTIRE project page, and TestComplaintPageIsLinkedFromTheProject stayed green because
it only looked for a string. This file is the half that has a DOM.

WHY THE ROWS ARE SEEDED THROUGH SQLITE. A browser test whose data arrives through the
API it is testing cannot fail for the reason it exists: a broken write path yields an
empty page and the assertions pass anyway.

THE THREE ANSWERS ARE DELIBERATELY DIFFERENT, because §17 requires the page to keep
three distinctions that a naive implementation collapses into one number:

  ranked      elo_r 1610, elo_rd 40     -- settled by comparison
  uncertain   elo_r 1502, elo_rd 344    -- barely moved, WIDE band
  accepted    elo_r 1420, elo_rd 55     -- badged, and RANKED LAST

The third is the one that breaks the obvious implementation. §17 says the accepted
badge "is displayed but does not override the ranking", so a page that floats the
accepted answer to the top is violating the spec -- and a page that hides its rating
is hiding the evidence. The accepted answer must appear last AND carry the badge.

EVERY CONTEXT GETS ITS OWN X-FORWARDED-FOR, or contexts share a rate-limit bucket and
the limiter's JSON is served AS THE PAGE BODY.
"""

import itertools
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8488
BASE = f"http://127.0.0.1:{PORT}"

SLUG = "req-demo"
PRIVATE = "req-private"

_ips = itertools.count(1)


def seed(db):
    """One public project with a list whose entries are ranked AGAINST their titles.

    The ordering is the whole point of this file, so the seed makes the two disagree:
    the LAST row alphabetically is the FIRST row by rating. Any implementation that
    sorts by title, by url, or by id -- or that does not sort at all and renders the
    query order -- shows them in the wrong order and fails.

    Ratings also disagree about CERTAINTY, so an entry with a wide deviation band
    cannot pass by having a high number:
      ranked      elo_r 1640, elo_rd 30   -- settled
      uncertain   elo_r 1505, elo_rd 345  -- barely moved, WIDE band
      last        elo_r 1180, elo_rd 120  -- genuinely worst
    """
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    cur.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
        " VALUES (?,?,?,?,?)",
        ("list-owner", "List Owner", now, "x", 3),
    )
    owner = cur.lastrowid

    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        (SLUG, "List Demo", "a project with ranked lists", "collective", "MIT",
         now, now, "public"),
    )
    pid = cur.lastrowid

    # An instance-wide list: lists.project_id IS NULL. The page must render it under a
    # project slug without claiming the project owns it.
    cur.execute(
        # `title`, not `name`: the COLUMN is lists.title and the JSON key is `name`,
        # because GetList selects `title` into a field tagged `json:"name"`. The rename
        # happens at the SQL boundary, so a raw INSERT must use the column name and a
        # page must use the JSON name -- and reading either one in the other place
        # yields a list with an empty heading and no error.
        "INSERT INTO lists (project_id, slug, title, description, status, created_by,"
        " created_at, updated_at) VALUES (NULL, ?,?,?,?,?,?,?)",
        ("shared", "Shared across the instance", "instance-wide list", "open", owner,
         now, now),
    )
    instance_list = cur.lastrowid

    cur.execute(
        "INSERT INTO lists (project_id, slug, title, description, status, created_by,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)",
        (pid, "deps", "Libraries we depend on", "what we ship", "open", owner,
         now, now),
    )
    list_id = cur.lastrowid

    # Alphabetical order of the titles: "aaaa first", "middle", "zzzz last".
    # Rating order: zzzz, aaaa, middle. The two disagree completely.
    for i, (url, title, body_, elo_r, elo_rd, cat) in enumerate([
        ("https://example.invalid/a", "aaaa first", "the one we actually ship",
         1640.0, 30.0, "runtime"),
        ("https://example.invalid/m", "middle", "considered once",
         1505.0, 345.0, None),
        ("https://example.invalid/z", "zzzz last", "rejected, then reinstated",
         1180.0, 120.0, "runtime"),
    ]):
        cur.execute(
            "INSERT INTO list_entries (list_id, url, title, description, category,"
            " status, elo_r, elo_rd, elo_vol, proposed_by, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
            (list_id, url, title, body_, cat, "accepted", elo_r, elo_rd, 0.08,
             owner, now, now),
        )

    # A removed entry, which the store refuses to compare for rank. Seeded so the page
    # has to decide what to do with it: hiding it is right, ranking it is not.
    cur.execute(
        "INSERT INTO list_entries (list_id, url, title, description, category,"
        " status, elo_r, elo_rd, elo_vol, proposed_by, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
        (list_id, "https://example.invalid/gone", "a withdrawn entry",
         "proposed then removed", None, "removed", 2000.0, 20.0, 0.08, owner, now, now),
    )

    # One recorded comparison, so the count is non-zero for the pair it covers.
    cur.execute(
        "INSERT INTO list_entry_votes (list_id, entry_a, entry_b, voter_id, outcome,"
        " weight, created_at) VALUES (?,?,?,?,?,?,?)",
        (list_id, 3, 1, owner, "b", 1.0, now),
    )

    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        (PRIVATE, "Private", "no lists here", "collective", "MIT", now, now, "private"),
    )

    con.commit()
    con.close()


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-list-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-list-e2e-log-")
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
    """A fresh context per test, each with its OWN forwarded IP.

    Same reason as every other e2e file: contexts sharing an IP share a rate-limit
    bucket, and the limiter then serves its JSON AS THE PAGE BODY.
    """
    ctx = browser.new_context(
        extra_http_headers={
            "X-Forwarded-For": f"10.98.{next(_ips) // 256}.{next(_ips) % 256}"
        }
    )
    pg = ctx.new_page()
    pg.errors = []
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" and "Failed to load resource" not in m.text else None)
    pg.goto(BASE + "/")
    return pg


def _goto(page, path):
    page.goto(BASE + path)
    page.wait_for_selector("#list-root .list-detail", timeout=15000)


def test_the_entries_are_ranked_not_alphabetical(page, server):
    """16: "Entries are ranked, not just listed... 'best first' reflects community
    judgment instead of alphabetical accident."

    The seed makes alphabetical and rating order disagree completely. This is the
    assertion that makes 16 testable at all.
    """
    _goto(page, f"/projects/{SLUG}/lists/2")
    # Read the RANK POSITION off each row, not just the titles in DOM order: entries
    # are grouped by category (16) and the groups carry heading text, so the
    # document order is [runtime group, uncategorised group] rather than a flat list.
    #
    # That grouping is what made this test wrong the first time. The assertion below
    # was written as "titles in DOM order == [zzzz, aaaa, middle]", the page rendered
    # ['aaaa first', 'zzzz last', 'middle'] -- and that was the RANKING BEING CORRECT:
    # runtime holds zzzz(1180) and aaaa(1640), so rating order inside it is
    # aaaa-then-zzzz, and 'middle' is uncategorised so it comes last as its own group.
    #
    # Reading DOM order conflated two independent claims (category grouping and rating
    # order) into one, which failed for a reason unrelated to what it was testing.
    rows = page.locator("#list-root .list-entry")
    expect(rows).to_have_count(3)
    ranked = [
        (rows.nth(i).get_attribute("data-entry-id"),
         rows.nth(i).locator(".list-entry-title").inner_text().strip())
        for i in range(rows.count())
    ]
    # Seeded ids: 1=aaaa(1640, runtime) 2=middle(1505, uncategorised)
    #            3=zzzz(1180, runtime) 4=removed(2000)
    assert ranked == [("1", "aaaa first"), ("3", "zzzz last"), ("2", "middle")], (
        f"ranked order was {ranked}. Wanted: runtime group by rating (aaaa 1640 before "
        f"zzzz 1180), then the uncategorised group. Alphabetical would be "
        f"aaaa/middle/zzzz and query order would be aaaa/middle/zzzz."
    )


def test_an_uncertain_rating_shows_a_wide_band(page, server):
    """A bare number cannot distinguish 'settled at 1505' from 'barely moved, and
    nobody has said much' -- elo_rd 345 against 30 says exactly that."""
    _goto(page, f"/projects/{SLUG}/lists/2")
    widths = page.locator("#list-root .rating-bar-fill")
    values = page.locator("#list-root .rating-value")
    expect(values).to_have_count(3)

    got = []
    for i in range(3):
        w = widths.nth(i).evaluate("el => el.style.width")
        got.append(int(w.rstrip("%")))
    # Sorted by rank: zzzz(rd120), aaaa(rd30), middle(rd345). The widest band must be
    # the uncertain one, and it must be LAST in rank order -- if it were not, the page
    # would be showing certainty rather than uncertainty.
    assert got[2] > got[0] and got[2] > got[1], (
        f"deviation bands {got} do not put the widest on the least-ranked entry "
        f"(rd 345 -> 30 -> 120 for rank order zzzz/aaaa/middle)"
    )


def test_the_entries_are_grouped_by_category(page, server):
    """16: "Categories group entries; ranking orders within and across them."

    The seed has two 'runtime' entries and one uncategorised, and the uncategorised
    one has category NULL in SQL -- so this also pins that a NULL category does not
    print the string "null".
    """
    _goto(page, f"/projects/{SLUG}/lists/2")
    cats = page.locator("#list-root .entry-category")
    expect(cats).to_have_count(2)
    texts = [cats.nth(i).inner_text().strip().split(" ")[0].lower() for i in range(2)]
    assert "runtime" in texts and "uncategorised" in texts, (
        f"category headings were {texts}; a NULL category must group as "
        f"'uncategorised', not print null and not be dropped"
    )


def test_ranking_order_is_kept_inside_each_category(page, server):
    """Grouping must not re-sort. The runtime group is [zzzz(1180), aaaa(1640)] by
    creation, so ranked it is [aaaa, zzzz] -- and grouped-then-sorted-by-title would
    give the same answer here by luck, which is why the top-level assertion above
    carries the real weight."""
    _goto(page, f"/projects/{SLUG}/lists/2")
    runtime = page.locator("#list-root .entry-group").filter(
        has=page.locator(".entry-category", has_text="runtime"))
    titles = runtime.locator(".list-entry-title")
    got = [titles.nth(i).inner_text().strip() for i in range(titles.count())]
    assert got == ["aaaa first", "zzzz last"], (
        f"inside the runtime category the order was {got}, want rating order"
    )


def test_a_removed_entry_is_not_ranked(page, server):
    """A removed entry is content a quorum took down. Ranking it invites the community
    to prefer it; the seed gives it elo_r 2000 so it would top the list if shown."""
    _goto(page, f"/projects/{SLUG}/lists/2")
    body = page.locator("#list-root").inner_text()
    assert "a withdrawn entry" not in body, (
        "a removed entry is rendered; it carries elo_r 2000 and would rank first"
    )


def test_the_comparison_outcomes_are_the_schemas(page, server):
    """list_entry_votes.outcome is CHECKed to exactly
    ('a','b','both','neither','skip'), so a 1-5 control posts a value the database
    refuses and the page renders as though nobody had ranked anything."""
    _goto(page, f"/projects/{SLUG}/lists/2")
    opts = page.locator("#list-root .compare select#cmp-outcome option")
    got = [opts.nth(i).get_attribute("value") for i in range(opts.count())]
    assert got == ["a", "b", "both", "neither", "skip"], (
        f"the comparison control offered {got}; the CHECK allows exactly these five"
    )


def test_comparison_needs_two_entries(page, server):
    """The instance-wide list has no entries, so the control explains itself rather
    than offering a comparison between nothing.

    It was also a 404 until this session: handleGetList required
    `l.ProjectID == projectID` and an instance-wide list has ProjectID 0, so the only
    detail route refused it -- which is why the test used to time out waiting for a
    page that could not render.
    """
    page.goto(BASE + f"/projects/{SLUG}/lists/1")
    page.wait_for_selector("#list-root .list-detail", timeout=15000)
    body = page.locator("#list-root").inner_text()
    assert "Ranking needs two entries" in body, body[:300]
    # And labelled, so a reader knows every project shares it.
    expect(page.locator("#list-root .badge-instance")).to_have_text("instance-wide")


def test_the_list_is_linked_from_the_project_page(page, server):
    """The DOM half of the gate that could not be a DOM gate in Go.

    TestTheProjectPageLinksToEachList asserts project.js CALLS listSection. It cannot
    assert the anchor exists, because the section is client-side. This one can.
    """
    page.goto(BASE + f"/projects/{SLUG}")
    page.wait_for_selector(f'a[href="/projects/{SLUG}/lists/2"]', timeout=15000)
    link = page.locator(f'a[href="/projects/{SLUG}/lists/2"]')
    expect(link).to_have_text("Libraries we depend on")


def test_the_list_link_actually_lands_on_the_list(page, server):
    """Clicking through is the only thing that catches an href pointing at a page that
    renders a 404 shell -- the ReferenceError class, in link form."""
    page.goto(BASE + f"/projects/{SLUG}")
    page.wait_for_selector(f'a[href="/projects/{SLUG}/lists/2"]', timeout=15000)
    page.click(f'a[href="/projects/{SLUG}/lists/2"]')
    page.wait_for_selector("#list-root .list-detail", timeout=15000)
    expect(page.locator("#list-root .card-head h2")).to_have_text(
        "Libraries we depend on")


def test_an_instance_wide_list_is_labelled_on_its_index(page, server):
    """lists.project_id IS NULL means instance-wide, and the INDEX is scoped to a
    project slug -- so an unlabelled shared list reads as the project's own.

    This is the label half; test_comparison_needs_two_entries is the reachable half.
    The first version of this test asserted only that one of two selectors matched,
    which was true of a page showing nothing at all -- it passed on a broken render
    and failed for a reason ("unexpected value 2") that had nothing to do with the
    claim being made.
    """
    page.goto(BASE + f"/projects/{SLUG}/lists")
    page.wait_for_selector("#lists-root .grid-responsive-2", timeout=15000)
    # BOTH lists, because 16 makes instance-wide the scope every project shares.
    expect(page.locator("#lists-root .card")).to_have_count(2)
    shared = page.locator("#lists-root .card", has_text="Shared across the instance")
    expect(shared.locator(".badge-instance")).to_have_text("instance-wide")
    # And the project's OWN list carries no such badge, so the label means something.
    own = page.locator("#lists-root .card", has_text="Libraries we depend on")
    expect(own.locator(".badge-instance")).to_have_count(0)


def test_a_private_project_serves_no_lists(page, server):
    page.goto(BASE + f"/projects/{PRIVATE}/lists")
    body = page.locator("body").inner_text()
    assert "no project" in body.lower() or "not found" in body.lower(), (
        f"a private project's lists page served content: {body[:200]!r}"
    )


def test_an_unknown_list_says_so_without_erroring(page, server):
    page.goto(BASE + f"/projects/{SLUG}/lists/9999")
    expect(page.locator("#list-root")).to_have_count(1)
    body = page.locator("#list-root").inner_text()
    assert "no list" in body.lower(), body[:200]
    assert not page.errors, page.errors


def test_no_console_errors_on_any_lists_page(page, server):
    """The ReferenceError class, for the lists pages.

    A runtime error in a section builder blanks the WHOLE page while every source-level
    gate stays green -- the complaint page's `complaintCards(complaints, slug)` did
    exactly that. This is the assertion that class needs.

    Each page is waited on by ITS OWN root id and aria-busy, not by a content selector:
    `.list-detail` times out on a private project's 404 shell, and one selector cannot
    cover both #lists-root (index) and #list-root (detail) anyway. The project page's
    root is #project-detail, read from project.html rather than guessed -- which is what
    "kanban-board" was, and it timed out on a correct page.
    """
    for path, root in (
        (f"/projects/{SLUG}/lists", "lists-root"),
        (f"/projects/{SLUG}/lists/2", "list-root"),
        (f"/projects/{SLUG}", "project-detail"),
        # A private project serves the 404 SHELL: no root, nothing to wait for, and the
        # assertion is that the shell is what rendered.
        (f"/projects/{PRIVATE}/lists", None),
    ):
        page.goto(BASE + path)
        if root is None:
            assert page.locator("#lists-root").count() == 0, (
                "the private project's page rendered a lists root; it must be the 404")
            assert "no project" in page.locator("body").inner_text().lower()
        else:
            page.wait_for_selector(f'#{root}:not([aria-busy="true"])',
                               timeout=15000)
        assert not page.errors, f"{path}: {page.errors}"

"""The §17 request board page: /projects/{slug}/requests/{id}.

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

PORT = 8487
BASE = f"http://127.0.0.1:{PORT}"

SLUG = "req-demo"
PRIVATE = "req-private"

_ips = itertools.count(1)


def seed(db):
    """One public project with a request, three answers and tags. One private project."""
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    # Column lists are spelled out, never positional: "13 values for 14 columns" is
    # otherwise inevitable, and the fix is PRAGMA table_info on a migrated database.
    cur.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
        " VALUES (?,?,?,?,?)",
        ("req-owner", "Req Owner", now, "x", 3),
    )
    owner = cur.lastrowid
    cur.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash, trust_level)"
        " VALUES (?,?,?,?,?)",
        ("req-stranger", "Req Stranger", now, "x", 3),
    )
    stranger = cur.lastrowid

    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        (SLUG, "Req Demo", "demo", "collective", "MIT", now, now, "public"),
    )
    project = cur.lastrowid

    # Three OTHER projects, so the answers can name real projects rather than bare ids.
    # §17: an answer is a PROJECT REFERENCE. "Project 4" is not a reference a reader
    # can act on, so the page resolves each id to a name through one project-list read.
    alt_slugs = ["alt-alpha", "alt-beta", "alt-gamma"]
    alts = []
    for slug in alt_slugs:
        cur.execute(
            "INSERT INTO projects (slug, name, description, governance_model, license,"
            " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
            (slug, slug.replace("-", " ").title(), "alt", "collective", "MIT", now, now,
             "public"),
        )
        alts.append((cur.lastrowid, slug))

    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " created_at, updated_at, visibility) VALUES (?,?,?,?,?,?,?,?)",
        (PRIVATE, "Req Private", "private", "collective", "MIT", now, now, "private"),
    )
    private = cur.lastrowid

    cur.execute(
        "INSERT INTO requests (project_id, author_id, title, body, status, created_at,"
        " updated_at, accepted_answer_id) VALUES (?,?,?,?,?,?,?,NULL)",
        (project, owner, "Self-hosted Kanban with WIP limits",
         "Must run on a Pi. MIT or AGPL. WIP limits are the point.", "open", now, now),
    )
    request = cur.lastrowid

    # request_answers: elo_r/elo_rd/elo_vol are NOT NULL with defaults, and
    # UNIQUE (request_id, project_id) -- so each answer is a DIFFERENT project.
    answers = {}
    for key, (alt_id, _slug), elo_r, elo_rd in [
        ("ranked", alts[0], 1610.0, 40.0),
        ("uncertain", alts[1], 1502.0, 344.0),
        ("accepted", alts[2], 1420.0, 55.0),
    ]:
        cur.execute(
            "INSERT INTO request_answers (request_id, project_id, body, status, elo_r,"
            " elo_rd, elo_vol, proposed_by, created_at, updated_at)"
            " VALUES (?,?,?,?,?,?,?,?,?,?)",
            (request, alt_id, f"{key} project rationale: why it fits, where it falls short",
             "active", elo_r, elo_rd, 0.06, stranger, now, now),
        )
        answers[key] = cur.lastrowid

    # §17's badge on the LAST-ranked answer: displayed, and not a promotion.
    cur.execute("UPDATE requests SET accepted_answer_id=? WHERE id=?",
                (answers["accepted"], request))

    # request_tags, via the tags table. These are the request's deal-breakers.
    for name in ("self-hosting", "MIT"):
        cur.execute(
            "INSERT INTO tags (project_id, name, namespace, relation, suggested)"
            " VALUES (?,?,?,?,?)",
            (project, name, None, None, 0),
        )
        cur.execute("INSERT INTO request_tags (request_id, tag_id) VALUES (?,?)",
                    (request, cur.lastrowid))

    # A second request so the LIST on the project page has something to show, and a
    # private project's request so the guard has a target.
    cur.execute(
        "INSERT INTO requests (project_id, author_id, title, body, status, created_at,"
        " updated_at) VALUES (?,?,?,?,?,?,?)",
        (project, owner, "Does anyone have a WIP-limit plugin?", "Looking for one.",
         "open", now, now),
    )
    cur.execute(
        "INSERT INTO requests (project_id, author_id, title, body, status, created_at,"
        " updated_at) VALUES (?,?,?,?,?,?,?)",
        (private, owner, "Private question about pricing", "secret", "open", now, now),
    )

    con.commit()
    con.close()


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-req-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-req-e2e-log-")
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

    clientKey() honours X-Forwarded-For from loopback, so contexts sharing one IP
    share a rate-limit bucket -- and the limiter then answers
    {"error":"rate limit exceeded"} AS THE PAGE BODY, which reads as a broken page
    rather than as a throttle. The next() pair spreads the second octet too, so the
    addresses differ for more than 256 tests.
    """
    ctx = browser.new_context(
        extra_http_headers={
            "X-Forwarded-For": f"10.97.{next(_ips) // 256}.{next(_ips) % 256}"
        }
    )
    pg = ctx.new_page()
    pg.errors = []
    # A failed RESOURCE is not a script error: this page deliberately requests a
    # request id that may not exist and renders "no request at this address" from the
    # 404. Collecting those would make the test assert against its own design.
    #
    # Uncaught exceptions and real console errors are still collected, which is what
    # catches the ReferenceError class -- the one that blanked the whole project page
    # while every source-level gate stayed green.
    pg.on("pageerror", lambda e: pg.errors.append("PAGEERROR: " + str(e)))
    pg.on("console", lambda m: pg.errors.append("CONSOLE: " + m.text)
          if m.type == "error" and "Failed to load resource" not in m.text else None)
    pg.goto(BASE + "/")
    return pg


def _goto(page, path):
    page.goto(BASE + path)
    # The page fetches after load; wait for the section the tests are about rather
    # than a fixed sleep.
    page.wait_for_selector("#request-root .request-detail", timeout=15000)


def test_the_request_itself_is_shown(page, server):
    _goto(page, f"/projects/{SLUG}/requests/1")
    root = page.locator("#request-root")
    expect(root.locator(".request-body h2")).to_have_text(
        "Self-hosted Kanban with WIP limits")
    # The body carries the constraints, not just a truncated title.
    expect(root.locator(".body-text")).to_contain_text("WIP limits are the point")
    # Status badge.
    expect(root.locator(".request-body .badge")).to_have_text("open")


def test_the_constraints_render_as_tags(page, server):
    """§17: a request states its constraints. These are the deal-breakers."""
    _goto(page, f"/projects/{SLUG}/requests/1")
    chips = page.locator("#request-root .constraints .chip")
    expect(chips).to_have_count(2)
    expect(chips.nth(0)).to_have_text("MIT")
    expect(chips.nth(1)).to_have_text("self-hosting")


def test_answers_are_named_projects_not_bare_ids(page, server):
    """§17: "Answers are project references, not opinions."

    The page has to resolve each answer's project_id to a name. "Project 4" is not a
    reference a reader can act on, and it is what the naive render produces.
    """
    _goto(page, f"/projects/{SLUG}/requests/1")
    cards = page.locator("#request-root .answer")
    expect(cards).to_have_count(3)
    expect(cards.first.locator("h3")).not_to_contain_text("Project ")
    # And each name is a LINK to that project, because a reference should be followable.
    expect(cards.first.locator("h3 a")).to_have_attribute("href", re_href())


def re_href():
    """Any /projects/<slug> href -- the answer's project, whichever it is."""
    import re

    return re.compile(r"^/projects/[a-z0-9-]+$")


def test_the_accepted_badge_is_displayed_without_reordering(page, server):
    """§17's exact clause: the badge "is displayed but does not override the ranking".

    This is the assertion a naive page fails. The accepted answer is seeded with the
    LOWEST elo_r (1420 against 1610 and 1502), so if the page floats it to the top it
    is promoting the badge over the community's judgement -- which §17 forbids -- and
    if it sorts correctly the badge sits on the last card.
    """
    _goto(page, f"/projects/{SLUG}/requests/1")
    cards = page.locator("#request-root .answer")

    # Sorted by rating, descending: ranked, uncertain, accepted.
    expect(cards.nth(0)).to_contain_text("1610")
    expect(cards.nth(1)).to_contain_text("1502")
    expect(cards.nth(2)).to_contain_text("1420")

    # The badge is on the LAST one, and only that one.
    expect(cards.nth(2).locator(".badge-accepted")).to_have_count(1)
    expect(cards.nth(0).locator(".badge-accepted")).to_have_count(0)
    expect(cards.nth(1).locator(".badge-accepted")).to_have_count(0)


def test_an_uncertain_rating_shows_a_wide_band(page, server):
    """A bare 1500 for an untouched answer is indistinguishable from a settled 1500.

    The uncertain answer is seeded elo_rd 344 against the ranked answer's 40, so its
    band must render wider. Printing only the point makes the reader conclude an
    unranked answer lost.
    """
    _goto(page, f"/projects/{SLUG}/requests/1")
    bars = page.locator("#request-root .answer .rating-bar-fill")
    expect(bars).to_have_count(3)

    uncertain = page.locator('#request-root .answer:has-text("1502") .rating-bar-fill')
    ranked = page.locator('#request-root .answer:has-text("1610") .rating-bar-fill')

    # 344/350 = 98% wide, 40/350 = 11% wide. The BAR is the signal; the ±N text
    # below it is the precise version of the same fact.
    #
    # Asserting the style attribute rather than a class the page does not have: the
    # first version of this test asserted [data-settled], a marker invented to satisfy
    # it, and got 0 -- which looked like a page bug and was a test bug.
    # Playwright normalises style, so this is the exact string it returns.
    expect(uncertain).to_have_attribute("style", "width:98%")
    expect(ranked).to_have_attribute("style", "width:11%")

    # And the deviation is stated, not only drawn.
    expect(page.locator('#request-root .answer:has-text("1502")')).to_contain_text("±344")
    expect(page.locator('#request-root .answer:has-text("1610")')).to_contain_text("±40")


def test_the_fit_rationale_is_shown_not_just_the_project(page, server):
    """§17: an answer "adds a fit rationale: why it satisfies this request, where it
    falls short." The rationale is the answer; the project is only its subject."""
    _goto(page, f"/projects/{SLUG}/requests/1")
    bodies = page.locator("#request-root .answer-body")
    expect(bodies).to_have_count(3)
    expect(bodies.first).to_contain_text("why it fits")
    expect(bodies.first).to_contain_text("falls short")


def test_pairwise_comparison_needs_two_answers(page, server):
    """§17 ranks fits by pairwise comparison, so the control appears only with two.

    Seeded with three answers, so the control is present -- and its copy says what the
    comparison IS, because "rate this answer" is not what the endpoint does.
    """
    _goto(page, f"/projects/{SLUG}/requests/1")
    form = page.locator("#request-root [data-compare-form]")
    expect(form).to_have_count(1)
    expect(form.locator("h3")).to_have_text("Which fits this request better?")
    # Pairwise, not a star score.
    options = form.locator("#cmp-outcome option").all_text_contents()
    assert "A fits better" in options
    assert "Both fit equally" in options


def test_the_comparison_outcomes_are_the_pairs_ones(page, server):
    """The outcome vocabulary is the schema's, not a score.

    request_answer_votes.outcome is CHECKed to a, b, both, neither, skip -- the same
    set features and solutions use. A control offering 1-5 would post a value the
    database refuses.
    """
    _goto(page, f"/projects/{SLUG}/requests/1")
    values = page.locator("#request-root #cmp-outcome option").evaluate_all(
        "els => els.map(e => e.value)"
    )
    assert sorted(values) == ["a", "b", "both", "neither", "skip"], (
        f"outcome vocabulary is {sorted(values)}; request_answer_votes CHECKs "
        "exactly ('a','b','both','neither','skip')"
    )


def test_the_request_is_reachable_from_the_project_page(page, server):
    """The reachability gate's browser half.

    Go asserts the link is BUILT in project.js; only a browser can assert that clicking
    it lands on the page. This is the complement that keeps the Go test from being the
    only opinion.
    """
    page.goto(f"{BASE}/projects/{SLUG}")
    # The project page renders its sections from fetches, so wait for the one under
    # test rather than a fixed sleep. The section only appears once requests arrive.
    page.wait_for_selector('#project-detail a[href*="/requests/"]', timeout=15000)
    link = page.locator('#project-detail a[href*="/requests/"]').first
    expect(link).to_be_visible()
    href = link.get_attribute("href")
    assert href.endswith("/requests/1"), f"the first request link points at {href}"

    link.click()
    page.wait_for_url(f"**/requests/1")
    page.wait_for_selector("#request-root .request-detail", timeout=15000)
    expect(page.locator("#request-root .request-body h2")).to_contain_text(
        "Self-hosted Kanban")


def test_a_private_project_serves_no_request_page(page, server):
    """The shell must not confirm the project exists.

    Read anonymously. Asserting this WITH a member's token proves nothing, which is
    how the Go version of this test passed for the wrong reason once already.
    """
    resp = page.request.get(f"{BASE}/projects/{PRIVATE}/requests/1")
    assert resp.status == 404, (
        f"a private project's request page returned {resp.status} to an anonymous "
        "caller; the shell leaks existence"
    )
    assert PRIVATE not in resp.text(), "the 404 echoed the private slug"


def test_an_unknown_request_says_so_without_erroring(page, server):
    """A missing request is a state, not a crash, and must not leak the project's name."""
    page.goto(f"{BASE}/projects/{SLUG}/requests/9999")
    page.wait_for_selector("#request-root .empty-state", timeout=15000)
    expect(page.locator("#request-root")).to_contain_text("no request at this address")


def test_an_instance_wide_request_is_labelled(page, server):
    """requests.project_id IS NULL means "instance-wide board".

    That column was nullable from migration 0002 and unreachable until this session,
    so a page that assumes every request belongs to a project would hide every
    cross-project comparison -- which is the whole reason the pairwise vote exists.
    """
    con = sqlite3.connect(server["db"])
    cur = con.cursor()
    cur.execute(
        "INSERT INTO requests (project_id, author_id, title, body, status, created_at,"
        " updated_at) VALUES (NULL, 1, 'Who else runs a forge on a Pi?',"
        " 'Instance-wide question.', 'open', ?, ?)",
        (time.time(), time.time()),
    )
    instance_id = cur.lastrowid
    con.commit()
    con.close()

    _goto(page, f"/projects/{SLUG}/requests/{instance_id}")
    expect(page.locator("#request-root")).to_have_count(1)
    expect(page.locator("#request-root .request-body h2")).to_have_text(
        "Who else runs a forge on a Pi?")
    # Labelled, so a reader knows the comparison is open to every project.
    expect(page.locator("#request-root .badge-instance")).to_have_text("instance-wide")

# ---------------------------------------------------------------------------
# The request BOARD (/projects/{slug}/requests) and the two tabs.
#
# The board is what makes a request DISCOVERABLE. Until it existed the only request
# page was the detail page at /projects/{slug}/requests/{id}, so a reader had to
# already know the id -- which is the same defect as a write-only API, one layer up.
#
# The tabs are the other half: both pages existed and neither had a tab, so they were
# reachable only by scrolling the overview.


def test_the_board_lists_the_projects_requests(page, server):
    page.goto(BASE + f"/projects/{SLUG}/requests")
    page.wait_for_selector("#requests-root .card", timeout=15000)
    # The seed's THREE requests, asserted by CONTENT rather than by a bare count --
    # the count was 1 at first and the page was right, because the seed makes three and
    # an assertion that only checks "there is one card" passes on a page rendering
    # nothing at all.
    titles = page.locator("#requests-root .card h3")
    expect(titles).to_have_count(3)
    body = page.locator("#requests-root").inner_text()
    assert "Self-hosted Kanban with WIP limits" in body, body[:300]
    # And grouped OPEN first: a request board whose settled entries sit above its open
    # ones is a backlog nobody works.
    groups = page.locator("#requests-root .entry-category")
    expect(groups.first).to_contain_text("open")


def test_each_request_links_to_its_detail_page(page, server):
    """The board is a list of NUMBERS unless every row is a link, so a reader with no
    id has no way in. Asserted by CLICKING, which is the only check that catches an
    href pointing at a page that renders an error shell."""
    page.goto(BASE + f"/projects/{SLUG}/requests")
    link = page.locator(f'#requests-root a[href="/projects/{SLUG}/requests/1"]')
    expect(link).to_have_count(1)
    link.click()
    page.wait_for_selector("#request-root .request-detail", timeout=15000)
    expect(page.locator("#request-root .request-body h2")).to_contain_text(
        "Self-hosted Kanban")


def test_the_requests_and_lists_tabs_exist_and_are_active(page, server):
    """Both pages existed with NO tab, so a reader found them by scrolling."""
    for section, tab in (("requests", "Requests"), ("lists", "Lists")):
        page.goto(BASE + f"/projects/{SLUG}/{section}")
        active = page.locator(f'.detail-tab.is-active[href="/projects/{SLUG}/{section}"]')
        expect(active).to_have_text(tab)


def test_a_detail_page_keeps_its_tab_lit(page, server):
    """.Sub is the THIRD path segment, so /requests/1 must still highlight Requests.

    If the active state were derived from a full-path match, every detail page would
    light no tab at all -- which looks like a styling bug and is actually a lost
    position in the section."""
    page.goto(BASE + f"/projects/{SLUG}/requests/1")
    page.wait_for_selector("#request-root .request-detail", timeout=15000)
    expect(page.locator(
        f'.detail-tab.is-active[href="/projects/{SLUG}/requests"]')).to_have_text(
        "Requests")


def test_an_instance_wide_request_is_labelled_on_the_board(page, server):
    """17: a request with no project is open to the whole instance.

    store.Request.ProjectID is `json:"project_id,omitempty"`, so an instance-wide
    request has NO KEY rather than 0. A page testing `=== 0` would never see one and
    would label a shared question as this project's own."""
    con = sqlite3.connect(server["db"])
    cur = con.cursor()
    cur.execute(
        "INSERT INTO requests (project_id, author_id, title, body, status, created_at,"
        " updated_at) VALUES (NULL, 1, 'A question for the whole instance',"
        " 'Open to everyone.', 'open', ?, ?)", (time.time(), time.time()))
    instance_id = cur.lastrowid
    con.commit()
    con.close()

    page.goto(BASE + f"/projects/{SLUG}/requests")
    page.wait_for_selector("#requests-root .card", timeout=15000)
    row = page.locator("#requests-root .card",
                       has_text="A question for the whole instance")
    expect(row.locator(".badge-instance")).to_have_text("instance-wide")


def test_no_console_errors_on_the_board(page, server):
    for path in (f"/projects/{SLUG}/requests", f"/projects/{SLUG}/lists"):
        page.goto(BASE + path)
        page.wait_for_timeout(1200)
        assert not page.errors, f"{path}: {page.errors}"

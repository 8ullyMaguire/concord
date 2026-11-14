"""The lifecycle stage as a READER sees it (docs/plans/post-port-roadmap.md, step 2b).

The stage is computed on the server (`concord_ranking::lifecycle`) and rendered by the page's own script from the field
`GET /api/v1/projects` carries. That makes it a DOM claim, not a probe claim: `probe_pages.py` asserts the mount contract because these
pages are client-rendered, so "the card says `dormant`" belongs here.

Four projects that differ only in the signals that decide the stage, so a suite that passes cannot be passing on the data:

    lc-seed      no repository, no features                     -> seed
    lc-sprout    a shipped feature, thirty days old             -> sprout
    lc-mature    shipped, four hundred days old, measured 0.8   -> mature
    lc-dormant   shipped, measured 0.7, no commit for 120 days  -> dormant
    lc-unmeasured  a repository this instance never read        -> unknown, and NOTHING is rendered

The last one is the assertion that matters most and the one a careless implementation loses: `unknown` is a statement about this
instance's measuring, not about somebody's project, so it must render no badge at all. A suite that only checked the four positive
stages would pass against a version that printed "unknown" as a warning label.
"""

import itertools
import tempfile

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

# One fixed port per suite; harness_selftest_test.py asserts they are unique.
PORT = 8521
BASE = f"http://127.0.0.1:{PORT}"

NOW = 1790000000          # 2026-09-20, the instant the other suites seed at
DAY = 86400
_ips = itertools.count(1)


def _project(con, slug, name, created_days_ago):
    return con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " created_at, updated_at, visibility) VALUES (?,?,?,'collective','MIT',?,?,?)",
        (slug, name, "lifecycle fixture", NOW - created_days_ago * DAY, NOW - created_days_ago * DAY, "public"),
    ).lastrowid


def _metrics(con, pid, repo, health, commit_days_ago):
    """One `project_metrics` row: the repository identity, the score, and when it last moved.

    `last_commit_at` is what makes a project dormant, and it is `NULL` — not `0` — when a repository has never been read: the whole
    point of the absence rule. `health_score` is `NULL` for the same reason when nothing has been measured.
    """
    con.execute(
        "INSERT INTO project_metrics (project_id, contributors, last_commit_at, health_score, computed_at)"
        " VALUES (?, 1, ?, ?, ?)",
        (
            pid,
            None if commit_days_ago is None else _ts(NOW - commit_days_ago * DAY),
            health,
            _ts(NOW),
        ),
    )
    con.execute("UPDATE projects SET repo_url = ? WHERE id = ?", (repo, pid))


def _ts(unix):
    """A `timestamptz` from unix seconds, in the dialect the candidate runs on.

    SQLite stores the number in a REAL column and Postgres needs an aware datetime; `harness.connect`'s adapter converts a NUMBER bound
    to a `timestamptz`, which is why the callers pass `_ts(...)`'s result rather than a formatted string.
    """
    import datetime

    return datetime.datetime.fromtimestamp(unix, datetime.UTC)


def _shipped(con, pid, author, title):
    con.execute(
        "INSERT INTO features (project_id, author_id, title, body, effort, status, created_at, updated_at)"
        " VALUES (?,?,?,'a body','M','shipped',?,?)",
        (pid, author, title, NOW, NOW),
    )


def seed(db):
    con = harness.connect(db)
    try:
        author = con.execute(
            "INSERT INTO users (username, display_name, created_at) VALUES ('lc-author','LC Author',?)",
            (NOW,),
        ).lastrowid

        seed_pid = _project(con, "lc-seed", "Lifecycle Seed", 10)
        _shipped_row = None  # nothing shipped, no repository

        sprout_pid = _project(con, "lc-sprout", "Lifecycle Sprout", 30)
        _shipped(con, sprout_pid, author, "First thing that works")

        mature_pid = _project(con, "lc-mature", "Lifecycle Mature", 400)
        _shipped(con, mature_pid, author, "Long shipped thing")
        _metrics(con, mature_pid, "https://example.invalid/mature.git", 0.8, 5)

        dormant_pid = _project(con, "lc-dormant", "Lifecycle Dormant", 400)
        _shipped(con, dormant_pid, author, "Shipped before the silence")
        _metrics(con, dormant_pid, "https://example.invalid/dormant.git", 0.7, 120)

        unmeasured_pid = _project(con, "lc-unmeasured", "Lifecycle Unmeasured", 400)
        # A repository identity with NO metrics row: the stage is `unknown`, and that is a fact about this instance, not the project.
        con.execute("UPDATE projects SET repo_url = ? WHERE id = ?",
                    ("https://example.invalid/unmeasured.git", unmeasured_pid))
        con.commit()
    finally:
        con.close()


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-lc-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-lc-log-")
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
    # A distinct client key per test: the rate limiter keys on X-Forwarded-For and a suite that opens five pages in one context
    # crosses the budget and sees 429s unrelated to its claim.
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": f"10.77.0.{next(_ips)}"})
    page = ctx.new_page()
    yield page
    ctx.close()


def card(page, slug):
    """The catalogue ROW for one project, addressed by its own href.

    Not an index, which another project's row would satisfy — and not `a.card`: `/projects` is the one catalogue page this tree still
    renders on the SERVER (a `<ul class="project-list">`, `project_index` in `crates/web/src/lib.rs`), so the stage badge is written by
    Rust there and by `project.js` on the project page. The first version of this suite looked for `a.card` because `projects.js` has a
    `card()` function that renders one — that script does not run on this page, and seven tests failed against a correct page. Dead
    client code is what a selector written from the source, rather than from the DOM, will believe in.
    """
    return page.locator(f'li:has(a[href="/projects/{slug}"])')


def test_the_catalogue_lists_a_seed(page, server):
    page.goto(f"{BASE}/projects")
    expect(card(page, "lc-seed")).to_be_visible()
    expect(card(page, "lc-seed")).to_contain_text("seed")


def test_a_young_project_that_ships_is_a_sprout(page, server):
    page.goto(f"{BASE}/projects")
    expect(card(page, "lc-sprout")).to_contain_text("sprout")


def test_an_old_measured_project_is_mature(page, server):
    page.goto(f"{BASE}/projects")
    expect(card(page, "lc-mature")).to_contain_text("mature")


def test_a_project_with_no_commit_for_120_days_is_dormant(page, server):
    page.goto(f"{BASE}/projects")
    expect(card(page, "lc-dormant")).to_contain_text("dormant")


def test_only_the_dormant_one_warns(page, server):
    """The warning colour is a claim about the reader's attention, so it gets its own assertion per project.

    `dormant` is the only stage a reader should act on; `unknown` in particular must never carry it, or the warning that matters is
    diluted by a warning about our own measuring.
    """
    page.goto(f"{BASE}/projects")
    expect(card(page, "lc-dormant").locator(".badge.badge-amber")).to_have_count(1)
    for slug in ("lc-seed", "lc-sprout", "lc-mature"):
        expect(card(page, slug).locator(".badge.badge-amber")).to_have_count(0)


def test_an_unmeasured_project_renders_no_stage_at_all(page, server):
    """The negative assertion, and the one worth the fixture.

    `lc-unmeasured` names a repository this instance has never read, so the server's stage is `unknown` — a gap in our measuring. A page
    that printed it would be telling a reader something about somebody's project that we do not know.
    """
    page.goto(f"{BASE}/projects")
    target = card(page, "lc-unmeasured")
    expect(target).to_be_visible()
    expect(target).not_to_contain_text("unknown")
    for word in ("seed", "sprout", "mature", "dormant"):
        expect(target.locator(".badge", has_text=word)).to_have_count(0)


def test_the_project_page_states_the_stage_too(page, server):
    """The catalogue and the project page read the same field, so they must say the same word.

    Checking both is not duplication: they are two renderers (`projects.js` and `project.js`), and a field added to one is exactly the
    kind of change that reaches the other a fortnight later.
    """
    page.goto(f"{BASE}/projects/lc-dormant")
    expect(page.locator("#project-detail")).to_be_visible()
    expect(page.locator("h1.page-title")).to_contain_text("dormant")


def test_the_reason_travels_with_the_badge(page, server):
    """The stage is not a label a reader has to take on faith: `stage_because` is the tooltip."""
    page.goto(f"{BASE}/projects")
    badge = card(page, "lc-dormant").locator(".badge.badge-amber").first
    expect(badge).to_have_attribute("title", "measured: no commit for ninety days or more")

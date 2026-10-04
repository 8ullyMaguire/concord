"""End-to-end tests for the Finder flow, driving real headless Chromium.

Prerequisites:  make build
Run:            pytest tests/e2e/finder_e2e.py -v

Finder is the one flow whose correctness is almost entirely in the browser: the
Go tests cover the engine and the API, but nothing covers the page deciding which
view to show, or the script's own state handling. Three defects found on the
live instance lived exactly there:

  - answering one question navigated the user off the flow onto results, because
    applyState conflated "no questions left" with "no candidates"
  - showResults() replaced the whole session with a payload carrying no
    candidate_count, so the short-list read "70 matches" at 0% fit
  - the seed view was show()n but never render()ed, so the category picker was
    empty on a page that looked loaded

So these tests assert VIEW and COUNT, not just that a request succeeded.

The fixture seeds a deliberately asymmetric capability matrix. The asymmetry is
the point: it makes a wrong answer a detectable error rather than a harmless
coincidence, and it guarantees the engine has a question with real information
gain to ask. A symmetric fixture would pass with a broken engine.
"""
import json
import os
import re
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8422
BASE = f"http://127.0.0.1:{PORT}"


def api(method, path, body=None):
    return harness.api(BASE, method, path, body)


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-finder-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-finder-e2e-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)

    seed(db)

    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


def seed(db):
    """A catalog with a capability matrix that genuinely splits.

    Eight projects across two language values, three governance models and four
    capability states. The design goal is that every dimension Finder can ask
    about has a non-flat distribution, so a broken information-gain
    calculation produces a visibly wrong question rather than an arbitrary one.
    """
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()
    cur.execute("INSERT INTO users (username, display_name, created_at) VALUES ('alice','Alice',?)",
                (now,))
    alice = cur.lastrowid

    # (slug, name, governance, license, language)
    projects = [
        ("alpha-board", "Alpha Board", "maintainer_led", "MIT", "go"),
        ("beta-board", "Beta Board", "maintainer_led", "MIT", "go"),
        ("gamma-board", "Gamma Board", "collective", "Apache-2.0", "go"),
        ("delta-board", "Delta Board", "collective", "Apache-2.0", "rust"),
        ("epsilon-board", "Epsilon Board", "collective", "AGPL-3.0", "rust"),
        ("zeta-board", "Zeta Board", "maintainer_led", "Apache-2.0", "rust"),
        ("eta-board", "Eta Board", "maintainer_led", "MIT", "rust"),
        ("theta-board", "Theta Board", "collective", "AGPL-3.0", "go"),
    ]
    ids = {}
    for slug, name, gov, lic, lang in projects:
        cur.execute(
            "INSERT INTO projects (slug, name, description, governance_model, license,"
            " created_at, updated_at) VALUES (?,?,?,?,?,?,?)",
            (slug, name, f"{name} is a kanban board for small teams.",
             gov, lic, now, now))
        ids[slug] = cur.lastrowid
        cur.execute("INSERT INTO project_languages (project_id, language, pct) VALUES (?,?,100.0)",
                    (cur.lastrowid, lang))
        cur.execute("INSERT INTO tags (project_id, name) VALUES (?, 'kanban')", (cur.lastrowid,))
        cur.execute("INSERT INTO projects_fts (slug, name, description, tags, languages)"
                    " VALUES (?,?,?,?,?)", (slug, name, f"{name} is a kanban board.", "kanban", lang))

    # Capabilities. Four boolean questions, each with a real split and at least
    # one project deliberately left UNKNOWN so §2.7 (unknown is not "no") is
    # exercised end to end rather than only in Go.
    for key, label, category in [
            ("wip-limits", "WIP limits", "features"),
            ("offline", "Offline use", "features"),
            ("self-hosted", "Self-hosted", "deployment"),
            ("arm64", "arm64 support", "platform")]:
        # "values" is a SQLite reserved word -- the same trap the 0021 migration
        # had, where the column has to be quoted.
        cur.execute(
            'INSERT INTO capabilities (key, label, category, kind, "values", created_at)'
            " VALUES (?,?,?,'boolean',?,?)",
            (key, label, category, json.dumps(["yes", "partial", "no", "unknown"]), now))

    # Asymmetric on purpose: no capability splits exactly in half, and two
    # projects carry no assertions at all, so "absent" and "unknown" are both
    # present and distinguishable.
    assertions = {
        "alpha-board": {"wip-limits": "yes", "self-hosted": "yes", "arm64": "yes"},
        "beta-board": {"wip-limits": "yes", "self-hosted": "yes", "arm64": "no"},
        "gamma-board": {"wip-limits": "no", "self-hosted": "no", "offline": "unknown"},
        "delta-board": {"wip-limits": "partial", "self-hosted": "no", "arm64": "yes"},
        "epsilon-board": {"wip-limits": "unknown", "arm64": "unknown"},
        "zeta-board": {"self-hosted": "yes", "offline": "no"},
        # eta-board, theta-board: no assertions at all.
    }
    for slug, caps in assertions.items():
        for key, value in caps.items():
            cur.execute(
                "INSERT INTO capability_assertions (capability, project_id, value,"
                " evidence, asserted_by, asserted_at) VALUES (?,?,?,?,?,?)",
                (key, ids[slug], value, "e2e fixture", alice, now))

    con.commit()
    con.close()


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


_xff_counter = {"n": 0}


@pytest.fixture()
def page(browser):
    _xff_counter["n"] += 1
    ip = f"10.22.{_xff_counter['n'] // 200}.{_xff_counter['n'] % 200}"
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": ip})
    pg = ctx.new_page()
    # Fail loudly on any page error or console error: a JS exception in
    # finder.js otherwise shows up as an empty div and a passing test.
    errors = []
    pg.on("pageerror", lambda e: errors.append(str(e)))
    pg.on("console", lambda m: errors.append(m.text) if m.type == "error" else None)
    pg.errors = errors
    yield pg
    ctx.close()


def start(page, seed_text=""):
    """Get to the first question, failing if the flow does not get there."""
    page.goto(BASE + "/finder")
    if seed_text:
        page.fill("#finder-seed-text", seed_text)
    page.click("#finder-seed-form button[type=submit]")
    expect(page.locator("#finder-flow")).to_be_visible(timeout=10000)
    # Wait for the OPTIONS, not just the container. #finder-flow is unhidden
    # before the options are written, so waiting on the container resolves a
    # moment too early and every later evaluate finds an empty list.
    #
    # A narrow seed legitimately arrives with no question and the stop prompt
    # instead (§4.4 stops at five or fewer candidates), so accept either.
    page.wait_for_function(
        "() => document.querySelector('#finder-options [data-option]') !== null"
        " || !document.getElementById('finder-stop-prompt').hidden",
        timeout=10000)


def at_stop_prompt(page):
    return not page.locator("#finder-stop-prompt").is_hidden()


def require_question(page):
    """Fail with a useful message if we are looking at the stop prompt instead."""
    if at_stop_prompt(page):
        pytest.fail("the stop prompt is showing; this test needs a question to answer")


def first_option(page):
    """The id of the first real option -- never the non-filtering 'any' mode."""
    return page.evaluate(
        "() => { const o = document.querySelectorAll('#finder-options [data-option]');"
        " for (const el of o) { const v = el.getAttribute('data-option');"
        " if (v && v !== 'any') return v; } return null; }")


def answer_first(page):
    """Answer with a real value and wait for the state to settle."""
    require_question(page)
    opt = first_option(page)
    assert opt, "no concrete option was offered"
    before = page.evaluate("() => window.__state && window.__state.n")
    page.click(f"#finder-options [data-option='{opt}']")
    page.wait_for_timeout(900)
    return opt


# ------------------------------------------------------------- the seed view

def test_finder_page_renders(server, page):
    page.goto(BASE + "/finder")
    expect(page).to_have_title(re.compile("Finder"))
    expect(page.locator("#finder-seed")).to_be_visible()


def test_seed_view_renders_its_categories(server, page):
    """The regression that a container-exists test cannot see.

    finder.js called show('seed') but not render(), so the page loaded fine and
    offered nothing to click. This asserts the RENDERED children.
    """
    page.goto(BASE + "/finder")
    expect(page.locator("#finder-categories .chip")).to_have_count(8)


def test_seed_input_is_not_required(server, page):
    """§4.1: leaving the seed blank is a supported way to start."""
    page.goto(BASE + "/finder")
    expect(page.locator("#finder-seed-text")).not_to_have_attribute("required", "")
    start(page)
    expect(page.locator("#finder-question-text")).to_be_visible()


def test_a_category_chip_can_be_selected(server, page):
    page.goto(BASE + "/finder")
    page.locator("#finder-categories .chip").first.click()
    expect(page.locator("#finder-categories .chip.chip-on")).to_have_count(1)


# ------------------------------------------------------- the question flow

def test_a_question_is_offered_with_options(server, page):
    start(page)
    expect(page.locator("#finder-question-text")).not_to_be_empty()
    expect(page.locator("#finder-options [data-option]")).not_to_have_count(0)
    # "Doesn't matter" is always offered and is not a real value.
    expect(page.locator("#finder-options [data-option='any']")).to_have_count(1)


def test_the_offered_options_are_the_real_distribution(server, page):
    """§5.4: never offer an option the catalog cannot deliver.

    Cross-checks the page against the API rather than a hardcoded list, so the
    two surfaces failing to agree is itself a failure.
    """
    cat = api("GET", "/api/v1/finder/questions")
    start(page)
    offered = page.locator("#finder-options [data-option]").all_inner_texts()
    assert len(offered) >= 2
    # Every offered label is a real value somewhere in the catalog.
    all_values = set()
    for d in cat["dimensions"]:
        all_values.update(d["values_present"])
    flat = " ".join(offered).lower()
    for d in cat["dimensions"]:
        if d["key"] == "language":
            for v in d["values_present"]:
                if v.lower() in flat or v in flat:
                    all_values.add(v)
    assert any(v.lower() in flat for v in all_values), \
        f"no catalog value appears among the offered options: {offered}"


def test_why_this_question_explains_itself(server, page):
    """§4.2: the page must be able to say why it asked."""
    start(page)
    expect(page.locator("#finder-why")).to_be_visible()
    expect(page.locator("#finder-why-text")).not_to_be_empty()


def test_the_shortlist_updates_live_and_the_count_drops(server, page):
    """§2.3: results are visible from the first question and refine live."""
    start(page)
    first = page.locator("#finder-shortlist-summary").inner_text()
    assert re.search(r"\d+ match", first), f"no count in {first!r}"

    answer_first(page)
    after = page.locator("#finder-shortlist-summary").inner_text()
    assert after != first, f"the count did not change: {first!r} -> {after!r}"


def test_answering_keeps_the_user_on_the_flow(server, page):
    """The regression from the live run.

    applyState treated "no questions left" as "no candidates" and jumped to
    results. With this fixture, one answer exhausts the useful questions, so
    every test that answers a question would have navigated away — and the flow
    would look broken the moment it worked.
    """
    start(page)
    answer_first(page)
    expect(page.locator("#finder-flow")).to_be_visible()
    expect(page.locator("#finder-results")).to_be_hidden()


def test_the_answer_is_recorded_in_the_tray(server, page):
    start(page)
    answer_first(page)
    expect(page.locator("#finder-answers li")).to_have_count(1)
    expect(page.locator("#finder-answers li").first).to_contain_text("required")


def test_the_progress_bar_advances(server, page):
    start(page)
    before = page.locator("#finder-progress-bar").get_attribute("style") or ""
    answer_first(page)
    after = page.locator("#finder-progress-bar").get_attribute("style") or ""
    assert before != after, "the progress bar did not advance after an answer"
    expect(page.locator("#finder-progress-text")).to_contain_text("Question")


def test_skip_does_not_filter(server, page):
    """§2.4: skipping never removes candidates.

    It IS still recorded in the answers tray, and that is correct rather than a
    leak: §4.3 says "doesn't matter" is recorded so Finder does not ask again,
    and knowing what you declined is the point of the tray. The first version of
    this test asserted an empty tray, which contradicted that and was simply
    wrong about the intended behaviour.
    """
    start(page)
    before = page.locator("#finder-shortlist-summary").inner_text()
    page.click("#finder-flow [data-mode='skip']")
    page.wait_for_timeout(900)
    after = page.locator("#finder-shortlist-summary").inner_text()
    assert before.split(" matches")[0] == after.split(" matches")[0], \
        f"skip changed the candidate count: {before!r} -> {after!r}"
    # Recorded, and visibly marked as skipped rather than answered.
    expect(page.locator("#finder-answers li")).to_have_count(1)
    expect(page.locator("#finder-answers li").first).to_contain_text("skip")


def test_doesnt_matter_does_not_filter(server, page):
    start(page)
    before = page.locator("#finder-shortlist-summary").inner_text()
    page.click("#finder-options [data-option='any']")
    page.wait_for_timeout(900)
    after = page.locator("#finder-shortlist-summary").inner_text()
    assert before.split(" matches")[0] == after.split(" matches")[0], \
        f"doesn't matter changed the count: {before!r} -> {after!r}"
    expect(page.locator("#finder-answers li").first).to_contain_text("doesnt-matter")


def test_back_restores_the_candidate_set(server, page):
    """§2.6: reversible. Going back must restore, not just decrement a counter."""
    start(page)
    before = page.locator("#finder-shortlist-summary").inner_text()
    answer_first(page)
    narrowed = page.locator("#finder-shortlist-summary").inner_text()
    assert narrowed != before

    page.click("#finder-back")
    page.wait_for_timeout(800)
    restored = page.locator("#finder-shortlist-summary").inner_text()
    assert restored.split(" matches")[0] == before.split(" matches")[0], \
        f"back did not restore the count: {narrowed!r} -> {restored!r}"
    # The answered question is offered again, which is what makes changing an
    # answer possible at all.
    expect(page.locator("#finder-answers li")).to_have_count(0)


def test_back_is_disabled_at_the_start(server, page):
    start(page)
    expect(page.locator("#finder-back")).to_be_disabled()


def test_the_stop_prompt_appears_when_questions_run_out(server, page):
    """§4.4: auto-prompt when there is nothing useful left to ask."""
    start(page)
    answer_first(page)
    expect(page.locator("#finder-stop-prompt")).to_be_visible()
    expect(page.locator("#finder-stop-reason")).not_to_be_empty()
    expect(page.locator("#finder-stop-top li")).not_to_have_count(0)


def test_keep_narrowing_returns_to_the_question_when_there_is_one(server, page):
    """The button is shown only when there is a question to continue into.

    The stop prompt appears for SATURATED (too few candidates) and LOW_GAIN
    (nothing left worth asking). Only the second has somewhere to go. Shown with
    no question behind it, the button re-rendered the same dead end and the user
    could click it forever -- which is exactly what this test first did.
    """
    start(page)
    answer_first(page)
    expect(page.locator("#finder-stop-prompt")).to_be_visible()
    if at_stop_prompt(page) and first_option(page) is None:
        expect(page.locator("#finder-stop-keep")).to_be_hidden()
    else:
        page.click("#finder-stop-keep")
        expect(page.locator("#finder-stop-prompt")).to_be_hidden()


def test_keep_narrowing_is_hidden_with_no_question(server, page):
    """§4.4's "keep narrowing anyway" is meaningless with nothing to narrow."""
    page.goto(BASE + "/finder?seed=go")
    expect(page.locator("#finder-flow")).to_be_visible(timeout=10000)
    expect(page.locator("#finder-stop-prompt")).to_be_visible(timeout=10000)
    expect(page.locator("#finder-stop-keep")).to_be_hidden()
    # "See full results" is still offered, so the user is never stranded.
    expect(page.locator("#finder-stop-go")).to_be_visible()


# ------------------------------------------------------------ the results

def test_stop_and_see_results_shows_the_ranked_list(server, page):
    start(page)
    answer_first(page)
    page.click("#finder-stop")
    page.wait_for_timeout(800)
    expect(page.locator("#finder-results")).to_be_visible()
    expect(page.locator("#finder-ranked li")).not_to_have_count(0)


def test_results_show_the_answers_that_produced_them(server, page):
    start(page)
    answer_first(page)
    page.click("#finder-stop")
    page.wait_for_timeout(800)
    expect(page.locator("#finder-results-answers li")).to_have_count(1)


def test_the_ranked_list_agrees_with_the_candidate_count(server, page):
    """The regression from the live run, in its other half.

    showResults() replaced the session with a payload carrying no
    candidate_count, so the short-list read "N matches" while the ranked rows
    showed a different number. Cross-check the two numbers.
    """
    start(page)
    answer_first(page)
    page.click("#finder-stop")
    page.wait_for_timeout(800)

    header = page.locator("#finder-ranked-heading").inner_text()
    m = re.search(r"\((\d+)\)", header)
    assert m, f"no count in the ranked heading: {header!r}"
    rows = page.locator("#finder-ranked li").count()
    assert int(m.group(1)) == rows, \
        f"the heading says {m.group(1)} but {rows} rows are rendered"


def test_every_ranked_entry_reports_a_fit_and_a_link(server, page):
    start(page)
    answer_first(page)
    page.click("#finder-stop")
    page.wait_for_timeout(800)
    first = page.locator("#finder-ranked li").first
    expect(first.locator("a")).to_have_attribute("href", re.compile(r"^/projects/"))
    expect(first.locator(".finder-fit")).to_contain_text("%")


def test_a_fit_score_is_not_zero_for_a_match(server, page):
    """The regression from the live run: 0% fit for a well-ranked candidate."""
    start(page)
    answer_first(page)
    page.click("#finder-stop")
    page.wait_for_timeout(800)
    text = page.locator("#finder-ranked li").first.locator(".finder-fit").inner_text()
    pct = int(re.search(r"(\d+)%", text).group(1))
    assert pct > 0, f"the top-ranked candidate shows {pct}% fit"


def test_the_export_link_points_at_the_session(server, page):
    start(page)
    answer_first(page)
    page.click("#finder-stop")
    page.wait_for_timeout(800)
    href = page.locator("#finder-export-json").get_attribute("href")
    assert re.match(r"^/api/v1/finder/sessions/[^/]+/results$", href), href


def test_a_ranked_entry_links_to_its_project_page(server, page):
    start(page)
    answer_first(page)
    page.click("#finder-stop")
    page.wait_for_timeout(800)
    page.locator("#finder-ranked li a").first.click()
    expect(page).to_have_url(re.compile(r"/projects/"))


# ------------------------------------------------------------- keyboard

def test_number_keys_select_options(server, page):
    start(page)
    page.keyboard.press("1")
    page.wait_for_timeout(800)
    expect(page.locator("#finder-answers li")).to_have_count(1)


def test_escape_goes_back(server, page):
    start(page)
    summary = page.locator("#finder-shortlist-summary")
    before = summary.inner_text()
    count_before = before.split(" matches")[0]

    # Wait for the SHOWN STATE, not for the clock. The old version slept 800ms
    # and read, which is a race that only loses under load: KNOWN-ISSUES.md
    # recorded this test failing ~1 run in 3, and it did not reproduce in nine
    # consecutive runs on an idle machine -- a flake that vanishes when you
    # measure it is still a race, it just needs the suite busy to show itself.
    #
    # expect(...).not_to_have_text() is the fix: it retries until the summary
    # differs from what a "1" press would leave, so the test cannot pass by
    # reading the pre-press text and calling it "restored".
    page.keyboard.press("1")
    expect(summary).not_to_have_text(re.compile(r"^\s*" + re.escape(count_before) + r" matches"))

    page.keyboard.press("Escape")
    # Back to the original count. to_have_text on the exact prefix, anchored, so
    # "12 matches" cannot satisfy a wait for "2 matches".
    expect(summary).to_have_text(re.compile(r"^\s*" + re.escape(count_before) + r" matches"))


def test_s_skips(server, page):
    start(page)
    before = page.locator("#finder-shortlist-summary").inner_text()
    page.keyboard.press("s")
    page.wait_for_timeout(800)
    after = page.locator("#finder-shortlist-summary").inner_text()
    assert before.split(" matches")[0] == after.split(" matches")[0], \
        "S did not skip without filtering"


def test_r_reveals_the_why(server, page):
    start(page)
    expect(page.locator("#finder-why")).not_to_have_attribute("open", "")
    page.keyboard.press("r")
    expect(page.locator("#finder-why")).to_have_attribute("open", "")


def test_v_jumps_to_results(server, page):
    start(page)
    page.keyboard.press("v")
    page.wait_for_timeout(800)
    expect(page.locator("#finder-results")).to_be_visible()


# --------------------------------------------------------------- deep link

def test_the_seed_query_parameter_prefills_and_starts(server, page):
    """§3: /finder?seed=note-taking pre-fills the starting category."""
    page.goto(BASE + "/finder?seed=go")
    expect(page.locator("#finder-seed-text")).to_have_value("go")
    expect(page.locator("#finder-flow")).to_be_visible(timeout=10000)


def test_a_deep_linked_seed_starts_without_a_click(server, page):
    """A deep link must land somewhere useful.

    A seed that narrows to five or fewer candidates has no question to ask --
    that is §4.4's SATURATED stop, not a failure -- so the assertion is that the
    user sees EITHER a question OR the stop prompt with a reason. Before this
    page was fixed it landed on an empty results view with neither, which is
    what made /finder?seed=go look broken.
    """
    page.goto(BASE + "/finder?seed=rust")
    expect(page.locator("#finder-flow")).to_be_visible(timeout=10000)
    page.wait_for_function(
        "() => document.querySelector('#finder-options [data-option]') !== null"
        " || !document.getElementById('finder-stop-prompt').hidden",
        timeout=10000)
    if at_stop_prompt(page):
        expect(page.locator("#finder-stop-reason")).not_to_be_empty()
    else:
        expect(page.locator("#finder-question-text")).not_to_be_empty()


def test_a_narrow_deep_linked_seed_explains_itself(server, page):
    """§4.4: a seed that already narrows far enough gets the stop prompt, with
    the reason and the short-list -- not an unexplained empty page."""
    page.goto(BASE + "/finder?seed=go")
    expect(page.locator("#finder-flow")).to_be_visible(timeout=10000)
    expect(page.locator("#finder-stop-prompt")).to_be_visible(timeout=10000)
    expect(page.locator("#finder-stop-reason")).to_contain_text("handful")
    # And the results are reachable from there, with a count.
    page.click("#finder-stop-go")
    page.wait_for_timeout(800)
    expect(page.locator("#finder-ranked-heading")).to_contain_text("(")


# ------------------------------------------------------- no JS errors

def test_the_flow_raises_no_javascript_errors(server, page):
    """A JS exception in finder.js otherwise shows as an empty div.

    Playwright's error listeners are attached in the page fixture, so any
    exception anywhere in the flow fails this one test rather than silently
    emptying a region in another.
    """
    start(page)
    page.keyboard.press("1")
    page.wait_for_timeout(600)
    page.click("#finder-stop")
    page.wait_for_timeout(600)
    page.keyboard.press("Escape")
    page.wait_for_timeout(600)
    assert not page.errors, f"javascript errors during the flow: {page.errors}"


def test_an_unknown_session_is_not_found_rather_than_an_error_page(server, page):
    """A session that never existed must 404, not render an empty page."""
    resp = page.goto(BASE + "/api/v1/finder/sessions/f1-nope/results")
    assert resp.status == 404


def test_no_fit_percentage_before_any_question_is_answered(server, page):
    """A percentage needs an answer to be a percentage OF.

    Before the first answer the page printed "0% fit" beside every project,
    which reads as "these are bad matches" rather than "you have not told me
    anything yet" -- the same misreading as the "15% fit for a perfect match"
    bug, in the opposite direction.
    """
    page.goto(BASE + "/finder?seed=go")
    expect(page.locator("#finder-flow")).to_be_visible(timeout=10000)
    expect(page.locator("#finder-stop-prompt")).to_be_visible(timeout=10000)
    page.click("#finder-stop-go")
    page.wait_for_timeout(800)
    fits = page.locator("#finder-ranked .finder-fit").all_inner_texts()
    for t in fits:
        assert "%" not in t, f"a fit percentage is shown before any answer: {t!r}"


def test_a_fit_percentage_appears_once_answered(server, page):
    start(page)
    answer_first(page)
    page.click("#finder-stop-go")
    page.wait_for_timeout(800)
    first = page.locator("#finder-ranked .finder-fit").first.inner_text()
    assert "%" in first, f"no fit percentage after answering: {first!r}"


def test_the_hidden_attribute_actually_hides(server, page):
    """`hidden` must beat a component's own display rule.

    `.btn { display: inline-flex }` outranks the user agent's
    `[hidden] { display: none }`, so any styled element carrying the `hidden`
    attribute stayed on screen. It is site-wide, not Finder-specific, and it
    presented as "the attribute is set but Playwright says visible" -- which is
    exactly the kind of contradiction that gets worked around instead of fixed.
    """
    page.goto(BASE + "/finder")
    # Set the attribute directly on a real button, so the check is about the CSS
    # and not about some element that happens to be hidden for another reason.
    result = page.evaluate("""() => {
        const b = document.querySelector('#finder-seed-form button[type=submit]');
        b.setAttribute('hidden', '');
        const shown = getComputedStyle(b).display !== 'none';
        b.removeAttribute('hidden');
        return { shown, display: getComputedStyle(b).display };
    }""")
    assert not result["shown"], (
        f"an element with the hidden attribute still renders (display: "
        f"{result['display']}); [hidden] must win over .btn's display: inline-flex")

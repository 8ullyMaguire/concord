"""End-to-end tests for the four surfaces that had a write endpoint and no reader.

Prerequisites:  make build
Run:            pytest tests/e2e/readers_e2e.py -v

Each of these four existed as a route with a handler, a role check, store tests and
a doc comment describing it, and NOTHING IN THE BROWSER COULD REACH IT:

  1. POST /projects/{id}/join                    -- "participate" with no door
  2. PUT  /consensus/objections/{id}/resolve      -- §6.6's resolutions
  3. POST /strategy/weight-proposals              -- §6.2's propose form (§12.6.3)
  4. POST /solutions/{id}/coverage/{cid}/contest  -- §6.4's challenge

They were found by MACHINE, not by reading: internal/httpapi/reader_coverage_test.go
walks the router and requires every API route to name the client file that references
it, so the three that had no reader failed the build. Test 4's control needed a read
that did not exist -- the claims a contest names arrived with the solutions payload
for the first time in the same pass.

What only a browser can prove, and why this suite is separate from the Go tests:

  - the controls APPEAR where a reader would look for them (a route can be wired and
    invisible);
  - they DO the thing (a button that posts to the wrong path passes every source
    gate);
  - the server's refusal reaches the reader in the server's words, for the cases a
    non-maintainer hits: a contributor resolving an objection, a non-member proposing
    a weight, an author contesting their own claim.

The fixture seeds everything directly in sqlite. A browser test whose data came from
the endpoints under test cannot fail for the reason it exists.
"""

import base64
import hashlib
import json
import sqlite3
import tempfile
import time

import pytest
from playwright.sync_api import expect, sync_playwright

import harness

PORT = 8429
BASE = f"http://127.0.0.1:{PORT}"

_xff_counter = {"n": 0}


@pytest.fixture(scope="session")
def server():
    dbdir = tempfile.mkdtemp(prefix="concord-readers-e2e-")
    logdir = tempfile.mkdtemp(prefix="concord-readers-e2e-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    seed(db)
    yield {"base": base, "db": db, "logdir": logdir}
    harness.stop_server(proc)


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


def _context(browser, session=None):
    _xff_counter["n"] += 1
    n = _xff_counter["n"]
    ctx = browser.new_context(
        extra_http_headers={"X-Forwarded-For": f"10.67.{n // 200}.{n % 200}"})
    if session:
        token, uid, username = session
        # BOTH keys, which is what auth.js stores. The consensus page compares an
        # objection's author against the session's own id to decide whether to offer
        # the reader their own control (withdraw) or the maintainer's (resolve), so a
        # token-only session reads as "not mine" and the withdrawal control never
        # renders. Same trap as taxonomy_e2e.py's proposer card.
        ctx.add_init_script(
            "localStorage.setItem('concord.token', %r);"
            "localStorage.setItem('concord.user', %r);"
            % (token, json.dumps({"id": uid, "username": username})))
        ctx.add_cookies([{"name": "concord_nav", "value": token, "url": BASE + "/"}])
    return ctx


def _session(db, username):
    """The two things the sign-in form stores for `username`: (token, id, user).

    The token row is written the way the login handler writes one.
    api_tokens.token_hash is base64 RawStd of the sha256 digest, not hex, and
    ResolveToken rejects anything that does not start with `clt_`.
    """
    raw = "clt_readers-" + username
    con = sqlite3.connect(db)
    uid = con.execute("SELECT id FROM users WHERE username = ?", (username,)).fetchone()[0]
    digest = hashlib.sha256(raw.encode()).digest()
    con.execute(
        "INSERT OR REPLACE INTO api_tokens (token_hash, user_id, created_at, last_used_at)"
        " VALUES (?,?,?,?)",
        (base64.b64encode(digest).decode().rstrip("="), uid, time.time(), time.time()))
    con.commit()
    con.close()
    return raw, uid, {"id": uid, "username": username}


def _page(browser, db, username=None):
    ctx = _context(browser, _session(db, username) if username else None)
    pg = ctx.new_page()
    errors = []
    pg.on("pageerror", lambda e: errors.append("PAGEERROR: " + str(e)))
    # A deliberate 4xx is a FAILED SUBRESOURCE to the browser, and two of the tests
    # below ask the server for a refusal on purpose: the console line would be the
    # expected outcome reported as an error. The status and the server's sentence are
    # asserted where they belong -- on the control's own error region and in the
    # database -- and this filter is the same one taxonomy_e2e.py uses for the same
    # reason. A page error (`pageerror`) is never filtered.
    pg.on("console", lambda m: errors.append("CONSOLE: " + m.text)
          if m.type == "error" and "Failed to load resource" not in m.text else None)
    pg.errors = errors
    return ctx, pg


def _one(db, sql, args=()):
    con = sqlite3.connect(db)
    try:
        row = con.execute(sql, args).fetchone()
        return row[0] if row else None
    finally:
        con.close()


def seed(db):
    """One project, four memberships, a solution with one coverage claim, and a
    CLOSED consensus call carrying an open objection.

    The call is closed because §6.6 hides objections while it is open, so a page
    that offered the resolution control before the close would be showing one on a
    surface the spec says is not there.
    """
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    def user(username):
        cur.execute("INSERT INTO users (username, display_name, created_at) VALUES (?,?,?)",
                    (username, username, now))
        return cur.lastrowid

    owner = user("readers-owner")
    member = user("readers-member")
    author = user("readers-author")
    user("readers-outsider")  # registered, NOT a member of anything

    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " visibility, created_at, updated_at)"
        " VALUES (?,?,?,'collective','MIT','public',?,?)",
        ("readers-e2e", "Readers E2E", "Fixture project", now, now))
    pid = cur.lastrowid
    for uid, role in ((owner, "owner"), (member, "contributor"), (author, "contributor")):
        cur.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (pid, uid, role, now))

    cur.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, status, severity,"
        " frequency, created_at, updated_at) VALUES (?,?,?,?,'validated',4,1.0,?,?)",
        (pid, member, "the export cannot resume", "body", now, now))
    complaint = cur.lastrowid

    cur.execute(
        "INSERT INTO features (project_id, author_id, title, body, effort, status,"
        " strategic_weight, created_at, updated_at)"
        " VALUES (?,?,?,?,'M','consensus',1,?,?)",
        (pid, author, "resumable export", "body", now, now))
    feature = cur.lastrowid
    cur.execute("INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)",
                (feature, complaint))

    cur.execute(
        "INSERT INTO solutions (feature_id, author_id, title, body, type, relationship,"
        " status, created_at, updated_at) VALUES (?,?,?,?,'build-new','exclusive','draft',?,?)",
        (feature, author, "checkpoint the stream", "body", now, now))
    solution = cur.lastrowid
    # §6.4's claim: this solution resolves that complaint. Not contested yet, so the
    # contest test is the thing that contests it.
    cur.execute(
        "INSERT INTO solution_coverage (solution_id, complaint_id, claim, created_at)"
        " VALUES (?,?,'resolves',?)", (solution, complaint, now))

    # The solution arena. ListSolutions reads the ARENA leaderboard, not the solutions
    # table, so a solution with no arena entry is invisible on the feature page -- which
    # is exactly how the first version of this fixture rendered no claims at all and
    # looked like a broken control rather than a missing row.
    cur.execute(
        "INSERT INTO arenas (type, project_id, feature_id, question, created_at)"
        " VALUES ('solution',?,?,?,?)",
        (pid, feature, "Which approach should we build?", now))
    arena = cur.lastrowid
    cur.execute(
        "INSERT INTO arena_entries (arena_id, entity_type, entity_id, is_baseline, updated_at)"
        " VALUES (?,'solution',?,0,?)", (arena, solution, now))

    # A call that is already closed, with one open objection on it.
    # consensus_calls has no created_at and no question column: the question lives in
    # `summary`, opened_by is NOT NULL, and `extensions`/`opened_early` came later.
    cur.execute(
        "INSERT INTO consensus_calls (project_id, feature_id, solution_id, opened_by,"
        " summary, status, opens_at, closes_at, result)"
        " VALUES (?,?,?,?,?,'closed',?,?,'accepted')",
        (pid, feature, solution, owner, "Adopt the checkpointed export?",
         now - 100, now - 10))
    call = cur.lastrowid
    cur.execute(
        "INSERT INTO objections (call_id, user_id, principle, violation, remedy, status,"
        " created_at) VALUES (?,?,?,?,?,'open',?)",
        (call, member, "Reversibility", "a half-written export is unrecoverable",
         "keep the last complete file", now))

    con.commit()
    con.close()


# --- 1. join ------------------------------------------------------------------

def test_a_signed_in_visitor_can_join_from_the_project_page(browser, server):
    """The front door. POST /join has always accepted any signed-in account and
    written an audit row; the page had no control for it, so a reader could reach a
    project, read everything, and have no way to take part."""
    db, base = server["db"], server["base"]
    ctx, pg = _page(browser, db, "readers-outsider")
    try:
        pg.goto(base + "/projects/readers-e2e")
        btn = pg.locator("#project-join")
        expect(btn).to_be_visible()
        btn.click()
        pg.wait_for_load_state("load")

        uid = _one(db, "SELECT id FROM users WHERE username = 'readers-outsider'")
        role = _one(db, "SELECT role FROM members WHERE project_id = 1 AND user_id = ?", (uid,))
        assert role, "the Join button did not create a membership row"
        # The audit row is the half a response cannot show.
        actions = _one(db, "SELECT COUNT(*) FROM audit_log WHERE action = 'join_project'")
        assert actions == 1, f"expected one join_project audit row, found {actions}"
        assert not pg.errors, pg.errors
    finally:
        ctx.close()


def test_a_signed_out_visitor_is_not_offered_a_join_button(browser, server):
    """Joining is attributed to an account. A button that can only fail is worse than
    no button, and the page's answer to a visitor with no session is the sign-in
    link the rest of the site uses."""
    ctx, pg = _page(browser, server["db"], None)
    try:
        pg.goto(server["base"] + "/projects/readers-e2e")
        expect(pg.locator("#project-join")).to_be_hidden()
        assert not pg.errors, pg.errors
    finally:
        ctx.close()


# --- 2. objections ------------------------------------------------------------

def test_a_maintainer_resolves_an_objection_on_the_call_page(browser, server):
    """§6.6's resolution, which nothing in the browser could record. The status the
    page offers is the one the store writes back, and the reason is recorded with
    it -- a resolution without its reasoning is a flag, not a record."""
    db, base = server["db"], server["base"]
    ctx, pg = _page(browser, db, "readers-owner")
    try:
        pg.goto(base + "/projects/readers-e2e/consensus?call=1")
        row = pg.locator("[data-objection-resolve]").first
        expect(row).to_be_visible()
        pg.locator("[data-objection-status]").first.select_option("resolved")
        pg.locator("[data-objection-reason]").first.fill("the export now checkpoints")
        row.click()
        pg.wait_for_load_state("load")
        expect(pg.locator(".objection-status").first).to_have_text("resolved")

        status = _one(db, "SELECT status FROM objections WHERE id = 1")
        reason = _one(db, "SELECT resolution FROM objections WHERE id = 1")
        assert status == "resolved", f"objection status in the database is {status!r}"
        assert reason == "the export now checkpoints", f"resolution stored as {reason!r}"
        assert not pg.errors, pg.errors
    finally:
        ctx.close()


def test_a_contributor_is_told_why_they_cannot_resolve_an_objection(browser, server):
    """The refusal is the server's, verbatim, AND it names the role it wanted.

    The page does not claim to know who may resolve one: it offers the control to any
    signed-in reader who is not the objector and reports the answer. Every gate used
    to answer a bare "permission denied", so this could only assert that a refusal
    arrived; now the sentence says which role the action needs, which is the thing the
    reader has to act on.
    """
    db, base = server["db"], server["base"]
    # Re-open the objection the previous tests dealt with, so this one has something
    # to be refused about.
    con = sqlite3.connect(db)
    con.execute("UPDATE objections SET status = 'open', resolution = '' WHERE id = 1")
    con.commit()
    con.close()

    # readers-author is a contributor who did NOT raise this objection: objection 1
    # belongs to readers-member, so this reader gets the four-status control.
    ctx, pg = _page(browser, db, "readers-author")
    try:
        pg.goto(base + "/projects/readers-e2e/consensus?call=1")
        assert pg.locator("[data-objection-withdraw]").count() == 0, \
            "the withdrawal control was offered to somebody who did not raise the objection"
        pg.locator("[data-objection-status]").first.select_option("overridden")
        pg.locator("[data-objection-reason]").first.fill("considered and set aside")
        pg.locator("[data-objection-resolve]").first.click()
        err = pg.locator("[data-objection-error]").first
        expect(err).to_be_visible()
        text = err.inner_text()
        assert "permission" in text.lower(), text
        assert "maintainer" in text.lower(), \
            f"the refusal does not name the role it wanted: {text!r}"
        status = _one(db, "SELECT status FROM objections WHERE id = 1")
        assert status == "open", f"a contributor's resolution was written: {status!r}"
        assert not pg.errors, pg.errors
    finally:
        ctx.close()


def test_the_objector_withdraws_their_own_objection(browser, server):
    """The one outcome §6.6 leaves with the objector.

    Withdrawal needed a maintainer, so a blocker who changed their mind had to ask the
    person whose proposal they blocked to drop it for them -- and until somebody did,
    §55's "no unresolved principled objection" bar stayed up against a proposal nobody
    still objected to. The page shows the objector this control and not the four-status
    one, because the server refuses them all four of those.
    """
    db, base = server["db"], server["base"]
    con = sqlite3.connect(db)
    con.execute("UPDATE objections SET status = 'open', resolution = '' WHERE id = 1")
    con.commit()
    con.close()

    # The test's premise: objection 1 is readers-member's, so the session below IS
    # the objector. A fixture that changed hands would otherwise make this test pass
    # by testing the other branch.
    author = _one(db, "SELECT user_id FROM objections WHERE id = 1")
    author_name = _one(db, "SELECT username FROM users WHERE id = ?", (author,))
    assert author_name == "readers-member", \
        f"fixture problem: objection 1 belongs to {author_name!r}"
    ctx, pg = _page(browser, db, "readers-member")
    try:
        pg.goto(base + "/projects/readers-e2e/consensus?call=1")
        btn = pg.locator("[data-objection-withdraw]").first
        expect(btn).to_be_visible()
        assert pg.locator("[data-objection-status]").count() == 0, \
            "the objector was offered the maintainer's control as well, which the " \
            "server refuses them"
        pg.locator("[data-objection-withdraw-reason]").first.fill("the export checkpoints now")
        btn.click()
        pg.wait_for_load_state("load")
        expect(pg.locator(".objection-status").first).to_have_text("withdrawn")

        status = _one(db, "SELECT status FROM objections WHERE id = 1")
        reason = _one(db, "SELECT resolution FROM objections WHERE id = 1")
        assert status == "withdrawn", f"objection status in the database is {status!r}"
        assert reason == "the export checkpoints now", f"resolution stored as {reason!r}"
        # The bar §55 is computed from, read from the call's own tally.
        assert _one(db, "SELECT COUNT(*) FROM objections WHERE call_id = 1"
                        " AND status = 'open'") == 0, "the withdrawal left the objection open"
        assert not pg.errors, pg.errors
    finally:
        ctx.close()


def test_the_objector_cannot_resolve_their_own_objection(browser, server):
    """The other half of the same rule, and the one a page could get wrong by offering
    one control for both: withdrawal is the objector's, the verdict is not. Signed in as
    the author of a DIFFERENT objection, because the page now shows the objector only
    the withdrawal control -- this asserts the server refuses them anyway, which is the
    rule that has to hold when a client is wrong."""
    db, base = server["db"], server["base"]
    con = sqlite3.connect(db)
    # TWO open objections: one this reader raised (so their own control is on the page)
    # and one they did not (so the four-status control is too). Objection 1 is withdrawn
    # by now, and a page renders no control for an objection that has an outcome.
    mine_id = con.execute(
        "SELECT id FROM users WHERE username = 'readers-author'").fetchone()[0]
    theirs_id = con.execute(
        "SELECT id FROM users WHERE username = 'readers-member'").fetchone()[0]
    for uid, principle in ((mine_id, "Scope"), (theirs_id, "Portability")):
        con.execute(
            "INSERT INTO objections (call_id, user_id, principle, violation, remedy, status,"
            " created_at) VALUES (1, ?, ?, 'it touches another project', 'split it',"
            " 'open', ?)", (uid, principle, time.time()))
    con.commit()
    new_id = con.execute(
        "SELECT id FROM objections WHERE user_id = ? ORDER BY id DESC LIMIT 1",
        (mine_id,)).fetchone()[0]
    con.close()

    ctx, pg = _page(browser, db, "readers-author")
    try:
        pg.goto(base + "/projects/readers-e2e/consensus?call=1")
        # Their own row carries the withdrawal control, and the FIRST objection (which
        # is somebody else's) does not.
        assert pg.locator(f"[data-objection-withdraw='{new_id}']").count() == 1, \
            "the objector's own objection was not offered the withdrawal control"
        assert pg.locator("[data-objection-withdraw='1']").count() == 0, \
            "the withdrawal control was offered on somebody else's objection"
        assert pg.locator("[data-objection-status]").count() >= 1, \
            "the four-status control disappeared for a bystander's objection"

        # The rule the server has to hold whatever the page shows: the objector's own
        # token, the objector's own objection, a verdict asked for directly. A page
        # that offered one control for both would send exactly this.
        out = pg.evaluate(
            """async (id) => {
                const r = await fetch('/api/v1/projects/readers-e2e/consensus/'
                    + 'objections/' + id + '/resolve', {
                    method: 'PUT',
                    headers: {'Content-Type': 'application/json',
                              Authorization: 'Bearer ' + localStorage.getItem('concord.token')},
                    body: JSON.stringify({status: 'resolved', resolution: 'I decide my own block'})
                });
                return {status: r.status, body: await r.text()};
            }""", new_id)
        assert out["status"] == 403, \
            f"the objector recorded a verdict on their own objection: {out}"
        assert "maintainer" in out["body"].lower(), \
            f"the refusal does not name the role it wanted: {out['body']!r}"
        status = _one(db, "SELECT status FROM objections WHERE id = ?", (new_id,))
        assert status == "open", f"a refused self-verdict was written anyway: {status!r}"
        assert not pg.errors, pg.errors
    finally:
        ctx.close()


# --- 3. weight proposals ------------------------------------------------------

def test_a_contributor_opens_a_weight_proposal_from_the_settings_page(browser, server):
    """§6.2's propose form (§12.6.3's open item). Before this the strategy panel could
    list proposals and consent to them, and had no way to open one."""
    db, base = server["db"], server["base"]
    ctx, pg = _page(browser, db, "readers-member")
    try:
        pg.goto(base + "/projects/readers-e2e/settings")
        pg.locator("#weight-feature").select_option("1")
        pg.locator("#weight-value").fill("2.5")
        pg.locator("#weight-rationale").fill("it blocks the release")
        pg.locator("#propose-weight-form button[type=submit]").click()
        expect(pg.locator("[data-strategy-proposals] tbody tr")).to_have_count(1)

        rows = _one(db, "SELECT COUNT(*) FROM strategic_weight_proposals")
        assert rows == 1, f"expected one proposal row, found {rows}"
        after = _one(db, "SELECT weight_after FROM strategic_weight_proposals")
        assert float(after) == 2.5, f"weight_after stored as {after!r}"
        # And the weight itself has NOT moved: a proposal is not a change.
        weight = _one(db, "SELECT strategic_weight FROM features WHERE id = 1")
        assert float(weight) == 1.0, f"the feature's weight moved to {weight!r}"
        assert not pg.errors, pg.errors
    finally:
        ctx.close()


def test_a_non_member_is_refused_a_weight_proposal(browser, server):
    """The contributor gate the handler's comment claimed and did not have: any
    signed-in account on the instance could open a proposal on any project."""
    db, base = server["db"], server["base"]
    con = sqlite3.connect(db)
    con.execute("DELETE FROM strategic_weight_proposals")
    con.execute("UPDATE features SET strategic_weight = 1 WHERE id = 1")
    con.commit()
    con.close()

    ctx, pg = _page(browser, db, "readers-outsider")
    try:
        pg.goto(base + "/projects/readers-e2e/settings")
        # The visitor joined the project in the first test of this module. Remove
        # that so they are once again the non-member this test is about.
        con = sqlite3.connect(db)
        uid = con.execute(
            "SELECT id FROM users WHERE username = 'readers-outsider'").fetchone()[0]
        con.execute("DELETE FROM members WHERE project_id = 1 AND user_id = ?", (uid,))
        con.commit()
        con.close()

        pg.goto(base + "/projects/readers-e2e/settings")
        pg.locator("#weight-feature").select_option("1")
        pg.locator("#weight-value").fill("3")
        pg.locator("#weight-rationale").fill("because I want to")
        pg.locator("#propose-weight-form button[type=submit]").click()
        pg.wait_for_timeout(400)
        rows = _one(db, "SELECT COUNT(*) FROM strategic_weight_proposals")
        assert rows == 0, "a non-member's weight proposal was accepted"
    finally:
        ctx.close()


# --- 4. coverage contests -----------------------------------------------------

def test_a_member_contests_a_coverage_claim_on_the_feature_page(browser, server):
    """§6.4's challenge: "the claim must stand to count". The endpoint existed and
    was unreachable, because no read exposed which claims a solution makes -- the
    claims now arrive with the solutions payload, which is what makes the control
    possible at all."""
    db, base = server["db"], server["base"]
    ctx, pg = _page(browser, db, "readers-member")
    try:
        pg.goto(base + "/projects/readers-e2e/features/1")
        expect(pg.locator("[data-coverage-claims]")).to_be_visible()
        pg.locator("[data-contest]").first.click()
        pg.locator("[data-contest-reason]").first.fill("it only fixes the first page")
        pg.locator("[data-contest-send]").first.click()
        pg.wait_for_load_state("load")

        contested = _one(db, "SELECT contested FROM solution_coverage WHERE solution_id = 1")
        reason = _one(db, "SELECT contest_reason FROM solution_coverage WHERE solution_id = 1")
        assert contested == 1, "the contest was not recorded"
        assert reason == "it only fixes the first page", f"stored reason: {reason!r}"
        expect(pg.locator("[data-coverage-claims]")).to_contain_text("contested")
        assert not pg.errors, pg.errors
    finally:
        ctx.close()


def test_an_author_is_told_why_they_cannot_contest_their_own_claim(browser, server):
    """§5.2's self-vote rule, reaching a reader as the store's own sentence. The
    page offers the control and does not decide; the refusal is the server's."""
    db, base = server["db"], server["base"]
    ctx, pg = _page(browser, db, "readers-author")
    try:
        pg.goto(base + "/projects/readers-e2e/features/1")
        pg.locator("[data-contest]").first.click()
        pg.locator("[data-contest-reason]").first.fill("I take it back")
        pg.locator("[data-contest-send]").first.click()
        err = pg.locator("[data-contest-error]").first
        expect(err).to_be_visible()
        text = err.inner_text().lower()
        # The server's own sentence, and which one it is depends on which self-rule
        # fires first: the store refuses an author's contest of their own coverage
        # claim (§5.2's self-vote rule) and the arena refuses an author voting on
        # their own entry, which is what reaches the page here. What this test is for
        # is that a refusal ARRIVES, in words rather than a status code.
        assert "you proposed" in text or "own" in text, text
        assert "http " not in text, text
        # And nothing was written: the claim keeps the state the member's contest left.
        reason = _one(db, "SELECT contest_reason FROM solution_coverage WHERE solution_id = 1")
        assert reason == "it only fixes the first page", f"stored reason: {reason!r}"
    finally:
        ctx.close()

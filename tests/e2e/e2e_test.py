"""End-to-end tests for the Concord web UI (premise-2 theme).

Prerequisites:  make build   (fresh bin/concord)
Run:            pytest tests/e2e/e2e_test.py -v

Starts a real server on a throwaway SQLite DB, seeds it through the public
API plus direct SQLite fixtures (features/board rows are actor-gated over
HTTP), then drives headless Chromium through the most common visitor flows.
Every browser context gets its own X-Forwarded-For so the per-visitor rate
limiter treats tests as distinct visitors — which also exercises the
limiter's proxy-header path under real browser load.
"""
import json
import os
import sqlite3
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path

import pytest
from playwright.sync_api import expect, sync_playwright

REPO = Path(__file__).resolve().parents[2]
BIN = REPO / "bin" / "concord"
PORT = 8421
BASE = f"http://127.0.0.1:{PORT}"


def api(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        BASE + path, data=data, method=method,
        headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=5) as resp:
        return json.loads(resp.read())


@pytest.fixture(scope="session")
def server(request):
    dbdir = tempfile.mkdtemp(prefix="concord-e2e-")
    db = os.path.join(dbdir, "concord.db")
    env = dict(os.environ, CONCORD_DB=db, CONCORD_LISTEN=f"127.0.0.1:{PORT}")
    # Clean up any leaked server from a previous crashed run, and refuse to
    # proceed if the port is still occupied by something we did not start.
    subprocess.run(["pkill", "-f", str(BIN)], check=False)
    time.sleep(0.5)
    proc = subprocess.Popen([str(BIN)], env=env,
                            stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT)
    request.addfinalizer(lambda: (proc.terminate(), proc.wait(timeout=5)))
    for _ in range(50):
        try:
            with urllib.request.urlopen(f"{BASE}/api/v1/healthz", timeout=1) as r:
                if b"ok" in r.read():
                    break
        except Exception:
            time.sleep(0.2)
    else:
        proc.terminate()
        raise RuntimeError("concord did not become healthy on :%d" % PORT)
    if proc.poll() is not None:
        raise RuntimeError("concord exited immediately — port %d already in use?" % PORT)

    # -- seed everything directly (actor-gated writes have no interim HTTP auth) --
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()
    cur.execute("INSERT INTO users (username, display_name, created_at) VALUES ('alice','Alice',?)", (now,))
    alice = cur.lastrowid
    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license, created_at, updated_at)"
        " VALUES ('governance-lab','Governance Lab',"
        " 'A research project exploring consent-based governance for open-source communities.',"
        " 'collective','AGPL-3.0',?,?)", (now, now))
    p1 = cur.lastrowid
    cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license, created_at, updated_at)"
        " VALUES ('feed-twin','Feed Twin',"
        " 'Twin social feeds with transparent, community-ranked discovery.',"
        " 'maintainer_led','MIT',?,?)", (now, now))
    tag_ids = {}
    for project_id, tag in [(p1, "consensus"), (p1, "governance"), (2, "feeds")]:
        cur.execute("INSERT INTO tags (project_id, name) VALUES (?,?)", (project_id, tag))
        tag_ids[(project_id, tag)] = cur.lastrowid
        cur.execute("INSERT INTO project_tags (project_id, tag_id, applied_by, created_at) VALUES (?,?,?,?)",
                    (project_id, tag_ids[(project_id, tag)], alice, now))
    cur.execute("INSERT INTO project_languages (project_id, language, pct) VALUES (1,'Go',100.0)")
    cur.execute(
        "INSERT INTO project_metrics (project_id, stars, forks, open_issues, commit_count, contributors,"
        " last_commit_at, median_review_hours, releases_90d, health_score)"
        " VALUES (1,140,12,5,1200,6,?,10,2,0.86)", (now - 4 * 86400,))
    cur.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, severity, frequency,"
        " strategic_multiplier, status, created_at, updated_at)"
        " VALUES (1,?,'Consensus flow is undocumented',"
        " 'Nobody knows how to start a consent round.',4,1.5,1.5,'open',?,?)", (alice, now, now))
    feats = []
    for title, body, status, elo in [
            ("Quorum calculator", "Show whether a consent round can pass.", "consensus", 1620.0),
            ("Health dashboard", "Visualise maintenance health over time.", "ready", 1540.0),
            ("Complaint triage queue", "Batch-review incoming complaints.", "draft", 1480.0)]:
        cur.execute(
            "INSERT INTO features (project_id, author_id, title, body, effort,"
            " status, elo_r, elo_rd, elo_vol, strategic_weight, created_at, updated_at)"
            " VALUES (1,?,?,?,?,?,?,350,0.06,1.0,?,?)",
            (alice, title, body, "M", status, elo, now, now))
        feats.append(cur.lastrowid)
    cols = []
    for phase, pos, wip in [("Backlog", 0, None), ("Consensus", 1, 2), ("In progress", 2, 2)]:
        cur.execute("INSERT INTO board_columns (project_id, phase, position, wip_limit) VALUES (1,?,?,?)",
                    (phase, pos, wip))
        cols.append(cur.lastrowid)
    cur.execute("INSERT INTO board_cards (project_id, kind, feature_id, column_id, entered_at) VALUES (1,'feature',?,?,?)",
                (feats[1], cols[0], now))  # Health dashboard -> Backlog
    cur.execute("INSERT INTO board_cards (project_id, kind, feature_id, column_id, entered_at) VALUES (1,'feature',?,?,?)",
                (feats[0], cols[1], now))  # Quorum calculator -> Consensus
    # The FTS index is populated by app code on API writes; mirror it for the
    # direct-seeded projects so full-text search finds them.
    cur.execute("INSERT INTO projects_fts (slug, name, description, tags, languages) VALUES (?,?,?,?,?)",
                ("governance-lab", "Governance Lab",
                 "A research project exploring consent-based governance for open-source communities.",
                 "consensus governance", "Go"))
    cur.execute("INSERT INTO projects_fts (slug, name, description, tags, languages) VALUES (?,?,?,?,?)",
                ("feed-twin", "Feed Twin",
                 "Twin social feeds with transparent, community-ranked discovery.",
                 "feeds", ""))
    con.commit()
    con.close()

    yield {"base": BASE}


@pytest.fixture(scope="session")
def browser(server):
    with sync_playwright() as p:
        yield p.chromium.launch(headless=True)


_xff_counter = {"n": 0}


@pytest.fixture()
def page(browser):
    """A fresh page whose context is its own rate-limit visitor."""
    _xff_counter["n"] += 1
    ip = f"10.11.{_xff_counter['n'] // 200}.{_xff_counter['n'] % 200}"
    ctx = browser.new_context(extra_http_headers={"X-Forwarded-For": ip})
    pg = ctx.new_page()
    yield pg
    ctx.close()


# ---------------------------------------------------------------- homepage

def test_homepage_title(server, page):
    page.goto(BASE + "/")
    expect(page).to_have_title("Home — Concord")


def test_homepage_hero(server, page):
    page.goto(BASE + "/")
    expect(page.locator(".hero-title")).to_contain_text("The forge for")
    expect(page.locator(".hero-title .gradient")).to_have_text("decisions")


def test_homepage_six_pillars(server, page):
    page.goto(BASE + "/")
    expect(page.locator(".section .grid-responsive-3 .card")).to_have_count(6)
    expect(page.get_by_text("Complaints, not feature requests")).to_be_visible()
    expect(page.get_by_text("Consensus, not command")).to_be_visible()


def test_homepage_stats_populated(server, page):
    page.goto(BASE + "/")
    expect(page.locator("#stat-projects")).to_have_text("2")
    expect(page.locator("#stat-tags")).to_have_text("3")
    expect(page.locator("#stat-languages")).to_have_text("1")


def test_homepage_cta_search_navigates(server, page):
    page.goto(BASE + "/")
    page.get_by_role("link", name="Start searching").click()
    expect(page).to_have_url(BASE + "/search")


def test_homepage_cta_projects_navigates(server, page):
    page.goto(BASE + "/")
    page.get_by_role("link", name="Explore projects").click()
    expect(page).to_have_url(BASE + "/projects")


def test_homepage_explore_grid(server, page):
    page.goto(BASE + "/")
    expect(page.locator(".grid-4 > a.card")).to_have_count(4)


def test_homepage_flip_section(server, page):
    page.goto(BASE + "/")
    expect(page.locator(".flip-section")).to_contain_text("a decision layer")


def test_logo_navigates_home(server, page):
    page.goto(BASE + "/projects")
    page.locator("a.brand").click()
    expect(page).to_have_url(BASE + "/")


def test_nav_search_link(server, page):
    page.goto(BASE + "/")
    page.locator("nav.site-nav a", has_text="Search").click()
    expect(page).to_have_url(BASE + "/search")


def test_nav_projects_link(server, page):
    page.goto(BASE + "/")
    page.locator("nav.site-nav a", has_text="Projects").click()
    expect(page).to_have_url(BASE + "/projects")


def test_footer_version(server, page):
    page.goto(BASE + "/")
    expect(page.locator(".site-footer")).to_contain_text("a forge for discussions")
    expect(page.locator(".site-footer .muted")).to_contain_text("v")


# ---------------------------------------------------------------- theme

def test_theme_css_applied(server, page):
    page.goto(BASE + "/")
    bg = page.evaluate("getComputedStyle(document.body).backgroundColor")
    assert bg == "rgb(241, 245, 249)", f"slate-50 body background expected, got {bg}"


def test_csp_header_on_pages(server, page):
    resp = page.goto(BASE + "/")
    csp = resp.headers.get("content-security-policy", "")
    assert "default-src 'self'" in csp
    assert resp.headers.get("x-frame-options") == "DENY"


def test_mobile_no_horizontal_overflow(server, page):
    page.set_viewport_size({"width": 375, "height": 812})
    page.goto(BASE + "/")
    overflow = page.evaluate(
        "document.documentElement.scrollWidth - window.innerWidth")
    assert overflow <= 0, f"horizontal overflow of {overflow}px at 375px viewport"
    expect(page.locator(".hero-title")).to_be_visible()


# ---------------------------------------------------------------- search

def test_search_empty_state(server, page):
    page.goto(BASE + "/search")
    expect(page.get_by_text("Type a query to start")).to_be_visible()


def test_search_finds_project(server, page):
    page.goto(BASE + "/search?q=governance")
    expect(page.locator("#search-results .card")).to_have_count(1)
    expect(page.locator("#search-results .card-title")).to_contain_text("Governance Lab")


def test_search_result_click_opens_project(server, page):
    page.goto(BASE + "/search?q=governance")
    page.locator("#search-results .card").first.click()
    expect(page).to_have_url(BASE + "/projects/governance-lab")


def test_search_no_results_empty_state(server, page):
    page.goto(BASE + "/search?q=zzzznothing")
    expect(page.get_by_text("No matches")).to_be_visible()


def test_search_tag_chip_filters(server, page):
    page.goto(BASE + "/search?q=governance")
    chip = page.locator(".filter-chip", has_text="consensus")
    expect(chip).to_be_visible()
    chip.click()
    expect(page.locator("#search-results .card-title")).to_contain_text("Governance Lab")


# ---------------------------------------------------------------- projects

def test_projects_grid_lists_seed(server, page):
    page.goto(BASE + "/projects")
    expect(page.locator("#projects-grid .card")).to_have_count(2)
    expect(page.locator("#projects-grid")).to_contain_text("Governance Lab")
    expect(page.locator("#projects-grid")).to_contain_text("Feed Twin")


def test_projects_health_bar_renders(server, page):
    page.goto(BASE + "/projects")
    card = page.locator("#projects-grid .card", has_text="Governance Lab")
    expect(card.locator(".health-fill-good")).to_be_visible()
    expect(card.locator(".health-label")).to_contain_text("health")


def test_projects_governance_badge(server, page):
    page.goto(BASE + "/projects")
    card = page.locator("#projects-grid .card", has_text="Governance Lab")
    expect(card.locator(".badge-indigo")).to_have_text("collective")
    expect(card.locator(".badge-slate", has_text="AGPL-3.0")).to_be_visible()


# ---------------------------------------------------------------- project detail

def test_project_detail_name_description(server, page):
    page.goto(BASE + "/projects/governance-lab")
    expect(page.locator("#project-detail .page-title")).to_contain_text("Governance Lab")
    expect(page.locator("#project-detail .page-subtitle")).to_contain_text("consent-based governance")


def test_project_detail_features(server, page):
    page.goto(BASE + "/projects/governance-lab")
    expect(page.locator("#project-detail .grid-responsive-2 .card")).to_have_count(3)
    expect(page.locator("#project-detail")).to_contain_text("Quorum calculator")
    expect(page.locator("#project-detail .badge-purple").first).to_contain_text("rating")


def test_project_detail_board_link(server, page):
    page.goto(BASE + "/projects/governance-lab")
    page.get_by_role("link", name="Open board").click()
    expect(page).to_have_url(BASE + "/projects/governance-lab/board")


def test_project_not_found_graceful(server, page):
    resp = page.goto(BASE + "/projects/nope-not-real")
    assert resp.status == 200  # shell renders; hydration shows the message
    expect(page.get_by_text("Project not found")).to_be_visible()


# ---------------------------------------------------------------- board

def test_board_renders_columns(server, page):
    page.goto(BASE + "/projects/governance-lab/board")
    expect(page.locator(".kanban-column")).to_have_count(3)
    expect(page.locator(".kanban-column-title")).to_have_text(["Backlog", "Consensus", "In progress"])


def test_board_card_in_column(server, page):
    page.goto(BASE + "/projects/governance-lab/board")
    backlog = page.locator(".kanban-column", has_text="Backlog")
    expect(backlog.locator(".kanban-card")).to_have_text(["Health dashboard"])
    consensus = page.locator(".kanban-column", has_text="Consensus")
    expect(consensus.locator(".kanban-card")).to_have_text(["Quorum calculator"])


def test_board_wip_badge(server, page):
    page.goto(BASE + "/projects/governance-lab/board")
    expect(page.locator(".kanban-column-count").nth(1)).to_contain_text("WIP 2")


def test_board_empty_state(server, page):
    page.goto(BASE + "/projects/feed-twin/board")
    expect(page.get_by_text("No board yet")).to_be_visible()


# ---------------------------------------------------------------- API integration

def test_api_healthz(server, page):
    r = page.request.get(BASE + "/api/v1/healthz")
    assert r.status == 200
    assert r.json()["status"] == "ok"


def test_api_projects_shape(server, page):
    r = page.request.get(BASE + "/api/v1/projects")
    assert r.status == 200
    projects = r.json()
    assert isinstance(projects, list) and len(projects) == 2
    by_slug = {p["slug"]: p for p in projects}
    assert by_slug["governance-lab"]["governance_model"] == "collective"
    assert by_slug["governance-lab"]["health_score"] == 0.86


def test_api_search_shape(server, page):
    r = page.request.get(BASE + "/api/v1/search?q=governance")
    assert r.status == 200
    data = r.json()
    assert len(data["results"]) == 1
    assert data["results"][0]["slug"] == "governance-lab"
    assert any(t["value"] == "consensus" for t in data["facets"]["tags"])


def test_api_404_shape(server, page):
    r = page.request.get(BASE + "/api/v1/projects/does-not-exist")
    assert r.status == 404
    assert "error" in r.json()

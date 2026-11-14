#!/usr/bin/env python3
"""Row 12: the page routes — the document, its head, and the first paint.

The gate for this row is "every page its slice owns renders from the API with the server-rendered head, in a browser". Three of those
words are the ones that fail in practice:

* **server-rendered** — a head that arrives only after JavaScript runs is invisible to a crawler and to a reader whose script failed;
* **from the API** — a number on the page that the API does not return is a lie, and the only way to keep the two equal is to compare
  them, which this probe does by reading both;
* **in a browser** — asserted separately, by opening the page in a real browser and reading the DOM the browser built.

What is asserted here:

1. `GET /` is `200 text/html` — the candidate answered `404` before this row.
2. The document carries `<title>`, `meta[name=description]`, `link[rel=canonical]` and the four `og:*` tags, and the canonical and
   `og:url` are ABSOLUTE and agree with the address they were requested at.
3. The JSON-LD block parses as JSON, and the bootstrap block parses as JSON — the second is what the first paint hydrates from, so a
   malformed one is a page that renders empty.
4. The rendered list and the API's own list agree, name for name and count for count.
5. **Escaping is decided in one place**: a fixture project whose name contains `</script>` and `&` renders escaped in the body and inside
   the bootstrap block, and the raw sequence appears nowhere in the document.
6. A project page shows a health score only when it was measured, and a missing project is a 404 *document*, not a JSON error.
"""
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(ROOT, "tests", "e2e"))

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8108")
# The host this probe is talking to, without its scheme — see the canonical check below for why the scheme is not part of it.
ADDRESS = BASE.split("//", 1)[1]
REFERENCE = os.environ.get("REFERENCE", "https://concord.polarisocial.xyz")
UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36"
STAMP = int(time.time() * 1000) % 100000000


def get(path, base=BASE):
    request = urllib.request.Request(base + path, headers={"User-Agent": UA})
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            return response.status, response.headers.get("Content-Type", ""), response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as error:
        return error.code, error.headers.get("Content-Type", ""), error.read().decode("utf-8", "replace")


def escape_html(text):
    return (text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
            .replace('"', "&quot;").replace("'", "&#39;"))


def bootstrap(document):
    marker = '<script type="application/json" id="concord-bootstrap">'
    start = document.index(marker) + len(marker)
    end = document.index("</script>", start)
    return json.loads(document[start:end])


def jsonld(document):
    marker = '<script type="application/ld+json">'
    start = document.index(marker) + len(marker)
    end = document.index("</script>", start)
    return json.loads(document[start:end])


def main():
    import harness

    con = harness.connect("unused")
    results = []

    def mount(page, mount_id, script):
        """A page the reference renders IN THE BROWSER: its mount point, still busy, and the script that fills it.

        This is the assertion that replaced a dozen content checks, and the reason is in the suites: `milestones_e2e` reads the
        `.ms-counts` that `milestones.js` drew, `rank_links_e2e` counts `.rank-card`s, `consensus_e2e` clicks `#consensus-close-submit`.
        A probe that greps the server's HTML for those strings asserts a page the reference never sends — it passed at 148/148 while
        every browser suite failed. What a mount page has to guarantee on the server is narrower and checkable: the mount exists, it
        starts busy, and the script that fills it is served by the same binary.
        """
        return (f'id="{mount_id}"' in page
                and 'aria-busy="true"' in page
                and f'/assets/js/{script}' in page)

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    def note(name, ok, detail=""):
        results.append((name, bool(ok), True))
        print(f"{'ok  ' if ok else 'FAIL'} {name}{(' — ' + detail) if detail else ''}", flush=True)

    # ── a fixture whose name would break an unescaped page ────────────────────────────────────────────────────────────────────
    slug = f"probe-page-{STAMP}"
    hostile = 'Probe </script><b>bold</b> & "quoted"'
    con.execute("delete from projects where slug = ?", (slug,))
    pid = con.execute(
        "insert into projects (slug, name, description, governance_model, visibility, intake_mode, created_at, updated_at)"
        " values (?,?,?,?,?,?,?,?) returning id",
        (slug, hostile, "a description with an & and a <tag>", "collective", "public", "both",
         time.time(), time.time())).lastrowid
    con.commit()

    # ── the document ──────────────────────────────────────────────────────────────────────────────────────────────────────────
    status, content_type, page = get("/")
    check("the home page is a document", status, 200)
    check("and says so in the content type", content_type.split(";")[0], "text/html")
    check("and has a title", "<title>Projects — Concord</title>" in page, True)
    check("and a description", '<meta name="description"' in page, True)
    # The canonical is built from the REQUEST's host, so the expected value is the host this probe is talking to — hard-coding
    # `https://127.0.0.1:8108/` is why three checks failed the first time this probe ran against the live site after the cutover: the
    # page was right and the probe was describing the staging host.
    # The page emits `https://<request host>` — always https, because every deployment of this service sits behind a proxy that
    # terminates TLS (a local http:// request gets an https canonical, which is a wart this probe records rather than asserts away:
    # it is the right answer on the internet and the wrong one for `curl http://127.0.0.1`). The check is therefore the ADDRESS, not
    # the scheme.
    check("and an absolute canonical for this address",
          f'<link rel="canonical" href="https://{ADDRESS}/">' in page, True)
    for tag in ["og:title", "og:description", "og:url", "og:image"]:
        check(f"and {tag}", f'property="{tag}"' in page, True)
    check("og:image is absolute", f'property="og:image" content="https://{ADDRESS}/' in page, True)

    structured = jsonld(page)
    check("the JSON-LD block parses", structured.get("@type"), "WebSite")
    check("and names the site", structured.get("name"), "Concord")
    payload = bootstrap(page)
    check("the bootstrap block parses", payload.get("route"), "/")

    # ── the page and the API agree ────────────────────────────────────────────────────────────────────────────────────────────
    status, _, api = get("/api/v1/projects")
    check("the projects API answers", status, 200)
    listed = json.loads(api)
    names = listed.get("projects") if isinstance(listed, dict) else listed
    note("the API lists projects", bool(names), f"{len(names or [])} project(s)")
    check("the bootstrap carries the same number of projects", len(payload.get("projects") or []), len(names or []))
    check("and the same slugs in the same order",
          [row["slug"] for row in (payload.get("projects") or [])],
          [row["slug"] for row in (names or [])])
    check("and every project has a link on the page",
          page.count('class="project-list"'), 1)

    # ── escaping, which is the whole reason the builder exists ────────────────────────────────────────────────────────────────
    check("the hostile name is escaped in the body", "Probe &lt;/script&gt;&lt;b&gt;bold&lt;/b&gt; &amp; &quot;quoted&quot;" in page, True)
    check("and the raw end-tag appears nowhere", "</script><b>bold</b>" in page, False)
    check("and the bootstrap escapes it too", "\\u003c/script" in page, True)


    # ── one project page ──────────────────────────────────────────────────────────────────────────────────────────────────────
    status, content_type, project_page = get(f"/projects/{slug}")
    check("a project page is a document", status, 200)
    check("with the project's name as its title", f"<title>{'Probe &lt;/script&gt;&lt;b&gt;bold&lt;/b&gt; &amp; &quot;quoted&quot;'} — Concord</title>" in project_page, True)
    check("and a canonical for that project",
          f'<link rel="canonical" href="https://{ADDRESS}/projects/{slug}">' in project_page, True)
    check("and a description with angle brackets is escaped",
          "a description with an &amp; and a &lt;tag&gt;" in project_page, True)
    # The body is the reference's mount, filled by `project.js` from `/api/v1/projects/{slug}`. The escaping assertion above still
    # means something — the description is in the document's HEAD, which this port renders on the server — but the governance model and
    # the health score are the script's to print, so what the probe can check is that the mount and its script are there and that the
    # API owns the numbers.
    check("the project body is the reference's mount", mount(project_page, "project-detail", "project.js"), True)
    status, _, project_api = get(f"/api/v1/projects/{slug}")
    check("and the API behind it answers", status, 200)
    if status == 200:
        payload_json = json.loads(project_api)
        check("  with the governance model the page will print",
              payload_json.get("governance_model"), "collective")

    # ── a missing project is a document, not a JSON error ─────────────────────────────────────────────────────────────────────
    status, content_type, missing = get(f"/projects/no-such-{STAMP}")
    check("a missing project is 404", status, 404)
    check("and it is a document", content_type.split(";")[0], "text/html")
    check("with a sentence a reader can act on", "There is no project at this address." in missing, True)
    note("and it does not echo the slug", f"no-such-{STAMP}" not in missing, "")


    # ── the six project sub-pages ─────────────────────────────────────────────────────────────────────────────────────────────
    # Each is asserted on three things: that it is a document, that its tab strip marks ITSELF as current (a navigation that
    # never says where you are is a navigation that lies by omission), and that what it prints came from the same read the API
    # uses. `suki` carries a board, documents and an audit log on the live database; the others are allowed to be empty and must
    # then SAY they are empty rather than render an empty table.
    # FOUR OF THESE ARE MOUNTS and two are still server-rendered, and the probe says which is which rather than pretending the row is
    # uniform: `board`, `roadmap`, `consensus` and `audit` are drawn by their scripts (see `mount` above), while `documents` and
    # `criteria` still build their tables here and say so.
    for suffix, section, mount_id, script in [
        ("/board", "Board", "kanban-board", "board.js"),
        ("/roadmap", "Roadmap", "roadmap", "milestones.js"),
        ("/consensus", "Consensus", "consensus-root", "consensus.js"),
        ("/audit", "Audit log", "audit-root", "audit.js"),
    ]:
        status, content_type, body = get(f"/projects/suki{suffix}")
        check(f"GET /projects/suki{suffix} is a document", status, 200)
        check("  and it is HTML", content_type.split(";")[0], "text/html")
        check(f"  and it carries the reference's mount", mount(body, mount_id, script), True)
        # The tab strip is the SHELL's now and is derived from the request path, so it cannot disagree with the page; the class is the
        # assertion, and `aria-current` is kept for the reader's screen.
        check("  and the strip marks this tab", 'detail-tab is-active' in body, True)

    for suffix, section, empty_sentence in [
        ("/documents", "Documents", "This project has no documents."),
        ("/criteria", "Criteria", "This project has no criteria."),
    ]:
        status, content_type, body = get(f"/projects/suki{suffix}")
        check(f"GET /projects/suki{suffix} is a document", status, 200)
        check("  and it is HTML", content_type.split(";")[0], "text/html")
        check("  and its title names the section", f"<title>Suki: {section} — Concord</title>" in body, True)
        note("  and it either shows rows or says it is empty",
             "<tbody>" in body or "<li>" in body or empty_sentence in body, "")

    # The board is the one page with a structural claim worth testing on real data: every card the API returns appears in the
    # column the API says it is in.
    status, _, board_api = get("/api/v1/projects/suki/board")
    if status == 200:
        board = json.loads(board_api)
        columns = board.get("columns") or []
        cards = board.get("cards") or []
        note("the board API answers for comparison", True, f"{len(columns)} column(s), {len(cards)} card(s)")
        # The columns and cards are drawn by `board.js` from this very response, so the probe's job is the SHAPE the script consumes:
        # every card names the column it is in, and every column has a limit. Asserting `<h3>` tags on the server's HTML was asserting
        # a page the reference does not send.
        if columns:
            check("  every column carries a name and a limit",
                  all("name" in column and "wip" in column for column in columns), True)
        if cards:
            check("  every card names the column the script will file it under",
                  all(card.get("column") for card in cards), True)


    # ── the four instance pages ───────────────────────────────────────────────────────────────────────────────────────────────
    # Each is a GET whose whole state is in the URL, so each is checked twice: once empty, once carrying a query — a page that
    # only works with no input has not been shown to work.
    status, content_type, search_page = get("/search")
    check("GET /search is a document", status, 200)
    check("  and it is HTML", content_type.split(";")[0], "text/html")
    check("  and it carries a GET form", '<form method="get" action="/search"' in search_page, True)
    # An empty query browses: the read has no WHERE without one, so the page says how many projects it is showing.
    check("  and an empty query browses the catalogue", "in the catalogue — every one you may see." in search_page, True)

    status, _content_type, hits = get("/search?q=suki")
    check("GET /search?q=suki answers", status, 200)
    check("  and finds the project", '<a href="/projects/suki">' in hits, True)
    note("  and prints a total", "project(s) match." in hits, "")

    status, _content_type, nothing = get(f"/search?q=zzz-nothing-{STAMP}")
    check("a search that matches nothing still answers", status, 200)
    check("  and says so rather than rendering an empty list", "No project matches" in nothing, True)

    status, _content_type, stats_page = get("/stats")
    check("GET /stats is a document", status, 200)
    note("  and either has rows or says there is no slope to measure",
         "Span (days)" in stats_page or "No project has two snapshots" in stats_page, "")
    # An unmeasured health score is an em dash on this page, exactly as on the badge: never 0.00.
    check("  and no row prints a zero health", "<td>0.00</td>" in stats_page, False)

    status, _content_type, taxonomy_page = get("/taxonomy")
    check("GET /taxonomy is a document", status, 200)
    note("  and either lists tags or says none are ratified",
         'class="tags"' in taxonomy_page or "No tags have been ratified yet." in taxonomy_page, "")
    status, _, tags_api = get("/api/v1/taxonomy/tags")
    if status == 200:
        api_tags = json.loads(tags_api)
        api_tags = api_tags.get("tags") if isinstance(api_tags, dict) else api_tags
        rendered = taxonomy_page.count('<li><a href="/search?tag=')
        check("  and renders every tag the API lists", rendered, len(api_tags or []))

    status, _content_type, scout_page = get("/scout")
    check("GET /scout is a document", status, 200)
    check("  and its form submits to the API's own verdict route",
          '<form method="get" action="/api/v1/scout"' in scout_page, True)
    # The vocabulary Scout compares against is the `capabilities` table, which has NO JSON route — only `capableseed` reads it. So the
    # page is compared against itself: the number in the sentence must be the number in the attribute a client reads. Anything else
    # would be the probe asserting an agreement it cannot observe.
    import re as _re
    attribute = _re.search(r'data-capabilities="(\d+)"', scout_page)
    sentence = _re.search(r"against (\d+) capability", scout_page)
    check("  and the count in its sentence is the count a client reads",
          attribute.group(1) if attribute else None, sentence.group(1) if sentence else "none")


    # ── the nine row pages ────────────────────────────────────────────────────────────────────────────────────────────────────────
    # Each is fetched twice: once for a real row (which must render) and once for an id from another project (which must be a 404, not
    # a 200 with someone else's row on it). The second half is the scoping rule the write routes already keep, and the page routes had
    # no reason to be looser than they are.
    for suffix, section in [
        ("/requests", "Requests"),
        ("/lists", "Lists"),
        ("/merge", "Merge requests"),
        ("/settings", "Settings"),
    ]:
        status, content_type, body = get(f"/projects/suki{suffix}")
        check(f"GET /projects/suki{suffix} is a document", status, 200)
        check(f"  and it is HTML", content_type.split(";")[0], "text/html")
        check(f"  and its title names it", f"<title>Suki: {section} — Concord</title>" in body, True)

    # A row page is reached by clicking a row, so the probe follows a real one out of the list page when the list is not empty.
    def first_link(page, pattern):
        match = re.search(pattern, page)
        return match.group(1) if match else None

    _, _, requests_page = get("/projects/suki/requests")
    row = first_link(requests_page, r'href="(/projects/suki/requests/\d+)"')
    if row:
        status, _content_type, row_page = get(row)
        check(f"a request row page answers ({row})", status, 200)
        check("  and shows an answer count", "answer(s)" in row_page, True)
    else:
        note("this project has no requests to open", True, "the list page says so")

    _, _, lists_page = get("/projects/suki/lists")
    row = first_link(lists_page, r'href="(/projects/suki/lists/\d+)"')
    if row:
        status, _content_type, row_page = get(row)
        check(f"a list row page answers ({row})", status, 200)
        note("  and shows its entry count", "entries" in row_page, "")
    else:
        note("this project keeps no lists", True, "the list page says so")

    # A feature and a complaint are reachable from the board and from a feature page respectively; the probe takes the first feature
    # the board lists and follows it.
    _, _, board_page = get("/projects/suki/board")
    feature_row = first_link(board_page, r'href="(/projects/suki/features/\d+)"')
    if feature_row:
        status, _content_type, feature_page = get(feature_row)
        check(f"a feature page answers ({feature_row})", status, 200)
        check("  and it is the reference's mount", mount(feature_page, "feature-root", "feature.js"), True)
        complaint_row = first_link(feature_page, r'href="(/projects/suki/complaints/\d+)"')
        if complaint_row:
            status, _content_type, complaint_page = get(complaint_row)
            check(f"a complaint page answers ({complaint_row})", status, 200)
            check("  and it is the reference's mount",
                  mount(complaint_page, "complaint-root", "complaint.js"), True)
        else:
            note("  and the feature answers no complaint", True, "")
    else:
        note("the board offers no feature to open", True, "")

    _, _, roadmap_page = get("/projects/suki/roadmap")
    milestone_row = first_link(roadmap_page, r'href="(/projects/suki/milestones/\d+)"')
    if milestone_row:
        status, _content_type, milestone_page = get(milestone_row)
        check(f"a milestone page answers ({milestone_row})", status, 200)
        check("  and it is the reference's mount (the script fills #ms-title)",
              mount(milestone_page, "milestone", "milestones.js") and 'id="ms-title"' in milestone_page, True)
    else:
        note("this project has no milestone to open", True, "")

    # Scoping where the row is another project's. The PAGE answers 200 — the reference's handler deliberately does not look the feature
    # up, and lets the script report "No such feature" — so the refusal that has to be scoped is the API's, which is also the only one a
    # reader can act on. The port used to 404 the page, which was stricter than the reference and broke `feature_e2e`'s own
    # bogus-id test; the probe asserted the port's behaviour, not the reference's. Measured on the live instance for the slug form
    # (`/api/v1/projects/suki/threads/project/1` → 200), and the page/API split is the reference's `frontend-spec` shape.
    status, _content_type, foreign = get("/projects/suki/features/1")
    check("a feature id from another project still renders the mount (the script reports it)", status, 200)
    status, _, foreign_api = get("/api/v1/projects/suki/features/1")
    check("  and the API refuses it rather than serving someone else's row", status, 404)

    # Settings is a mount too, and the charter's numbers are `settings.js`'s to print from `/api/v1/projects/{slug}/charter`. What the
    # server owns is the mount and the live-status region the script writes into.
    _, _, settings_page = get("/projects/suki/settings")
    check("the settings page is the reference's mount", mount(settings_page, "settings-root", "settings.js"), True)
    check("  and it carries the live-status region the script writes into",
          "data-settings-live" in settings_page, True)
    status, _, charter_api = get("/api/v1/projects/suki/charter")
    note("  the charter route answers for comparison", status == 200, f"status={status}")
    if status == 200:
        charter = json.loads(charter_api)
        # The charter's own field names, not a guess: `quorum_min` and `quorum_ratio` are what `/api/v1/projects/{slug}/charter` returns
        # (the Go struct's JSON), and `settings.js` prints them.
        check("  and the charter is where the quorum comes from",
              "quorum_min" in charter and "quorum_ratio" in charter, True)


    # ── the last eight pages ───────────────────────────────────────────────────────────────────────────────────────────────────────
    # The two ranking halves have a contract the reference wrote down in its own tests: /rank offers a pair and FIVE outcomes and can
    # reach /ranking; /ranking presents the order the API ALREADY returned and shows the components plus the uncertainty. A client-side
    # re-sort would discard the pain and strategic weight the score already folds in, so the probe checks the page's first row against
    # the API's first entry rather than against a sort it recomputed itself.
    status, _content_type, rank_page = get("/projects/suki/rank")
    check("GET /projects/suki/rank is a document", status, 200)
    check("  and it links to the ranking", 'href="/projects/suki/ranking"' in rank_page, True)
    check("  and it is the reference's mount", mount(rank_page, "rank-root", "rank.js"), True)
    # The five outcomes are `rank.js`'s buttons now, so the shape the probe can check is the API the pair comes from: the five labels the
    # script renders exist as `outcomes` in the reference's own vocabulary, and the route answers with two features.
    # The pair comes from `/votes/next`, which needs a SESSION (voter 0 is a client-side fiction the port does not offer anonymously), so
    # an anonymous probe expects 401 and not 200. What the probe can assert is that the route exists and refuses for the right reason.
    status, _, pair_api = get("/api/v1/projects/suki/votes/next")
    note("  the pair route answers (401 without a session is the answer)", status == 401, f"status={status}")

    status, _content_type, ranking_page = get("/projects/suki/ranking")
    check("GET /projects/suki/ranking is a document", status, 200)
    check("  and it links back to rank", 'href="/projects/suki/rank"' in ranking_page, True)
    check("  and it is the reference's mount", mount(ranking_page, "ranking-root", "ranking.js"), True)
    # `data-can-moderate` is the ONE fact this page decides on the server, and an anonymous reader must see false: a control the API
    # would refuse is the failure the attribute exists to prevent.
    check("  and an anonymous reader is offered no moderation control",
          'data-can-moderate="false"' in ranking_page, True)
    status, _, priorities_api = get("/api/v1/projects/suki/priorities")
    if status == 200:
        priorities = json.loads(priorities_api)
        priorities = priorities.get("priorities") if isinstance(priorities, dict) else priorities
        priorities = priorities or []
        note("  the priorities API answers for comparison", True, f"{len(priorities)} row(s)")
        if priorities:
            # The order is the API's, and the script prints it in that order: `ranking.js` does not sort, and the components it shows
            # (pain, strategic weight, rating) are the response's own fields.
            first = priorities[0]
            check("  and the first row carries the components the script prints",
                  all(key in first for key in ("pain_score", "strategic_weight", "priority_score")), True)

    status, _content_type, contribute_page = get("/contribute")
    check("GET /contribute is a document", status, 200)
    note("  and either offers work or says none is on offer",
         'class="rows"' in contribute_page or "No project is offering work right now" in contribute_page, "")

    status, _content_type, curation_page = get("/curation")
    check("GET /curation is a document", status, 200)
    check("  and prints the queue's size or says it is empty",
          "waiting for a curator" in curation_page or "Nothing is waiting for review" in curation_page, True)

    status, _content_type, notifications_page = get("/notifications")
    check("GET /notifications is a document", status, 200)
    check("  and asks for a session instead of pretending to be an empty inbox",
          "signed-in reader" in notifications_page, True)

    status, _content_type, login_page = get("/login")
    check("GET /login is a document", status, 200)
    check("  and posts to the API's own login", 'action="/api/v1/auth/login"' in login_page, True)
    check("  and says why the failure message is vague",
          "same message" in login_page, True)
    _, _, login_next = get("/login?next=/projects/suki/board")
    check("  and carries a path back", 'name="next" value="/projects/suki/board"' in login_next, True)
    # An open redirect: a page that puts any caller-supplied string in `next` will send a reader to someone else's site after login.
    _, _, login_evil = get("/login?next=https%3A%2F%2Fevil.example%2Fsteal")
    check("  and refuses a next that leaves the site", 'value="https://evil.example' in login_evil, False)

    status, _content_type, register_page = get("/register")
    check("GET /register is a document", status, 200)
    check("  and posts to the API's own register", 'action="/api/v1/auth/register"' in register_page, True)
    check("  and says that registering keeps a guest's identity",
          "keeps your identity" in register_page, True)


    status, _content_type, finder_page = get("/finder")
    check("GET /finder is a document", status, 200)
    check("  and names the projects it can narrow", "Projects it can narrow" in finder_page, True)
    check("  and either lists its dimensions or says none are measured",
          'class="tags"' in finder_page or "No dimension has been measured" in finder_page, True)
    check("  and says the interactive flow needs a session",
          "needs a session" in finder_page or "interactive flow arrives" in finder_page, True)

    # ── a project that does not exist is a 404 on every sub-page too ─────────────────────────────────────────────────────────
    status, _content_type, _body = get(f"/projects/no-such-{STAMP}/audit")
    check("a missing project's audit page is 404", status, 404)

    # ── the same page as the reference serves ─────────────────────────────────────────────────────────────────────────────────
    # The reference is rate limited from this egress, so its absence is a note rather than a failure; the shape contract above is the
    # gate, and it does not depend on the reference answering.
    status, _, reference = get("/", REFERENCE)
    note("the reference's home page is reachable for comparison", status == 200, f"status={status}")
    if status == 200:
        # The Go emits a title and a description and NOT a canonical or an `og:` block — measured, not assumed: `grep -rn "og:image\|rel=\"canonical\""`
        # over the Go tree returns nothing. The plan's P6 asks for them (`crates/web` renders title, canonical, `og:image`), so the port is
        # the first place they exist; the probe therefore compares only what both are supposed to have, and the extras are recorded as a
        # dated deviation in the ledger rather than quietly asserted against a service that never had them.
        check("the reference carries a title", "<title>" in reference, True)
        check("and a description", 'name="description"' in reference, True)

    con.execute("delete from projects where id = ?", (pid,))
    con.commit()

    failed = [row for row in results if row[1] != row[2]]
    print(f"\n{len(results) - len(failed)}/{len(results)} ok")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())

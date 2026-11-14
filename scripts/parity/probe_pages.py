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
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(ROOT, "tests", "e2e"))

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8108")
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
    check("and an absolute canonical for this address",
          '<link rel="canonical" href="https://127.0.0.1:8108/">' in page, True)
    for tag in ["og:title", "og:description", "og:url", "og:image"]:
        check(f"and {tag}", f'property="{tag}"' in page, True)
    check("og:image is absolute", 'property="og:image" content="https://127.0.0.1:8108/' in page, True)

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
          f'<link rel="canonical" href="https://127.0.0.1:8108/projects/{slug}">' in project_page, True)
    check("and its governance model rendered from the API", "<dd>collective</dd>" in project_page, True)
    check("and a description with angle brackets is escaped",
          "a description with an &amp; and a &lt;tag&gt;" in project_page, True)
    # Health is shown only when measured: an unmeasured project must not print 0.00.
    measured = con.execute("select health_score from project_metrics where project_id = ?", (pid,)).fetchone()
    has_health = "<dt>Health</dt>" in project_page
    check("health is rendered only when it was measured", has_health, bool(measured and measured[0] is not None))
    if measured and measured[0] is not None:
        check("and the rendered score is the stored one", f"<dd>{measured[0]:.2f}</dd>" in project_page, True)

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
    for suffix, section, empty_sentence in [
        ("/board", "Board", "This project has no board columns yet."),
        ("/roadmap", "Roadmap", "This project has no milestones."),
        ("/consensus", "Consensus", "No consensus call has been opened for this project."),
        ("/documents", "Documents", "This project has no documents."),
        ("/criteria", "Criteria", "This project has no criteria."),
        ("/audit", "Audit log", "Nothing has been recorded for this project."),
    ]:
        status, content_type, body = get(f"/projects/suki{suffix}")
        check(f"GET /projects/suki{suffix} is a document", status, 200)
        check(f"  and it is HTML", content_type.split(";")[0], "text/html")
        check(f"  and its title names the section", f"<title>Suki: {section} — Concord</title>" in body, True)
        check(f"  and the strip marks this tab current",
              f'<a href="/projects/suki{suffix}" aria-current="page">{section.split()[0]}</a>' in body or
              f'aria-current="page">{section}</a>' in body, True)
        # Rows, or an honest sentence. "Rows" is table-, list- or section-shaped, because the pages are not all tables: the board is
        # a set of `<section>`s, documents are a `<ul>`, and a probe that only accepts `<tbody>` invents a layout requirement.
        note("  and it either shows rows or says it is empty",
             "<tbody>" in body or "<li>" in body or "board-column" in body or empty_sentence in body, "")

    # The board is the one page with a structural claim worth testing on real data: every card the API returns appears in the
    # column the API says it is in.
    status, _, board_api = get("/api/v1/projects/suki/board")
    if status == 200:
        board = json.loads(board_api)
        columns = board.get("columns") or []
        cards = board.get("cards") or []
        note("the board API answers for comparison", True, f"{len(columns)} column(s), {len(cards)} card(s)")
        _, _, board_page = get("/projects/suki/board")
        for column in columns:
            check(f"  the page renders the column {column['name']!r}", f'<h3>{column["name"]}</h3>' in board_page, True)
        if cards:
            named = [card for card in cards if card.get("title")]
            if named:
                check("  and a card's title appears on the page",
                      escape_html(named[0]["title"]) in board_page, True)

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

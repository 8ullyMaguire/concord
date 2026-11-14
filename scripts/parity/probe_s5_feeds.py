#!/usr/bin/env python3
"""Row 11: the thirteen feed documents, parsed by a real XML parser.

Gate 47 is "every feed the manifest declares parses", so this parses — `xml.etree`, not a regex — and then
checks the five rules that a well-formed document can still get wrong:

* **the parameter beats the suffix.** `/feed.xml?format=atom` must be Atom. The opposite order is a bug the Go
  tests record: the suffix was consulted first, so an explicit request was silently ignored.
* **an unknown format is a 400, not a default.** A reader asking for `?format=pdf` and receiving RSS cannot tell
  what happened.
* **an empty feed is STABLE.** Two fetches of a project with nothing in it must carry the same `<updated>`; a
  document timestamped with the server's clock makes every reader re-download everything, forever.
* **a private project's feed is a 404 identical to a nonexistent one**, because a feed is fetched by anonymous
  aggregators and a 200-with-content would publish the project.
* **no tally in the consensus feed.** §6.6 is a rule about who may see a number; a feed is copied and kept by
  third parties, so the count must not be there at all.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(HERE)), "tests", "e2e"))

# ── consent ──────────────────────────────────────────────────────────────────────────────────────────────────────
# Every mutation on this instance must carry the accepted cookie notice: the server refuses with 403 `consent_required`
# otherwise, and that is the reference's behaviour, not a detail of this probe. Rather than thread the header through every call
# below, the probe carries it the way a browser does.
_ORIGINAL_REQUEST = urllib.request.Request


def _consented_request(*args, **kwargs):
    if len(args) > 2:
        headers = dict(args[2] or {})
        args = args[:2]
        kwargs["headers"] = headers
    else:
        kwargs["headers"] = dict(kwargs.pop("headers", None) or {})
    kwargs["headers"].setdefault(
        "Cookie", os.environ.get("CONCORD_CONSENT_COOKIE", "concord_consent=1"))
    return _ORIGINAL_REQUEST(*args, **kwargs)


urllib.request.Request = _consented_request

BASE = os.environ.get("CANDIDATE", "http://127.0.0.1:8108")
SITE = os.environ.get("SITE_BASE", "https://concord.polarisocial.xyz")

ATOM = "{http://www.w3.org/2005/Atom}"


def fetch(path):
    request = urllib.request.Request(BASE + path)
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            return response.status, response.headers.get("Content-Type", ""), response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.headers.get("Content-Type", ""), error.read()


def main():
    import harness

    con = harness.connect("unused")
    now = time.time()
    stamp = int(now * 1000) % 100000000
    slug = f"probe-s5-feed-{stamp}"
    private_slug = f"probe-s5-feed-private-{stamp}"
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Feed probe", "A project with **markup** in its description.", "collective", "public",
         "both", now, now),
    ).lastrowid
    private_id = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (private_slug, "Feed probe private", "", "collective", "private", "both", now, now),
    ).lastrowid
    # One complaint, so the project feed has an item and an author to resolve.
    author = con.execute(
        "INSERT INTO users (username, display_name, created_at) VALUES (?,?,?) RETURNING id",
        (f"feedprobe{stamp}", "Feed Probe Author", now),
    ).lastrowid
    con.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, severity, frequency,"
        " strategic_multiplier, status, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)",
        (project, author, "The export button loses <angle brackets>", "# Problem\n\nIt **breaks** on `&` signs.",
         2, 1.0, 1.0, "validated", now, now),
    )
    con.commit()
    con.close()

    results = []

    def check(name, got, want):
        results.append((name, got, want))

    # --- every declared route answers and parses ------------------------------------------------
    routes = [
        ("/feed.xml", "rss"), ("/rss.xml", "rss"), ("/atom.xml", "atom"),
        (f"/projects/{slug}/feed.xml", "rss"), (f"/projects/{slug}/rss.xml", "rss"),
        (f"/projects/{slug}/atom.xml", "atom"),
        (f"/projects/{slug}/board/feed.xml", "rss"), (f"/projects/{slug}/board/atom.xml", "atom"),
        (f"/projects/{slug}/milestones/feed.xml", "rss"),
        (f"/projects/{slug}/consensus/feed.xml", "rss"), (f"/projects/{slug}/consensus/atom.xml", "atom"),
        (f"/projects/{slug}/documents/feed.xml", "rss"), (f"/projects/{slug}/documents/atom.xml", "atom"),
    ]
    parsed = {}
    for path, want_format in routes:
        status, content_type, body = fetch(path)
        root = None
        if status == 200:
            try:
                root = ET.fromstring(body).tag
            except ET.ParseError as error:
                root = f"PARSE ERROR: {error}"
        check(f"{path} answers 200", status, 200)
        check(
            f"{path} is {want_format}",
            "atom" if (root or "").endswith("feed") else ("rss" if (root or "").endswith("rss") else root),
            want_format,
        )
        check(
            f"{path} content type",
            content_type,
            "application/atom+xml; charset=utf-8" if want_format == "atom"
            else "application/rss+xml; charset=utf-8",
        )
        parsed[path] = (status, body)

    # --- the parameter beats the suffix ---------------------------------------------------------
    status, content_type, body = fetch(f"/projects/{slug}/feed.xml?format=atom")
    check("?format=atom overrides /feed.xml", content_type.split(";")[0], "application/atom+xml")
    status, content_type, body = fetch(f"/projects/{slug}/atom.xml?format=rss")
    check("?format=rss overrides /atom.xml", content_type.split(";")[0], "application/rss+xml")

    # --- an unknown format is refused ----------------------------------------------------------
    status, _content_type, _body = fetch(f"/projects/{slug}/feed.xml?format=pdf")
    check("?format=pdf is a 400", status, 400)

    # --- items are absolute, and a private project is a 404 ------------------------------------
    _status, body = parsed[f"/projects/{slug}/feed.xml"]
    items = ET.fromstring(body).findall("./channel/item")
    check("the project feed carries its complaint", len(items), 1)
    if items:
        check("the item id is absolute", items[0].findtext("guid", "").startswith(SITE + "/"), True)
        summary = items[0].findtext("description", "") or ""
        check("markup is stripped from the summary", "**" in summary or "`" in summary, False)
        # The TITLE carries the angle brackets, and the summary is the body: a document that survives `&` in the
        # body and `<` in the title has proved both halves of escaping.
        check(
            "angle brackets survive in the title",
            items[0].findtext("title", ""),
            "The export button loses <angle brackets>",
        )
        check("an ampersand survives in the summary", "&" in summary, True)
        check("the author is a name, not an id", items[0].findtext("author", ""), "Feed Probe Author")

    status, _content_type, body = fetch(f"/projects/{private_slug}/feed.xml")
    status_missing, _c, _b = fetch("/projects/no-such-project-at-all/feed.xml")
    check("a private project's feed is a 404", status, 404)
    check("and indistinguishable from a missing one", status, status_missing)

    # --- the public feed does not carry a private project ---------------------------------------
    _status, _c, public_body = fetch("/feed.xml")
    public_ids = [item.findtext("guid", "") for item in ET.fromstring(public_body).findall("./channel/item")]
    check(
        "the public feed carries no private project",
        any(private_slug in (item or "") for item in public_ids),
        False,
    )

    # --- an empty feed is stable ---------------------------------------------------------------
    first = parsed[f"/projects/{slug}/consensus/feed.xml"][1]
    _status, _c, second = fetch(f"/projects/{slug}/consensus/feed.xml")
    first_updated = ET.fromstring(first).findtext("./channel/pubDate")
    second_updated = ET.fromstring(second).findtext("./channel/pubDate")
    check("an empty feed's timestamp does not move", first_updated, second_updated)
    check("an empty feed has no items", len(ET.fromstring(first).findall("./channel/item")), 0)

    # --- no tally anywhere in the consensus feed -----------------------------------------------
    _status, _c, consensus_body = fetch(f"/projects/{slug}/consensus/feed.xml?format=atom")
    text = consensus_body.decode("utf-8", "replace")
    check("the consensus feed carries no tally", "tally" in text.lower() or "consent_count" in text, False)

    # --- the instance feeds --------------------------------------------------------------------
    for path in ("/feed.xml", "/atom.xml", "/feed.xml?kind=stats"):
        status, _content_type, body = fetch(path)
        check(f"{path} parses", status, 200)
        if status == 200:
            try:
                ET.fromstring(body)
                check(f"{path} is well formed", True, True)
            except ET.ParseError as error:
                check(f"{path} is well formed", str(error), True)

    # --- clean up ------------------------------------------------------------------------------
    con = harness.connect("unused")
    con.execute("DELETE FROM complaints WHERE project_id = ?", (project,))
    con.execute("DELETE FROM projects WHERE id IN (?, ?)", (project, private_id))
    con.execute("DELETE FROM users WHERE id = ?", (author,))
    con.commit()
    con.close()

    failures = 0
    for name, got, want in results:
        ok = got == want
        if not ok:
            failures += 1
        print(f"{'ok  ' if ok else 'FAIL'} {name}: got {got!r} want {want!r}")
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

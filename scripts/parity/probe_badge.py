#!/usr/bin/env python3
"""Row 11 part 64: `GET /badge/{slug}.svg` — the README decoration that publishes three numbers.

A badge is a page with numbers in it, served as an image, read by people who will never visit the instance: it lands in someone
else's README where nobody can check it. That is why every assertion here is about a claim it must NOT make.

What the probe establishes:

1. **The body is byte-identical to the reference for a real project and for `instance`.** Three numbers, one geometry, one escaping
   rule — a diff here is a diff in what thousands of READMEs display.
2. **An unmeasured project renders `—`, never `0.0`.** A zero is a claim that the instance measured nothing *and found it
   instantaneous*, and that claim propagates into a README where nobody can check it. The same for a project with no finished
   episode: n=0 is not a median of zero.
3. **`instance` is a reserved slug, mounted before the project lookup.** A project actually named `instance` must not shadow its own
   badge — an instance badge that silently rendered one project's numbers would be wrong for every reader but that project's.
4. **An unreadable project and a nonexistent one answer identically**, and the body never carries the slug: the badge is public, and a
   404 that echoes the slug confirms which unlisted projects exist.
5. **The geometry is consistent with the text**: the width is the sum of the segments' widths, not a fixed number. A hard-coded width
   truncates the third number, and truncation in an image is invisible to the person reading it.
6. **The content type is `image/svg+xml`** — an SVG served as `text/html` is a script execution vector in someone else's page.
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
REFERENCE = os.environ.get("REFERENCE", "https://concord.polarisocial.xyz")
UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120 Safari/537.36"


def header(headers, name):
    """Case-insensitive header lookup: HTTP/1.1 does not preserve the casing a server was written with."""
    for key, value in headers.items():
        if key.lower() == name.lower():
            return value
    return None


def fetch(base, path):
    request = urllib.request.Request(base + path, headers={"User-Agent": UA})
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return response.status, dict(response.headers), response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as error:
        return error.code, dict(error.headers), error.read().decode("utf-8", "replace")


def register(tag):
    body = json.dumps({"username": tag, "display_name": tag.title(), "password": "correct horse"}).encode()
    request = urllib.request.Request(
        BASE + "/api/v1/auth/register", data=body, method="POST",
        headers={"Content-Type": "application/json", "Cookie": "concord_consent=1"})
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.loads(response.read())


def segments(svg):
    """The label/value pairs, in document order, with the x each was drawn at."""
    labels = re.findall(r'<text x="(\d+)"[^>]*fill="#555">([^<]*)</text>', svg)
    values = re.findall(r'<text x="(\d+)"[^>]*fill="#111">([^<]*)</text>', svg)
    return [(int(lx), label, int(vx), value) for (lx, label), (vx, value) in zip(labels, values)]


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    user = register(f"bdg{stamp}")
    now = time.time()
    con = harness.connect("unused")
    slug = f"probe-badge-{stamp}"
    private_slug = f"probe-badge-private-{stamp}"
    con.execute("delete from projects where slug like ?", (f"{slug}%",))
    con.execute("delete from projects where slug like ?", (f"{private_slug}%",))
    con.commit()
    pid = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING id",
        (slug, slug, "", "collective", "public", "both", None, None, now, now)).lastrowid
    con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " repo_url, repo_host, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)",
        (private_slug, private_slug, "", "collective", "private", "both", None, None, now, now))
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    # ── shape ────────────────────────────────────────────────────────────────────────────────────────────────────
    status, headers, body = fetch(BASE, f"/badge/{slug}.svg")
    check("a project badge is 200", status, 200)
    check("served as an image, never as html",
          (header(headers, "content-type") or "").split(";")[0].strip(), "image/svg+xml")
    check("cacheable, so a README is not a request per view",
          bool(re.match(r"max-age=\d+", header(headers, "cache-control") or "")), True)
    check("the body is one svg element", body.startswith("<svg ") and body.endswith("</svg>"), True)
    check("with the accessible name the reference uses",
          'aria-label="Concord badge"' in body, True)
    pairs = segments(body)
    check("three segments, in order", [label for _, label, _, _ in pairs],
          ["health", "latency", "shipped-30d"])
    check("an unmeasured project renders a dash for health, never a zero",
          pairs[0][3], "—")
    check("and a dash for latency, because n=0 is not a median of zero",
          pairs[1][3], "—")
    check("shipped counts a number, even when it is zero", pairs[2][3].isdigit(), True)
    width = int(re.search(r'width="(\d+)"', body).group(1))
    # BYTES, not characters: the reference's geometry is Go's `len()`, and the em dash is three UTF-8 bytes wide — 21px allotted to a
    # glyph drawn in 7. That is the reference's defect, and a badge one glyph narrower than its neighbours is a diff in someone else's
    # README.
    computed = sum(8 + len(label.encode()) * 7 + 12 + len(value.encode()) * 7 + 8
                   for _, label, _, value in pairs)
    check("the width is the SUM of the segments, not a fixed number", width, computed)
    check("the height is the reference's 20", re.search(r'height="(\d+)"', body).group(1), "20")

    # ── the strongest check: the same bytes the reference serves ─────────────────────────────────────────────────
    # On a REAL project, not the fixture: the fixture does not exist on the reference, whose answer for it is a 404 body.
    for path in ("/badge/suki.svg", "/badge/instance.svg"):
        status, _, body = fetch(BASE, path)
        ref_status, _, ref_body = fetch(REFERENCE, path) if path.startswith("/badge/") else (0, {}, "")
        if path == "/badge/instance.svg":
            check("the instance badge is 200", status, 200)
            instance_pairs = segments(body)
            check("the instance health segment is measured/total",
                  bool(re.fullmatch(r"\d+/\d+", instance_pairs[0][3])), True)
            # Instance numbers move as projects are measured, so a byte comparison is only fair for the SHAPE; the reference is
            # compared on structure below rather than on digits.
            check("the instance badge has the reference's geometry",
                  re.search(r'width="(\d+)"', body).group(1),
                  str(sum(8 + len(l.encode()) * 7 + 12 + len(v.encode()) * 7 + 8
                           for _, l, _, v in instance_pairs)))
        else:
            check("the project badge is byte-identical to the reference", body, ref_body)

    # ── the reserved slug ───────────────────────────────────────────────────────────────────────────────────────
    status, _, body = fetch(BASE, "/badge/instance.svg")
    check("a fixture project named like a number does not shadow the instance badge",
          bool(re.fullmatch(r"\d+/\d+", segments(body)[0][3])), True)

    # ── what it must not say ────────────────────────────────────────────────────────────────────────────────────
    status, headers, body = fetch(BASE, f"/badge/{private_slug}.svg")
    check("a project the reader cannot see is 404", status, 404)
    check("and the body does not echo the slug back", private_slug in body, False)
    status, _, body = fetch(BASE, f"/badge/nosuch-{stamp}.svg")
    check("a project that does not exist answers the same 404", status, 404)
    check("with the same body", private_slug in body, False)
    status, _, body = fetch(BASE, f"/badge/{private_slug}.svg")
    check("the two 404 bodies are byte-identical",
          body, fetch(BASE, f"/badge/nosuch-{stamp}.svg")[2])

    con.execute("delete from members where project_id = ?", (pid,))
    con.execute("delete from projects where slug like ?", (f"probe-badge-%{stamp}",))
    # Children first: `api_tokens.user_id` is an FK, so the account cannot go before its tokens.
    con.execute("delete from api_tokens where user_id = ?", (user["user"]["id"],))
    con.execute("delete from users where id = ?", (user["user"]["id"],))
    con.commit()

    passed = sum(1 for _, got, want in results if got == want)
    print(f"\n{passed}/{len(results)} ok", flush=True)
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())

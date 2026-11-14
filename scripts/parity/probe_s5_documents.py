#!/usr/bin/env python3
"""Row 11d part 54 (B7): project documents and threads.

Two surfaces, and one of them was already ported but unproven — `routes.py` reported the thread routes as missing because a
comment sat between `.route(` and its path and the inventory's regex demanded the string immediately after the paren. The
routes existed; nothing had ever called them. Fixed the inventory and wrote the probe, in that order, because a count that
overstates the missing work is as wrong as one that understates it.

Documents (§8.3's second reputation source, and the only place the port raises its body limit):

1. **writing needs the contributor role**, and reading a private project's documents needs access — the visibility check runs
   before the write, because a private project's spec is exactly the content that must not leak;
2. **`put` is an upsert that bumps `revision`**, not a replace: the row keeps its id, so `/documents/41` and every comment
   threaded on it keep resolving, and `created_at` stays where it was;
3. **`kind` is enumerated**, because it is the first half of the document's address — a typo makes a document no page lists;
4. **deleting needs the maintainer role**, not a contributor, and is scoped to the project in the URL.

Threads:

5. **an unknown kind is a 400, not an empty list** — "no comments" and "not a thread kind" are different answers;
6. **a comment cannot be filed in another project's thread**, and a reply cannot point at a parent in another thread. Both are
   guards the schema cannot provide: `thread_id` has no foreign key, and `parent_id` is a real FK that does not know about
   threads.
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


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {}
    if data is not None:
        headers["Content-Type"] = "application/json"
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(BASE + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            raw = response.read()
            return response.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw) if raw else None
        except ValueError:
            return error.code, raw.decode("utf-8", "replace")


def register(tag):
    status, body = call(
        "POST", "/api/v1/auth/register",
        {"username": tag, "display_name": tag.title(), "password": "correct horse"},
    )
    if status != 201:
        raise SystemExit(f"register {tag} failed: {status} {body}")
    return body["user"]["id"], body["token"]


def cleanup(con, like):
    projects = [row[0] for row in con.execute("select id from projects where slug like ?", (like,)).fetchall()]
    if not projects:
        return
    marks = ",".join("?" * len(projects))
    params = tuple(projects)
    features = [row[0] for row in con.execute(
        f"select id from features where project_id in ({marks})", params).fetchall()]
    if features:
        fmarks = ",".join("?" * len(features))
        con.execute(f"delete from comments where thread_kind = 'feature' and thread_id in ({fmarks})",
                    tuple(features))
    con.execute(f"delete from comments where project_id in ({marks})", params)
    con.execute(f"delete from project_documents where project_id in ({marks})", params)
    # Features before projects: `fk_features_project_id` is a real foreign key, so a cleanup that removes the project
    # first fails at the END of an otherwise green run.
    con.execute(f"delete from features where project_id in ({marks})", params)
    con.execute(f"delete from audit_log where project_id in ({marks})", params)
    con.execute(f"delete from members where project_id in ({marks})", params)
    con.execute(f"delete from projects where id in ({marks})", params)
    con.commit()


def main():
    import harness

    stamp = int(time.time() * 1000) % 100000000
    contributor_id, contributor_token = register(f"dwriter{stamp}")
    maintainer_id, maintainer_token = register(f"dmaint{stamp}")
    plain_id, plain_token = register(f"dplain{stamp}")
    outsider_id, outsider_token = register(f"douter{stamp}")
    now = time.time()

    con = harness.connect("unused")
    slug = f"probe-docs-{stamp}"
    cleanup(con, f"{slug}%")
    project = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (slug, "Docs probe", "", "collective", "public", "both", now, now),
    ).lastrowid
    private = con.execute(
        "INSERT INTO projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) RETURNING id",
        (f"{slug}-private", "Secret", "", "collective", "private", "both", now, now),
    ).lastrowid
    for user, role in ((contributor_id, "contributor"), (maintainer_id, "maintainer"),
                       (plain_id, "user")):
        con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                    (project, user, role, now))
    # `outsider` is a member of the PRIVATE project only: that is what makes "a member of the private project can
    # write there" a real claim rather than a 404 dressed up as a pass.
    con.execute("INSERT INTO members (project_id, user_id, role, joined_at) VALUES (?,?,?,?)",
                (private, outsider_id, "contributor", now))
    feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (project, contributor_id, "Feature", "", "draft", now, now),
    ).lastrowid
    other_feature = con.execute(
        "INSERT INTO features (project_id, author_id, title, body, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?) RETURNING id",
        (private, outsider_id, "Elsewhere", "", "draft", now, now),
    ).lastrowid
    con.commit()

    results = []

    def check(name, got, want):
        results.append((name, got, want))
        print(f"{'ok  ' if got == want else 'FAIL'} {name}: got {got!r} want {want!r}", flush=True)

    docs = f"/api/v1/projects/{slug}/documents"

    # ── who may write ───────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("PUT", docs, {"kind": "readme", "slug": "intro", "body": "x"})
    check("writing anonymously is 401", status, 401)
    status, _ = call("PUT", docs, {"kind": "readme", "slug": "intro", "body": "x"},
                     token=outsider_token)
    check("a non-member is 403", status, 403)
    status, _ = call("PUT", docs, {"kind": "readme", "slug": "intro", "body": "x"},
                     token=plain_token)
    check("a `user`-role member is 403 — a document is contributor work", status, 403)
    # `plain` is a member of the public project and nothing else, so this is a genuine outsider to the private one.
    status, _ = call("PUT", f"/api/v1/projects/{slug}-private/documents",
                     {"kind": "spec", "slug": "secret", "body": "x"}, token=plain_token)
    check("an outsider cannot write into a private project (404, not 403)",
          status, 404)

    # ── the upsert ──────────────────────────────────────────────────────────────────────────────────────────────
    status, doc = call("PUT", docs, {"kind": " SPEC ", "slug": " architecture ", "body": "first"},
                       token=contributor_token)
    check("a contributor may write", status, 200)
    doc = doc or {}
    check("the kind is trimmed and lowercased", doc.get("kind"), "spec")
    check("the slug is trimmed", doc.get("slug"), "architecture")
    check("an empty title falls back to the slug", doc.get("title"), "architecture")
    check("the first write is revision 1", doc.get("revision"), 1)
    first_id = doc.get("id")
    created = doc.get("created_at")

    status, again = call("PUT", docs, {"kind": "spec", "slug": "architecture",
                                       "title": "Architecture", "body": "second"},
                         token=maintainer_token)
    again = again or {}
    check("the second write is revision 2", again.get("revision"), 2)
    check("it is the SAME row, not a new document", again.get("id"), first_id)
    check("created_at did not move", again.get("created_at"), created)
    check("the body was replaced", again.get("body"), "second")
    check("and the title with it", again.get("title"), "Architecture")
    check("so there is one document at that address, not two",
          con.execute("SELECT count(*) FROM project_documents WHERE project_id = ? AND kind = 'spec'"
                      " AND slug = 'architecture'", (project,)).fetchone()[0], 1)

    status, other = call("PUT", docs, {"kind": "plan", "slug": "architecture", "body": "p"},
                         token=contributor_token)
    check("the same slug under another kind is a separate document",
          (status, (other or {}).get("revision"), (other or {}).get("id") != first_id), (200, 1, True))

    status, body = call("PUT", docs, {"kind": "nonsense", "slug": "x", "body": ""},
                        token=contributor_token)
    check("an unknown kind is 400", status, 400)
    check("and the refusal lists the kinds",
          all(kind in json.dumps(body or {}) for kind in ("readme", "spec", "plan", "adr")), True)
    status, _ = call("PUT", docs, {"kind": "spec", "slug": "   ", "body": "x"},
                     token=contributor_token)
    check("an empty slug is 400", status, 400)

    status, listed = call("GET", docs)
    check("the list holds every document", len(listed or []) >= 2, True)
    check("ordered by kind then slug",
          [row["slug"] for row in (listed or [])][:2] if listed else None, ["architecture", "architecture"])

    # ── deletion ────────────────────────────────────────────────────────────────────────────────────────────────
    status, _ = call("DELETE", f"{docs}/{first_id}")
    check("deleting anonymously is 401", status, 401)
    status, _ = call("DELETE", f"{docs}/{first_id}", token=contributor_token)
    check("a contributor cannot delete a document", status, 403)
    status, body = call("DELETE", f"{docs}/{first_id}", token=maintainer_token)
    check("a maintainer may", (status, (body or {}).get("status")), (200, "deleted"))
    check("the row is gone",
          con.execute("SELECT count(*) FROM project_documents WHERE id = ?", (first_id,)).fetchone()[0], 0)
    status, _ = call("GET", f"{docs}/{first_id}")
    check("and it reads as gone", status, 404)
    status, _ = call("DELETE", f"{docs}/{first_id}", token=maintainer_token)
    check("deleting it twice is 404", status, 404)
    status, _ = call("DELETE", f"{docs}/notanumber", token=maintainer_token)
    check("a non-integer id is 400", status, 400)

    # A document of another project, named under this project's URL.
    status, foreign = call("PUT", f"/api/v1/projects/{slug}-private/documents",
                           {"kind": "adr", "slug": "1-foreign", "body": "f"},
                           token=outsider_token)
    check("a member of the private project can write there", status, 200)
    status, _ = call("DELETE", f"{docs}/{(foreign or {}).get('id')}", token=maintainer_token)
    check("another project's document cannot be deleted under this URL", status, 404)
    check("and it survives",
          con.execute("SELECT count(*) FROM project_documents WHERE id = ?",
                      ((foreign or {}).get("id"),)).fetchone()[0], 1)

    # ── threads ────────────────────────────────────────────────────────────────────────────────────────────────
    thread = f"/api/v1/projects/{project}/threads/feature/{feature}"
    status, empty = call("GET", thread)
    check("an empty thread is []", (status, empty), (200, []))
    # Asymmetric on purpose, and the reference is asymmetric the same way: the WRITE path refuses `release` (there is
    # no releases table to thread on) while the READ path answers [] for it. Asserted as it IS, and noted, because a
    # probe that "fixed" this would be inventing a rule the Go service does not have.
    status, _ = call("GET", f"/api/v1/projects/{project}/threads/release/{feature}")
    check("reading a `release` thread is an empty list — the reference's own asymmetry", status, 200)
    status, _ = call("POST", f"/api/v1/projects/{project}/threads/release/{feature}",
                     {"body": "x"}, token=contributor_token)
    check("but writing one is refused", status, 400)
    status, _ = call("GET", f"/api/v1/projects/{project}/threads/nonsense/{feature}")
    check("an unknown kind is 400, not an empty list", status, 400)

    status, _ = call("POST", thread, {"body": "hello"})
    check("commenting anonymously is 401", status, 401)
    status, _ = call("POST", thread, {"body": "   "}, token=contributor_token)
    check("a blank body is 400", status, 400)
    status, _ = call("POST", thread, {"body": "hello", "label": "nonsense"}, token=contributor_token)
    check("an unknown label is 400", status, 400)
    status, first = call("POST", thread, {"body": "hello", "label": "question"},
                         token=contributor_token)
    check("a contributor may comment", status, 201)
    first = first or {}
    check("the comment records its thread",
          (first.get("thread_kind"), first.get("thread_id")), ("feature", feature))
    check("its score starts at zero and its label is kept",
          (first.get("score"), first.get("label")), (0.0, "question"))

    status, _ = call("POST", thread, {"body": "reply", "parent_id": first.get("id")},
                     token=maintainer_token)
    check("a reply in the same thread is fine", status, 201)

    # A parent that exists but sits on another thread: the FK is satisfied, the thread is not.
    status, stray = call("POST", f"/api/v1/projects/{private}/threads/feature/{other_feature}",
                         {"body": "elsewhere"}, token=outsider_token)
    check("a comment on the private project's feature", status, 201)
    status, _ = call("POST", thread, {"body": "cross", "parent_id": (stray or {}).get("id")},
                     token=contributor_token)
    check("a reply cannot point at a parent from another thread", status, 400)

    status, _ = call("POST", f"/api/v1/projects/{project}/threads/feature/{other_feature}",
                     {"body": "wrong project"}, token=contributor_token)
    check("a comment cannot be filed in another project's thread", status, 400)

    status, thread_rows = call("GET", thread)
    check("the thread reads back oldest first",
          [row["body"] for row in (thread_rows or [])], ["hello", "reply"])

    cleanup(con, f"{slug}%")
    con.commit()
    con.close()
    for user in (contributor_id, maintainer_id, plain_id, outsider_id):
        c = harness.connect("unused")
        c.execute("DELETE FROM api_tokens WHERE user_id = ?", (user,))
        c.execute("DELETE FROM users WHERE id = ?", (user,))
        c.commit()
        c.close()

    failures = sum(1 for _, got, want in results if got != want)
    print(f"\n{len(results) - failures}/{len(results)} ok")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())

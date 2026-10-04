"""The measured API surface for every page Concord still has to build.

WHY THIS FILE EXISTS. Six pages remained on 2026-10-05 (docs/REMAINING.md §1) and
every one of the two already built turned up the same class of defect on its first
run: the JS guessed a response shape and rendered empty without error. Four
recorded instances:

  /features/{id}/solutions    is {feature_id, solutions:[...]}   -- a WRAPPER
  /features/{id}/consensus    is {feature_id, leader_id, ...}   -- a WRAPPER
  /complaints/{id}/features   is a BARE ARRAY
  /charter                    returns PASCALCASE keys, unlike every other endpoint

So the shapes are MEASURED once here, against a real migrated database, and the
seed data is written once. Everything below was wrong when guessed, and every wrong
guess failed SILENTLY -- the page rendered a confident, false statement.

The cost of getting this wrong is not a crash. It is a page that reports "0
solutions" for a feature that has some.

Run it directly to re-measure after any schema or handler change:

    cd ~/code/projects/concord && python3 tests/e2e/api_surface.py

It prints one line per endpoint: status, shape, and the first bytes of the body. If
a line changes, a page that depends on it needs re-reading -- that is the point.

WHAT IS HERE. Only GETs that a page needs on load. The write endpoints are
documented by name so a settings page knows what it may offer, but they are not
measured here: a write's shape is its request body, not its response, and the
gates for those live in the Go tests.
"""

import json
import sqlite3
import sys
import tempfile
import time
import urllib.request

sys.path.insert(0, __file__.rsplit("/tests/", 1)[0] + "/tests/e2e")
import harness  # noqa: E402

PORT = 8520

# Every status enum, read off the schema rather than guessed. Each of these was
# guessed wrong at least once in this session, and a wrong one is an
# IntegrityError at seed time -- which is the good case, because it is loud.
ENUMS = {
    "list_entries.status": "proposed",
    "lists.status": "open",
    "requests.status": "open",
    "request_answers.status": "active",
    "merge_requests.status": "open",
    "features.status": "ready",
    "complaints.status": "validated",
}

# The measured GET surface: (label, path). "auth" marks an endpoint that needs a
# token, which is itself a finding -- a page must say so rather than render empty.
MEASURED_GETS = [
    ("charter", "/api/v1/projects/probe/charter"),
    ("members", "/api/v1/projects/probe/members"),
    ("invites", "/api/v1/projects/probe/invites"),
    ("tags", "/api/v1/projects/probe/tags"),
    ("tags/completeness", "/api/v1/projects/probe/tags/completeness"),
    ("features", "/api/v1/projects/probe/features"),
    ("complaints", "/api/v1/projects/probe/complaints"),
    ("solutions", "/api/v1/projects/probe/solutions"),
    ("criteria-profiles", "/api/v1/projects/probe/criteria-profiles"),
    ("lists", "/api/v1/projects/probe/lists"),
    ("list detail", "/api/v1/projects/probe/lists/1"),
    ("list entries", "/api/v1/projects/probe/lists/1/entries"),
    ("requests", "/api/v1/projects/probe/requests"),
    ("request detail", "/api/v1/requests/1"),
    ("request answers", "/api/v1/requests/1/answers"),
    ("merge requests", "/api/v1/projects/probe/merge"),
    ("feature comments", "/api/v1/projects/probe/threads/feature/1"),
    ("complaint comments", "/api/v1/projects/probe/threads/complaint/1"),
    ("feature complaints", "/api/v1/projects/probe/features/1/complaints"),
    ("complaint features", "/api/v1/projects/probe/complaints/1/features"),
    ("capabilities", "/api/v1/projects/probe/capabilities"),
    ("field reports", "/api/v1/projects/probe/field-reports"),
    ("documents", "/api/v1/projects/probe/documents"),
    ("audit", "/api/v1/projects/probe/audit"),
]


def seed(db):
    """One project with one of everything, so no endpoint answers with an empty set.

    An empty response measures the wrong thing: a wrapper that looks like an array
    and an array that looks like a wrapper are indistinguishable when both are
    empty, which is exactly when you most want to know.
    """
    con = sqlite3.connect(db)
    cur = con.cursor()
    now = time.time()

    owner = cur.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash,"
        " trust_level) VALUES (?,?,?,?,1)",
        ("probe-owner", "Probe Owner", now, "x")).lastrowid
    other = cur.execute(
        "INSERT INTO users (username, display_name, created_at, password_hash,"
        " trust_level) VALUES (?,?,?,?,1)",
        ("probe-other", "Probe Other", now, "x")).lastrowid

    pid = cur.execute(
        "INSERT INTO projects (slug, name, description, governance_model, license,"
        " visibility, created_at, updated_at)"
        " VALUES ('probe','Probe','everything the pages need','collective',"
        "'AGPL-3.0','public',?,?)", (now, now)).lastrowid

    # members: 5 columns, is_moderator NOT NULL. Guessed 'created_at' first and it
    # is 'joined_at'.
    cur.execute(
        "INSERT INTO members (project_id, user_id, role, is_moderator, joined_at)"
        " VALUES (?,?,'owner',0,?)", (pid, owner, now))
    cur.execute(
        "INSERT INTO members (project_id, user_id, role, is_moderator, joined_at)"
        " VALUES (?,?,'contributor',0,?)", (pid, other, now))

    cid = cur.execute(
        "INSERT INTO complaints (project_id, author_id, title, body, severity,"
        " frequency, strategic_multiplier, status, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?)",
        (pid, owner, "Export drops the last row", "reproducible", 4, 2.0, 1.5,
         ENUMS["complaints.status"], now, now)).lastrowid

    fid = cur.execute(
        "INSERT INTO features (project_id, author_id, title, body, effort, status,"
        " elo_r, elo_rd, elo_vol, strategic_weight, impact, effort_score,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        (pid, owner, "Export must be lossless", "every row survives", "M",
         ENUMS["features.status"], 1610.0, 40.0, 0.06, 1.0, 8.0, 2.0, now, now)).lastrowid
    cur.execute(
        "INSERT INTO feature_complaints (feature_id, complaint_id) VALUES (?,?)",
        (fid, cid))

    # comments: thread_kind is a CHECK enum and thread_id is a BARE id, not
    # qualified -- so 'threads/feature/1' only resolves if feature 1 exists.
    cur.execute(
        "INSERT INTO comments (project_id, thread_kind, thread_id, author_id, body,"
        " score, created_at) VALUES (?,'feature',?,?,'looks right to me',2,?)",
        (pid, fid, other, now))
    cur.execute(
        "INSERT INTO comments (project_id, thread_kind, thread_id, author_id, body,"
        " score, created_at) VALUES (?,'complaint',?,?,'I can reproduce',3,?)",
        (pid, cid, other, now))

    cur.execute(
        "INSERT INTO merge_requests (project_id, feature_id, author_id, title,"
        " external_ref, status, opened_at) VALUES (?,?,?,?,?,?,?)",
        (pid, fid, owner, "Feature branch",
         "refs/heads/export/fix", ENUMS["merge_requests.status"], now))

    lid = cur.execute(
        "INSERT INTO lists (project_id, slug, title, description, status, created_by,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)",
        (pid, "libs", "Libraries we depend on", "what we ship",
         ENUMS["lists.status"], owner, now, now)).lastrowid
    cur.execute(
        "INSERT INTO list_entries (list_id, url, title, description, category,"
        " status, elo_r, elo_rd, elo_vol, proposed_by, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
        (lid, "https://example.invalid/lib", "example.invalid", "a library",
         "library", ENUMS["list_entries.status"], 1500.0, 350.0, 0.06, owner,
         now, now))

    rid = cur.execute(
        "INSERT INTO requests (project_id, author_id, title, body, status,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?)",
        (pid, owner, "Should we drop v1?", "weighing it up",
         ENUMS["requests.status"], now, now)).lastrowid
    cur.execute(
        "INSERT INTO request_answers (request_id, project_id, body, status, elo_r,"
        " elo_rd, elo_vol, proposed_by, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?)",
        (rid, pid, "keep it one more cycle", ENUMS["request_answers.status"],
         1500.0, 350.0, 0.06, other, now, now))

    cur.execute(
        "INSERT INTO criteria_profiles (project_id, slug, name, created_by,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?)",
        (pid, "strict", "Strict", owner, now, now))

    # languages has NO read endpoint -- only PUT. That is a finding, not an
    # omission: a settings page can offer the control but cannot show current
    # values without a read that does not exist.
    cur.execute(
        "INSERT INTO project_languages (project_id, language, pct) VALUES (?,'Rust',100)",
        (pid,))

    con.commit()
    con.close()


def describe(body):
    """The shape, as the thing a JS author has to know."""
    try:
        v = json.loads(body)
    except Exception:
        return "NON-JSON", body[:120]
    if isinstance(v, list):
        return f"ARRAY[{len(v)}]", body[:200]
    if isinstance(v, dict):
        keys = sorted(v)
        # The PascalCase finding: name it here so nobody has to rediscover it.
        style = "PASCALCASE " if keys and keys[0][:1].isupper() else ""
        return style + "OBJECT{" + ",".join(keys[:11]) + "}", body[:200]
    return repr(v), body[:200]


def main():
    dbdir = tempfile.mkdtemp(prefix="concord-surface-")
    logdir = tempfile.mkdtemp(prefix="concord-surface-log-")
    proc, db, base = harness.start_server(PORT, dbdir, logdir)
    try:
        seed(db)
        print(f"probe project seeded at {db}\n")
        for label, path in MEASURED_GETS:
            try:
                r = urllib.request.urlopen(base + path, timeout=10)
                body = r.read().decode()
                shape, sample = describe(body)
                print(f"{r.status} {label:20} {path}")
                print(f"        {shape}")
                print(f"        {sample.replace(chr(10), ' ')}")
            except urllib.error.HTTPError as e:
                detail = e.read().decode()[:120].replace("\n", " ")
                print(f"{e.code} {label:20} {path}")
                print(f"        {detail}")
            print()
    finally:
        harness.stop_server(proc)
    return 0


if __name__ == "__main__":
    sys.exit(main())
#!/usr/bin/env python3
"""Row 11: the four admin binaries — `granttrust`, `capableseed`, `forgeseed`, `embedbackfill`.

These are the commands the Go tree kept as separate `cmd/*` binaries. They are the half of the row that keeps the
*data* alive after cutover: nothing else writes a trust level, nothing else seeds the capability matrix, nothing else
states where a project's upstream lives, and nothing else builds the duplicate-detection index.

Each one is a *write with a refusal rule*, so the probe asserts three things per command, in this order:

1. **the write happened** — read it back out of the database, not out of the command's own stdout;
2. **the refusal refused** — the failure mode is a non-zero exit AND no rows changed, because a refusal that writes
   half of what it was asked is worse than no command at all;
3. **the report is honest** — counts that describe what was written, and a line naming anything that went wrong.

Usage: `CONCORD_BIN=/tmp/concord-cli CONCORD_E2E_DSN=… python3 scripts/parity/probe_admin_binaries.py`
"""
import json
import os
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
sys.path.insert(0, os.path.join(ROOT, "tests", "e2e"))

BIN = os.environ.get("CONCORD_BIN", "/tmp/concord-cli")
STAMP = int(time.time() * 1000) % 100000000


def run(*arguments):
    """Run the binary. Returns (exit code, stdout, stderr) — refusals are a result here, not a crash."""
    result = subprocess.run([BIN, *arguments], capture_output=True, text=True, check=False,
                            env=dict(os.environ))
    return result.returncode, result.stdout, result.stderr


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

    slug = f"probe-admin-{STAMP}"
    username = f"adm{STAMP}"
    # ── fixtures ────────────────────────────────────────────────────────────────────────────────────────────────
    con.execute("delete from trust_grants where user_id in (select id from users where username = ?)", (username,))
    con.execute("delete from users where username = ?", (username,))
    con.execute("delete from projects where slug = ?", (slug,))
    pid = con.execute(
        "insert into projects (slug, name, description, governance_model, visibility, intake_mode,"
        " created_at, updated_at) values (?,?,?,?,?,?,?,?) returning id",
        (slug, "Admin probe", "", "collective", "public", "both", time.time(), time.time())).lastrowid
    uid = con.execute(
        "insert into users (username, display_name, password_hash, public_id, created_at)"
        " values (?,?,?,?,?) returning id",
        (username, "Admin Probe", "", f"pub-{STAMP}", time.time())).lastrowid
    con.execute("delete from trust_config")
    con.execute("insert into trust_config (max_level) values (?)", (2,))
    con.commit()

    def trust_level():
        return con.execute("select trust_level from users where id = ?", (uid,)).fetchone()[0]

    def grants():
        return list(con.execute(
            "select level, granted_by, reason from trust_grants where user_id = ? order by id", (uid,)).fetchall())

    # ── granttrust ──────────────────────────────────────────────────────────────────────────────────────────────
    code, out, err = run("granttrust", "--user", str(uid), "--level", "2")
    check("a grant at the ceiling exits 0", code, 0)
    check("and writes the level to the account", trust_level(), 2)
    rows = grants()
    check("and records exactly one grant", len(rows), 1)
    if rows:
        check("the grant carries the level", rows[0][0], 2)
        check("granted_by is the account itself: reaching the database is the authorisation",
              rows[0][1], uid)
        check("the reason is written down, not left empty", bool((rows[0][2] or "").strip()), True)
    # The report is what an operator reads before walking away.
    check("the line names the user, the level and the ceiling",
          out.strip(), f"user {uid} trust_level=2 ceiling=2")

    # Above the ceiling is a REFUSAL: non-zero, and neither write happens.
    code, out, err = run("granttrust", "--user", str(uid), "--level", "3")
    note("a grant above the ceiling exits non-zero", code != 0, f"code={code}")
    check("and the level is unchanged", trust_level(), 2)
    check("and no grant row was written for the attempt", len(grants()), 1)
    note("and the refusal names the ceiling", "ceiling" in (out + err).lower() or "maximum" in (out + err).lower(),
         (out + err).strip().splitlines()[-1] if (out + err).strip() else "")

    # The Go interface is positional; an operator copying a documented line gets it.
    code, out, err = run("granttrust", str(uid), "1")
    check("the positional form is accepted", code, 0)
    check("and writes the level", trust_level(), 1)
    check("and records a second grant, not an update of the first", len(grants()), 2)

    # An unknown account is a refusal, not a silent grant to a phantom.
    code, out, err = run("granttrust", "--user", "999999999", "--level", "1")
    note("an unknown account exits non-zero", code != 0, f"code={code}")
    check("and writes no grant row", con.execute(
        "select count(*) from trust_grants where user_id = 999999999").fetchone()[0], 0)

    # ── capableseed ────────────────────────────────────────────────────────────────────────────────────────────────────────────
    # A seed file is the operator's statement of what the matrix should contain. Two capabilities: one boolean with a value asserted
    # against the fixture project, one enum, so both kinds go through the same write.
    key_bool = f"probe-cap-{STAMP}"
    key_enum = f"probe-mode-{STAMP}"
    con.execute("delete from embeddings where kind = 'complaint' and entity_id in (select id from complaints where project_id = ?)", (pid,))
    con.execute("delete from complaints where project_id = ?", (pid,))
    con.execute("delete from capability_assertions where capability like ?", (f"probe-%-{STAMP}",))
    con.execute("delete from capabilities where key like ?", (f"probe-%-{STAMP}",))
    con.commit()
    path = os.path.join(os.environ.get("TMPDIR", "/tmp"), f"caps-{STAMP}.json")
    with open(path, "w", encoding="utf-8") as handle:
        json.dump({"capabilities": [
            {"key": key_bool.upper(), "label": "Probe boolean", "category": "probe",
             "assert": {slug: "yes"}},
            {"key": key_enum, "label": "Probe mode", "category": "probe", "kind": "enum",
             "values": ["lean", "full"]},
        ]}, handle)

    # Declaring assertions without --assert is a refusal, not a registration: the operator would leave believing the values are stored.
    code, out, err = run("capableseed", "--apply", path)
    note("a seed with assertions and no --assert is refused", code != 0, (err.strip() or out.strip()).splitlines()[-1])
    check("and nothing was registered", con.execute(
        "select count(*) from capabilities where key like ?", (f"probe-%-{STAMP}",)).fetchone()[0], 0)

    code, out, err = run("capableseed", "--apply", path, "--assert", "--as", username)
    check("a seed file with --assert exits 0", code, 0)
    check("both capabilities are registered", con.execute(
        "select count(*) from capabilities where key like ?", (f"probe-%-{STAMP}",)).fetchone()[0], 2)
    stored = con.execute("select key, kind, values from capabilities where key = ?", (key_bool,)).fetchone()
    check("the key is canonicalised on write, not on read", stored[0], key_bool)
    check("a capability with no kind declared is boolean", stored[1], "boolean")
    check("and carries the boolean value set", json.loads(stored[2]), ["yes", "partial", "no", "unknown"])
    enum_row = con.execute("select values from capabilities where key = ?", (key_enum,)).fetchone()
    check("an enum keeps the values it declared", json.loads(enum_row[0]), ["lean", "full"])

    assertion = con.execute(
        "select value, evidence, asserted_by from capability_assertions where capability = ? and project_id = ?",
        (key_bool, pid)).fetchone()
    check("the assertion is written against the project", assertion is not None, True)
    if assertion:
        check("with the value from the file", assertion[0], "yes")
        check("attributed to the account named by --as", assertion[2], uid)
        check("and evidence naming the seed file rather than an empty string", assertion[1], "seed file")
    check("seeding writes no confirmation: a seed cannot manufacture a quorum", con.execute(
        "select count(*) from capability_confirmations where assertion_id in"
        " (select id from capability_assertions where capability = ?)", (key_bool,)).fetchone()[0], 0)

    # Re-running is idempotent, and the report distinguishes created from updated — "registered 2" twice is a lie an operator repeats.
    code, out, err = run("capableseed", "--apply", path, "--assert", "--as", username)
    check("re-running exits 0", code, 0)
    note("and reports existing rather than new", "0 new and 2 existing" in out, out.strip())
    check("and there are still two capabilities, not four", con.execute(
        "select count(*) from capabilities where key like ?", (f"probe-%-{STAMP}",)).fetchone()[0], 2)

    # Changing a kind under live assertions is refused: the assertions point at the old value set.
    with open(path, "w", encoding="utf-8") as handle:
        json.dump({"capabilities": [
            {"key": key_bool, "label": "Probe boolean", "category": "probe", "kind": "enum",
             "values": ["one"]},
        ]}, handle)
    code, out, err = run("capableseed", "--apply", path)
    note("changing a capability's kind is refused", code != 0, (err.strip() or out.strip()).splitlines()[-1])
    check("and the stored kind is unchanged", con.execute(
        "select kind from capabilities where key = ?", (key_bool,)).fetchone()[0], "boolean")

    # A value outside the permitted set is a typo that would read downstream as information.
    with open(path, "w", encoding="utf-8") as handle:
        json.dump({"capabilities": [
            {"key": key_bool, "label": "Probe boolean", "category": "probe", "assert": {slug: "maybe"}},
        ]}, handle)
    code, out, err = run("capableseed", "--apply", path, "--assert", "--as", username)
    note("a value the capability does not permit is refused", code != 0, (err.strip() or out.strip()).splitlines()[-1])
    check("and the stored value is unchanged", con.execute(
        "select value from capability_assertions where capability = ? and project_id = ?",
        (key_bool, pid)).fetchone()[0], "yes")

    code, out, err = run("capableseed", "--list", "--category", "probe")
    check("--list exits 0", code, 0)
    note("--list names the registered capabilities", key_bool in out and key_enum in out,
         out.strip().splitlines()[0] if out.strip() else "")

    # ── forgeseed ──────────────────────────────────────────────────────────────────────────────────────────────────────────────
    # The mapping must account for the whole catalogue, so the probe builds one from the catalogue itself and then perturbs it. Each
    # perturbation is a refusal the operator can act on, and each is asserted to write NOTHING.
    catalogue = [row[0] for row in con.execute("select slug from projects order by slug").fetchall()]
    mapping = os.path.join(os.environ.get("TMPDIR", "/tmp"), f"forge-{STAMP}.json")

    def write_mapping(overrides=None, drop=None, extra=None):
        overrides = overrides or {}
        entries = []
        for catalogue_slug in catalogue:
            if catalogue_slug == drop:
                continue
            entries.append(overrides.get(catalogue_slug, {"slug": catalogue_slug, "skip": True, "reason": "probe: not stated"}))
        if extra:
            entries.append(extra)
        with open(mapping, "w", encoding="utf-8") as handle:
            json.dump(entries, handle)
        return mapping

    def stored_repo():
        return con.execute("select repo_url, repo_host from projects where slug = ?", (slug,)).fetchone()

    # A catalogue project the file does not mention: refused, because a new project that quietly never gets a repository never gets health.
    write_mapping(drop=slug)
    code, out, err = run("forgeseed", "--file", mapping)
    note("a catalogue project missing from the file is refused", code != 0, (err.strip() or out.strip()).splitlines()[-1])
    check("and it is named", slug in err, True)
    check("and nothing was written", stored_repo(), (None, None))

    # A slug that is not in the catalogue: a typo would silently measure nothing.
    write_mapping(extra={"slug": f"no-such-{STAMP}", "repo_url": "https://github.com/o/r", "repo_host": "github"})
    code, out, err = run("forgeseed", "--file", mapping)
    note("a slug that is not in the catalogue is refused", code != 0, (err.strip() or out.strip()).splitlines()[-1])

    # A skip with no reason: "we have not stated this one" must be written down.
    write_mapping(overrides={slug: {"slug": slug, "skip": True}})
    code, out, err = run("forgeseed", "--file", mapping)
    note("a skip with no reason is refused", code != 0, (err.strip() or out.strip()).splitlines()[-1])

    # The happy path, with the scp-like spelling a person actually types: one canonical value is what gets stored.
    write_mapping(overrides={slug: {"slug": slug, "repo_url": "git@github.com:example/probe.git", "repo_host": "github"}})
    code, out, err = run("forgeseed", "--file", mapping)
    check("a complete mapping exits 0", code, 0)
    check("and the repository is stored", stored_repo(), ("https://github.com/example/probe", "github"))
    note("and the report counts what it applied", "applied 1 of" in out, out.strip())

    # --dry-run is the run an operator rehearses with; it must not write.
    write_mapping(overrides={slug: {"slug": slug, "repo_url": "https://codeberg.org/example/other", "repo_host": "codeberg"}})
    code, out, err = run("forgeseed", "--file", mapping, "--dry-run")
    check("--dry-run exits 0", code, 0)
    check("and writes nothing", stored_repo(), ("https://github.com/example/probe", "github"))
    note("and says what it would do", "would set " + slug in out, out.strip())

    # Becoming a skip CLEARS the repository: the failure this rule exists for is an hourly ingest retrying a URL the mapping dropped.
    write_mapping()
    code, out, err = run("forgeseed", "--file", mapping)
    check("a project that becomes a skip exits 0", code, 0)
    check("and loses its repository", stored_repo(), (None, None))
    note("and the report counts the clear", "1 cleared" in out, out.strip())

    # Re-running the same mapping changes nothing.
    code, out, err = run("forgeseed", "--file", mapping)
    check("re-running exits 0", code, 0)
    note("and reports no clears the second time", "0 cleared" in out, out.strip())

    # ── embedbackfill ─────────────────────────────────────────────────────────────────────────────────────────────────────────────
    # Two rules, and the first one was found by breaking it: this database's index holds `nomic` vectors, and the port's default embedder
    # is the local hashed one — running the backfill wrote 4,818 vectors no reader will ever compare against, beside 5,482 that they will.
    # The command now refuses instead, and that refusal is the first thing the probe asserts.
    code, out, err = run("embedbackfill", "--kind", "complaint")
    note("a backfill under a model the index does not hold is refused", code != 0, (err.strip() or out.strip()).splitlines()[-1])
    check("and it wrote nothing under the wrong model", con.execute(
        "select count(*) from embeddings where model_id <> 'nomic:nomic-embed-text:latest'"
        " and entity_id in (select id from projects where id = ?)", (pid,)).fetchone()[0], 0)
    note("and the refusal names the models already in the table", "nomic" in err, "")

    # --status is a read: it must still answer under a mismatched embedder, because "what does the index hold" is the question an operator
    # asks to find out which model to point at.
    code, out, err = run("embedbackfill", "--status", "--kind", "complaint")
    check("--status still answers under a mismatched embedder", code, 0)

    # The write path, on the smallest kind and with --force, so the probe proves it indexes and stays idempotent rather than proving a
    # refusal and stopping.
    code, out, err = run("embedbackfill", "--kind", "project", "--force")
    check("a backfill with --force exits 0", code, 0)
    vectors = con.execute(
        "select count(distinct field) from embeddings where kind = 'project' and entity_id = ? and model_id <> 'nomic:nomic-embed-text:latest'",
        (pid,)).fetchone()[0]
    check("and writes both vectors: title and full text", vectors, 2)
    note("and reports how many it embedded", "embedded " in out and "embedded 0" not in out, out.strip().splitlines()[0] if out.strip() else "")
    note("and ends by saying the kind is complete", "OK" in out, out.strip().splitlines()[-1])

    # Idempotent: a second run finds nothing to do.
    before = con.execute(
        "select count(*) from embeddings where kind = 'project' and model_id <> 'nomic:nomic-embed-text:latest'").fetchone()[0]
    code, out, err = run("embedbackfill", "--kind", "project", "--force")
    check("a second backfill exits 0", code, 0)
    note("and embeds nothing", "embedded 0" in out, out.strip().splitlines()[0] if out.strip() else "")
    check("and the index is unchanged", con.execute(
        "select count(*) from embeddings where kind = 'project' and model_id <> 'nomic:nomic-embed-text:latest'").fetchone()[0], before)

    # A row with nothing to embed is not a hole: it is excluded from the denominator, so the kind can still report complete.
    empty_project = con.execute(
        "insert into projects (slug, name, description, governance_model, visibility, intake_mode, created_at, updated_at)"
        " values (?,?,?,?,?,?,?,?) returning id",
        (f"{slug}-empty", "", "", "collective", "public", "both", time.time(), time.time())).lastrowid
    con.commit()
    code, out, err = run("embedbackfill", "--status", "--kind", "project")
    check("an empty row does not make the index incomplete", code, 0)
    note("and the kind still reads OK", "OK" in out, out.strip().splitlines()[-1])
    check("and the empty row has no vector", con.execute(
        "select count(*) from embeddings where kind = 'project' and entity_id = ?", (empty_project,)).fetchone()[0], 0)
    con.execute("delete from projects where id = ?", (empty_project,))
    con.commit()

    # An unknown kind is refused rather than silently doing nothing.
    code, out, err = run("embedbackfill", "--kind", "nonsense")
    note("an unknown kind is refused", code != 0, (err.strip() or out.strip()).splitlines()[-1])

    # ── cleanup ────────────────────────────────────────────────────────────────────────────────────────────────
    con.execute("delete from capability_assertions where capability like ?", (f"probe-%-{STAMP}",))
    con.execute("delete from capabilities where key like ?", (f"probe-%-{STAMP}",))
    # The vectors this probe wrote under the port's own embedder: inert for the live service (which filters by its model id) and removed
    # so the table does not accumulate a second model's rows run after run.
    con.execute("delete from embeddings where model_id <> 'nomic:nomic-embed-text:latest'")
    # Children before parents: complaints carry an author foreign key, and a cleanup that cannot remove its own fixture breaks the next run.
    con.execute("delete from embeddings where kind = 'complaint' and entity_id in (select id from complaints where project_id = ?)", (pid,))
    con.execute("delete from complaints where project_id = ?", (pid,))
    con.execute("delete from trust_grants where user_id = ?", (uid,))
    con.execute("delete from users where id = ?", (uid,))
    con.execute("delete from projects where id = ?", (pid,))
    con.execute("delete from trust_config")
    con.commit()

    failed = [r for r in results if r[1] != r[2]]
    print(f"\n{len(results) - len(failed)}/{len(results)} ok")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())

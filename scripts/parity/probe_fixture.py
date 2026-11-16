#!/usr/bin/env python3
"""Seed the one project `scripts/parity/probe_pages.py` assumes exists, into a scratch database.

`probe_pages` walks `/projects/suki/…` — board, roadmap, consensus, audit, documents, criteria — and asserts each
is a 200 document. It seeds only its own hostile-named project (`probe-page-<stamp>`), so on an empty scratch
database those six pages answer 404 and the probe reports 39 failures that say nothing about the code. That is why
it could not be a gate: it needs a fixture, and the fixture was never written.

This is the fixture. It inserts `suki`, idempotently, through the same `harness.connect` the suites use, so it
works against PostgreSQL (the only engine this tree ships a schema for) and refuses a database whose name does not
say it is a test database — the fixture must never be what puts a row in production.

Usage:
    CONCORD_E2E_DSN=postgres://…/concord_test python3 scripts/parity/probe_fixture.py
"""
from __future__ import annotations

import pathlib
import sys
import time

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "tests/e2e"))

import harness  # noqa: E402

SLUG = "suki"
# `Suki`, not `suki`: the probe asserts `<title>Suki: Documents — Concord</title>`, because the reference's seed
# data was named that way. The slug stays lowercase; the display name is what lands in the title.
NAME = "Suki"
DESCRIPTION = "the fixture project probe_pages walks its tabs through"
GOVERNANCE = "collective"
VISIBILITY = "public"
INTAKE = "both"


def main() -> int:
    if not harness.SEED_DSN:
        raise SystemExit(
            "probe_fixture: CONCORD_E2E_DSN is not set — this seeds a project into a database, so it must be "
            "pointed at a scratch one (see scripts/parity/e2e.sh)."
        )
    con = harness.connect(harness.SEED_DSN)
    stamp = time.time()
    con.execute("delete from projects where slug = ?", (SLUG,))
    project_id = con.execute(
        "insert into projects (slug, name, description, governance_model, visibility, intake_mode, created_at, updated_at)"
        " values (?,?,?,?,?,?,?,?) returning id",
        (SLUG, NAME, DESCRIPTION, GOVERNANCE, VISIBILITY, INTAKE, stamp, stamp),
    ).lastrowid
    con.commit()
    print(f"probe_fixture: project {SLUG} #{project_id} in the scratch database")
    con.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())

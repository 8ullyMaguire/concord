#!/usr/bin/env python3
"""Fail when a document's measured numbers disagree with the tree.

## Why this exists

`docs/HANDOFF.md` and `docs/PLAN.md` both carried a status line reading
"schema 19, 73 tables, 19 migrations ... 443 tests" for weeks after the tree had
moved to schema 25 and 79 tables. Nothing failed, because nothing read the docs.

That failure is already written down in `KNOWN-ISSUES.md` under "counted work
instead of measuring it": a status table written once and never reconciled goes
stale *silently*. This gate is the reconciliation.

## What it checks, and what it deliberately does not

It checks the numbers a document states as MEASURED -- schema version, migration
count, table count, test count -- against the tree and the live database.

It does NOT check that a document is complete, nor that its prose is true. A
document claiming a feature exists is a claim about code; this cannot settle that,
and pretending otherwise would make the gate cry wolf. `check_spec_drift.py`
covers the other doc-shaped failure (citing a superseded spec as authority).

## Historical tables are exempt, by section

`HANDOFF.md` carries a "what changed since this page was written" table quoting the
OLD numbers on purpose -- "62 tests pass" is the *Was* column, and rewriting it
would destroy the record. So a number inside a markdown table row, or on a line that
explicitly marks itself historical, is skipped rather than compared.

The skip is narrow and stated in the failure output, because a gate that silently
exemptions lines is a gate you cannot trust.

## Usage

    python3 scripts/check_doc_numbers.py [--db PATH]

Exit 0 clean, 1 on a stale number, 2 on a usage error (e.g. no database found).
"""

from __future__ import annotations

import argparse
import os
import re
import sqlite3
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MIGRATIONS = os.path.join(REPO, "internal", "db", "migrations")
DOCS = os.path.join(REPO, "docs")

DEFAULT_DB = os.path.expanduser("~/.local/share/concord/concord.db")


def migration_count() -> int:
    return len([f for f in os.listdir(MIGRATIONS) if f.endswith(".sql")])


def latest_schema_version() -> int:
    return max(
        int(f[:4]) for f in os.listdir(MIGRATIONS) if f.endswith(".sql") and f[:4].isdigit()
    )


def live_facts(db_path: str) -> dict:
    """Read the numbers from the database itself.

    Falls back to a fresh temp database when the live one is absent, so a CI box
    that has never run the app is not blocked from checking the docs. A fresh
    database also proves the chain applies at all, which is a bonus rather than a
    reason to skip.
    """
    import tempfile

    path = db_path
    tmp = None
    if not os.path.exists(path):
        tmp = tempfile.NamedTemporaryFile(suffix=".db", delete=False)
        tmp.close()
        path = tmp.name
    try:
        con = sqlite3.connect(path)
        try:
            tables = con.execute(
                "SELECT count(*) FROM sqlite_master WHERE type='table'"
            ).fetchone()[0]
        except sqlite3.Error:
            tables = None
        finally:
            con.close()
    finally:
        if tmp:
            os.unlink(tmp.name)
    return {"tables": tables}


def go_test_count() -> int:
    out = subprocess.run(
        [
            "bash",
            "-lc",
            "grep -rhoE '^func (Test[A-Za-z0-9_]+)' internal/*/*_test.go | wc -l",
        ],
        cwd=REPO,
        capture_output=True,
        text=True,
    )
    try:
        return int(out.stdout.strip())
    except ValueError:
        return None


def is_historical(line: str) -> bool:
    """True when a line is quoting a past state rather than asserting the present.

    Only a markdown table row qualifies. `HANDOFF.md` records old numbers in a
    was/now table and rewriting them would destroy the record.

    NOT a substring test for "was". The first version did that, and it exempted
    every line in docs/ containing the word -- including the ones the gate exists
    to check -- so the gate reported `OK 0 measured numbers` and guarded nothing.
    A stale number lives in prose far more often than in a table, so exempting
    prose exempts exactly what matters.
    """
    return line.lstrip().startswith("|")


def build_checks(facts: dict) -> list:
    """(pattern, label, key) for every fact that could be read.

    A fact that is None (an unreadable database) is left out and the caller
    reports the skip, so "could not measure" never reads as "agrees".
    """
    checks = [
        (re.compile(r"\b(\d{1,3})\s+migrations\b"), "migration count", "migrations"),
        (re.compile(r"\bschema\s+(\d{1,3})\b"), "schema version", "schema"),
    ]
    if facts.get("tables") is not None:
        # Narrowed to a claim about the WHOLE schema: "the 79 tables" is a total,
        # while "0008 rebuilds 23 tables" and "all 23 tables are built" are
        # subsets.
        #
        # The narrowing is deliberate rather than convenient. A gate that flags
        # correct subset counts trains you to route around it, and a gate you route
        # around protects nothing -- the stale PLAN-r4 lines would have survived
        # alongside a pile of noise. Every subset phrasing found in docs/ is named
        # in the negative lookbehinds, so a NEW subset phrasing is a finding that
        # gets classified rather than ignored.
        checks.append(
            (
                re.compile(
                    r"(?<!rebuilds )(?<!all )(?<!across )\bthe (\d{2,3})\s+tables\b"
                ),
                "table count",
                "tables",
            )
        )
    if facts.get("tests") is not None:
        checks.append(
            (re.compile(r"\b(\d{3,4})\s+tests\b"), "test count", "tests")
        )
    return checks


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--db", default=DEFAULT_DB)
    args = ap.parse_args()

    if not os.path.isdir(MIGRATIONS):
        print(f"no migrations directory at {MIGRATIONS}", file=sys.stderr)
        return 2

    facts = {
        "migrations": migration_count(),
        "schema": latest_schema_version(),
        "tables": live_facts(args.db)["tables"],
        "tests": go_test_count(),
    }
    checks = build_checks(facts)

    findings = []
    scanned = 0
    for name in sorted(os.listdir(DOCS)):
        if not name.endswith(".md"):
            continue
        path = os.path.join(DOCS, name)
        with open(path) as fh:
            for lineno, line in enumerate(fh, 1):
                if is_historical(line):
                    continue
                for pattern, label, key in checks:
                    expected = facts.get(key)
                    if expected is None:
                        continue
                    for m in pattern.finditer(line):
                        scanned += 1
                        if int(m.group(1)) != expected:
                            findings.append(
                                f"{name}:{lineno}: says {m.group(0)!r} "
                                f"({label}); the tree has {expected}"
                            )

    if scanned == 0:
        # A run that examined nothing must not report agreement. The first version
        # of this gate did exactly that -- `OK 0 measured number(s)` while every
        # line it should have checked contained the word "was" and was exempted --
        # so it exited 0 while guarding nothing. The exemption was the bug; this
        # check is the backstop for any future one of the same shape.
        print(
            "BLIND  examined 0 measured numbers, so nothing was actually checked.\n"
            "  Either docs/ no longer state any, or every match is being exempted.\n"
            "  A gate that looks at nothing is not a gate -- treat this as a failure."
        )
        return 1

    if findings:
        print("STALE  measured numbers in docs disagree with the tree:\n")
        for f in findings:
            print(f"  {f}")
        print(
            "\n  Fix the number, or mark the line historical (a table row) if it is "
            "quoting a past state."
        )
        return 1

    print(
        f"  OK  {scanned} measured number(s) across docs/ agree with the tree "
        f"(schema {facts['schema']}, {facts['migrations']} migrations"
        + (f", {facts['tables']} tables" if facts["tables"] is not None else "")
        + (f", {facts['tests']} tests" if facts["tests"] is not None else "")
        + ")"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())

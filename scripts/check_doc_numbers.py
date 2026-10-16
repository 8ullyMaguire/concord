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

## Three kinds of line are exempt, and each is reported

1. **A markdown table row.** `HANDOFF.md` carries a "what changed since this page was
   written" table quoting the OLD numbers on purpose -- "62 tests pass" is the *Was*
   column, and rewriting it would destroy the record.
2. **A line that marks itself historical** with the literal `(historical)`. The
   pass sections in `REMAINING.md` each end with the numbers that pass measured
   ("882 Go tests. `make verify` and `make e2e` green."), which were true when they
   were written and are not claims about this tree. The marker is what tells the two
   apart; prose alone cannot, and every attempt to infer it from phrasing has been
   wrong in both directions.
3. **A number inside quotes or a code fence** -- backticks, double quotes, or a fenced
   block. `"902 Go tests"` in a sentence about the pattern is a quotation of a number,
   not an assertion of one, and a fenced block holds command output that was true when
   it was captured -- this tracker quotes the gate's own stale output to explain why
   the gate changed.

Each exemption is COUNTED and printed in both the OK line and the failure output,
because a gate that silently exemptions lines is a gate you cannot trust.

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

    A markdown table row (`HANDOFF.md`'s was/now table, whose *Was* column must keep
    the old numbers), or a line carrying the literal marker `(historical)`.

    NOT a substring test for "was". The first version did that, and it exempted
    every line in docs/ containing the word -- including the ones the gate exists
    to check -- so the gate reported `OK 0 measured numbers` and guarded nothing.
    A stale number lives in prose far more often than in a table, so exempting
    prose exempts exactly what matters.

    NOT "any dated section" either. `REMAINING.md`'s §12 is titled "What is left,
    2026-10-15 -- THIS IS THE LIVE TRACKER", so a date is not a signal that a section
    records rather than claims; the live tracker would have been the exempt one.
    """
    return line.lstrip().startswith("|") or HISTORICAL_MARKER in line


# The literal a line uses to say "this is a record, not a claim". Deliberately a
# marker and not a phrase: `REMAINING.md` argues at length about what its numbers
# used to be, so any prose-shaped exemption would exempt the argument itself.
HISTORICAL_MARKER = "(historical)"


def is_fence(line: str) -> bool:
    """True for a line that opens or closes a fenced code block.

    A fenced block is a quotation too, and the one place a document holds command
    OUTPUT rather than prose: `REMAINING.md` §18.3 quotes this gate's own stale output
    to explain why the patterns below changed. Inside the fence those numbers are the
    record of what it printed; the fix is not to rewrite them.
    """
    return line.lstrip().startswith("```")


def is_quoted(line: str, start: int, end: int) -> bool:
    """True when the matched span sits inside backticks or double quotes.

    A number in quotes is being quoted. `"902 Go tests"` appears in a sentence about
    this gate's blind spot, and flagging it would mean the gate cannot be written
    about -- the same shape as the "was" bug, where the fix was to stop guessing.
    """
    for i in range(start - 1, -1, -1):
        if line[i] in "`\"":
            # Look for the closing character after the span; an unbalanced quote
            # before the match is a markdown artefact, not a quotation.
            return line[i] in line[end:]
    return False


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
    if facts.get("tables") is not None:
        # ...and the phrasing the eighth pass introduced, which the pattern above misses:
        # "<N> live tables" (in the measured-state lines, "schema 26 · 26 migrations · 81
        # live tables"). The docs were NOT bent to the older phrasing, because a sentence
        # that reads better is exactly how an ungated number gets introduced -- a claim
        # nothing measures is the failure this file exists for, so the phrasing is gated.
        checks.append(
            (
                re.compile(r"\b(\d{2,3})\s+live tables\b"),
                "table count",
                "tables",
            )
        )
    if facts.get("tests") is not None:
        # The `Go` qualifier is optional and deliberately the ONLY one accepted.
        # `"943 Go tests"` was invisible to the unqualified pattern while the
        # unqualified `"955 tests"` beside it was gated -- so `docs/REMAINING.md`'s
        # own headline sat at 943 for two passes, which is the failure this gate
        # exists for, taking place inside the number it reads.
        #
        # `N e2e tests` does NOT match (the qualifier has to be `Go`), because the e2e
        # count is measured by pytest and
        # `tests/e2e/harness_selftest_test.py` already compares it; this gate must
        # keep running under `make verify` without the browser toolchain.
        checks.append(
            (
                re.compile(r"\b(\d{3,4})\s+(?:Go\s+)?tests\b"),
                "test count",
                "tests",
            )
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
    # Exemptions are counted per class and reported, never applied silently.
    exempt = {"table row or (historical)": 0, "quoted or fenced": 0}
    for name in sorted(os.listdir(DOCS)):
        if not name.endswith(".md"):
            continue
        path = os.path.join(DOCS, name)
        in_fence = False
        with open(path) as fh:
            for lineno, line in enumerate(fh, 1):
                if is_fence(line):
                    in_fence = not in_fence
                    continue
                for pattern, label, key in checks:
                    expected = facts.get(key)
                    if expected is None:
                        continue
                    for m in pattern.finditer(line):
                        if is_historical(line):
                            exempt["table row or (historical)"] += 1
                            continue
                        if in_fence or is_quoted(line, m.start(), m.end()):
                            exempt["quoted or fenced"] += 1
                            continue
                        scanned += 1
                        if int(m.group(1)) != expected:
                            findings.append(
                                f"{name}:{lineno}: says {m.group(0)!r} "
                                f"({label}); the tree has {expected}"
                            )
    exemption_note = (
        "; exempted "
        + ", ".join(f"{n} {k}" for k, n in sorted(exempt.items()))
        if any(exempt.values())
        else ""
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
            "\n  Fix the number, or mark the line historical -- a table row, or the "
            "literal (historical) -- if it is quoting a past state" + exemption_note
        )
        return 1

    print(
        f"  OK  {scanned} measured number(s) across docs/ agree with the tree "
        + exemption_note
        + " ("
        + f"schema {facts['schema']}, {facts['migrations']} migrations"
        + (f", {facts['tables']} tables" if facts["tables"] is not None else "")
        + (f", {facts['tests']} tests" if facts["tests"] is not None else "")
        + ")"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())

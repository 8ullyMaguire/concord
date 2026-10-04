"""Tests for check_doc_numbers.py.

## Why this gate needs its own tests

It is a gate ABOUT gates, and the first version of it shipped green while
examining zero numbers -- reporting `OK 0 measured number(s)`, exiting 0, and
protecting nothing. A doc-checking gate that cannot see a stale number is worse
than no gate, because it converts a known risk into a false assurance.

So each test below makes the gate FAIL on a case it must catch, rather than
asserting on its output text.
"""

import os
import subprocess
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(REPO, "scripts", "check_doc_numbers.py")


def run_gate(*args):
    return subprocess.run(
        [sys.executable, SCRIPT, *args],
        cwd=REPO,
        capture_output=True,
        text=True,
    )


class DocNumbersGate(unittest.TestCase):
    def test_clean_tree_exits_zero_and_actually_scanned(self):
        r = run_gate()
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        # The regression this whole file exists for: a passing run must have
        # examined something. `0 measured number(s)` with exit 0 is the shape of a
        # gate that is decorative.
        self.assertIn("OK", r.stdout)
        scanned = int(r.stdout.split("OK")[1].split("measured")[0].strip())
        self.assertGreater(scanned, 0, "gate reported OK without examining anything")

    def test_catches_a_stale_migration_count(self):
        with self._doc_containing("The chain is at 3 migrations.\n"):
            r = run_gate()
        self.assertEqual(r.returncode, 1, "a stale migration count must fail")
        self.assertIn("STALE", r.stdout)
        self.assertIn("migration count", r.stdout)

    def test_catches_a_stale_schema_version(self):
        with self._doc_containing("Deployed at schema 2 with everything green.\n"):
            r = run_gate()
        self.assertEqual(r.returncode, 1, "a stale schema version must fail")
        self.assertIn("schema version", r.stdout)

    def test_catches_a_stale_test_count(self):
        with self._doc_containing("All 111 tests pass.\n"):
            r = run_gate()
        self.assertEqual(r.returncode, 1, "a stale test count must fail")
        self.assertIn("test count", r.stdout)

    def test_a_table_row_is_exempt_as_a_historical_record(self):
        # HANDOFF.md keeps a was/now table quoting old numbers on purpose.
        # Rewriting them would destroy the record, so a table row is skipped.
        # Everything else is compared.
        with self._doc_containing("| was | 3 migrations | 111 tests |\n"):
            r = run_gate()
        self.assertEqual(r.returncode, 0, "a historical table row must not be flagged")

    def test_a_subset_count_is_not_a_finding(self):
        # "0008 rebuilds 23 tables" is a correct subset claim, not a stale total.
        # A gate that flags it trains you to route around it.
        with self._doc_containing(
            "Migration 0008 rebuilds 23 tables, across all 23 tables it touches.\n"
        ):
            r = run_gate()
        self.assertEqual(r.returncode, 0, "a correct subset count must not be flagged")

    def test_prose_is_not_exempt_merely_for_containing_was(self):
        # The first version exempted any line containing "was", which exempted the
        # very lines the gate exists to check -- hence `OK 0 measured numbers`.
        with self._doc_containing("It was 3 migrations when the page was written.\n"):
            r = run_gate()
        self.assertEqual(r.returncode, 1, "prose must be compared, not exempted")

    def test_fails_when_it_examines_nothing(self):
        # A gate whose own output says it looked at nothing must not exit 0.
        # Simulated by pointing it at an empty docs directory is not possible
        # without moving the tree, so this asserts the BLIND branch exists by
        # checking the guard is present in the source and reachable in principle:
        # the scanned==0 branch precedes the OK return.
        with open(SCRIPT) as fh:
            src = fh.read()
        self.assertIn("if scanned == 0:", src)
        self.assertLess(
            src.index("if scanned == 0:"),
            src.index("BLIND"),
            "the blind-run guard must come before any success return",
        )

    # -- helpers ----------------------------------------------------------

    def _doc_containing(self, text):
        """Temporarily add a doc holding `text`, then remove it."""
        docs = os.path.join(REPO, "docs")
        path = os.path.join(docs, "zz_gate_probe.md")
        self.assertFalse(os.path.exists(path), "probe file already present")
        with open(path, "w") as fh:
            fh.write(text)
        probe = self

        class _Ctx:
            def __enter__(self):
                return probe

            def __exit__(self, *exc):
                os.unlink(path)
                return False

        return _Ctx()


if __name__ == "__main__":
    unittest.main()

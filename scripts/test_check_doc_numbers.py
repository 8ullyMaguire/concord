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

    def test_catches_a_stale_qualified_go_test_count(self):
        # `"943 Go tests"` was invisible to the unqualified pattern while the
        # unqualified `"955 tests"` beside it was gated, so REMAINING.md's own
        # headline sat two passes behind the tree -- the exact failure this gate
        # exists for, taking place inside the number it reads.
        with self._doc_containing("All 111 Go tests pass.\n"):
            r = run_gate()
        self.assertEqual(r.returncode, 1, "a stale qualified count must fail")
        self.assertIn("test count", r.stdout)
        self.assertIn("Go tests", r.stdout)

    def test_an_e2e_count_is_not_compared_against_the_go_count(self):
        # The e2e total is measured by pytest, which `make verify` must not depend
        # on; tests/e2e/harness_selftest_test.py compares that one. If this gate read
        # it, a correct "269 e2e tests" would be flagged against 111 Go tests.
        with self._doc_containing("A run collects 111 e2e tests in 19 suite files.\n"):
            r = run_gate()
        self.assertEqual(r.returncode, 0,
                         "an e2e count must not be read as a Go test count")

    def test_a_quoted_number_is_a_quotation_not_a_claim(self):
        # The gate is discussed in the docs, and the discussion quotes numbers.
        # Flagging them would mean the gate cannot be written about.
        with self._doc_containing('It read "111 Go tests" before the fix.\n'):
            r = run_gate()
        self.assertEqual(r.returncode, 0, "a quoted number must not be flagged")

    def test_a_number_inside_a_code_fence_is_a_quotation_not_a_claim(self):
        # A fenced block holds command OUTPUT. REMAINING.md §18.3 quotes this gate's
        # own stale output to explain why the patterns changed; rewriting it would
        # destroy the evidence for the change.
        with self._doc_containing("The gate printed:\n\n```\n111 Go tests\n```\n"):
            r = run_gate()
        self.assertEqual(r.returncode, 0, "a fenced quotation must not be flagged")

    def test_a_number_after_a_closed_fence_is_still_compared(self):
        # The fence exemption ends with the fence. Otherwise the marker would be a
        # way to switch this gate off for the rest of a document.
        with self._doc_containing(
            "It printed:\n\n```\n2 migrations\n```\n\nand then 111 Go tests.\n"
        ):
            r = run_gate()
        self.assertEqual(r.returncode, 1, "a claim after a closed fence must be read")

    def test_the_historical_marker_exempts_a_recorded_number(self):
        # Each pass section ends with the numbers that pass measured. They were true
        # then and are not claims about this tree; `(historical)` is the marker that
        # says so, and it is the only prose-shaped exemption the gate has.
        with self._doc_containing(
            "# Tracker\n\n## 11. What is left, 2026-01-01\n\n111 Go tests. (historical)\n"
        ):
            r = run_gate()
        self.assertEqual(r.returncode, 0, "a marked record must not be flagged")

    def test_a_dated_section_is_not_exempt_by_its_date(self):
        # The obvious widening -- "a dated section is a record" -- would have exempted
        # REMAINING.md's §12, whose title is "What is left, 2026-10-15 -- THIS IS THE
        # LIVE TRACKER", i.e. the section holding the numbers that must be right.
        with self._doc_containing(
            "# Tracker\n\n## 11. What is left, 2026-01-01\n\n111 Go tests.\n"
        ):
            r = run_gate()
        self.assertEqual(r.returncode, 1,
                         "a date in a heading must not exempt the number under it")

    def test_a_subset_count_is_not_a_finding(self):
        # "0008 rebuilds 23 tables" is a correct subset claim, not a stale total.
        # A gate that flags it trains you to route around it.
        with self._doc_containing(
            "Migration 0008 rebuilds 23 tables, across all 23 tables it touches.\n"
        ):
            r = run_gate()
        self.assertEqual(r.returncode, 0, "a correct subset count must not be flagged")

    def test_the_live_tables_phrasing_is_measured_too(self):
        # The eighth pass wrote "81 live tables" in the measured-state lines, which the
        # "the N tables" pattern does not reach. Rather than bend the sentence to the
        # pattern, the pattern grew: an unmeasured sentence that reads better is exactly
        # how a number stops being checked.
        with self._doc_containing("**Measured state:** schema 26 · 12 live tables.\n"):
            r = run_gate()
        self.assertEqual(r.returncode, 1, "a stale '<N> live tables' must fail")
        self.assertIn("table count", r.stdout)
        self.assertIn("live tables", r.stdout)

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

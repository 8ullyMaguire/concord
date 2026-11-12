"""Mutation gate for the inbox page (docs/specs/notification-spec.md).

Every mutant here is a client-side defect that answers 200 -- or renders a plausible page -- on
the server side, which is why the Go gates cannot see any of them:

  the mark-read control is never wired      the button renders, the click does nothing, and the
                                            card stays unread. This is the defect the whole
                                            "look, then choose" design exists to avoid.
  the list fetch drops the credential       a signed-in reader is told to sign in. Proved live:
                                            this happens for real when the script runs before
                                            session.js, which is why the fix waits for it.
  the empty state reads "No notifications"  the sentence a reader sees when nothing has happened
                                            becomes a statement about the FEATURE instead of
                                            about their own record, which is the one thing the
                                            spec's §4.5 exists to prevent.

A SURVIVED line means the browser suite lost the fixture, never that the wiring is unnecessary.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ui_mutants import run_mutants  # noqa: E402

JS = "internal/httpapi/assets/js/notifications.js"
SUITE = "tests/e2e/notifications_e2e.py"

MUTANTS = [
    ("the mark-read control is never wired",
     "Array.prototype.forEach.call(root.querySelectorAll('[data-read]'), function (btn) {",
     "Array.prototype.forEach.call([], function (btn) {"),
    ("the list fetch drops the reader's credential",
     "return fetch('/api/v1/notifications', { headers: apiAuth() })",
     "return fetch('/api/v1/notifications', { headers: {} })"),
    ("the empty state describes the feature instead of the reader's record",
     "'<p class=\"empty-state-title\">Nothing has been removed from your contributions.</p>'",
     "'<p class=\"empty-state-title\">No notifications.</p>'"),
]


if __name__ == "__main__":
    sys.exit(run_mutants(JS, SUITE, MUTANTS, "notifications.js"))

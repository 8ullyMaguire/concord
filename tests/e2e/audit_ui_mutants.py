"""Mutation gate for the audit viewer page (docs/plans/audit-log.md step A5).

The runner lives in `ui_mutants.py`: it rebuilds the binary that embeds the page's
JavaScript and runs the browser suite, which is the only thing that can kill these
mutants -- every one of them is a client-side defect a Go test cannot observe, because
the Go side serves the same 200 either way.

WHY THE RUNNER IS SHARED RATHER THAN COPIED. The first version of this file called
`python3 -m pytest` and, in an environment whose `python3` has no pytest, reported seven
kills in a row whose tails were empty: a broken toolchain impersonating a killed mutant.
The evidence requirement that fixes it is one function, and it is the same function for
every page.

WHY EACH MUTANT IS HERE, and what it would have looked like shipping:

  detail unescaped                 `detail` is free text from ~20 AddAudit call
                                   sites. A caller-supplied `<script>` executes
                                   in every reader's session.
  actor display name unescaped     the same class, in the "Who" column.
  action name unescaped            the same class, in the action column.
  count line uses the FILTERED     the API's `total` is COUNT(*) over the
  total                            filtered set, so the page claimed "Showing
                                   all 5 entries" over a list of 1 -- telling
                                   the reader their filter excluded nothing.
  deleted actor shows its raw id   "user 987654" invites a reader to complain
                                   about an account that no longer exists.
  search sends text= not q=        filters nothing and returns every row,
                                   looking exactly like a filter that worked.
  filtered-empty says "no activity" "this project has no history" when it has
                                   seven rows and your filter missed them.

A SURVIVED line here means a missing fixture, not a bad mutant: each of these
produces byte-identical output against an inert row, so a surviving mutant is a
statement about the DATA, never about the escaping being unnecessary.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ui_mutants import run_mutants  # noqa: E402

JS = "internal/httpapi/assets/js/audit.js"
SUITE = "tests/e2e/audit_e2e.py"

MUTANTS = [
    ("detail is written into innerHTML unescaped",
     '\'<p class="cell-note">\' + esc(e.detail) + \'</p>\'',
     '\'<p class="cell-note">\' + e.detail + \'</p>\''),
    ("the actor display name is written into innerHTML unescaped",
     "if (e.actor_display_name) return esc(e.actor_display_name);",
     "if (e.actor_display_name) return e.actor_display_name;"),
    ("the action name is written into innerHTML unescaped",
     '\'<td class="cell-title"><code>\' + esc(e.action) + \'</code>\'',
     '\'<td class="cell-title"><code>\' + e.action + \'</code>\''),
    ("the count line compares shown against the FILTERED total",
     "var of = filtered ? (unfilteredTotal != null ? unfilteredTotal : page.total) : page.total;",
     "var of = page.total;"),
    ("a deleted actor is rendered as its raw id instead of 'deleted account'",
     "return '<span class=\"dim\">deleted account</span>';",
     "return '<span class=\"dim\">' + esc(e.actor_id) + '</span>';"),
    ("the search sends text= instead of q=, so it filters nothing",
     "if (state.q) params.push('q=' + encodeURIComponent(state.q));",
     "if (state.q) params.push('text=' + encodeURIComponent(state.q));"),
    ("a filter matching nothing is reported as a project with no activity",
     "(filtered ? 'Nothing matches this filter' : 'No audit activity yet')",
     "'No audit activity yet'"),
]


if __name__ == "__main__":
    sys.exit(run_mutants(JS, SUITE, MUTANTS, "audit.js"))

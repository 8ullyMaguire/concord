"""UI mutation gate for §4.5.7's work-item panel in project.js.

`internal/mutate.py` rewrites Go source and runs `go test`. These mutants rewrite the
page's JAVASCRIPT, rebuild the binary that embeds it, and run the browser suite -- the
only thing that can kill them, because every one is a client-side defect and the Go side
serves the same 200 either way.

WHY THIS PANEL NEEDS ITS OWN GATE. The reader-coverage gate asserts that project.js
mentions each route, and it does, in a fetch path. A mutated panel still mentions the
route and still renders a card; what breaks is the PAIRING between the card and the call.
That is the class of defect this repository keeps finding (a wired control posting to the
wrong verb, a panel that renders a value it never fetched), and each mutant below is one
rule:

  the claim posts to a route that        the button looks right and the request 404s, and
  does not exist                         the panel resets to "Claim this" -- the reader
                                         clicks forever and nothing says why.

  the panel offers no claim control      the wiring finds nothing to attach to, so the
                                         panel is a list with no action. Indistinguishable
                                         from a project whose work is all taken.

  a claimed item is offered anyway        two people implement the same feature. The API
                                         refuses the second claim with a 409, so the harm
                                         is a stranger doing the work before finding out.

  the label form sends the wrong         the write succeeds (200) and the value is
  field name                             unchanged: the form says Saved and the badge
                                         still says 60 minutes.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import ui_mutants

JS = "internal/httpapi/assets/js/project.js"
SUITE = "tests/e2e/workitems_e2e.py"

MUTANTS = [
    ("the claim posts to a route that does not exist",
     "'/features/' + id + '/claim', {})",
     "'/features/' + id + '/claims', {})"),
    ("the panel offers no claim control",
     "'<button type=\"button\" class=\"btn\" data-claim=\"'",
     "'<button type=\"button\" class=\"btn\" data-claim-off=\"'"),
    ("a claimed item is offered anyway",
     "        if (w.claimed_by) {",
     "        if (false) {"),
    ("the label form sends the wrong field name",
     "if (minutes !== '') body.time_budget_minutes = parseInt(minutes, 10);",
     "if (minutes !== '') body.time_budget = parseInt(minutes, 10);"),
]

if __name__ == "__main__":
    sys.exit(ui_mutants.run_mutants(JS, SUITE, MUTANTS, "project.js (work items)"))

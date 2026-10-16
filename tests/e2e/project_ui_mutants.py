"""UI mutation gate for project.js: §M1's complaint verdicts (§M1).

`internal/mutate.py` rewrites Go source and runs `go test`. These mutants rewrite the
page's JAVASCRIPT, rebuild the binary that embeds it, and run the browser suite -- the
only thing that can kill them, because every one of these is a client-side defect and the
Go side serves the same 200 either way.

WHY project.js NEEDS A GATE OF ITS OWN. A complaint card's Validate and Reject controls
were the only way to reach §M1's Validated and Rejected states, and the wiring is a table:

    [['data-validate', 'validate'], ['data-reject', 'reject']].forEach(...)

That table is exactly the shape a copy/paste error survives: swap the two verbs and Reject
posts to `/validate`, writing the WRONG state -- 200, no error, every Go test green, and
the complaint recorded as validated instead of rejected. The Go-side reachability gate
cannot see it either: it asserts that project.js MENTIONS the route, and it does.

Each mutant below is one rule, and the suite that must notice:

  the reject control posts to the        the table's second column is the route verb. This
  validate endpoint                      is the defect above: the button looks right, the
                                         request succeeds, the state is wrong.

  the verdicts are never wired           the buttons render and do nothing at all. The
                                         badge stays "open" after a click, which is
                                         indistinguishable from a slow network -- so the
                                         test waits on the badge and the mutant hangs it.

  a refused verdict reports nowhere      the refusal path is the ONLY feedback for a
                                         non-member, a guest or a logged-out reader. This
                                         mutant keeps the 403 and drops the message, which
                                         is the worst outcome available: the reader is
                                         told nothing and the page looks broken.

  a decided complaint still offers its   §M1's statuses are not reversible states of a
  verdicts                               button; a complaint that is already validated
                                         offers no Validate and no Reject. A control that
                                         is present where it cannot act teaches the reader
                                         that the page does not know what it is for.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from ui_mutants import run_mutants

JS = "internal/httpapi/assets/js/project.js"
SUITE = "tests/e2e/project_panels_e2e.py"

MUTANTS = [
    ("the reject control posts to the validate endpoint",
     "encodeURIComponent(btn.getAttribute(pair[0])) + '/' + pair[1], {})",
     "encodeURIComponent(btn.getAttribute(pair[0])) + '/' + 'validate', {})"),
    ("the verdicts are never wired",
     "    [['data-validate', 'validate'], ['data-reject', 'reject']].forEach(function (pair) {",
     "    [].forEach(function (pair) {"),
    ("a refused verdict reports nowhere",
     "              showCardRefusal(btn, err.message || String(err));",
     "              void 0;"),
    ("a decided complaint still offers its verdicts",
     "        (c.status === 'open'\n          ? '<button class=\"btn btn-quiet\" data-validate=\"'",
     "        (true\n          ? '<button class=\"btn btn-quiet\" data-validate=\"'"),
    # §28's field-report write half. The read half has been routed since phase3, so the panel's
    # rate and cards are already covered; what these four cover is the pair of controls that can
    # now change them, at the three places such a control goes wrong: the path, the payload, and
    # whether it is wired at all.
    # The mutation is the PATH, not the query string: the first version of this mutant appended
    # ?x=1 and SURVIVED, because the server routes on the path and a stray query parameter is
    # ignored -- the write landed and the test was right to pass. A mutant has to model the defect.
    ("the report form posts to the project instead of the reports route",
     "        postJSON(base + '/field-reports', {",
     "        postJSON(base, {"),
    ("the outcome select loses a value",
     "        '<option value=\"migrated-away\">migrated away</option>' +",
     "        '<option value=\"migrated away\">migrated away</option>' +"),
    ("the reply form sends the wrong field name",
     "            { body: body }).then(function () {",
     "            { reply: body }).then(function () {"),
    ("the reply forms are never wired",
     "    var replyForms = document.querySelectorAll('form[data-respond-report]');",
     "    var replyForms = document.querySelectorAll('form[data-respond-report-never]');"),
]

if __name__ == "__main__":
    sys.exit(run_mutants(JS, SUITE, MUTANTS, "project.js"))

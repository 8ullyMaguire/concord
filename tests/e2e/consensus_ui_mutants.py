"""Mutation gate for the Consensus page's close control (`consensus.js`, §19).

Three client-side rules this pass added, none of which a Go test can observe: the Go side
serves the same 200 whether the page offers the control, sends the note, or renders the
answer. `ui_mutants.py` rebuilds the binary that embeds the JavaScript and runs the
browser suite, so a kill is a rendered page disagreeing with the rule.

WHY EACH MUTANT IS HERE, and what it would have looked like shipping:

  the control is offered on a    a maintainer closes a call, the page reloads, and the
  CLOSED call                    page offers to close it again -- a second verdict on a
                                 decision that has one, and the "Close the call" button
                                 is the only thing that says the call still needs one.
  the note is never sent         §6.6's "objector's remedy answered in writing" silently
                                 becomes an empty string: every override fails with the
                                 server's refusal (visibly) or, worse on a server that
                                 did not check, records an override that answered
                                 nothing.
  the resolution is not          the answer is stored, the wire carries it, and the page
  rendered                       drops it -- a block shown as "overridden" with no
                                 sentence saying what answered the objection. This is
                                 §19.4's defect one layer up: a record nothing reads.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ui_mutants import run_mutants  # noqa: E402

JS = "internal/httpapi/assets/js/consensus.js"
SUITE = "tests/e2e/consensus_e2e.py"

MUTANTS = [
    ("the close control is offered on a closed call",
     "    if ((call.status || '') !== 'open') {",
     "    if (false) {"),
    ("the note is never sent with the close",
     "      body: JSON.stringify({ note: note })",
     "      body: JSON.stringify({})"),
    ("the resolution is not rendered under the objection",
     """      if (o.resolution) {
        var res = document.createElement('p');
        res.className = 'objection-resolution';
        res.textContent = 'resolution: ' + o.resolution;
        li.appendChild(res);
      }""",
     ""),
]


if __name__ == "__main__":
    sys.exit(run_mutants(JS, SUITE, MUTANTS, "consensus.js"))

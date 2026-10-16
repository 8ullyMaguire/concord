"""Mutation gate for the Consensus page's close control and §6.6's override window.

Five client-side rules, none of which a Go test can observe: the Go side serves the same
200 whether the page offers the control, sends the note, renders the answer, or shows the
window at all. `ui_mutants.py` rebuilds the binary that embeds the JavaScript and runs the
browser suite, so a kill is a rendered page disagreeing with the rule.

WHY EACH MUTANT IS HERE, and what it would have looked like shipping:

  the close control is offered on a    a maintainer closes a call, the page reloads, and the
  CLOSED call                          page offers to close it again -- a second verdict on a
                                       decision that has one, and the "Close the call" button
                                       is the only thing that says the call still needs one.
  the note is never sent with the      §6.6's "objector's remedy answered in writing" silently
  override window's close              becomes an empty string: every override fails with the
                                       server's refusal (visibly) or, worse on a server that
                                       did not check, records an override that answered
                                       nothing.
  the resolution is not rendered       the answer is stored, the wire carries it, and the page
  under the objection                  drops it -- a block shown as "overridden" with no
                                       sentence saying what answered the objection: a record
                                       nothing reads.
  the override section is shown on     every call grows a second ballot, including one that is
  ANY call                             still running and one that closed accepted. A control
                                       that is present where it cannot act teaches the reader
                                       that the page does not know what it is for.
  the window's own state is never      the section appears and then says nothing: no progress,
  read                                 no stance buttons, no result -- the page offering a
                                       window it never looked at. This is the read that makes
                                       the section stateful rather than a static claim.

§6.5's decision, its chain, and §6.6's hold (ninth pass). Three routes -- /outcome,
/fallback, /hold -- had handlers, store tests and NO reader, and the coarse
reader-inventory row for "/consensus" was satisfied by any page that read a call:

  the fallback chain is never         §6.5's "pre-agreed fallback" is agreed only if a reader
  rendered                            can see it while the call is OPEN. A chain that appears
                                      after the call stalls is not an agreement, it is a
                                      post-mortem, and the ranking is the one thing a voter
                                      needs to know before deciding.
  the decision panel is never         the record becomes unwritable from the browser, which is
  offered                             how it was: /outcome existed, was refused for any closed
                                      call, and nothing on any page called it.
  the decision is never sent          the page collects an outcome and posts an empty body: the
  (the body is empty)                 server answers 400 and the reader is told their decision
                                      was recorded nowhere, or worse, a default lands.
  the hold is never shown             a suspended call looks entirely ordinary -- close offered,
                                      positions offered, every write refused -- and the reader is
                                      left to guess why.
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
    ("the note is never sent with the override window's close",
     "    overrideSend('POST', '/close', { note: (overrideNote.value || '').trim() }).then(function (data) {",
     "    overrideSend('POST', '/close', { note: '' }).then(function (data) {"),
    ("the resolution is not rendered under the objection",
     """      if (o.resolution) {
        var res = document.createElement('p');
        res.className = 'objection-resolution';
        res.textContent = 'resolution: ' + o.resolution;
        li.appendChild(res);
      }""",
     ""),
    ("the override section is shown on any call",
     "    var blocked = (call.status || '') === 'closed' && call.result === 'blocked';",
     "    var blocked = true;"),
    ("the window's own state is never read",
     "    show(overrideEl, true);\n    loadOverride();",
     "    show(overrideEl, true);"),
    ("the fallback chain is never rendered",
     "      show(fallbackEl, true);\n      renderFallbackOptions();",
     "      renderFallbackOptions();"),
    ("the decision panel is never offered",
     "    show(outcomeEl, true);\n  }",
     "    show(outcomeEl, false);\n  }"),
    ("the decision is never sent",
     "    var body = { outcome: outcomeChoice };",
     "    var body = {};"),
    ("the hold is never shown",
     "      show(holdEl, true);\n    }).catch(function () { show(holdEl, false); });",
     "      show(holdEl, false);\n    }).catch(function () { show(holdEl, false); });"),
]


if __name__ == "__main__":
    sys.exit(run_mutants(JS, SUITE, MUTANTS, "consensus.js"))

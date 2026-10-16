"""Mutation gate for the cookie notice's consequences (docs/specs/guest-interactions-spec.md).

Every rule here is client-side, and the Go side serves the same response whichever
way it is broken: the server refuses a write with no consent either way, so no Go test
can see whether a VISITOR was told. `ui_mutants.py` rebuilds the binary that embeds
the JavaScript and runs the browser suite, so a kill is a browser disagreeing with the
rule.

WHY EACH MUTANT IS HERE, and what it would have looked like shipping:

  the notice's Accept button records       the button looks like it worked -- it is
  nothing                                  disabled, then re-enabled -- and the notice
                                           never goes away, so the visitor can never
                                           interact and is given no reason.
  the notice's Accept button is not        the same outcome with no sign of a mistake at
  wired at all                             all: a dead control on every page, which is
                                           the one failure the notice exists to prevent.
  the local consent guard is removed       a write is attempted, the server refuses it,
                                           and nothing on the page says why -- the
                                           button appears to do nothing.
  the guest identity is never minted       "guests can interact" becomes a claim with no
                                           mechanism: every anonymous action answers 401
                                           with no explanation, on a site whose whole
                                           point is that a visitor may act.
  the limit refusal is reported as a       the sentence that matters goes missing: the
  generic failure                          visitor is told something went wrong but not
                                           that the instance has grown, nor that creating
                                           an account is the way forward.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from ui_mutants import run_mutants  # noqa: E402

JS = "internal/httpapi/assets/js/consent.js"
AUTH_JS = "internal/httpapi/assets/js/auth.js"
SUITE = "tests/e2e/guest_e2e.py"

MUTANTS = [
    ("the notice's Accept button records nothing",
     "      pendingAccept = originalFetch('/api/v1/consent', {",
     "      pendingAccept = originalFetch('/api/v1/consent-is-not-here', {"),
    ("the notice's Accept button is not wired at all",
     "    button.addEventListener('click', function () {",
     "    button.addEventListener('no-such-event', function () {"),
    ("the local consent guard is removed",
     "    if (!consented()) {",
     "    if (false) {"),
    ("the guest identity is never minted",
     "    if (session && !session.signedIn()) {",
     "    if (false) {"),
    # The limit refusal is told in TWO places -- when the identity cannot be minted,
    # and again on the write that wanted it -- and the first version of this mutant
    # targeted only the first. It SURVIVED, correctly: the second call site said the
    # same sentence, so an outcome-level test could not see the difference. The rule
    # that a test CAN pin is the one both sites share: the refusal must offer the way
    # forward, not just state the problem.
    ("the way forward is not offered when the guest door is shut",
     "        if (!token) return refused(GUEST_REFUSED, { href: '/login', text: 'Create an account' });",
     "        if (!token) return refused(GUEST_REFUSED);"),
]


# auth.js carries the two rules the fold depends on, and both were found by the
# browser suite after the Go suite was already green:
#
#   a guest is bounced away from        a token exists, so the auth page sent the
#   the register page                   visitor to /projects -- and the visitor who
#                                       must be able to register is exactly the one
#                                       holding a guest token. The fold was
#                                       unreachable through the interface.
#   the guest token is not presented    the server has no way to know the visitor did
#   when registering                    anything: the guest token is the ONLY thing
#                                       that says so (no cookie can), so the account
#                                       was created beside the guest's work instead of
#                                       on top of it.
AUTH_MUTANTS = [
    ("a guest is bounced away from the register page",
     "    if (existing && !isGuest) {",
     "    if (existing) {"),
    ("the guest token is not presented when registering",
     "        if (auth.Authorization) headers.Authorization = auth.Authorization;",
     ""),
]


if __name__ == "__main__":
    rc = run_mutants(JS, SUITE, MUTANTS, "consent.js")
    rc |= run_mutants(AUTH_JS, SUITE, AUTH_MUTANTS, "auth.js")
    sys.exit(rc)

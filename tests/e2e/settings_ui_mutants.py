"""UI mutation gate for settings.js's §6.2 themes.

`internal/mutate.py` rewrites Go and runs `go test`. These rewrite the page's JAVASCRIPT,
rebuild the binary that embeds it, and run the browser suite -- the only layer that can kill
them: a client-side defect serves the same 200 to every Go test.

Each mutant is one rule, and the test that must notice it:

  a 202 consent is reported as        THE defect this panel can have. 202 means RECORDED
  ratified                            and below quorum. A page that prints "Ratified" tells
                                      the reader the roadmap moved when nothing did, and the
                                      ranking page contradicts them a click later.

  the propose form is never wired     the form renders, takes input, and submits nowhere.
                                      The button looks like it worked; only the pending
                                      table below it (which stays empty) disagrees.

  the expiry column is dropped        §366's "unless renewed" is only meaningful if the
                                      reader can see WHEN it lapses. A themes table with the
                                      weight and no expiry is a permanent-looking dial --
                                      the thing the expiry rule exists to prevent.

  renew prepares the weight too       renewal is a NEW proposal for the same tag, and the
                                      weight is the thing being decided. Pre-filling it from
                                      the live row turns "renew this, and at what" into one
                                      click that answers its own question -- and the reader
                                      never has the moment the rule exists to give them.

  renew fills nothing                 the button renders, looks like it worked, and the tag
                                      never reaches the form. The reader believes a theme
                                      that is about to lapse has been renewed.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from ui_mutants import run_mutants

JS = "internal/httpapi/assets/js/settings.js"
SUITE = "tests/e2e/settings_themes_e2e.py"

MUTANTS = [
    ("a 202 consent is reported as ratified",
     'say("Consent recorded. " + (body.message || "The quorum is not reached yet."));',
     'say("Ratified. " + (body.message || "The theme is now in force."));'),
    ("the theme propose form is never wired",
     '    var form = document.getElementById("propose-theme-form");\n    if (form) {',
     '    var form = null;\n    if (form) {'),
    ("the expiry column is dropped from the live themes",
     '"<td>" + esc(t.expires_at ? whenUnix(t.expires_at) : "") + "</td>" +',
     '"<td></td>" +'),
    ("renew prepares the weight for the reader as well as the tag",
     '        form.querySelector("#theme-tag").value = tag;',
     '        form.querySelector("#theme-tag").value = tag;\n'
     '        form.querySelector("#theme-weight").value ='
     ' btn.closest("tr").children[1].textContent.trim();'),
    ("renew fills nothing, so the tag never reaches the form",
     '        form.querySelector("#theme-tag").value = tag;',
     '        form.querySelector("#theme-tag").value = "";'),
]

if __name__ == "__main__":
    sys.exit(run_mutants(JS, SUITE, MUTANTS, "settings.js"))

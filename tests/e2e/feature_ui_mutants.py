"""UI mutation gate for feature.js's §6.2 tag panel.

The tag list is the input the theme mechanism steers by, and it is the ONLY place a user can
change it. Every mutant here is a client-side defect: the Go side answers the same 200s.

  the tags panel is never rendered    the section renders its empty state, which reads as
                                      "this feature has no tags" -- and the tags are what
                                      decide which ratified themes count toward its priority.
                                      The reader would conclude the feature is untouched by
                                      a theme it actually matches.

  the add form is never wired         the input takes text and the button does nothing.
                                      The e2e waits for the pill that never appears.

  the remove control posts to the     a swapped path is the classic copy/paste defect: the
  add endpoint                        request SUCCEEDS (the tag is already there, so the
                                      insert is a no-op), the server answers 200, and the
                                      tag stays. Only a browser notices, because the API is
                                      right the whole time.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from ui_mutants import run_mutants

JS = "internal/httpapi/assets/js/feature.js"
SUITE = "tests/e2e/feature_e2e.py"

MUTANTS = [
    ("the tags panel is never rendered",
     "    var tagList = (res[4] && res[4].tags) || [];",
     "    var tagList = [];"),
    ("the add-tag form is never wired",
     "    var form = document.getElementById('add-tag-form');\n    if (form) {",
     "    var form = null;\n    if (form) {"),
    ("the remove control posts to the add endpoint",
     "        send(base + '/features/' + encodeURIComponent(featureID) + '/tags/remove',",
     "        send(base + '/features/' + encodeURIComponent(featureID) + '/tags',"),
]

if __name__ == "__main__":
    sys.exit(run_mutants(JS, SUITE, MUTANTS, "feature.js"))

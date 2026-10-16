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

  §354's extra link (§26), three ways it is wrong and none of them visible to a Go test:

  the attach posts to the feature     the same swapped-path defect as above, one route over:
  instead of the link route           the POST lands on the feature resource, the server
                                      refuses it, and the page says so -- so the link is
                                      never written and the section still reads 1.

  the attach sends the wrong field    the server answers 400 for a body it cannot read, so
  name                                the request is well-formed HTTP and wrong JSON. A
                                      handler test posting the field the handler expects
                                      cannot see this; the page's own payload can.

  the select is never rendered        the panel silently becomes the "nothing to attach"
                                      paragraph, which reads as a fact about the project
                                      rather than as a missing control.
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
    ("the extra link posts to the feature instead of the link route",
     "      writer.send(base + '/features/' + encodeURIComponent(featureID) + '/complaints',",
     "      writer.send(base + '/features/' + encodeURIComponent(featureID),"),
    ("the extra link sends the wrong field name",
     "        { complaint_id: id }, form.querySelector('button[type=submit]'));",
     "        { complaint: id }, form.querySelector('button[type=submit]'));"),
    ("the attach select is never rendered",
     """    var eligible = (candidates || []).filter(function (c) {
      return linkedIds.indexOf(Number(c.id)) === -1;
    });""",
     "    var eligible = [];"),
]

if __name__ == "__main__":
    sys.exit(run_mutants(JS, SUITE, MUTANTS, "feature.js"))

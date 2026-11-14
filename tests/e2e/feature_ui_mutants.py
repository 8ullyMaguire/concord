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

And the DETACH (2026-11-12), which is the inverse of that link. It needs its own suite
because the detach test lives in `milestones_e2e.py` -- the join could grow and never
shrink, and the browser is the only place a control that renders but does nothing is
visible:

  the detach is never wired           the button renders, the click does nothing, the
                                      complaint stays attached, and nothing on the page
                                      says so. This is the defect the pass removed.

  the detach URL drops the id         DELETE .../complaints/ answers "No such API route",
                                      which reads like a missing route and is a missing
                                      path segment.

  the detach sends POST               the same silence, one method over: the route answers
                                      404 and the evidence stays.

Both suites run from this file. A SURVIVED line in the detach half means the fixture lost
its linked complaint, never that the wiring is unnecessary.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from ui_mutants import run_mutants

# The asset the candidate SERVES, not the Go tree's copy: since the port embeds `crates/web/assets`, mutating
# `internal/httpapi/assets/js/feature.js` would change a file nothing in the candidate reads.
JS = os.environ.get("CONCORD_UI_JS", "crates/web/assets/js/feature.js")
SUITE = "tests/e2e/feature_e2e.py"
DETACH_SUITE = "tests/e2e/milestones_e2e.py"

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

DETACH_MUTANTS = [
    ("the detach control is never wired",
     "var rows = root.querySelectorAll('[data-unlink-complaint]');",
     "var rows = [];"),
    ("the detach URL drops the complaint id",
     "'/complaints/' + encodeURIComponent(id), undefined, btn);",
     "'/complaints/', undefined, btn);"),
    ("the detach sends POST where the route answers DELETE",
     "writer.sendAs('DELETE',",
     "writer.sendAs('POST',"),
    # A FOURTH MUTANT WAS TRIED AND REMOVED, and the reason matters more than the count.
    # Inverting the `if (!id) return` guard survived, because every button this page
    # renders carries an id (`data-unlink-complaint="${c.id}"`), so no fixture can produce
    # a button without one. A mutant the suite cannot reach is not unproven code, it is a
    # missing fixture nothing needs -- and leaving it in would have made this gate print
    # UNPROVEN forever for a defect that has no shape on this page. If the button ever
    # starts rendering conditionally, the guard needs a fixture rather than a mutant.
]

if __name__ == "__main__":
    # Each set rebuilds the binary once per mutant, so the two sets together outlast a single outer timeout on a host that is also
    # building something else — the first full run was killed mid-DETACH with the TAG verdict already printed. `--only` makes the
    # sets independently runnable, so a slow host costs a re-run of the set that did not finish, not of both.
    import sys as _sys

    only = None
    if "--only" in _sys.argv:
        only = _sys.argv[_sys.argv.index("--only") + 1]
    rc = 0
    if only in (None, "tags"):
        rc |= run_mutants(JS, SUITE, MUTANTS, "feature.js")
    if only in (None, "detach"):
        rc |= run_mutants(JS, DETACH_SUITE, DETACH_MUTANTS, "feature.js (detach)")
    _sys.exit(rc)

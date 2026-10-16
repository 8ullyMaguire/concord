"""Mutation gate for §29's arena loop in project.js.

Each mutant breaks ONE thing the control has to get right, and each must be killed by
tests/e2e/arena_e2e.py. The suite runs on its own here rather than inside
project_ui_mutants.py because a mutant is only meaningful against the test that reads
the behaviour it breaks -- and the two bugs this pass really shipped (a control posting
to /arenas/undefined, and a Recompute button wired to nothing) were both invisible to
every Go test and to every other browser suite in the repo.

Four places a control like this goes wrong, one mutant each:
  - the PATH it fetches (a typo, or the neighbouring route),
  - the PAYLOAD it sends (the outcome is read from the wrong place and every vote is
    a draw),
  - whether the field the reader needs is RENDERED at all (the reason, the drift rows),
  - whether the control is WIRED at all (a click listener attached to an element that
    does not exist yet).
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from ui_mutants import run_mutants

JS = "internal/httpapi/assets/js/project.js"
SUITE = "tests/e2e/arena_e2e.py"

MUTANTS = [
    ("the pair control fetches a route that does not exist",
     "'/arenas/' + encodeURIComponent(arenaID) + '/next-pair'",
     "'/arenas/' + encodeURIComponent(arenaID) + '/next-pairs'"),
    ("the vote sends every comparison as a draw",
     "outcome: fd.get('outcome'),",
     "outcome: 'both',"),
    ("the vote log renders without the reason",
     "(v.reason ? '<span class=\"log-reason\">' + esc(v.reason) + '</span>' : '') +",
     "'' +"),
    ("the verdict is fetched from the vote-log route",
     "'/verification', { headers: apiAuth() })",
     "'/votes', { headers: apiAuth() })"),
    ("the repair posts to a route that does not exist",
     "postJSON(base + '/arenas/' + encodeURIComponent(id) + '/recompute', {})",
     "postJSON(base + '/arenas/' + encodeURIComponent(id) + '/recompute-now', {})"),
    ("the divergence rows are never rendered",
     ".filter(function (e) { return !e.agrees; })",
     ".filter(function (e) { return false; })"),
]

if __name__ == "__main__":
    sys.exit(run_mutants(JS, SUITE, MUTANTS, "project.js/arena"))

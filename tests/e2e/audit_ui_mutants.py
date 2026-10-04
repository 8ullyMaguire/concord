"""Mutation gate for the audit viewer page (docs/plans/audit-log.md step A5).

`internal/mutate.py` rewrites Go source and runs `go test`. This rewrites the
page's JAVASCRIPT and runs the browser suite, which is the only thing that can
kill these seven mutants -- every one of them is a client-side defect that a Go
test cannot observe, because the Go side serves the same 200 either way.

THE REASON THIS FILE EXISTS RATHER THAN BEING A ONE-OFF. The first run of these
mutants reported "detail unescaped" SURVIVED, and the correct reading was that
the fixture was inert, not that escaping was untestable. Three of the seven
needed fixture data carrying HTML in the column they attack before they died.
A gate nobody can re-run is a gate that reports one opinion once.

WHY EACH MUTANT IS HERE, and what it would have looked like shipping:

  detail unescaped                 `detail` is free text from ~20 AddAudit call
                                   sites. A caller-supplied `<script>` executes
                                   in every reader's session.
  actor display name unescaped     the same class, in the "Who" column.
  action name unescaped            the same class, in the action column.
  count line uses the FILTERED     the API's `total` is COUNT(*) over the
  total                            filtered set, so the page claimed "Showing
                                   all 5 entries" over a list of 1 -- telling
                                   the reader their filter excluded nothing.
  deleted actor shows its raw id   "user 987654" invites a reader to complain
                                   about an account that no longer exists.
  search sends text= not q=        filters nothing and returns every row,
                                   looking exactly like a filter that worked.
  filtered-empty says "no activity" "this project has no history" when it has
                                   seven rows and your filter missed them.

A SURVIVED line here means a missing fixture, not a bad mutant: each of these
produces byte-identical output against an inert row, so a surviving mutant is a
statement about the DATA, never about the escaping being unnecessary.
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
JS = os.path.join(REPO, "internal/httpapi/assets/js/audit.js")
SUITE = "tests/e2e/audit_e2e.py"

MUTANTS = [
    ("detail is written into innerHTML unescaped",
     '\'<p class="cell-note">\' + esc(e.detail) + \'</p>\'',
     '\'<p class="cell-note">\' + e.detail + \'</p>\''),
    ("the actor display name is written into innerHTML unescaped",
     "if (e.actor_display_name) return esc(e.actor_display_name);",
     "if (e.actor_display_name) return e.actor_display_name;"),
    ("the action name is written into innerHTML unescaped",
     '\'<td class="cell-title"><code>\' + esc(e.action) + \'</code>\'',
     '\'<td class="cell-title"><code>\' + e.action + \'</code>\''),
    ("the count line compares shown against the FILTERED total",
     "var of = filtered ? (unfilteredTotal != null ? unfilteredTotal : page.total) : page.total;",
     "var of = page.total;"),
    ("a deleted actor is rendered as its raw id instead of 'deleted account'",
     "return '<span class=\"dim\">deleted account</span>';",
     "return '<span class=\"dim\">' + esc(e.actor_id) + '</span>';"),
    ("the search sends text= instead of q=, so it filters nothing",
     "if (state.q) params.push('q=' + encodeURIComponent(state.q));",
     "if (state.q) params.push('text=' + encodeURIComponent(state.q));"),
    ("a filter matching nothing is reported as a project with no activity",
     "(filtered ? 'Nothing matches this filter' : 'No audit activity yet')",
     "'No audit activity yet'"),
]


def run(cmd, timeout):
    return subprocess.run(cmd, cwd=REPO, capture_output=True, text=True, timeout=timeout)


def main():
    original = open(JS).read()
    # A backup of the SOURCE, byte-compared on restore. A backup taken from a
    # tree already carrying a previous run's mutation is how a gate reports
    # "restore verified" against its own corruption.
    with tempfile.NamedTemporaryFile("w", suffix=".js", delete=False) as fh:
        fh.write(original)
        backup = fh.name

    killed, survived, not_applied = [], [], []
    try:
        for name, old, new in MUTANTS:
            mutated = original.replace(old, new)
            if mutated == original:
                not_applied.append(name)
                print(f"  NOT APPLIED  {name}")
                continue
            open(JS, "w").write(mutated)
            try:
                build = run(["go", "build", "-o", "bin/concord", "./cmd/concord"], 400)
                if build.returncode != 0:
                    survived.append(name)
                    print(f"  BUILD-RED    {name}")
                    continue
                # The binary embeds the JS, so a stale bin is not a flake, it is
                # a false PASS. Rebuilt every time above for exactly that reason.
                res = run(["python3", "-m", "pytest", "-p", "no:cacheprovider",
                           SUITE, "-q", "--no-header"], 600)
                if res.returncode == 0:
                    survived.append(name)
                    print(f"  SURVIVED     {name}")
                else:
                    killed.append(name)
                    tail = [l for l in res.stdout.strip().split("\n") if l.strip()]
                    print(f"  killed       {name}   ({tail[-1] if tail else ''})")
            finally:
                open(JS, "w").write(original)

        # The real binary from the real source, and a byte-compared restore.
        run(["go", "build", "-o", "bin/concord", "./cmd/concord"], 400)
        restored = open(JS).read() == open(backup).read() == original
        print(f"\n  source restored byte-identical: {restored}")
        print(f"[audit.js] ---- {len(killed)}/{len(MUTANTS)} killed, "
              f"{len(survived)} survived, {len(not_applied)} not applicable")
        if survived or not_applied or not restored:
            print("[audit.js] FAIL: the rules above are UNPROVEN. A surviving mutant "
                  "here is a missing fixture carrying hostile input, not a mutant "
                  "that is unnecessary.")
            return 1
        return 0
    finally:
        open(JS, "w").write(original)
        shutil.copy(backup, JS)
        os.unlink(backup)


if __name__ == "__main__":
    sys.exit(main())
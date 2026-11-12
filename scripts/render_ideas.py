#!/usr/bin/env python3
"""Render docs/IDEAS.md from seed/concord_ideas.json.

The JSON is the source of truth: it is what scripts/file_ideas.py uploads and what this renders,
so the document a reader opens and the rows on the instance cannot disagree. Re-run after editing
the seed:

    python3 scripts/render_ideas.py            # writes docs/IDEAS.md
    python3 scripts/render_ideas.py --check    # exits 1 if docs/IDEAS.md is stale
"""
import argparse
import json
import pathlib
import sys

REPO = pathlib.Path(__file__).resolve().parent.parent
SEED = REPO / "seed" / "concord_ideas.json"
OUT = REPO / "docs" / "IDEAS.md"


def render(data: dict) -> str:
    ideas = data["ideas"]
    has_value = any("value" in i for v in ideas.values() for i in v)
    total = sum(len(v) for v in ideas.values())
    out = []
    w = out.append

    w("# Concord — feature depth catalog\n")
    w(f"_Generated from `seed/concord_ideas.json` on {data['generated']}. "
      f"{len(data['shipped'])} shipped surfaces, {len(data['planned'])} planned milestones, "
      f"{total} filed ideas across {len(data['themes'])} themes._\n")
    w("Three parts, and the order matters:\n")
    w("1. **Shipped** — what the instance runs today, so a reader can tell an idea from an absence.")
    w("2. **Planned** — the milestones that already have a ticket, so the new list does not re-file them.")
    w("3. **Filed** — ideas that were NOT already on the roadmap, grouped by the complaint they answer.")
    w("")
    w("Every idea in part 3 exists on the live instance as a draft feature linked to that theme's "
      "complaint, filed by `scripts/file_ideas.py`. The effort tag is the filer's estimate "
      "(**S** hours, **M** a day or two, **L** a week, **XL** more), never a promise.\n")
    if has_value:
        w("Where an idea carries `V<n>`, that is its value out of ten and it is uploaded as the "
          "feature's `impact`, with `effort_score` as weeks, so the page's impact-over-effort "
          "order is this document's order. The filing rule was stated before the scores were "
          "written: **6 or more is filed as a draft, 3 to 5 is filed as `rejected` with the "
          "reason inside the body, below 3 is not worth a row.** An idea below the line is kept "
          "as a rejected row on purpose -- a decision recorded is a decision not re-litigated.\n")

    w("---\n")
    w("## Part 1 — shipped\n")
    by_area = {}
    for s in data["shipped"]:
        by_area.setdefault(s["area"], []).append(s)
    for area, items in by_area.items():
        w(f"### {area}\n")
        for s in items:
            w(f"- **{s['title']}** — {s['body']}")
        w("")

    w("---\n")
    w("## Part 2 — planned (already ticketed)\n")
    w("| id | milestone | what it does |")
    w("|---|---|---|")
    for p in data["planned"]:
        w(f"| {p['id']} | **{p['title']}** | {p['body']} |")
    w("")

    w("---\n")
    w(f"## Part 3 — filed: {total} ideas\n")
    w("Grouped by theme. The line under each heading is the complaint the ideas are filed against "
      "on the instance — the reason the group exists, in pain terms rather than feature terms.\n")

    counts = []
    for t in data["themes"]:
        items = ideas.get(t["key"], [])
        counts.append((t["label"], len(items)))
        w(f"### {t['label']} ({len(items)})\n")
        w(f"> **Complaint:** _{t['complaint_title']}._ {t['complaint_body']}\n")
        for i, idea in enumerate(items, 1):
            score = f"**V{idea['value']}** · " if "value" in idea else ""
            w(f"{i}. **{idea['title']}** ({score}{idea['effort']}) — {idea['body']}")
        w("")

    w("---\n")
    w("## Totals\n")
    w("| theme | ideas |")
    w("|---|---|")
    for label, n in counts:
        w(f"| {label} | {n} |")
    w(f"| **all** | **{total}** |")
    w("")
    w("Effort mix: "
      + ", ".join(f"{e} {sum(1 for v in ideas.values() for i in v if i['effort'] == e)}"
                  for e in ("S", "M", "L", "XL"))
      + ".\n")
    w("This file is generated. Edit `seed/concord_ideas.json`, then re-run "
      "`python3 scripts/render_ideas.py`; a test in `scripts/` fails when the two disagree.")
    return "\n".join(out) + "\n"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="fail if docs/IDEAS.md is out of date")
    args = ap.parse_args()

    data = json.loads(SEED.read_text())
    text = render(data)
    if args.check:
        current = OUT.read_text() if OUT.exists() else ""
        if current != text:
            print("docs/IDEAS.md is stale: re-run scripts/render_ideas.py", file=sys.stderr)
            return 1
        print("docs/IDEAS.md matches seed/concord_ideas.json")
        return 0
    OUT.write_text(text)
    print(f"wrote {OUT.relative_to(REPO)} ({len(text.splitlines())} lines)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

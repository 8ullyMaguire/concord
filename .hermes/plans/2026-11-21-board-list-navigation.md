# Plan: board and list navigation

1. Write `tests/e2e/board_list_nav_e2e.py` on port 8465, seeding a project with 30 board cards and a list with 30 entries plus duplicates. Reproduce RED against the current candidate.
2. In `crates/web/assets/js/board.js`, add `#board-filter` and `#board-show-all` controls above the columns; render only matching titles and cap each column at 50 unless show-all is on. Re-wire moves after every render.
3. In `crates/web/assets/js/list.js`, dedupe by `title|url` keeping the first (ranked) row, add `#list-filter` and `#list-page` controls with Prev/Next, page size 25, and keep `wire()` on the visible page.
4. Add small CSS for the controls. Rebuild once; rerun the targeted suite; record the real line.
5. Wire suite into gate; commit D-222; push; deploy; live DOM probe; rewrite notes.

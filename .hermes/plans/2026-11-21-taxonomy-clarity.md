# Plan: taxonomy clarity

1. Write `tests/e2e/taxonomy_clarity_e2e.py` on port 8464, seeding three `forum` rows and two `performance8103685` rows in namespace `topic`. Tests assert unique server-rendered labels, visible counts, and no digit-suffixed tooltip on interactive chips. Reproduce RED with the current candidate.
2. In `crates/web/src/top.rs`, add a display-name helper that strips trailing `#`/digits, group tags by `(namespace, display)`, sort by name, and render one link with a `.tag-count` badge showing the number of ratified rows.
3. In `crates/web/assets/js/taxonomy.js`, strip the same suffix from the visible chip label and title; keep `data-tag` as the original server name.
4. Add CSS for `.tag-count` if needed; rebuild once; rerun the targeted suite; record the real line.
5. Wire `taxonomy_clarity_e2e` into `scripts/gate.sh`; commit D-221; `make push` once; deploy in one background job; live DOM probe; rewrite notes.

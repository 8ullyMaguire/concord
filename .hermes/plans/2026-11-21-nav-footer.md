# Plan: navigation and footer

1. Write `tests/e2e/nav_footer_e2e.py` on port 8467. Reproduce RED against the candidate: no nav groups, footer with 0 links.
2. In `crates/web/src/shell.rs`, render the existing links inside three `role="group"` spans with visible labels; add a grouped footer with plain anchors and keep the version line.
3. In `crates/web/assets/css/style.css`, style the group labels/separators and a two-row footer without shrinking 44px targets.
4. Rebuild once; run the targeted suite; wire into the gate; commit D-224; push; deploy; live DOM probe; rewrite notes.

# Plan: settings field clarity

1. Write `tests/e2e/settings_fields_e2e.py` on port 8463 with a seeded public project. Tests assert humanized labels, four help paragraphs, sticky actions, dirty on edit, and Reset restoring the original value. Run it against the current candidate to reproduce RED: labels contain underscores, help count 0, no reset/dirty.
2. In `crates/web/assets/js/settings.js`: add `FIELD_HELP` for `glicko_tau`, `lam`, `mu`, `pain_halflife_days`; humanize visible labels with a `human()` helper; render help under the input; add Reset and a `data-dirty` live region; wire input/submit/reset in `wireSave()` without changing `input[name]`.
3. In `crates/web/assets/css/style.css`: add `.field-help` styling and `.charter-actions { position: sticky; bottom: 0; ... }`.
4. Rebuild candidate once in a tracked background job; rerun the same targeted suite; record the real summary.
5. Wire `settings_fields_e2e` into `scripts/gate.sh` after `a11y_axe_e2e`; keep `RECORDED_UNRUN = 0`. Commit D-220 in the roadmap Decisions section in the same commit.
6. `make push` once; record the gate line. `make -o release deploy` in one background job; live DOM probe on the public host; rewrite all state notes in place.

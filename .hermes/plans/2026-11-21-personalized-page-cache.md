# Plan: personalized pages must not be cached

1. RED: add `test_personalized_page_is_not_cacheable` to `tests/e2e/signed_in_visual_e2e.py`. It uses the signed fixture, fetches `/notifications` with credentials, and asserts the response `cache-control` is `no-store`. Run only this suite with `CONCORD_E2E_CANDIDATE=$PWD/target/debug/concord-cli scripts/parity/e2e_candidate.sh tests/e2e/signed_in_visual_e2e.py -q`; expect one failure naming the missing header.
2. GREEN: in `crates/web/src/lib.rs`, `render_with_unread` builds the `HtmlResponse`, then inserts `Cache-Control: no-store` on the response before returning. Rebuild the candidate in one tracked background job; rerun the same targeted suite; record the real summary line.
3. Commit code, test and docs together; record D-217 in the roadmap Decisions section.
4. `make push` once at the end; record the real gate line.
5. `make -o release deploy` in one tracked background job; live browser probe asserts the `/notifications` header and that a fresh minted session opens the page with no gate; revoke/delete the session.
6. Rewrite the state notes in place with the measured output.

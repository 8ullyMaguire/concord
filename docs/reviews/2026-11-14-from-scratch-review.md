# Concord, built from scratch: what I would do differently

Written 2026-11-14, after the port to Rust took over `concord.polarisocial.xyz` and the first five post-port roadmap steps landed.
This is a critique of the product as it exists, not of the port as a project: the port did what it set out to do, and most of what
follows is a critique of the design the port inherited and faithfully reproduced.

Every claim below is a measurement, and the command that produced it is in the sentence. Where I am guessing, it says so.

## Work log

**CI note:** run `43923118762` failed at `probe_pages` with `TimeoutError: The read operation timed out` against the CI's own
scratch server; the next run, `43925202819`, passed with the same tree shape. No product code changed between them — the
failure is a slow page read in CI, not a regression. Action if it recurs: raise `probe_pages`' socket timeout.

- **2026-11-17 — step 3 twenty-second module live (7207cc1).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: scout ?idea=… 200 dict 7
  anonymous_walk: 0 holes · headers PASS · CI 44133293671 success
  ```

- **2026-11-17 — step 3 twenty-third module live (213364c).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: projects 200 (73) · suki 200 dict · unknown slug 404 (existing envelope)
  anonymous_walk: 0 holes · headers PASS · CI 44137911389 success
  ```

- **2026-11-17 — step 3 twenty-fourth module live (f75a021).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: anon register {} 422 (missing username, no account created)
  anonymous_walk: 0 holes · headers PASS · CI 44142046977 success
  ```

- **2026-11-17 — step 3 twenty-fifth module live (986d263).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: anon create/archive/ai-involvement 403 consent_required
  anonymous_walk: 0 holes · headers PASS · CI 44150720321 success
  ```

- **2026-11-17 — step 3 twenty-sixth module live (373dfc5).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: features 200 (329) · anon POST/PUT 403 consent_required
  anonymous_walk: 0 holes · headers PASS · CI 44162505969 success
  ```

- **2026-11-17 — step 3 twenty-seventh module live (b4be69b).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: complaints 200 (332) · anon POST/impact 403 consent_required
  anonymous_walk: 0 holes · headers PASS · CI 44174995983 success
  ```

- **2026-11-17 — step 3 twenty-eighth move live (52bdda8).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: anon reject/merge 403 consent_required
  anonymous_walk: 0 holes · headers PASS · CI 44182701701 success
  ```

- **2026-11-17 — live defect after fef1a22: milestones 404 (D-47).** Live anon GET /projects/suki/milestones and
  /roadmap both answered 404 despite gate PASS. Cause: api/milestones.rs routes() was never merged into lib.rs.
  The route_policies gate was strengthened to require every routes() module to be mounted: RED unmerged: 1 - milestones,
  FAIL exit 1; after adding `.merge(api::milestones::routes())`, unmerged: 0, PASS.
- **2026-11-17 — step 3 thirtieth module live (62982b5).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: tags 200 (0) · completeness 200 dict · languages 200 (0) · anon writes 403
  route_policies: PASS — 30 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44199542168 success
  ```

- **2026-11-17 — step 3 thirty-first module live (edcfda8).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: criteria 200 (0) · profiles 200 (0) · charter 200 dict 22 · anon writes 403
  route_policies: PASS — 31 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44203187335 success
  ```

- **2026-11-18 — step 3 thirty-second module live (4fa3539).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: lists 200 (2) · anon POST/PUT 403 consent_required
  route_policies: PASS — 32 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44214531180 success
  ```

- **2026-11-18 — step 3 thirty-third module live (bd9c570).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: feature tags 200 dict 2 · anon POST/PUT 403 consent_required
  route_policies: PASS — 33 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44223857215 success
  ```

- **2026-11-18 — step 3 thirty-fourth module live (120099f).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: deps 200 dict 3 · evidence 200 list 0 · anon writes 403 consent_required
  route_policies: PASS — 34 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44228621160 success
  ```

- **2026-11-18 — step 3 thirty-fifth module live (7a695a6).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: consensus 200 list 0 · anon POST 403 consent_required
  route check: /consensus/abc 400 call id must be an integer (route matched)
  route_policies: PASS — 35 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44236198926 success
  ```

- **2026-11-18 — step 3 thirty-sixth module live (13411cd).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: requests 200 list 0 · project requests 200 list 0 · anon POST 403 consent_required
  route_policies: PASS — 36 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44242070130 success
  ```

- **2026-11-18 — step 3 thirty-seventh module live (cd0b084).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: anon POST answers / PUT accept / POST vote all 403 consent_required
  route_policies: PASS — 37 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44245668629 success
  ```

- **2026-11-18 — step 3 thirty-eighth module live (416c07d).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: threads 200 list 0 · anon POST/DELETE/vote 403 consent_required
  route_policies: PASS — 38 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44248263346 success
  ```

- **2026-11-18 — step 3 thirty-ninth module live (baab193).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: curation 200 dict 2 · anon arena remove/restore and vote 403 consent_required
  route_policies: PASS — 39 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44251396868 success
  ```

- **2026-11-18 — step 3 fortieth module live (48f3feb).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: search 200 · stats 200 · summary 200 · history 200 · responsiveness 200
  route_policies: PASS — 40 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44254461696 success
  ```

- **2026-11-18 — step 3 forty-first module live (1672c43).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: anon strategy reads 401 identity required · anon POST 403 consent_required
  route_policies: PASS — 41 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44257658683 success
  ```

- **2026-11-18 — step 3 forty-second module live (8af9d4f).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: solution-call 200 dict 9 · consensus alias 200 dict 9 · anon POST 403
  fallback bad id 400 proves route matched
  route_policies: PASS — 42 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44260547188 success
  ```

- **2026-11-18 — step 3 forty-third module live (b744df9).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: anon POST outcome 403 consent_required
  GET on POST-only path 500 route_declares_no_policy (pre-existing, three probes agree)
  route_policies: PASS — 43 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44263456328 success
  ```

- **2026-11-18 — step 3 forty-fourth module live (80fbdc1).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: anon override open/position/close all 403 consent_required
  route_policies: PASS — 44 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44267064538 success
  ```

- **2026-11-18 — step 3 forty-fifth move live (f20d15b).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: anon hold POST and release 403 consent_required
  byte-identity: hold_block_bytes_identical True · sha256 8efd25edf2540f57
  route_policies: PASS — 44 modules, 0 unmerged
  anonymous_walk: 0 holes · headers PASS · CI 44270030258 success
  ```

- **2026-11-18 — step 3 forty-sixth pure move: objection resolution into api/consensus.rs (D-64).** Request type,
  handler and one PUT route moved (26 lines); lib.rs 1816 -> 1785. Byte-identity proof passed. Policy 7 passed;
  0 failed, route_policies: 44 modules, 0 unmerged, replay 0 disagreed of 18 checked, 3 skipped.
- **2026-11-18 — step 3 forty-fifth pure move: hold handlers into api/emergency_holds.rs (D-63).** get_call_hold,
  HoldRequest and place_emergency_hold plus one route moved (75 lines); lib.rs 1895 -> 1816. Byte-identical block proof
  passed. Policy 7 passed; 0 failed, route_policies: 44 modules, 0 unmerged, replay 0 disagreed of 18 checked, 3 skipped.
- **2026-11-18 — step 3 forty-fourth pure move: api/overrides.rs (D-62).** Override window reader and three writes plus
  three routes moved (108 lines); lib.rs 2015 -> 1895. Emergency holds stayed separate. Policy 7 passed; 0 failed,
  route_policies: 44 modules, 0 unmerged, replay 0 disagreed of 16 checked, 5 skipped.
- **2026-11-18 — step 3 forty-third pure move: api/consensus_outcomes.rs (D-61).** Outcome request/ammendment types
  and recorder plus one route moved (65 lines); lib.rs 2083 -> 2015. Charter reader stayed root-owned. Policy 7 passed;
  0 failed, route_policies: 43 modules, 0 unmerged, replay 0 disagreed of 18 checked, 3 skipped.
- **2026-11-18 — step 3 forty-second pure move: api/solution_calls.rs (D-60).** Readiness, open and arena fallback
  plus three routes moved (128 lines); lib.rs 2223 -> 2083. Policy 7 passed; 0 failed, route_policies: 42 modules,
  0 unmerged, replay 0 disagreed of 16 checked, 5 skipped.
- **2026-11-18 — step 3 forty-first pure move: api/strategy.rs (D-59).** Strategy caller, proposals, consents,
  withdrawals, theme refusal mapper and seven routes moved (256 lines); lib.rs 2507 -> 2223. Shared refusal mappers
  stayed at the root. Policy 7 passed; 0 failed, route_policies: 41 modules, 0 unmerged, replay 0 disagreed of 18
  checked, 3 skipped.
- **2026-11-18 — step 3 fortieth pure move: api/stats.rs (D-58).** Stats and search plus five public GET routes moved
  (137 lines); lib.rs 2649 -> 2507. Policy 7 passed; 0 failed, route_policies: 40 modules, 0 unmerged, replay 0
  disagreed of 18 checked, 3 skipped.
- **2026-11-18 — step 3 thirty-ninth pure move: api/moderation.rs (D-57).** Moderation trust gate, arena remove/restore
  and curation votes plus three routes moved (246 lines); lib.rs 2904 -> 2649. Policy 7 passed; 0 failed,
  route_policies: 39 modules, 0 unmerged, replay 0 disagreed of 16 checked, 5 skipped.
- **2026-11-18 — step 3 thirty-eighth pure move: api/comments.rs (D-56).** Thread reads and comment writes plus three
  routes moved (95 lines); lib.rs 3013 -> 2904. Policy 7 passed; 0 failed, route_policies: 38 modules, 0 unmerged,
  replay 0 disagreed of 18 checked, 3 skipped.
- **2026-11-18 — step 3 thirty-seventh pure move: api/answers.rs (D-55).** Accept badge, answers and vote writes plus
  three routes moved (217 lines); lib.rs 3240 -> 3013. Policy 7 passed; 0 failed, route_policies: 37 modules,
  0 unmerged, replay 0 disagreed of 16 checked, 5 skipped.
- **2026-11-18 — step 3 thirty-sixth pure move: api/requests.rs (D-54).** Request filing, duplicate gate, similar search
  and tags plus four routes moved (323 lines); lib.rs 3576 -> 3240. Answers left for the next slice. Policy 7 passed;
  0 failed, route_policies: 36 modules, 0 unmerged, replay 0 disagreed of 16 checked, 5 skipped.
- **2026-11-18 — step 3 thirty-fifth pure move: api/consensus.rs (D-53).** Reads and four writes moved in two slices
  around shared root helpers (225 lines); lib.rs 3824 -> 3576. Policy 7 passed; 0 failed, route_policies: 35 modules,
  0 unmerged, replay 0 disagreed of 16 checked, 5 skipped.
- **2026-11-18 — step 3 thirty-fourth pure move: api/feature_work.rs (D-52).** Work-item, dependency and evidence writes
  plus five routes moved to EOF (231 lines); lib.rs 4075 -> 3824. Shared helper check passed before the cut. Policy 7
  passed; 0 failed, route_policies: 34 modules, 0 unmerged, replay 0 disagreed of 16 checked, 5 skipped.
- **2026-11-18 — step 3 thirty-third pure move: api/feature_meta.rs (D-51).** Tag/milestone/strategic-weight writes and
  four routes moved (173 lines); lib.rs 4242 -> 4075. RED: five modules import caller_feature from the root; returned it
  to lib.rs. Policy 7 passed; 0 failed, route_policies: 33 modules, 0 unmerged, replay 0 disagreed of 16 checked,
  5 skipped.
- **2026-11-18 — step 3 thirty-second pure move: api/lists.rs (D-50).** List and project-settings writes plus six routes
  moved (261 lines); lib.rs 4517 -> 4242. A script assert aborted after two of three writes; the module was rebuilt from
  the HEAD slice. Compiler named three missing imports. Policy 7 passed; 0 failed, route_policies: 32 modules, 0 unmerged,
  replay 0 disagreed of 16 checked, 5 skipped.
- **2026-11-17 — step 3 thirty-first pure move: api/criteria.rs (D-49).** Criteria writes, charter update, helper and
  seven routes moved (278 lines); lib.rs 4810 -> 4517. Compiler named 10 missing root imports; fixed. Module merged;
  route_policies: 31 modules, 0 unmerged. Policy 7 passed; 0 failed, replay 0 disagreed of 18 checked, 3 skipped.
- **2026-11-17 — step 3 thirtieth pure move: api/project_settings.rs (D-48).** Four settings writes and four routes moved
  (159 lines); lib.rs 4978 -> 4810. Readers stay root-owned, GET+PUT routes move with writes. Module merged in the same
  edit; route_policies: 30 modules, 0 unmerged. Policy 7 passed; 0 failed, replay 0 disagreed of 16 checked, 5 skipped.
- **2026-11-17 — step 3 twenty-ninth pure move: api/milestones.rs (D-46).** Milestone reads, roadmap, helpers, six writes
  and seven routes moved (227 lines plus 27 route lines); lib.rs 5231 -> 4977. RED: naive route rewrite produced nested
  axum::routing calls and a duplicate roadmap route; fixed to `.method()` chaining. Policy 7 passed; 0 failed,
  route_policies PASS, replay 0 disagreed of 16 checked, 5 skipped.

- **2026-11-17 — step 3 twenty-ninth move live (b635794).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: milestones 200 (0) · roadmap 200 · anon writes 403 consent_required
  route_policies: PASS — api route modules: 29; unmerged: 0
  anonymous_walk: 0 holes · headers PASS · CI 44194647904 success
  ```

- **2026-11-17 — step 3 twenty-eighth pure move: complaint verdicts into api/complaints.rs (D-45).** reject and
  merge_complaints (62 lines) plus two routes moved; lib.rs 5301 -> 5231. Byte-identical block proven by SHA-256 of the
  rstripped source slice. Policy 7 passed; 0 failed, route_policies PASS, replay 0 disagreed of 16 checked, 5 skipped.

- **2026-11-17 — step 3 twenty-seventh pure move: api/complaints.rs (D-44).** Public list and three write handlers plus
  four routes moved (209 lines); lib.rs 5521 -> 5301 (corrected: the two moved routes also left lib.rs). RED: 14 missing imports and two routes still bound in lib.rs;
  moved the impact/validate routes into the module and imported compiler-named root helpers. Policy 7 passed; 0 failed,
  route_policies PASS, replay 0 disagreed of 16 checked, 5 skipped.

- **2026-11-17 — step 3 twenty-sixth pure move: api/features.rs (D-43).** Feature reads/writes and three routes moved
  (257 lines); lib.rs 5789 -> 5521. RED: over-wide cut included complaints/milestones/roadmap handlers (E0425 cname);
  reverted and recut at the next resource doc comment. Shared FeatureQuery/write_error stayed at root. Policy 7 passed;
  0 failed, route_policies PASS, replay 0 disagreed of 18 checked, 3 skipped.

- **2026-11-17 — step 3 twenty-fifth pure move: api/project_lifecycle.rs (D-42).** Create/claim/archive/unarchive/AI
  handlers plus helper and two request structs moved (274 lines); lib.rs 6070 -> 5789. RED: policy test searched only
  lib.rs for create_project; fixed to use the module-aware source list. Mutation: breaking is_guest( gives E0425, restore
  gives policy 7 passed; 0 failed. route_policies PASS, replay 0 disagreed of 16 checked, 5 skipped.

- **2026-11-17 — step 3 twenty-fourth pure move: api/accounts.rs (D-41).** Registration handler, request struct and route
  moved (79 lines); lib.rs 6149 -> 6070. No RED; TOKEN_NOTE kept at the root for login. Policy 7 passed; 0 failed,
  route_policies PASS (203 declared), replay 0 disagreed of 18 checked, 3 skipped.

- **2026-11-17 — step 3 twenty-third pure move: api/catalog.rs (D-40).** Two catalogue reads moved (20 lines);
  lib.rs 6170 -> 6149. RED: route_policies.py crashed because its router-end marker was the moved `list_projects` handler;
  scanner now finds the first function after router() and sees all three /projects method routes. Policy 7 passed; 0 failed,
  route_policies PASS, replay 0 disagreed of 16 checked, 5 skipped.

- **2026-11-17 — step 3 twenty-second pure move: api/scout.rs (D-39).** Report, coverage helper and query struct moved
  whole (149 lines); lib.rs 6319 -> 6170. RED: missing State and bearer_user imports; fixed. Policy 7 passed; 0 failed,
  route_policies PASS, replay 0 disagreed of 18 checked, 3 skipped.

- **2026-11-17 — step 3 twenty-first pure move: api/forgejo_webhook.rs (D-38).** Handler, HMAC helpers and date parser
  moved (397 lines); RED: unresolved import crate::app_mutant — the mutation helper lived inside this section but belongs to
  the crate, so it was hoisted to lib.rs as the only definition. No refusal text changed. Policy 7 passed; 0 failed,
  route_policies PASS, replay 0 disagreed of 18 checked, 3 skipped.

- **2026-11-17 — step 3 twentieth pure move: `api/ops_writes.rs` (D-37).** Arena rating replay + route moved verbatim
  (58 lines); `lib.rs` 6,766 → 6,705. No RED: policy `7 passed; 0 failed`, `route_policies: PASS`, replay
  `0 disagreed of 16 checked, 5 skipped`. Mutation branch and membership-before-role order unchanged (D-35).

- **2026-11-17 — step 3 nineteenth pure move: `api/taxonomy.rs` (D-36).** Five handlers and three routes moved verbatim
  (174 lines); `lib.rs` 6,948 → 6,766. RED: 15 unresolved imports (`Deserialize`, `Query`, `HeaderMap`, `StatusCode`,
  `bearer_user`, `role_rank`); fixed, `cargo check` green, policy `7 passed; 0 failed`, `route_policies: PASS`,
  replay `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 eighteenth pure move: `api/emergency_holds.rs` (D-35).** Release handler, refusal mapper and route
  moved verbatim (57 lines); `lib.rs` 7,008 → 6,948. No RED: policy `7 passed; 0 failed`, `route_policies: PASS`,
  replay `0 disagreed of 16 checked, 5 skipped`. Mutation-sensitive ordering and `CONCORD_MUTANT` branch untouched.

- **2026-11-17 — step 3 seventeenth pure move: `api/field_reports.rs` (D-34).** Read half (17 lines) and write half
  (112 lines) reunited in one module; `lib.rs` 7,144 → 7,008. No RED — earlier visibility promotions supplied every dependency.
  Policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 sixteenth pure move: `api/contributions.rs` (D-33).** Feed + two reads + three helpers moved
  verbatim (403 lines); `lib.rs` 7,550 → 7,144. Cut before the field-report doc comment (D-28/D-33); no route behavior change.
  No RED: `cargo check` green, policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 fifteenth pure move: `api/identity.rs` (D-32).** D-31 unblock landed first (`a26e5e5`): policy
  test now brace-matches `auth_gate` across all sources, `7 passed; 0 failed`. Then the whole consent/guest/login/middleware
  section moved (589 lines); `lib.rs` 8,141 → 7,550. RED: missing imports (`policy`, `TOKEN_NOTE`, `Value`, `IntoResponse`)
  and lib.rs middleware names; fixed. `credential_tests` moved with it: `4 passed; 0 failed`; policy `7 passed; 0 failed`,
  `route_policies: PASS`, replay `0 disagreed of 16 checked, 5 skipped`.

- **2026-11-17 — step 3 fourteenth pure move: `api/ops_reads.rs` (D-31).** Three operator reads moved verbatim (47 lines);
  `lib.rs` 8,190 → 8,141. The identity section was deferred because it carries cross-cutting middleware and a policy-test
  anchor; its unblock is recorded in D-31. No RED: `cargo check` green, policy `7 passed; 0 failed`, `route_policies: PASS`,
  replay `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 thirteenth pure move: `api/alternatives.rs` (D-30).** The S2 section — four handlers, arena
  resolver and both refusal maps — moved verbatim (256 lines); `lib.rs` 8,457 → 8,190. RED: 21 unresolved imports
  (`Deserialize`, `StatusCode`, `readable`, `require_role`); fixed, `cargo check` green, policy `7 passed; 0 failed`,
  `route_policies: PASS`, replay `0 disagreed of 16 checked, 5 skipped`.

- **2026-11-17 — step 3 twelfth pure move: `api/documents.rs` (D-29).** Document reads (near line 610) and writes (§5.7)
  moved together (155 lines); `lib.rs` 8,623 → 8,457. RED: `DefaultBodyLimit` is in `axum::extract`, not `axum::routing`;
  fixed, and the now-unused import in `lib.rs` removed. `cargo check` green, policy `7 passed; 0 failed`,
  `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 eleventh pure move: `api/merge_requests.rs` (D-28).** The merge half of the §5.7 source section
  — five handlers, `merge_id`, `merge_refusal` — moved verbatim (182 lines); `lib.rs` 8,811 → 8,623. Document writes that
  followed in the same section stayed for their own module. RED: `cannot find type StatusCode`; fixed by importing it.
  `cargo check` green, policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 tenth pure move: `api/feature_votes.rs` (D-27).** The §5.1 feature-vote section — request struct,
  `cast_feature_vote`, `feature_vote_refusal` — moved verbatim (104 lines); `lib.rs` 8,915 → 8,811. Kept separate from
  `api/arena_votes.rs` because the arenas rank different entities (D-27). No RED: `cargo check` green, policy
  `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 ninth pure move: `api/coverage.rs` (D-26).** The whole §6.3/§6.4 section — two requests, two
  handlers, `solution_in_feature` and `coverage_refusal` — moved verbatim (114 lines); `lib.rs` 9,036 → 8,915. No
  promotion for the section-private helper; no RED. `cargo check` green, policy `7 passed; 0 failed`,
  `route_policies: PASS`, replay `0 disagreed of 16 checked, 5 skipped`.

- **2026-11-17 — step 3 eighth pure move: `api/arena_votes.rs` (D-25).** The §5.1 solution-vote section — request struct,
  `vote_solutions`, `vote_refusal` — moved verbatim (125 lines); `lib.rs` 9,164 → 9,036. All four borrowed helpers were
  already `pub(crate)`, so no RED: `cargo check` green, policy `7 passed; 0 failed`, `route_policies: PASS`, replay
  `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 seventh pure move: `api/solutions.rs` (D-24).** The three solution reads (~4,070–4,200) and
  the §6.3 write/refusal moved into one module (261 lines); `lib.rs` 9,436 → 9,164. All three route stanzas, including
  GET+POST on one path, now come from the module. RED: `cannot find STANDINGS_PER_FEATURE`; fixed by importing it.
  `cargo check` green, policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 sixth pure move: `api/badges.rs` (D-23).** The whole §3.4 badge section — `badge_svg`,
  `badge_svg_text` and the XML escaper — moved verbatim (191 lines); `lib.rs` 9,627 → 9,436. RED: 9 unresolved imports
  (`internal` ×6, `readable`, `StatusCode`, `app_mutant`); fixed with `crate`/`axum` imports and a `pub(crate)` promotion.
  `cargo check` green, policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 fifth pure move: `api/invites.rs` (D-22).** The whole `invites (§8.2)` section — four handlers,
  two request structs and `invite_refusal` — moved verbatim (175 lines); `lib.rs` 9,810 → 9,627. Two helpers promoted
  `pub(crate)`; the GET+POST `/invites` route stayed a single MethodRouter. No RED: `cargo check` green, policy
  `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 16 checked, 5 skipped`.

- **2026-11-17 — step 3 fourth pure move: `api/notifications.rs` (D-21).** All three notification handlers and their
  three route stanzas moved verbatim (45 lines); `lib.rs` 9,866 → 9,810. Two REDs: body lookup `mark_all_notifications_read exists`,
  then route oracle `pinned but no (method, path) row is enforced`. Fixed by searching `api/*.rs` for bodies and using
  `router_body()` for routes; policy `7 passed; 0 failed`, `route_policies: PASS`, replay `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 third pure move: `api/board.rs` (D-20).** `get_board`, `move_card` and both query structs
  moved verbatim (97 lines); `lib.rs` 9,967 → 9,866. Board PUT left its old `MethodRouter` chain and joined the
  module's `routes()`; paths are distinct so dispatch order is unchanged. RED: `pub mod board;` before `//!` module
  docs gave E0753; fixed by placing declarations after the docs. `cargo check`: `Finished dev profile`;
  policy `7 passed; 0 failed`; `route_policies: PASS`; parity replay `0 disagreed of 18 checked, 3 skipped`.

- **2026-11-17 — step 3 second pure move: `api/records.rs` (D-19).** `document_kinds` and `search_documents`
  moved verbatim with their two routes; `lib.rs` 10,020 → 9,967 lines. RED: `use super::{…}` gave
  `E0432: no AppState in api`; switched to `crate::{…}`, and `project_for` became `pub(crate)` so the
  module borrows the shared visibility rule. `cargo check`: `Finished dev profile`; policy `7 passed; 0 failed`;
  `route_policies: PASS`; parity replay `0 disagreed of 18 checked, 3 skipped`.
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live d96eeb8: /version equality True · kinds 200 dict 1 · search 200 list 2
  anonymous_walk: 0 holes · headers PASS · CI 44065751641 success
  ```

- **2026-11-17 — step 3 third module live (`4ea99f5`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: board 200 dict cards 329 · anon move 403 consent_required · version equality True
  anonymous_walk: 0 holes · headers PASS · CI 44067946502 success
  ```

- **2026-11-17 — step 3 fourth module live (`79240b0`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: notifications 401 unauthenticated · read-all and read 403 consent_required
  anonymous_walk: 0 holes · headers PASS · CI 44070134200 success
  ```

- **2026-11-17 — step 3 fifth module live (`cd9f396`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: invites 401 unauthenticated · redeem 401 unauthenticated · version equality True
  anonymous_walk: 0 holes · headers PASS · CI 44072419927 success
  ```

- **2026-11-17 — step 3 sixth module live (`66e38b5`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: badge instance 200 (838 bytes) · missing 404 without slug echo
  anonymous_walk: 0 holes · headers PASS · CI 44074509285 success
  ```

- **2026-11-17 — step 3 seventh module live (`41303eb`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: project solutions 200 dict 4 · feature solutions 200 · anon POST 403 consent_required
  anonymous_walk: 0 holes · headers PASS · CI 44076613424 success
  ```

- **2026-11-17 — step 3 eighth module live (`5ad8ff3`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: solution vote anon 403 consent_required · version equality True
  anonymous_walk: 0 holes · headers PASS · CI 44078651040 success
  ```

- **2026-11-17 — step 3 ninth module live (`bbb2d46`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: coverage claim anon 403 · contest anon 403 · version equality True
  anonymous_walk: 0 holes · headers PASS · CI 44080948764 success
  ```

- **2026-11-17 — step 3 tenth module live (`2bb0667`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: feature vote anon 403 consent_required · version equality True
  anonymous_walk: 0 holes · headers PASS · CI 44083552359 success
  ```

- **2026-11-17 — step 3 eleventh module live (`97ee6b7`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: merge list 200 [] · anon approve 403 consent_required
  anonymous_walk: 0 holes · headers PASS · CI 44085897053 success
  ```

- **2026-11-17 — step 3 twelfth module live (`d02e7ff`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: doc list 200 (175) · doc one 200 · anon PUT 403 consent_required
  anonymous_walk: 0 holes · headers PASS · CI 44089787805 success
  ```

- **2026-11-17 — step 3 thirteenth module live (`6eef770`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: alternatives 200 dict 2 · anon create 403 consent_required
  anonymous_walk: 0 holes · headers PASS · CI 44093504753 success
  ```

- **2026-11-17 — step 3 fourteenth module live (`06eeb8f`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: admin-ledger anon 401 · rising 200 [] · lists 200 (2)
  anonymous_walk: 0 holes · headers PASS · CI 44098171339 success
  ```

- **2026-11-17 — step 3 fifteenth module live (`012fad1`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: guest 200 (8) · consent anon 200 · login anon 422 · logout anon 204
  anonymous_walk: 0 holes · headers PASS · CI 44102040582 success
  ```

- **2026-11-17 — step 3 sixteenth module live (`a92a3d7`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: contribute next 200 (5) · work items 200 (329) · votes next 401 · POST join 403
  GET on POST-only join: 500 route_declares_no_policy (intended, method not declared)
  anonymous_walk: 0 holes · headers PASS · CI 44106144002 success
  ```

- **2026-11-17 — step 3 seventeenth module live (`9634925`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: field reports GET 200 (5) · POST anon 403 · response anon 403
  anonymous_walk: 0 holes · headers PASS · CI 44109701061 success
  ```

- **2026-11-17 — step 3 eighteenth module live (`735d7f4`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: anon POST release(999999) 403 consent_required
  anonymous_walk: 0 holes · headers PASS · CI 44114112184 success
  ```

- **2026-11-17 — step 3 nineteenth module live (`5d0a04b`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: tags 200 (68) · proposals 200 (0) · anon propose 403 · anon consent 403
  anonymous_walk: 0 holes · headers PASS · CI 44118300295 success
  ```

- **2026-11-17 — step 3 twenty-first module live (6daf820).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: anon webhook 503 (no secret configured — deliberate refusal)
  anonymous_walk: 0 holes · headers PASS · CI 44127516322 success
  ```

- **2026-11-17 — step 3 twentieth module live (`96b433b`).**
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live: anon recompute(nonexistent) 403 consent_required
  anonymous_walk: 0 holes · headers PASS · CI 44123442614 success
  ```

- **2026-11-17 — step 3 first pure move: `api/meta.rs` (D-18).** `healthz` and `version` plus their three routes moved
  out of `lib.rs` verbatim; `lib.rs` 10,043 → 10,020 lines. RED measurements: `routes() -> Router` gave 177 E0308
  errors (whole chain inferred `Router<()>`); after typing it `Router<AppState>`, `cargo check` passed but the policy
  test failed with `2 stale row(s): GET /api/v1/healthz, GET /api/v1/version` (`6 passed; 1 failed`). Both route
  scanners now include `api/*.rs`; `7 passed; 0 failed`, `route_policies: PASS`. Corpus replay `0 disagreed`.
  ```
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live ed1488c: /version equality True · /healthz 200 · /api/v1/healthz 200
  anonymous_walk: 0 holes · headers PASS · CI 44063176583 success
  ```

- **2026-11-17 — step 2b twenty-ninth slice: the whole project page is a node tree (D-16 closed).** New
  `project-render.js` owns panels, forms, arena controls, work items and `render()`; `project.js` keeps loading, failure
  and wire. `check_dom_project.js` covers the populated page with hostile values; behavioral RED with an obfuscated
  `node['innerHTML']` write, restore → PASS. Census 2→1 (only the allowlisted Markdown sink). D-14 superseded: arena
  controls are nodes in this page.
  ```
  gate: FAIL on 300945e — error[E0748] crates/web/src/lib.rs:512 (unterminated raw string)
  fix 4fce32c: restored "#. cargo check -p concord-web --locked -> Finished dev profile
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 1 · allowlisted 1
  live 4fce32c: served renderer PASS checker · page loads project-render.js · walk 0 holes · CI 44059916800 success
  ```

- **2026-11-17 — step 2b twenty-eighth slice: `project.js` outer states are nodes.** Empty-slug, skeleton, 404, 401 and
  load-failure states use `el()`; `skeleton.js` gained `titleNode()`/`textNode()`. `check_dom_project.js` RED/GREEN;
  census 7→2 (1 allowlisted). Main `render()` remains a string; D-16 still open for it.
  ```
  check_dom_project outer states: PASS — slug is text, skeleton and refusals are nodes
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 2 · allowlisted 1
  live 6267468: served assets PASS checker · walk 0 holes · CI 44054471991 success
  ```

- **2026-11-17 — step 2b twenty-seventh slice: `curation.js` has no string sink.** The list refresh clear became
  `replaceChildren()`; `check_dom_curation.js` RED/GREEN covered hostile prose, append/refresh, vote, undo and both
  failure paths. Census 8→7 (1 allowlisted).
  ```
  check_dom_curation: PASS — hostile prose is text, refresh clears, votes and undo kept
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 7 · allowlisted 1
  live d76a47e: served assets PASS checker · walk 0 holes · CI 44050456868 success
  ```

- **2026-11-17 — step 2b twenty-sixth slice: `scout.js` and `merge.js` have no string sinks.** Scout clears became
  `replaceChildren()`; merge rows, gate cell, create form and failure state are `el()` trees. Combined checker RED/GREEN
  on both files; census 12→8 (1 allowlisted).
  ```
  check_dom_scout_merge: PASS — hostile fields are text, gates and counts verbatim
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 8 · allowlisted 1
  live 7319005: served assets PASS checker · walk 0 holes · CI 44047996973 success
  ```

- **2026-11-17 — step 2b twenty-fifth slice: `lists.js` has no string sink.** Cards, empty state and both failure states
  are `el()` trees; `check_dom_lists.js` RED/GREEN covered hostile fields and failure paths. Census 15→12 (1 allowlisted).
  ```
  check_dom_lists: PASS — hostile fields are text, href verbatim, all failure states kept
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 12 · allowlisted 1
  live 7e207af: served assets PASS checker · walk 0 holes · CI 44045226573 success
  ```

- **2026-11-17 — step 2b twenty-fourth slice: `project-writes.js` has no string sink.** The duplicate panel is filled with
  `el()` trees; its `panel()` string contract stays for `project.js` (D-16). `check_dom_project_writes.js` RED/GREEN covered
  hostile matches, coverage caveat, refusals and both field-report writes. Census 19→15 (1 allowlisted).
  ```
  check_dom_project_writes: PASS — hostile duplicates are text, hrefs verbatim, all refusals kept
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 15 · allowlisted 1
  live 6800e66: served assets PASS checker · walk 0 holes · CI 44043109307 success
  ```

- **2026-11-17 — step 2b twenty-third slice: both milestone pages have no string sink.** Roadmap totals/timeline/form and
  milestone subtitle/groups/candidates are `el()` trees; `check_dom_milestones.js` RED/GREEN covered hostile fields, create,
  reorder and assign. Census 24→19 (1 allowlisted).
  ```
  check_dom_milestones: PASS — hostile titles/errors are text, dates and provenance kept
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 19 · allowlisted 1
  live bbb1ff5: served assets PASS checker · walk 0 holes · CI 44034605439 success
  ```

- **2026-11-17 — step 2b twenty-second slice: `new-project.js` has no string sink.** Intent answers, no-match hint,
  hit list, clear and failure states are `el()` trees; `check_dom_new_project.js` RED/GREEN covered hostile hits and
  all four create-form refusals. Census 29→24 (1 allowlisted).
  ```
  check_dom_new_project: PASS — hostile hits are text, hrefs verbatim, all refusals kept
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 24 · allowlisted 1
  live c380518: served assets PASS checker · walk 0 holes · CI 44032325486 success
  ```

- **2026-11-17 — step 2b twenty-first slice: `search.js` has no string sink.** Result cards, health bars, tag chips,
  skeleton and both empty/failure states are `el()` trees; `check_dom_search.js` RED/GREEN covered hostile fields and
  chip toggle semantics. Census 34→29 (1 allowlisted).
  ```
  check_dom_search: PASS — hostile results and tags are text, hrefs verbatim
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 29 · allowlisted 1
  live bf06d9a: served assets PASS checker · walk 0 holes · CI 44030180561 success
  ```

- **2026-11-17 — step 2b twentieth slice: `board.js` has no string sink.** Columns/cards, move controls, milestone
  filter, skeleton and all empty/error states are `el()` trees; `check_dom_board.js` RED/GREEN covered hostile fields,
  a refused move and five other states. Census 41→34 (1 allowlisted).
  ```
  check_dom_board: PASS — hostile columns/cards and refusals are text, attrs verbatim
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 34 · allowlisted 1
  live e91c55c: served asset PASS checker · walk 0 holes · CI 44027725594 success
  ```

- **2026-11-17 — step 2b nineteenth slice: `documents.js` has no string sink.** Rail, list, reader, TOC, lazy sections,
  skeleton and error states are `el()` trees; the single Markdown sink moved into `ConcordMarkdown.renderInto` (D-15).
  `check_dom_documents.js` RED mutation and GREEN restore covered hostile fields, search and the large-document path.
  Census 49→41 (1 allowlisted).
  ```
  check_dom_documents: PASS — hostile fields are text, one allowlisted markdown sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 41 · allowlisted 1
  live 26be342: served assets PASS checker · walk 0 holes · CI 44025387898 success
  ```

- **2026-11-17 — step 2b eighteenth slice: `project-arena.js` has no string sink.** Pair form, vote log, verification
  verdict and all four note states are `el()` trees; `ConcordArena.controls()` stays a string (D-14, project.js
  concatenation). `check_dom_arena.js` RED mutation and GREEN restore covered hostile fields, recompute and refusals.
  Census 57→49.
  ```
  check_dom_arena: PASS — hostile pair/log/verdict and refusal are text, attrs verbatim
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 49
  live c5df26f: served asset PASS checker · walk 0 holes · CI 44023086042 success
  ```

- **2026-11-17 — step 2b seventeenth slice: `rank.js` has no string sink.** Cards, actions, keyboard hint, sign-in
  prompt and all refusal states are `el()` trees. `check_dom_rank.js` RED mutation and GREEN restore covered eight
  scenarios with hostile fields. Census 66→57.
  ```
  check_dom_rank: PASS — hostile cards and all refusal states are text, links verbatim
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 57
  live a6a9af3: served asset PASS checker · walk 0 holes · CI 44020904994 success
  ```

- **2026-11-17 — step 2b sixteenth slice: `finder.js` has no string sink.** Seed chips, options, stop prompt,
  shortlist, results and 401 refusal are `el()` trees; one shared top-candidate builder. `check_dom_finder.js` RED
  mutation and GREEN restore walked the whole flow with hostile fields. Census 77→66.
  ```
  check_dom_finder: PASS — hostile fields and refusal are text, hrefs and data attrs verbatim
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 66
  live 2615564: served asset PASS checker · walk 0 holes · CI 44019018091 success
  ```

- **2026-11-17 — step 2b fifteenth slice: `stats.js` has no string sink.** Facet tables, latency cards, both
  leaderboards and four failure states are `el()` trees; `check_dom_stats.js` RED mutation and GREEN restore measured
  with hostile fields and failing fetches. Census 87→77.
  ```
  check_dom_stats: PASS — hostile facets and projects are text, hrefs verbatim, failure states kept
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 77
  live cf3ca57: served asset PASS checker · walk 0 holes · CI 44016490431 success
  ```

- **2026-11-17 — step 2b fourteenth slice: `taxonomy.js` has no string sink.** Tag chips/groups, proposal cards/lists,
  propose form and live region are `el()` trees; `check_dom_taxonomy.js` RED mutation and GREEN restore measured with
  hostile fields and a hostile refusal. Census 90→87.
  ```
  check_dom_taxonomy: PASS — hostile tags and refusal are text, attrs verbatim, no string sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 87
  live cf4ea03: served asset PASS checker · walk 0 holes · CI 44014472354 success
  ```

- **2026-11-17 — step 2b thirteenth slice: `settings.js` has no string sink.** All panel builders and the live region are
  `el()` trees on the 1024-line page; the wire half is unchanged. `check_dom_settings.js` RED mutation and GREEN restore
  measured with hostile values from nine reads, a hostile refusal and all wire hooks asserted. Census 92→90.
  ```
  check_dom_settings: PASS — hostile fields and refusal are text, all wire hooks kept, no string sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 90
  live cf30d00: served asset PASS checker · walk 0 holes · CI 44012704225 success
  ```

- **2026-11-17 — step 2b twelfth slice: `requests-index.js` has no string sink.** Cards, open/settled groups and both empty
  states are `el()` trees; `check_dom_requests_index.js` RED mutation and GREEN restore measured with hostile titles, a
  duplicate id and an empty list. Census 95→92.
  ```
  check_dom_requests_index: PASS — hostile titles are text, dedupe and groups kept, no string sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 92
  live db6a087: served asset PASS checker · walk 0 holes · CI 44011216475 success
  ```

- **2026-11-17 — step 2b eleventh slice: `requests.js` has no string sink.** Rating bars, answer cards, compare panel,
  forms and error state are `el()` trees; `check_dom_requests.js` RED mutation and GREEN restore measured with hostile
  fields and a hostile post refusal.  Census 97→95.
  ```
  check_dom_requests: PASS — hostile fields and refusal are text, answer/compare controls kept, no string sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 95
  live 7ba4e48: served asset PASS checker · walk 0 holes · CI 44010036979 success
  ```

- **2026-11-17 — step 2b tenth slice: `ranking.js` has no string sink.** Table rows, moderation cell and error state are
  `el()` trees; `check_dom_ranking.js` RED mutation and GREEN restore measured, retry path exercised.  Census 100→97.
  ```
  check_dom_ranking: PASS — hostile fields and error are text, mod href verbatim, no string sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 97
  live c8a4425: served asset PASS checker · walk 0 holes · CI 44008259225 success
  ```

- **2026-11-17 — step 2b ninth slice: `projects.js` has no string sink.** Cards, health bars and empty/error states are
  `el()` trees; `check_dom_projects.js` RED mutation and GREEN restore measured across hostile fields, empty and failed GET.
  Census 103→100.
  ```
  check_dom_projects: PASS — hostile fields are text, href verbatim, all three states sink-free
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 100
  live ddccc04: served asset PASS checker · walk 0 holes · CI 44006585416 success
  ```

- **2026-11-17 — step 2b eighth slice: `feature.js` has no string sink.** All HTML builders on the 661-line page became
  `el()` trees; `check_dom_feature.js` RED mutation and GREEN restore measured with hostile values from all nine reads.
  Census 107→103.
  ```
  check_dom_feature: PASS — hostile fields and error are text, controls kept, no string sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 103
  live 15ec60d: served asset PASS checker · walk 0 holes · CI 44005529068 success
  ```

- **2026-11-17 — step 2b seventh slice: `comments.js` has no string sink.** Thread rows/replies, form, fail notice and
  load-error are `el()` trees; `check_dom_comments.js` RED mutation and GREEN restore measured, reply/fail paths exercised.
  Census 112→107.
  ```
  check_dom_comments: PASS — hostile fields and refusal are text, reply nesting kept, no string sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 107
  live 8ea0934: served asset PASS checker · walk 0 holes · CI 44004327396 success
  ```

- **2026-11-17 — step 2b sixth slice: `audit.js` has no string sink.** Rows, undo controls, filters, count line and
  empty/error states are `el()` trees; `check_dom_audit.js` RED mutation and GREEN restore measured. Census 115→112.
  ```
  check_dom_audit: PASS — hostile fields and error are text, attributes verbatim, no string sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 112
  live 4f6607e: served asset PASS checker · walk 0 holes · CI 44002973857 success
  ```

- **2026-11-17 — step 2b fifth slice: `complaint.js` has no string sink.** Impact meter, linked rows, empty/error and
  not-found are `el()` trees; `check_dom_complaint.js` RED mutation and GREEN restore measured. Census 119→115.
  ```
  check_dom_complaint: PASS — hostile fields and error are text, links verbatim, no string sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 115
  live 2a9ae0b: served asset PASS checker · walk 0 holes · CI 44001726487 success
  ```

- **2026-11-17 — step 2b fourth slice: `consensus.js` has no string sink.** Three list clears became `replaceChildren()`,
  the decision-record link became an `el()` tree. `check_dom_consensus.js` RED mutation (`wrote to an HTML string sink`)
  and GREEN restore measured. Census 123→119.
  ```
  check_dom_consensus: PASS — no string sink, clears and record link use the DOM
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks 119
  live d2affbd: served asset PASS checker · walk 0 holes · CI 43999913369 success
  ```


- **2026-11-17 — step 2b third slice: `criteria.js` has no string sink (D-13).** All five HTML builders became `el()`
  trees; the checker feeds hostile names, titles, ids, descriptions, profiles and a hostile error through the shipped page.
  RED mutation (`a string sink was used`) and GREEN restore measured. The census now counts sink syntax, 125→123; the
  earlier textual 139→137 count included comments and is superseded.
  ```
  check_dom_criteria: PASS — hostile fields and error are text, attributes verbatim, no string sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · sinks: 123
  live 2a6066c: served criteria asset PASS checker · walk 0 holes · CI 43998086155 success
  ```

- **2026-11-17 — step 2b second slice: `list.js` has no string sink (D-12).** All four sinks went through `el()`;
  hostile title, URL, description, category and API error are text/attributes in the shipped file. The page checker was
  RED-mutation proven and restored GREEN. The census now counts `insertAdjacentHTML` as well as `innerHTML`: 139→137.
  ```
  check_dom_list: PASS — hostile fields and error are text, attributes verbatim, no string sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · js: string sinks 137
  live 9961157: served assets PASS checker · walk 0 holes · headers PASS · CI 43996013466 success
  ```

- **2026-11-17 — step 2b begins: text-only renderer, first page migrated (D-11).** `dom.js` exports `el()` with text
  nodes and `setAttribute` only. `notifications.js` lost all three `innerHTML` assignments; hostile prose is now literal
  text. `check_dom_builder.js` executes both shipped files with an XSS payload and is wired into the gate. Mutation RED:
  `el() did not create  exactly one text child`; restored GREEN. Sink census 137→136.
  ```
  node --check: dom.js, notifications.js parse
  check_dom_builder: PASS — hostile prose is text, no innerHTML sink
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS · js: 136 sinks remaining
  live 4c05881: served assets PASS checker · dom.js tag served · walk 0 holes · CI 43993478687 success
  ```

- **2026-11-16 — D-10: a path is not a `(method, path)`. REGRESSION FOUND AND REVERTED.** Eight anonymous GET handlers were
  converted to `Extension<User>` after an audit matched same-path enforced POST rows. Live probes returned **500
  `Missing request extension`** on all eight; the conversions were reverted, 95 handlers pinned, `bearer_user` 40. The
  repo's `anonymous_reads_are_not_declared_private` test already listed those paths as anonymous-by-design. The structural
  test now verifies each pinned handler has a registered enforced `(method, path)` row, and the corrected audit finds 0
  enforced routes with a duplicate lookup.
  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 66ba49a: eight anon reads 200/404, no 500 · walk 0 holes · headers PASS · JS PASS · CI 43991394503 success
  ```

  Mutation proof of the new cross-check: un-enforcing the `edit_feature` row reddened
  `the_enforced_gate_supplies_identity_to_its_handler` with `edit_feature is pinned but no (method, path) row is enforced`
  (and `the_enforced_set_is_a_ratchet`); restoring it returned `7 passed; 0 failed`.

- **2026-11-16 — last Account write enforced (D-9)** (step 1b, twentieth slice). `PUT /features/{feature}` refused an
  anonymous caller; its Account row is enforced (95→96), the handler takes `Extension<User>`, and `is_guest` plus the
  owner/maintainer check stay. 95 handlers pinned, `bearer_user` 41→40. D-9 records the Account-row decision.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 24ae93f: anon 401 gate text · guest 403 guest-cannot-edit
                walk 0 holes · headers PASS · served JS PASS · CI 43984146300 success
  ```

- **2026-11-16 — final large write batch enforced** (step 1b, nineteenth slice, ten routes). Notification read,
  alternative entry, complaint merge, objection resolve, call outcome, override position, solution call, strategic weight,
  arena remove and claim release all refused anonymous callers; three needed valid bodies first. Rows enforced (85→95),
  handlers take `Extension<User>`, 94 pinned, `bearer_user` 51→41. Six no longer take `HeaderMap`.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 1543ff7: ten routes anon 401 · guest merge 403 maintainer
                walk 0 holes · headers PASS · served JS PASS · CI 43981319524 success
  ```

- **2026-11-16 — solution, list, document and taxonomy writes enforced** (step 1b, eighteenth slice, eight routes). All
  eight answered anonymous `401`; rows enforced (77→85), handlers take `Extension<User>`, 84 pinned, `bearer_user` 59→51.
  `consent_taxonomy_proposal` no longer takes `HeaderMap`.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 9409d89: eight routes anon 401 · guest tag add 403 contributor
                walk 0 holes · headers PASS · served JS PASS · CI 43978407151 success
  ```

- **2026-11-16 — arena, override, hold and claim writes enforced** (step 1b, seventeenth slice, eight routes). All eight
  refused anonymous callers; complaint link needed a valid body first. Rows enforced (69→77), handlers take
  `Extension<User>`, 76 pinned, `bearer_user` 67→59. Seven of the eight no longer take `HeaderMap`.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 3fa5326: eight routes anon 401 · guest claim 403 contributor
                walk 0 holes · headers PASS · served JS PASS · CI 43975188539 success
  ```

- **2026-11-16 — complaint and request writes enforced** (step 1b, sixteenth slice, six routes). All six answered an
  anonymous caller `401`; rows enforced (63→69), handlers take `Extension<User>`, 68 pinned, `bearer_user` 73→67.
  `create_instance_request` no longer takes `HeaderMap`.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 31920c2: six routes anon 401 · guest complaint 403 contributor
                walk 0 holes · headers PASS · served JS PASS · CI 43972801451 success
  ```

- **2026-11-16 — alternatives, consensus and vote writes enforced** (step 1b, fifteenth slice, seven routes). All seven
  refused anonymous callers; consensus position/objection needed valid bodies first. Rows enforced (56→63), handlers take
  `Extension<User>`, 62 pinned, `bearer_user` 80→73. The two consensus handlers no longer take `HeaderMap`.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 1470834: seven routes anon 401 · guest list vote 403 contributor
                walk 0 holes · headers PASS · served JS PASS · CI 43970446455 success
  ```

- **2026-11-16 — five more private writes enforced** (step 1b, fourteenth slice). Emergency-hold release, field-report
  response, project languages, merge request and arena restore all answered anonymous `401`; rows enforced (51→56),
  handlers take `Extension<User>`, 55 pinned, `bearer_user` 85→80. `arena_restore` and `release_emergency_hold` no longer
  take `HeaderMap` at all.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 5d7d952: five routes anon 401 · guest languages 403 contributor
                walk 0 holes · headers PASS · served JS PASS · CI 43967272783 success
  ```

- **2026-11-16 — ten-parameter cleanup plus six feature/thread writes** (step 1b, thirteenth slice). Removed the ten
  unused `headers: HeaderMap` handler parameters recorded after `c1411d1`. Six routes — post comment, feature status,
  add/remove evidence, add/remove dep — all answered anonymous `401` (feature status only after a valid body, as in the
  eighth slice); their rows are enforced (45→51), handlers take `Extension<User>`, 50 pinned, `bearer_user` 91→85.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live a0b8a1c: six routes anon 401 · guest evidence 403 maintainer
                walk 0 holes · headers PASS · served JS PASS · CI 43963894091 success
  unused headers warnings: 10 before, 0 after
  ```

- **2026-11-16 — cleanup plus five direct-identity writes** (step 1b, twelfth slice). Removed the unused `headers`
  parameter from `strategy_project` and its nine call sites (the warning recorded after `30f035a`). Five routes —
  read-all notifications, delete comment, remove feature tag, create criterion and work-item label — all answered an
  anonymous caller `401`; their rows are enforced (40→45), their handlers take `Extension<User>`, 44 handlers pinned.
  During the edit a broad `replace_all` also added an extension param to `vote_comment`; that was reverted before test,
  and `delete_comment`'s old lookup was removed. `bearer_user` 96→91.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live c1411d1: five routes anon 401 · guest criterion 403 maintainer
                walk 0 holes · headers PASS · served JS PASS · CI 43959295298 success
  ```

- **2026-11-16 — D-8 closes with the strategy family** (step 1b, eleventh slice, nine routes). All nine
  `strategy_project` callers — three list reads, two proposals, two consents, two withdrawals — answered an anonymous
  caller `401`. The helper now takes the gate's `&User` and keeps project-existence plus member-role checks; the handlers
  take `Extension<User>` and their rows are enforced (31→40). 39 handlers pinned. The earlier "ten callers" in D-8 was
  a miscount; the measured number is nine.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 30f035a: nine routes anon 401 · guest themes GET 200 [] · walk 0 holes
                headers PASS · served JS PASS · CI 43955076347 success
  ```

- **2026-11-16 — D-8's milestone half lands** (step 1b, tenth slice, six routes). All six milestone writes answered an
  anonymous caller `401 sign in to change a milestone`, emitted by the shared `milestone_write_context`. The helper now takes
  the gate-supplied `&User` and keeps only `readable_project_segment` + maintainer checks; all six handlers take
  `Extension<User>` and their rows are enforced (25→31). 30 handlers pinned. `strategy_project`'s ten callers remain.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 4175b9c: six routes anon 401 · guest milestone 403 maintainer
                walk 0 holes · headers PASS · served JS PASS · CI 43950166710 success
  ```

- **2026-11-16 — tags and field reports move behind the gate** (step 1b, ninth slice). Anonymous probes returned `401` from
  `apply_project_tags` and `create_field_report`; both rows are enforced (23→25), both handlers take `Extension<User>`, and
  contributor/visibility checks stay. Milestone and theme routes also refuse anonymous but resolve identity through shared
  helpers; D-8 records why they are not flipped in this batch.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 07bab00: two routes anon 401 · guest tags 403 contributor
                walk 0 holes · headers PASS · served JS PASS · CI 43944935662 success
  ```

- **2026-11-16 — charter, features, invites and consensus move behind the gate** (step 1b, eighth slice, four routes).
  Anonymous probes with a consent cookie returned `401` from their handlers; `features` needed a valid body (`{}` failed
  deserialisation with `422 missing field title` before the identity check). All four rows are enforced (19→23), the
  handlers take `Extension<User>`, and maintainer/contributor checks stay. D-6 applies; 22 handlers pinned.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 8cbdb27: four routes anon 401 · guest charter 403 maintainer · invites 404 visibility
                walk 0 holes · headers PASS · served JS PASS · CI 43940504648 success
  ```

- **2026-11-16 — merge, vote, metrics and join move behind the gate** (step 1b, seventh slice, six routes). Each answered an
  anonymous caller `401` from its handler; all six rows are enforced (13→19) and the handlers take `Extension<User>`.
  `require_role` and charter checks stay in the handlers, D-6's uniform text applies, and the structural test now pins 18
  handlers. Another batch so one build covers six conversions.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 1ba7c21: six routes anon 401 · guest metrics/execute 403 role depth
                walk 0 holes · headers PASS · served JS PASS · CI 43935004893 success
  ```

- **2026-11-16 — five private mutations move behind the gate in one commit** (step 1b, sixth slice). Five anonymous probes
  with a consent cookie each returned `401` from their handler: `redeem_invite` (`sign in to redeem an invite`),
  `curation_vote`, `set_project_visibility`, `set_criterion_active`, `set_intake_mode`. Their rows are enforced (8→13) and
  their handlers take `Extension<User>`; `require_role`/member checks remain in the handlers. No new decision — D-6 applies.
  Batching avoids five separate 10–20 minute builds for the same proof.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live fc71a02: five routes anon 401 · guest criterion 403 maintainer role
                walk 0 holes · headers PASS · served JS PASS · CI 43930380335 success
  ```

- **2026-11-16 — project adoption moves behind the gate** (step 1b, fifth slice). `curl -X POST
  https://concord.polarisocial.xyz/api/v1/projects/lorebook/claim -H 'Cookie: concord_consent=1' …` →
  `401 {"code":"unauthenticated","error":"sign in to adopt a project"}` from `claim_project`. The row is enforced (7→8),
  the handler takes `Extension<User>`, and the guest refusal stays. No new decision: D-6's uniform gate text applies.

- **2026-11-16 — AI declaration moves behind the gate** (step 1b, fourth slice). `curl -X PUT
  https://concord.polarisocial.xyz/api/v1/projects/lorebook/ai-involvement -H 'Cookie: concord_consent=1' …` →
  `401 {"code":"unauthenticated","error":"sign in to declare a project's AI involvement"}` from `declare_ai_involvement`.
  The row is now enforced (6→7), the handler takes `Extension<User>`, and the guest-account refusal stays with it. The
  uniform gate message replaces the route-specific one; `auth_me`'s dead `State` parameter was removed in the same commit.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live d055c2a: anon PUT 401 authentication required · guest PUT 403 needs an account
                walk 0 holes · headers PASS · served JS PASS · CI 43919591949 success
  NOT RUN: tests/e2e/ai_declaration_e2e.py — no pytest or playwright module on this host.
  ```

- **2026-11-16 — four more duplicate identity checks removed** (step 1b, third slice). The four enforced handlers that still
  opened their own store read — `auth_me`, `list_notifications`, `list_invites`, `next_vote_pair` — now take the `User` the
  gate already resolved. `grep -c 'bearer_user(' crates/app/src/lib.rs`: 121 → 117. The structural test was widened from one
  handler to the list of five converted handlers and matches braces on the comment-blanked source so the check cannot stop
  at a brace inside a string.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live bf7afab: guest token → auth/me 200 · notifications 200 · votes/next 200 · invites 404 (visibility)
                no token → auth/me 401 · notifications 401 · walk 0 holes · headers PASS · served JS PASS
  CI 43910750496 success
  ```

- **2026-11-16 — first in-handler refusal moved to the gate** (step 1b, second slice). Reproduced with consent cookie on the
  live host (a bare mutation is stopped earlier by `interaction_gate`):
  `curl -X POST https://concord.polarisocial.xyz/api/v1/projects -H 'Cookie: concord_consent=1' …` →
  `401 {"code":"unauthenticated","error":"authentication required"}`, emitted by `create_project`'s own
  `bearer_user(...)?` branch — one of the 122 copies the critique counts. The row is now `enforced`, `auth_gate` inserts the
  resolved `User`, and the handler takes `Extension<User>`; the guest check remains in the handler because the gate proves
  identity, not an account.

  ```
  cargo test -p concord-app --lib policy: 7 passed; 0 failed
  gate: PASS — pages: 132/132 ok · rate: probe_rate_limit: PASS
  live 005acdf: anon POST /projects 401 unauthenticated · guest POST 403 forbidden (depth intact)
                anonymous_walk: 0 holes · 0 declared but not enforced · PASS · headers PASS · served JS PASS
  CI 43895629708 success
  ```

- **2026-11-16 — 19 policy rows were wrong, not unenforced** (step 1b, first slice, commit `b74c75b`). `anonymous_walk.py --candidate
  https://concord.polarisocial.xyz --json` answered a stranger on `89` routes: `0 holes`, `19 declared but not enforced`,
  e.g. `GET /api/v1/projects/{slug}/merge (Session)` and `GET .../arenas/{arena_id}/verification (Session)`. Reading the
  handlers settled what the table could not: `list_merge_requests` never reads identity, `arena_verification` says
  "any reader may run", and `project.js` fetches `/lists` and `/work-items` anonymously with a non-fatal catch. All 19 are
  reads where identity narrows visibility or is unused, so they are `Optional`; enforcement would have broken public pages.
  `crates/app/src/policy.rs` now reclassifies all 19 and pins them in `anonymous_reads_are_not_declared_private`.

  ```
  route_policies: PASS — every registered route declares who may call it   (203/203, 0 undeclared)
  cargo test -p concord-app --lib policy: 6 passed; 0 failed
  live b74c75b: anonymous_walk: 89 walked · 0 unreachable · 0 hole(s) · 0 declared but not enforced · PASS
                probe_security_headers: PASS · served session.js: PASS · concord-rs active / concord inactive
  ```

- **2026-11-16 — the page half of the cookie, and two gates that can fail** (this session took over the still-running goal
  session after releasing its turn lease; its uncommitted files were verified here, not discarded). `session.js` stopped reading
  and writing `concord.token`: a stale token is deleted on load, `signedIn()` uses the user record after a reload,
  `authHeaders()` returns no header, and sign-out always calls the server even when no token is held in memory. The dead
  `token()` helpers in `rank.js` and `scout.js` are gone, and `presented_token` ignores an empty `Bearer ` so it cannot shadow
  a valid cookie. Four `credential_tests` cover that contract; `scripts/parity/check_session_storage.js` executes the shipped
  `session.js` in a fake browser and is wired into the gate's frontend arm beside `node --check`.

  Both new instruments were seen to fail. Removing the empty-Bearer filter gave
  `test result: FAILED. 3 passed; 1 failed` with `left: Some("") / right: Some("cookie-token")`; restoring it gave
  `test result: ok. 15 passed; 0 failed`. Re-adding `localStorage.setItem(TOKEN_KEY, token)` gave
  `AssertionError: store() persisted the token to localStorage`; restoring the file gave
  `session storage: PASS — no token in localStorage, reload stays signed in, no bearer header without a token`.
  `probe_rate_limit.py` is now wired into the gate's smoke layer with `CONCORD_RATE_LOGIN=3` and `--budget 3`.

  Deployed `085cf01`: `/api/v1/version` → `concord-rs 0.0.0+085cf01+vendor`, `concord-rs.service` active and
  `concord.service` inactive. Live probes: `anonymous_walk: 89 route(s) walked with no token · 0 unreachable · 0 hole(s) · PASS`,
  `probe_security_headers: PASS`, and the **served** `/assets/js/session.js` passed the same checker
  (`session storage: PASS`). The walk also measured the next debt: `19 declared but not enforced` routes (step 1b).

- **2026-11-16 — rate limiting, reconciled from the session that was writing it** (second session, finished here). Two
  sessions were developing this repo at once and both reached step 2b; that session is stopped and its work is verified and
  committed here rather than discarded. What it wrote: `crates/app/src/rate.rs` (class chosen by the **matched route**, client
  from `polaris_ratelimit::client_key` so a self-declared `CF-Connecting-IP` is only believed from a loopback peer, 429 with
  `Retry-After`, `X-RateLimit-*` on every response, budgets overridable with `CONCORD_RATE_*`), `AppState.rate`, the middleware
  layered above the identity gates, `limiter_from_env()` at boot, ADR 0017, and `scripts/parity/probe_rate_limit.py`.

  Verified here, not taken on faith — the release binary built from this tree, a scratch database, `CONCORD_RATE_LOGIN=3`:

  ```
  rate limits classes=["default", "api", "asset", "login"]
  login # 1 422 · #2 422 · #3 422 · #4 429 retry-after=19s limit=3
  second client 422  (own bucket)
  api read      200  (separate budget)
  probe_rate_limit: PASS — the budget holds, the refusal says when to retry, and it is per client and per class
  ```

  The 422s are the route refusing bad credentials, which is the point: three attempts are *answered*, the fourth never reaches
  the handler. At the time, `cargo test -p concord-app --lib` → **`11 passed; 0 failed`**, the two new tests being
  `rate::tests::the_class_follows_the_route_not_the_caller` and `rate::tests::the_login_bucket_refuses_and_says_when_to_retry`.
  `probe_rate_limit.py` is now **in `scripts/gate.sh`**'s smoke layer, next to `probe_pages` because it needs a server whose
  budget it was told (`CONCORD_RATE_LOGIN=3`, `--budget 3`). The count later moved to `15 passed; 0 failed` with the cookie's four tests.

- **2026-11-16 — the session cookie became a credential** (`df6f881`). `login` had been setting `concord_nav` as
  `HttpOnly; SameSite=Lax` since the port, and nothing read it: identity came only from `Authorization: Bearer`, which the page
  filled from `localStorage` — readable by any script that gets to run, exactly what `script-src 'self'` narrows but cannot
  promise. `presented_token` now falls back to that cookie, and `bearer_user` goes through it rather than parsing the header a
  second time, so all 122 call sites follow at once. Header first, so no existing client or suite changed behaviour.

  ```
  no credential            401  (want 401)
  cookie only              200  (want 200)
  header only ("***)  200  (want 200)
  cookie attributes, plain http          ; Path=/; HttpOnly; SameSite=Lax
  cookie attributes, behind the tunnel   ; Path=/; HttpOnly; SameSite=Lax; Secure
  /api/v1/auth/me by cookie  {"display_name":"Cookie Probe","id":1,"role":"member", ...}
  ```

  The commit had to be cut out of a working tree another session is editing (`crates/app/src/lib.rs`, five hunks, two mine):
  `git add -p` is unusable in this repo because diffs go through an external differ that dies mid-file, so the two cookie hunks
  were staged by filtering `git diff --no-ext-diff -U3` on their own added lines — `grep -c 'mod rate|polaris_ratelimit'` on the
  staged diff = **0**, their work left untouched. **Not deployed:** the release binary that proves this also contains that
  session's uncommitted rate limiting, and shipping uncommitted work to prove a commit is not a trade worth making. Live proof of
  the cookie needs a production login, which needs an account nobody has given me — it is in the roadmap's Blocked section.

- **2026-11-16 — one CSP per response, and a probe that could not fail correctly** (`f869676`, estate `9227ce7`). The CSP
  shipped, but every response carried **two** policies — the app's strict `script-src 'self'` and nginx's estate-wide
  `script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'` — and a browser enforces every policy it is sent, so the header
  anyone grepped was not the one protecting the page. The duplicate came from nginx's `http {}`-level include being inherited
  by a location that declared no `add_header` of its own; the fix is declaring one, which cancels the inherited set.
  `footer-concord`'s `location /` and the `/api/` snippet generated by `estate/scripts/install-api-404-edge.sh` now declare
  only the estate's version headers.

  ```
  csp=1 xfo=1 nosniff=1        /  ·  /projects  ·  /api/v1/version  ·  /api/v1/no-such-route
  probe_security_headers: PASS — every response carries the policy and no page needs an inline script
  ```

  The probe meant to catch this had a bug in the safe direction, which is still a bug: it tested `"none" in
  directives(p).get("frame-ancestors")` while `directives()` keeps the quotes, so it **failed on `frame-ancestors 'none'`**
  — the strictest value — and passed on `'self'`. Fixed in the same commit. Browser proof of the single strict policy on the
  live site: `/projects/suki/board` rendered **329 of 329** `[data-feature-id]` cards, zero `securitypolicyviolation` events,
  `window.ConcordApi` defined — and the 329 was read from the API first, because a board rendering zero cards under a broken
  policy looks exactly like a board with no work in it.

- **2026-11-16 — authorization became a table, and the table was measured before it was enforced** (step 1a of
  `docs/plans/refactor-roadmap.md`). `crates/app/src/policy.rs` now declares `(method, path, policy, enforced)` for every
  `/api/…` route, `policy_for` matches the path axum registered exactly, and `auth_gate` answers **500** for a route with no
  row — the defect, closed: a route can no longer boot without somebody deciding who may call it. `scripts/parity/route_policies.py`
  derives the table from the handlers and compares it with the router in `gate.sh`:

  ```
  api routes registered: 203
  routes with a declared policy: 203
  routes with no policy: 0
  route_policies: PASS — every registered route declares who may call it
  ```

  Evidence on `de88ab0`, from GitHub's runner (`unit: ok`, then `gate: PASS`, run 43841609784) because thinkcentre sat at load
  average 82 from unrelated jobs; plus `route_policies: PASS — every registered route declares who may call it` (203/203) and
  `anonymous_walk: 1 hole(s) · 19 declared but not enforced · 0 wrongly refused`, the hole being `/api/v1/admin-ledger`.
  Deployed (`concord-rs 0.0.0+df9bb08+vendor`), the same walk reports **`0 hole(s)`** and the route itself answers
  `401 {"code":"unauthenticated","error":"authentication required"}` where it answered 200, with `/api/v1/search` and
  `/api/v1/projects` still 200 for an anonymous caller.

  The derivation said 120 routes need a session. Measured against the live instance with no token, **22 of the 34 looks-like-a-session
  reads answered 200** — `/api/v1/search`, `projects/{slug}/tags`, `{project_id}/lists`, `documents/kinds`. A table like that is a
  description of the derivation's confidence, not of the site's behaviour, so enforcement became a per-row column and started at the
  five rows the measurement was unambiguous about: the four reads that already answered `401` (`auth/me`, `notifications`,
  `projects/{slug}/invites`, `projects/{slug}/votes/next`) and `GET /api/v1/admin-ledger`, which answered **`200` to an anonymous
  caller** — the one hole of 216. The enforced count is a ratchet in the tests (may grow, never shrink).

- **2026-11-15 — the critique turned on the code, and the numbers were worse than the prose.** Measured on `64cb134`:
  `crates/app/src/lib.rs` is **10,035 lines** holding 179 routes, 219 functions and 526 `concord_db::` call sites, tested by
  **4 unit tests**; `crates/domain` is one doc comment; the frontend is 12,477 lines across 42 files with no build; `crates/db/sql`
  has two migrations for 79 tables. The port moved the product and kept the shape — the right call then, and the reason the layer with
  the most decisions has the fewest tests. Decided in ADR 0014: split by surface, not rewrite; type the twelve most-read shapes;
  one error contract; ES modules for the frontend; keep the db crate and the schema; no domain layer. Every step is gated by the
  recorded parity corpus (`0 disagreed of 21 checked`) and the probes, so the wire cannot move while the files do.

- **2026-11-15 — the catalogue was 25% fixtures, and the gate runs 8 of 29 suites.** Two findings from one question ("what does the site
  actually publish"). First: `/projects` listed 24 projects nobody wrote — `archive-probe-220154/220933/222202` and 21 ULID-tailed slugs
  (`f-01m7dw…`, `w-01m7dw…`, `d-`, `l-`, `m-`, `p-`, `dup-`), named `Fixture`, `Writable`, `A project`. They were seeded on 2026-11-14 by
  an e2e run whose `CONCORD_E2E_DSN` pointed at production, and every number the product published since — `97 projects`, the stage
  distribution, `23 of 97` — was computed against them. Removed with `clear_probe_litter.py` (which learned `--match` and `--dry-run`;
  `351 rows would be deleted` then `375 rows deleted, left: 0`, projects **97 → 73**), audit rows kept because they record that the acts
  happened. The seeding is now refused: `tests/e2e/harness.py` stops a `CONCORD_E2E_DSN` whose database name does not contain `test`,
  with `CONCORD_ALLOW_PROD_FIXTURES=1` as the deliberate way past, and a meta-test asserts both sides. Second, and worse: retargeting the
  two meta-tests that had been **red since the port landed** (one asserted a `make e2e` target that no longer exists — `ValueError:
  substring not found`; the other asserted `docs/REMAINING.md`'s Go-era `326 e2e tests in 28 suite files` against this tree) measured what
  nobody had: **`scripts/gate.sh` runs 8 of the tree's 29 `*_e2e.py` suites; 21 are run by nobody.** The replacement test is a ratchet on
  that number, proved able to fail (adding `tests/e2e/zzz_probe_e2e.py` → `2 failed`; removed → `14 passed`). `4c8ff0a`.

- **2026-11-15 — the gate failed for the wrong reason, which is a defect in the gate.** Running `make gate` on a host that had not sourced
  `~/.config/concord/.env` gave `gate: FAIL` — but not the designed refusal: with **neither** `CONCORD_TEST_DSN` nor `DATABASE_URL` set,
  the production-guard branch (guarded on `DATABASE_URL` being *present*) never fired, `cargo test` ran `-p concord-db --test store` with no
  DSN, and six tests panicked at `CONCORD_TEST_DSN must point at a scratch PostgreSQL database …: NotPresent`. The failure was honest
  — it did not touch production — and it was illegible: six panics and a verdict that reads like broken code. Now the missing case is named
  and still fails (`a1827de`): `gate: neither CONCORD_TEST_DSN nor DATABASE_URL is set, so there is no scratch database to test against.`
  → `unit: not run` → `gate: FAIL`. With the environment sourced the same command gives, on its own scratch database,
  `migrate: 1 applied, 1 adopted as baseline, 0 already recorded` · `routes: all 13 answered` · **`gate: PASS`**. CI agreed on both pushes:
  run `43757525185` (`7106b87`) `success`, run `43757764235` (`a1827de`) `success`. Same lesson as the `--locked` and production-DSN rows
  above: a gate that reports the wrong reason is a gate an operator learns to ignore, and every one of these three was found by running it
  rather than by reading it.

- **2026-11-15 — the adoption pool's missing half was measurable after all, and the measurement said "not by guessing".** The pool has been
  blocked on "68 projects state no upstream" since the roadmap was written. It is now **39 of 97 stated** (was 23), by reading the `origin`
  remote of every checkout under the code root (`scripts/parity/upstream_candidates.py`, 46 owned remotes, 19 of 74 unstated matched) rather
  than by guessing a URL — with normalisation instead of prefix matching, because `suki-gate`/`suki-m215`/`suki-wt-tmp` are worktrees of one
  repository and a prefix match would have claimed three upstreams for one project. Two things had to be fixed to get there: `KNOWN_HOSTS`
  knew three forges and `api_root` hardcoded `git.polarisocial.xyz` for every Forgejo repository, so the sixteen `git.disroot.org` projects
  were refused as "not a known forge" and would have been fetched from the wrong API (`495f288`, `cargo test -p concord-cli --lib forge` →
  `6 passed`); and three of the nineteen matches (`ascend`, `blackrep`, `suki`) push to repositories that **404** — the ingest found them,
  `FAILED suki: … -> 404 Not Found`, so the script grew `--verify` and only a 404 becomes a skip (a timeout keeps the URL: disroot being
  down does not make the URL false). After: `healthsync: synced 16, deferred 7, failed 16` — deferred is the GitHub token blocker showing up
  in the ingest's own output, failed is `git.disroot.org` whose front page says *"Major hardware failure on storage server … Forgejo -
  are disabled"*. The product's answer did not move and should not have: `23 of 97 projects have a measured commit history`, because those
  sixteen have metrics rows with no `last_commit_at` and the stage function now renders them `unknown` rather than `seed`
  (`seed 52 · sprout 29 · unknown 16`), and `dormancysweep` reports `39 considered, 16 with a repository but no readable commit age`.

- **2026-11-15 — item 4 finished: one command runs the whole gate, and it has passed.** The live layer of `scripts/gate.sh` was two
  probes; the eight e2e suites, the signed-out crawl and the claim probe were run by hand, in an order chosen that day — precisely
  what item 4 exists to end, and two of those suites had been erroring in setup for a day with nothing noticing. `--live` now runs
  all of it (`f6613cb`), and the first full pass is a `gate: PASS` with every layer measured: `js: all 42 assets parse` ·
  `js: API paths with a stray trailing '/': 0` · `unit: ok` · `routes: all 13 answered` · `136/136 ok` (pages) · `86/86 answered`
  (segments) · eight suites at `26 · 15 · 8 · 5 · 18 · 15 · 7 · 5 passed` · `VERDICT: POST reached the route` (claim) ·
  crawl `20 pages · 48 requests · 404/405/5xx: 0`. The layer caught its own bug on the way: `probe_requests` ran under the system
  python and died with `ModuleNotFoundError: No module named 'playwright'` while the suites beside it passed — a probe that cannot
  import is not a probe that passed. `make gate` (unit + smoke) stays the fast path; `make gate-live` is the whole thing.

- **2026-11-15 — the gate could be green over a stale lock, and could point a writing suite at production.** Two defects in
  `scripts/gate.sh`, both measured. First: when `eb20faf` added seven crates to the workspace it left `Cargo.lock` uncommitted, and
  the gate stayed green because `cargo` rewrites the lock unless it is passed `--locked` — so the committed tree was not
  reproducible and nothing said so. Both cargo calls now pass `--locked`, and the guard is provably able to fail: adding one
  unused dependency to a manifest gives `error: cannot update the lock file …/Cargo.lock because --locked was passed to prevent
  this`. On the committed tree, `cargo test --workspace --no-fail-fast --locked` against a migrated scratch database exits 0.
  Second, and worse: with no scratch database the gate did `export CONCORD_TEST_DSN="$DATABASE_URL"`, and on this host
  `DATABASE_URL` is production. That is reachable — running `cargo test --workspace` with `.env` sourced reported
  `error: 1 target failed` against the production database. Measured afterwards, nothing had been written: no row in `users`,
  `projects`, `features`, `complaints`, `project_documents` or `api_tokens` has a `created_at` later than 2026-11-14. Luck, not a
  design — the fallback now refuses (`gate: no scratch database and no CONCORD_TEST_DSN; refusing to point the store tests at
  DATABASE_URL.` … `unit: not run` … `gate: FAIL`) and `CONCORD_ALLOW_PROD_TESTS=1` is the deliberate way to do it anyway.
  Gate after: `js: all 42 assets parse` · `js: API paths with a stray trailing '/': 0` · `unit: ok` · GitHub run `43740347035`
  **success**. Commit `91ad518`.

- **2026-11-15 — item 4's CI half: it had never run, and the reason was that the repository could not compile.** `gh run list`
  over the GitHub remote returned ONE run in the repository's life: 2026-11-14T20:30, `70890ab`, **failure**, 40s. Fourteen
  pushes since then produced no run at all. `gh api …/actions/permissions` says `{"enabled":true,"allowed_actions":"all"}`, so
  Actions was not switched off. Adding a `workflow_dispatch` trigger made the question askable, and the answer was not the gate:

      error: failed to load manifest for workspace member `…/crates/db`
      Caused by: failed to load manifest for dependency `polaris-authz`
      Caused by: failed to read `…/polaris-authz/Cargo.toml`
        No such file or directory (os error 2)

  Concord's workspace depended on **seven crates by path, none of them in any repository**: `polaris-core-rust` (1,044 lines),
  `polaris-healthz` (156), `polaris-authz` (1,249), `polaris-ledger` (466), `polaris-ratelimit` (592), `polaris-slug` (169),
  `polaris-comments` (376) — 4,052 lines that the production service cannot be rebuilt without and that existed on one host.
  `git remote -v` printed nothing in all seven; `polaris-authz`'s declared remote answers `Repository not found`. The Go tree this
  port replaced had `internal/ledger` and `internal/comments` **in** the repository.

  Vendored verbatim into `vendor/` (`diff -r`: all seven verbatim), added as workspace members, ADR 0013 with the alternatives
  that lost. Proof that this is the defect: a clean clone with no sibling directory reachable —

      $ cd ~/tmp/concord-clean            # git clone of f8121cb; ls ../polaris-authz -> No such file or directory
      $ cargo build -p concord-cli --offline
          Finished `dev` profile [unoptimized + debuginfo] target(s) in 4m 02s

  And the first green gate in this repository's history, on GitHub's own runner (run `43732462823`, commit `f8121cb`):

      js: all 42 assets parse
      js: API paths with a stray trailing '/': 0
      routes: all 13 answered
      gate: PASS

  56 `test result: ok` lines, including the vendored crates' own suites (authz, ledger, ratelimit), which nothing had ever run.
  Two things it also caught about the copy: each estate crate carried a convenience symlink of its own name pointing at its
  absolute path on this host, copied along with it — six removed in `f8121cb`, because a vendored tree that resolves only on the
  machine it was copied from is the same defect one level down.

- **2026-11-15 — the split finished, and it found five live defects the split did not cause.** `project.js` went 1,617 → 902 lines
  across three capability files (`project-arena.js` 238, `project-panels.js` 303, `project-writes.js` 234), each registering itself
  on `window` and loaded in dependency order; helpers (`setStatus`, `postJSON`, `showCardRefusal`) are bound once from the file that
  owns them, never copied. What the work found: the claim button and the sizing form were sending **GETs to POST/PUT routes**
  (`sendJSON` was `ConcordApi.get(path, {method: …})`, and `api.js`'s `get` hardcodes GET and drops the body) — `405`, button
  re-enabled silently, since `54848be`; and **four API paths carried a trailing slash the port answers `404` where Go answered both**
  (`/similar/{kind}/` — the duplicate panel, dead since cutover; `PUT /documents/`; `/features/` in settings; `/features/` in
  milestones), two of them wrapped in `.catch`/`getOrNull`. `duplicates_e2e` had never run at all: it seeded SQLite directly
  (`no such table: users`, 5 errors, 0 tests). Repaired, it failed honestly, then passed. Gate: `duplicates_e2e 5 passed` (first
  PostgreSQL run in its life) · `project_panels_e2e 26` · `feature_e2e 15` · `probe_pages 136/136` · `api_paths 0` ·
  `probe_claim_button VERDICT: POST reached the route`. Commits `ede8c2c`, `e5f02df`, `d6c942b`, `0c8272b`.

- **2026-11-15 — item 9 done: one client, and the pages it revealed.** Every page script now calls `api.js`; `grep -c 'fetch('` over
  `crates/web/assets/js/*.js` gives **4 sites in 39 files**, all four the transport itself. What the conversion found, in the order it
  found it: `/stats` loaded no script at all and had never shown its three numbers; the project overview had never finished loading
  because a count badge fetched `11.35 MB` of document bodies; and four files I had just edited shipped not parsing (`merge.js`,
  `ranking.js`, `requests-index.js`, `scout.js`) while every suite stayed green. `scripts/gate.sh` now parses all 39 assets before it
  compiles anything. Full account in item 9 below; commits `25e9443`, `53062e9`, `4b8b2f9`, `54848be`, `34fbf04`, `9b7abdc`, `6f6d9db`,
  `947d9ab`.
- **2026-11-15 — the suites that could not start, and the pages they found.** Both `documents_e2e` and `requests_e2e` seeded SQLite
  directly instead of through `harness.connect`, so neither had ever tested the Rust binary. Making them run found eight defects across
  three pages, none of which any probe could see because a page that renders an empty state is a `200`:
  the documents list shipped every document BODY (**11.7 MB, still downloading at thirty seconds** — now `?fields=meta`, `37104b` in
  `0.20s`); `#project-documents`, `#requests-root`, `#request-root` and `#lists-root` were never mounted, so no page script ran;
  `skeleton.js` and `markdown.js` were not loaded on the pages whose scripts require them (the reader threw and the region stayed
  `aria-busy` forever); `GET /requests/{id}/tags` answered a bare array where the client documents `{tags: […]}`, so every request page
  said "No constraints recorded" whatever the database held; an unknown request answered with the global 404 page instead of the
  project's shell, so the script could not say why the address was empty; and `[data-slug]` — read by six scripts, written by no page —
  made every one of them ask `/api/v1/projects//…` and draw an empty state over a project full of content. **That last one is the
  general defect**: `shell.rs` now writes the slug once, from the path, as element text.
  Gate: `documents_e2e 5 passed` · `requests_e2e 18 passed` (from `16 failed, 2 passed`) · `board_e2e 8 passed`, including the private-board
  test that had been open · `feature_e2e 15`, `project_panels_e2e 26`, `consensus_e2e 15`, `ai_declaration_e2e 7` · `probe_pages 136/136`,
  `segments 86/86`. Commits `92c815f`, and the one below.
— what of this is now done (updated 2026-11-14)

| # | item | state | commit | gate |
|---|---|---|---|---|
| 4 | the cutover did not move the writers | **done** | `ba134b4` | `is-enabled` → rs enabled, Go unit + both Go timers disabled; rollback still one `start` away |
| 1 | 18 repository identities only in the Go database | **done** | `b5f91a2` | `psql` 5 → 23 rows; `/api/v1/projects` {sprout 28, seed 45}; `dormancysweep` 5 → 23 considered |
| 2 | migrations + `schema_version` | **done** | `c590240` | 4 scratch scenarios: empty → 81 tables; legacy → adopted; ahead → refused by version; live adopted at boot |
| 3 | health ingest | **done, with a correction** | `eba381d` | the `--fetch` code existed; the unit was misconfigured and disabled. Hourly timer live: `synced 16, deferred 7, failed 0`; 23/23 measured |
| 6 | a route that edits a feature's body/impact/effort | **done** | `70890ab` | owner 200 with a field-by-field audit row; `{}` 400; impact 11 → 400; stranger 403; anonymous 401 |
| 7 | `make gate` + CI | **done** | `70890ab` | `scripts/gate.sh` three layers + `.github/workflows/gate.yml` |
| 2 | one renderer per page | **done for the project page** | `f595960` | header server-rendered (h1, slug, stage, AI badge, description, actions); `project.js` lost 2,684 chars of header; probes 136/136 + 86/86, five suites 62 tests green |
| 8 | lifecycle as (measured stage) + (acts) | **done for `archived`** | `805f8f8` | `0002_project_archived.sql` (first migration through the new mechanism); POST/DELETE archive with a required reason; live: stage `archived`, tooltip = the maintainer's sentence, 400 without a reason, un-archive restores `seed` |
| 9 | the frontend's 13k lines | **done**: one client (`grep -c 'fetch(' crates/web/assets/js/*.js` → 4 sites in 39 files, all the transport); `project.js` 1,617 → 903 lines over three capability files | `947d9ab` | the pages it revealed: `/stats` never loaded a script, a count badge fetched 11.35 MB of bodies, four files shipped not parsing · probes 136/136 + 86/86, eight suites |
| 10 | docs describing the dead lineage | **done for the live ones**: `concord-spec-r4`, `PLAN-r4`, `REMAINING` carry historical banners, `port-context.md` corrected, and the roadmap's own mapping table re-measured (it had said NEW for three shipped ideas) | `a3f94e7`, this commit | `harness_selftest_test.py` now gates the banner rather than the dead document's numbers — the assertion it replaces had been red since the port landed |

**Two things this pass learned about its own documents.** First, decision 5 as written was wrong — `healthsync --fetch` had existed since the port and I
wrote "nothing produces that file" from a `--from` signature and a Go-era note; the section is corrected in place. Second, the migration gate
found that the baseline file carries its own `BEGIN`/`COMMIT`, so wrapping it in a transaction produces two `sqlx` notices and no error: the
outer transaction wins, the file's own `COMMIT` ends it, and the version row is written after. It works, it is subtle, and it is written down
here so nobody "fixes" it into an outage.


## Evidence base

| fact | value | how |
|---|---|---|
| Rust source | 45,473 lines across 11 crates | `find crates -name '*.rs' \| xargs wc -l` |
| Client JavaScript | 13,024 lines, no build step, no modules | `find crates/web/assets/js -name '*.js' \| xargs wc -l` |
| Routes registered in the app | 177 `.route(` calls | `grep -c '\.route(' crates/app/src/lib.rs` |
| Database modules | 61 files in `crates/db/src` | `ls crates/db/src/*.rs \| wc -l` |
| Tables | 80 | `information_schema.tables` on production |
| Users | 89, of which 3 guest | production query |
| Projects | 73, of which 5 state a repository | production query |
| Browser suites | 29 files, 97 tests in the eight suites run every time | `ls tests/e2e/*_e2e.py` |
| CI | none | `ls .github/workflows` → absent |
| Pages with a server-rendered `<h1>` | 5 of 12 checked have none | `curl` + `grep '<h1'` |

## The ten things I would do differently

**The gate passes end to end, once, on a committed tree (2026-11-15).** The line, verbatim from `/tmp/gate10.log` on `thinkcentre`:

```
scratch database: gate_3203801 (migrated: 2 files)
=== 1. unit tests
unit: ok
=== 2. smoke — fresh database, the real binary, real HTTP
version: {"version":"concord-rs 0.0.0 (unstamped build)"}
routes: all 13 answered
live: not run (--live <url> to include the probes)
=== result
gate: PASS
```

`unit: ok` is the first time layer 1 has passed with a scratch database, and it passed because the gate now applies its own migrations before
asserting. The run took fifty minutes on a host at load 35 with eight gigabytes swapped out — the number is worth writing down, because "the
gate is slow" is otherwise indistinguishable from "the gate is stuck".

**What the gate has now proven, and what it still has not.** Layers 2 and 3 are green against the deployed instance: `routes: all 13
answered`, `probe_pages 136/136`, `probe_project_segments 86/86`, five suites 62 tests. Layer 1 (the unit tests) had never passed with a
scratch database — it failed on `relation "users" does not exist` because a scratch database is empty and the store tests are not a schema.
The gate applies its shipped migrations before asserting now (`0d8d5dc`); the full pass is the next thing to read, not a thing to assume.

**A lesson the gate taught about running gates.** The first two gate runs died for two different environmental reasons and both looked like
"the suite is red": one was killed with the shell that started it, and one read `crates/web/src/lib.rs` while I was still writing it, so it
compiled a half-edited file and reported `expected ';', found body` at a line that was correct thirty seconds later. A gate that runs
concurrently with edits measures the edit, not the tree. The third run, from a clean checkout state and under `systemd-run` so it could not
be killed by a closing session, reported `gate: FAIL` for layer 1 (that stale read) and `routes: all 13 answered` for layer 2 — which is
the honest split: the smoke layer was never broken, and the unit layer has to be re-proved on the committed tree.

### 1. A migration system, from the first table

**What exists.** One file, `crates/db/sql/0001_baseline.sql`, and no runner: the baseline is applied by hand, and this session's
`ai_involvement` column was added to the baseline **and** to the live database with an `alter table … add column if not exists` typed into
a shell.

**Why it is wrong.** The two statements can disagree and nothing notices. A fresh database gets the column; the running one gets it only
because somebody remembered. The failure is silent until a feature reads a column that exists in one place and not the other, and it fails
in production first. This is the single highest-risk decision in the tree.

**What I would do.** Numbered migrations, `0001` … `00N`, applied in order at startup behind a `schema_version` table, with the binary
refusing to serve if the database is ahead of it. Every change is then one artefact that both a fresh database and the running one consume.

**Cost to change now:** medium, and it should be the next thing built. Roughly an afternoon: move the baseline to `0001`, add a
`migrations` table seeded with the live maximum, add an `apply` step to the CLI, and a gate that fails when a file is skipped.

### 2. One renderer per page, decided once

**What exists.** Some pages are rendered on the server (`/`, `/projects`, `/taxonomy`, `/stats`, `/scout`), some are a `<div>` plus a
script (`/projects/{slug}`, `/search`, `/finder`, `/rank`, `/ranking`, `/curation`, `/contribute`, `/notifications`). The second group has
**no `<h1>` in the HTML at all** until JavaScript runs, and `/projects/{slug}` is 5.5 KB of `aria-busy="true"`.

**Why it is wrong.** It is not one design with a performance trade-off; it is two designs that both have to be maintained, and this session
paid for it directly: I added the stage badge to `projects.js`, a file whose `card()` renders exactly the markup a test looked for, and seven
tests failed against a correct page because `/projects` is server-rendered and that script never runs. Twenty minutes of my life, and a
permanent invitation to the next person.

It also means the product is invisible to anything that does not run JavaScript: crawlers, link previews, and readers on a bad connection
get an empty box where the project is.

**What I would do.** Server-render every page that has a URL, and use JavaScript for interaction only (vote, filter, the arena pair, the
finder session). A project page is a document with a title, a description, and its panels — that belongs in HTML.

**Cost to change now:** medium per page, and the same work twice if the JS is kept in parallel. Do them one at a time, delete the client
branch in the same commit, and let the existing browser suites prove the interaction still works.

### 3. The API should have been designed, not frozen

**Status 2026-11-15: decided.** `docs/adr/0012-the-api-is-a-designed-surface.md` — the audit is now a script, the count
is 174 routes / 166 read / 8 unread, and `/archive` (the audit's first finding) is wired into the project page.

**What exists.** 177 routes, additive-only since the port began, because the Go-era Playwright tests speak HTTP and the port's gate was
"byte-identical shapes". Two shapes now carry a `stage`, a `stage_because` and an `ai_involvement` that the reference never had, and the
parity corpus had to be re-recorded twice in one session.

**Why it is wrong.** "Additive-only" was the right rule for a port and the wrong rule for a product. It made every shape a compatibility
surface before anyone depended on it, and it means a wrong field name is permanent. `project_id` is a slug in paths and a number in bodies;
`project` JSON carries a computed stage next to a stored visibility next to a measured health score with no marker of which is which.

**What I would do.** Two clients exist — the browser and the CLI — and both are ours. Design the API for them: one versioned surface,
objects that say whether a field is measured or declared (`{"stage": {"value": "dormant", "because": "…"}, "ai_involvement": null}`), and
delete the routes nobody calls. Grep is the whole audit: every route with no reader is a route to remove.

**Cost to change now:** high, and it is a decision rather than a task. A middle path worth taking: publish one new `/api/v2/projects` that
is designed rather than inherited, keep v1 until the web client migrates, then delete.

### 4. The cutover moved the rows and not the writers

**What exists.** On 2026-11-14 the Rust binary took over `concord.polarisocial.xyz` and the Go service was stopped, with its SQLite file
kept as the rollback. **The Go timers were not stopped with it.** Measured today:

```
$ systemctl --user list-unit-files 'concord*'
concord-healthsync.timer    enabled
concord-proposalsweep.timer enabled      # both the Go units, still on
$ journalctl --user -u concord-healthsync -n 1
Nov 14 20:06:15 healthsync: synced 23, skipped 0, deferred 0, failed 0 of 23
```

So a service that is "stopped" still runs two jobs a day, and they still write to the rollback database. The rollback is not a snapshot of
the cutover; it is a diverging copy.

**And it shows in the data.** The Go database has **23 projects with a `repo_url` and 23 with a measured health score**; PostgreSQL has
**5 and 5**, because the repository identity I applied today came from `seed/forge_repos.json`, which states five and skips sixty-eight.
The dead lineage measures more of the product than the live one does.

**Why it is wrong.** A cutover is a data question *and* a writer question. I wrote the first half down (the reconciliation script, in the
cutover ledger) and missed the second, and the cost is that the live instance is less informed than the corpse.

**What I would do.** The cutover checklist has three items, not two: reconcile the rows, move the **writers**, and disable the losing side's
units in the same change. Then the rollback really is frozen and its timestamp means something.

**Fixed the same day, after writing this:** `concord-rs.service` was *disabled* while `concord.service` was *enabled*, so a reboot
would have silently rolled production back to Go. Both are now the right way round and the two Go timers are stopped and disabled.

**Cost to change now:** minutes for the timers (`systemctl --user disable concord-healthsync.timer concord-proposalsweep.timer`), an hour
for the eighteen repository identities that already exist in the Go database and have never been copied forward.

### 5. Health ingest: the code existed, the job did not — and I said the opposite here first

**What I wrote in the first draft of this review, and it was wrong.** I claimed `healthsync` needs `--from measurements.json` and that
"nothing produces that file". `grep -n 'healthsync_fetch' crates/cli/src/lib.rs` says otherwise: `healthsync --fetch` has existed since the
port and it works —

```
$ concord-rs healthsync --fetch --dry-run --only kindred,commons,orbital
dry-run commons:  commits=257 contributors=0 releases90=0 review latency unknown
dry-run kindred:  commits=128 contributors=0 releases90=0 review latency unknown
dry-run orbital:  commits=255 contributors=0 releases90=0 review latency unknown
```

I read the `--from` signature in the CLI and the Go-era note that the port "moved the scraping out", and wrote a conclusion from two
half-facts. The lesson is the review's own rule, applied to the review: grep the symbols before trusting the document, including this one.

**What was actually wrong, and it is worse than a missing feature.** The installed unit was

    ExecStart=/personal/documents/code/rust/concord/deploy/concord-cli healthsync

— a **local build artefact** rather than the deployed binary, and **no `--fetch`**, so the job would have failed every run demanding a
file. It was also disabled. Three defects in one line, and none of them visible from the code: the capability was written, tested and
unreachable, which is the failure mode this repository has produced more often than any other (see `docs/KNOWN-ISSUES.md`, "every page
existed").

**What I did instead of writing the ingest.** Fixed the unit (`%h/concord-deploy/concord-rs healthsync --fetch`), added it to `deploy/`
with an hourly timer, enabled it, and ran it through systemd rather than my shell:

```
Nov 14 21:15:17 DEFERRED zeph: https://api.github.com/... -> 403 Forbidden: API rate limit exceeded
Nov 14 21:15:17 healthsync: synced 16, deferred 7, skipped 0, failed 0
```

`deferred 7` is the honest number: the seven GitHub repositories exceed the unauthenticated 60-requests-an-hour budget once a few calls
each are made, and the job reports a deferral instead of writing a measurement of zero. Against the database: 23 of 23 stated repositories
have a `last_commit_at` and a fresh `health_score` — `stash-box 0.656`, `stash 0.582`, `gravity 0.491`.

**Still blocked, and it is a credential:** `CONCORD_FORGE_TOKEN` would take the seven GitHub repositories from deferred to measured. Until
then the timer measures the 16 Forgejo ones every hour and says so.

### 6. There should be one discovery engine, not four

**What exists.** `search` (Postgres `ts_rank` over name and description), `embeddings` + `/similar/{kind}` (cosine over `nomic`, 5342
feature vectors and 146 project vectors), `finder` (entropy-driven questionnaire with its own scoring, 12 tests), `scout` (opportunity
radar, 22 tests), plus `taxonomy` for the vocabulary and `ranking` for arenas.

**Why it is wrong.** They answer overlapping questions — "what should I work on", "what exists like this", "what is missing" — with four
scoring paths over the same tables, and each has its own idea of what "unknown" means. This session's step 3 exists because of this: I wrote
a fifth (term-overlap intent scoring), the compiler refused it, and the right answer turned out to be the `/similar` route that was already
there. The lesson was cheap only because the compiler was in the way.

**What I would do.** One candidate scorer with modes (`by_text`, `by_questionnaire`, `by_gap`), one place that decides what a score means,
and the four surfaces as thin readers of it. Or, more honestly: keep two, freeze two, and say which two in a sentence.

**Cost to change now:** high. The defensible minimum is a written statement of which engine owns which question, so the next feature does
not add a fifth.

### 7. No CI, so every gate is a ritual

**What exists.** `cargo test`, `probe_pages.py`, `probe_project_segments.py`, `concord-parity`, eight browser suites, four mutation
harnesses — all run by hand, from memory, on one host, in an order I chose each time. There is no `.github/workflows`, no `Makefile`
target that runs the set, and no staging instance the probes can be pointed at that is not production.

**Why it is wrong.** A gate that depends on somebody remembering it is a gate that will be skipped on the day it matters, and this session
skipped `ui_mutants` and `probe_s5_*` for hours at a time without noticing, because nothing told me.

**What I would do.** One `make gate` that runs unit tests, the two probes against a scratch server, the parity replay and the suites; a CI
workflow that runs it on every push with a Postgres service container; and a nightly job that runs the mutation harnesses, because they are
slow and they only need to be proved once a day.

**Cost to change now:** low to medium, and it is the cheapest quality improvement available.

### 8. The lifecycle model should have been (measured stage) + (acts), from the start

**What exists.** A computed stage — `seed`/`sprout`/`mature`/`dormant`/`unknown` — and two acts that are absent from it: `archived` and
`forked & alive`. The sweep that marks dormancy writes an `audit_log` row rather than a column.

**Why it is wrong.** Not the split, which is right, but the order in which I found it: the note asks for six stages, I built four and
documented at length why the other two could not be inferred — which is a design discovery that belongs in the first hour, not at
implementation time. A stage that the code assigns to somebody's project is a claim; an act is a person's. That sentence should have been
the design brief, and then `archived` would have existed on day one as a column plus a route, instead of being deferred to a step that is
blocked on data.

**What I would do (and would still do).** Model every lifecycle fact as either *measured* (derived on read, never stored) or *declared*
(a row with an actor, a date and a reason). Then `archived` is a declaration with a route, `forked` is a declaration with a parent pointer,
and the projection is a join, not a rule.

**Cost to change now:** low for `archived` (a column, a route, an audit row, a badge — about two hours), and it should be done before the
adoption pool, because "declared abandoned" is the pool's entry condition.

### 9. The frontend should not be 13,000 lines of scripts with no build

**What exists.** `crates/web/assets/js/*.js` — 13,024 lines, loaded by URL, sharing state through `window` and `localStorage`, escaped by a
local `esc()` per file, with no module system, no bundler, no type checking and no unit tests. The interaction tests are Playwright suites
that drive a real browser (97 of them), which is the most expensive way to test a pure function and the only way to test these files.

**Why it is wrong.** Not because vanilla JS is wrong — it is fast and dependency-free, and that is a real virtue. It is wrong because there
is no boundary: `project.js` is 1,696 lines that fetch ten endpoints, render a board, run an arena, and post a vote. A bug in a date format
is found by a browser suite, and the escaping rule is re-implemented eleven times.

**What I would do.** Keep it build-free if you like, but split by capability (`board.js`, `arena.js`, `comments.js`), put the API calls in
one module with one error model, and share one `esc`. If a build step is acceptable, Svelte or Preact with a bundler buys components and
typing for about a day of setup — and the Playwright suites survive it unchanged, because they speak HTTP and the DOM.

**Cost to change now:** medium, and it is not urgent. The one thing worth doing immediately is extracting the fetch/error/escape helpers,
because that is where the subtle bugs live.

**Done 2026-11-15 for the escaping half** (`209c981`, `escape.js` + 29 delegations). `crates/web/assets/js/escape.js` holds the one
implementation: a single pass over `/[&<>"']/` with a map. All 29 copies are gone — 19 were byte-identical, 7 used `String(s)` after a null
check, 1 (consensus.js) chained five `.replace()` calls, and 1 was `escapeHTML` in the markdown renderer; every one produced the same five
replacements, and the single-pass form is the one that cannot double-escape.

Two things this cost, both worth recording because both were invisible until a suite ran:

* The first deploy **broke the live site for about twenty minutes**. The page scripts were emitted in the body and `escape.js` was emitted
  just before `</body>`, so `var esc = window.ConcordEscape.esc` read `undefined` and every page script threw before rendering. `feature_e2e`
  caught it — 13 failed, 2 passed — and nothing else did: the server answered 200, the HTML was correct, and the page was blank to a reader
  with JavaScript on. The three globals (`escape.js`, `session.js`, `consent.js`) are emitted in `<head>` now, and the same suite passes
  `15 passed in 33.68s`.
* A production deploy also failed once with `cp: cannot create regular file …: Text file busy` because the old process still held the
  binary; the fix is write-then-`mv`, which is an atomic rename and does not care who has the old inode open. The service was down for the
  length of my next command, which is the cost of doing that step by hand instead of in `make deploy` — which does it correctly and which I
  did not use.

**And the fetch/error half, 2026-11-15.** `crates/web/assets/js/api.js` is now the one way to call the API: headers built from `session.js`,
body parsed once, and one error model — `ApiError` with `status`, the server's own sentence, and `auth` (the only flag a page can act on,
because "sign in" is the only refusal a form fixes). A network failure is `status: 0`, not a 500: the server did not answer and saying it
failed would be inventing a response. `project.js`, `requests.js` and `adopt.js` delegate; `project_panels_e2e` is `26 passed in 40.59s`
against the deployed binary.

The live probe found a bug in the module itself, which is why the probe exists: a 404 whose body is `{"error":{"code":"not_found","message":
"No such API route."}}` produced the message `[object Object]` in a reader's face, because `payload.error` was concatenated as if it were a
string. It is now a `sentence()` that accepts a string in `error`, in `error.error`, or in `error.message`, and falls back to the status.
Live after: `404 -> status=404 msg=No such API route.`

Still open in this item: the split by capability (`project.js` is still 1,600 lines that fetch ten endpoints). That is a reorganisation
with no correctness edge; the adoption of the module at every call site is done, and the number is measured rather than estimated.

**And the adoption finished, 2026-11-15 (`api3` … `api10`).** Measured with `grep -c 'fetch(' crates/web/assets/js/*.js`: **4 sites
in 39 files**, and all four are the transport itself — `api.js` (1, the one request), `session.js` (1, reading the token), `auth.js`
(2, the sign-in form). 36 files carry none. The conversions deleted sixteen duplicate helpers: `apiAuth` in nine files (three were the
same 785-character copy), `headers()` in four, `authHeaders()` in three, and four bespoke `{status, body}` wrappers.

Three things this cost, all recorded because each was invisible to every suite:

* **`/stats` had never worked.** The page loaded no script and had none of the four containers `stats.js` fills, so the three numbers the
  page exists for — a summary, complaint/feature latency, newcomer responsiveness — were never fetched. The live page was 3.8 KB of
  navigation over one empty paragraph. Found by opening the page after converting its script. Live after: summary `discussion 1188`,
  latency `Complaint triage 0.0 h n=1414 unfinished=996`.
* **The project overview had never finished loading.** It fetched `/documents` — every body, `11.35 MB`, still downloading at `40 s` — to
  render a count badge, and `Promise.all` waited for all eleven panels. `?fields=meta` is `37 KB` in `0.64 s`. Live before:
  `aria-busy="true"`, 0 characters rendered. After: `aria-busy="false"`, 349,804 characters. Every suite passed throughout, because a
  seeded project has three documents and this one has 175.
* **Four of my own files shipped not parsing.** A bulk helper deletion in `api8` left a closing brace at the top of `merge.js`,
  `ranking.js`, `requests-index.js` and `scout.js`; every page still answered 200 and every suite still passed, because they assert HTML
  and nobody parsed the files. Caught while converting the last eight. `scripts/gate.sh` now runs `node --check` over all 39 assets
  (`js: all 39 assets parse`) before it compiles anything.

Two dead branches fell out of the same reading: `curation.js`'s "Recorded. Not decided yet" and `taxonomy.js`'s 202 branch both keyed on
`status === 202`, which neither Rust route ever answers (they return 200 with a `ratified` flag). Both had been unreachable since cutover.

Final gate on the deployed binary (`947d9ab`, `concord-rs 0.0.0+api10`): `probe_pages 136/136`, `probe_project_segments 86/86`, seven
e2e suites / 94 tests green, `route_readers.py` 174 registered / 166 proved / 8 declared / 0 unknown, and eight pages opened live with
`console errors: []`.

### 10. Docs that describe the dead lineage

**What exists.** `docs/concord-spec-r4.md` (826 lines, `§`-numbered), `docs/PLAN-r4.md`, `docs/REMAINING.md`, `docs/HANDOFF.md` — all
describing the **Go** implementation, with a gate (`check_doc_numbers.py`) that enforces numbers against a tree that is stopped. The Rust
port has one roadmap file and a ledger, both of which I wrote.

**Why it is wrong.** The governing documents of this product describe code that is not running. `HANDOFF.md` "names what is next" for a
service that is stopped. A new session reads the spec, believes it, and implements something the live binary does not do — which is exactly
the failure the port's own ledger documented, in a different form.

**What I would do.** One living document per concern: an architecture overview that says which binary serves and which is rollback, a spec
for the *product* (not the Go implementation), and the ledger for the port as history. Mark the Go docs `(historical)` in their first line,
the way the docs gate already knows how to read.

**Cost to change now:** low, purely editorial, and it prevents the next wrong turn.

## From scratch, round two: the shape of the code (2026-11-15)

The ten sections above are about the product. This one is about the code. **The implementation critique proper is
`docs/reviews/2026-11-15-implementation-critique.md`** — nine criticisms, each with the command that produced its number, and it is
where the debt this file cannot express is written down (122 opt-in `bearer_user` call sites, 0 of 330 SQL queries compile-checked, no
CSP and no rate limiting, a session token in `localStorage`). What follows is only the part that belongs to *this* review: what I would
have done differently from the first handler, and what that implies for the plan (`docs/plans/refactor-roadmap.md`).

Measured on `64cb134`:

```
crates/app/src/lib.rs              10,035 lines · 179 routes · 219 functions · 526 `concord_db::` calls · 4 tests
crates/domain/src/lib.rs           1 line (a doc comment naming a plan that never filled it)
crates/db/                         21,681 lines over 64 files · 47 tests
crates/web/assets/js/              12,477 lines over 42 files · no build step · 3 tests in the crate
crates/db/sql/                     2 migrations; the baseline holds 79 tables and its own BEGIN/COMMIT
```

**The port moved the product and kept the shape.** That was the right call at the time — a rewrite
that also changed behaviour could not be judged — and it means the sins are the Go service's, faithfully
carried: one file is the router, the policy, the persistence call sites and the wire format; the layer
with the most decisions has the fewest tests; the wire contract is a `json!` literal, so it is written
in 179 places and documented in none. The port's own plan said "S1: projects", "S2: the board", which
is a *feature* decomposition. A surface decomposition would have produced fifteen files of 600 lines
and a router, and the same two weeks.

What I would have done differently, in order of how much it would have saved:

1. **One module per surface from the first handler**, with `lib.rs` as a composer. The cost is a
   directory; the saving is that a 10k-line file never exists, so "where does this go" is never a
   question and a diff can be read.
2. **Response types on the first route.** `#[derive(Serialize)]` per shape, with a round-trip test.
   The parity corpus exists precisely because shapes were strings: `3 shape(s) disagreed of 21 checked`
   is what a string contract costs, and it was found by a replay, not by a compiler.
3. **Handler tests against a scratch database, from the first route.** `State<PgPool>` already makes
   this possible; the four tests in a 10k-line file are the evidence that it was never done. The e2e
   suites are excellent and they are the *second* layer; 21 of 29 of them are run by nobody, which is
   what happens when the pyramid is inverted and the base is a browser.
4. **Delete `crates/domain` on day one, or fill it.** An empty crate is a promise the tree cannot keep;
   every reader spends a minute wondering what belongs there. The rules worth isolating were
   `lifecycle` and `health`, and they landed in `concord_ranking` where they are pure and tested
   (43 tests) — that is the right home, so the empty crate is pure cost.
5. **One error contract, chosen once, written in one file.** Two shapes on one origin (the estate's
   nested, the Go flat) plus nginx's third meant `api.js` grew a `sentence()` that tries three keys —
   a reader-side workaround for a writer-side indecision.
6. **ES modules from the first file.** `window` registration is why `project.js` could reach 1,617
   lines and grow `project-arena.js`, `project-panels.js`, `project-writes.js` as three files that
   still share one mutable global. The split happened; the seam is still a window.
7. **Migrations from the first table.** Done, late (`c590240`), and the two-file baseline still carries
   its own `BEGIN`/`COMMIT` — subtle, documented, and a trap for whoever wraps it in a transaction.
8. **The 79-table schema is not a defect.** The breadth is the product (projects, features, arenas,
   consensus, criteria, curation, taxonomy, trust, notifications). What was missing was a way to change
   it; that existed at the end and not the start.

**Would I refactor into it now?** Yes — by splitting, not rewriting, and only in steps whose gate is
the recorded corpus. `docs/adr/0014-the-app-layer-is-split-by-surface.md` is the decision, the six
steps, the files and the gate for each. What I would *not* do: rewrite the frontend (12,477 shipping
lines behind 44 e2e suites is an asset), consolidate the tables (a live schema's table count is not a
defect), or add a service/use-case layer between app and db (two-line pass-throughs, no new test
surface). The order is: freeze the corpus, split the router, type the shapes, one error contract,
modules in the frontend, then wire the 21 unrun suites — last, so they cover the new layout.

## Smaller things, each a one-line fix

- **`/projects/new` is orphaned.** I built the create page this session and no page links to it: `grep -rn 'projects/new' crates/web` finds
  the route and the handler and nothing else. A page nobody can reach is a page that does not exist. *Fixed in the same commit as this
  review: a link on `/projects`, and the empty state names it.*
- **Seven pages have no `<h1>` in the HTML** (`/search`, `/finder`, `/curation`, `/rank`, `/ranking`, `/contribute`, `/notifications`) —
  a consequence of decision 2, and the cheapest accessibility win available.
- **`/projects/{slug}` answers 5.5 KB to a crawler**, so a project's content is invisible to search engines and link previews even though its
  `og:` tags are correct.
- **Finder sessions live in a mutex**, and every response says `persisted: false`. That is honest, and it means a restart loses a reader's
  session without warning beyond that field.
- **Rate limiting is per-process and per-`X-Forwarded-For`**, so it is a concurrency control only while there is one process, and it is
  spoofable by a client that sets the header. Behind Cloudflare that is mostly fine; it is not a security control and should not be read as
  one.
- **`embedbackfill` only prints at the end**, so a killed run looks like a completed one. It cost this session an hour and a false "done".
- **Three Rust timer units are installed disabled** (`-healthsync`, `-statsnapshot`, `-proposalsweep`) — wired to nothing, and the Go
  equivalents are what actually run. Either they run or they should be removed; "installed disabled" is the state that reads as done and is
  not.
- **Two lineages have no common ancestor** (`git log` for the Rust tree starts at the port). That is fine, but it means `git blame` on this
  codebase answers "the port", so provenance has to come from the ledger, and the ledger is a markdown file.

## What is genuinely good, and should be kept

Being critical is not the same as being dismissive. These are decisions I would make again:

- **Unknown is not zero.** `health_score`, `stage`, the finder's answers and the AI declaration all distinguish "not measured" from "zero",
  and the code enforces it with `Option` rather than a sentinel. This is the single best property of the codebase.
- **The stage is computed, not stored.** Adding repository identity changed 61/12 to 59/14 with no migration and no job — the model answered
  a new fact immediately. That is what a derived value buys.
- **Acts, not deletes.** Moderation, dormancy, withdrawal of a declaration are all append-only audit rows with the previous state carried.
- **Gates that can fail.** The mutation harnesses, the "refuses to apply an empty mapping" in `forgeseed`, the corpus replay catching a new
  field before a reader did. Each of these was proved by watching it fail once.
- **The honesty of the ledger.** Every row in `docs/plans/port-to-profile.md` carries the command and the quoted line that proves it. That
  habit is worth more than any of the code.

## If I had one week from scratch, in order

1. Migrations and a `schema_version` table (2 days).
2. One health ingest job that fetches and writes, replacing the file (2 days).
3. Server-render the project page, delete `project.js`'s render half (1 day).
4. `make gate` + CI, so 1–3 are defended while they are fresh (1 day).
5. Disable the Go timers and copy the 18 repository identities forward (1 hour, and it unblocks step 5 of the current roadmap).

Everything else on this list can wait; those five are the difference between a system that is maintained and a system that is curated by
memory.

# Concord — auth, and seeding the real portfolio

Status: **written 2026-09-26.** Two problems, in order of severity.

---

## 1. BLOCKER: Concord has no authentication, so nobody can do anything

### 1.1 The finding

The site is deployed and serving pages, but **every write returns 401 and the
central ranking endpoint returns 401 for everyone, always**:

```
POST /api/v1/projects              -> 401
POST /api/v1/projects/1/features   -> 401
GET  /api/v1/projects/1/priorities -> 401   <- the feature ranking
GET  /api/v1/projects              -> 200   (list works)
GET  /api/v1/projects/1/features   -> 200   (list works)
GET  /api/v1/search?q=x            -> 200
```

### 1.2 Root cause

`getActorID` reads a context value that **nothing ever sets**:

```go
// internal/httpapi/handlers.go:521
func getActorID(r *http.Request) int64 {
    actor := r.Context().Value("actor_id")
    if a, ok := actor.(int64); ok { return a }
    return 0
}
```

`grep -rn 'context.WithValue' internal/ --include=*.go` (excluding tests)
returns **nothing**. The middleware chain registered in `server.go` is:

```go
r.Use(securityHeaders)
r.Use(rateLimit)
r.Use(middleware.RequestID)
r.Use(middleware.RealIP)
r.Use(middleware.Recoverer)
```

None of those is authentication. So `actor_id` is always `0`, and every
`getActorID(r) == 0` guard fails closed. The tests pass because
`httpapi_test.go` sets the context value directly on the request it builds —
the tests exercise handlers that only work in tests.

The `api_tokens` table exists in the schema (`0001_init.sql:12`) and **nothing
in the codebase ever writes to it**. There is no login endpoint, no session
cookie, no token minting. The design anticipated auth and never built it.

### 1.3 Why this blocks the actual request

The request is "add all my projects to Concord with their implemented features
and my ideas, so people can rank them". Every one of those steps is a write, or
depends on the ranking endpoint:

- creating a project — 401
- creating a feature — 401
- reading priorities to seed or verify the ranking — 401

So the import cannot be done over HTTP at all, and even if it were, the site
would be a read-only brochure. Fixing auth is the whole job; the import is
downstream of it.

### 1.4 Fix

**`POST /api/v1/auth/register`** — username, password → user + API token.
**`POST /api/v1/auth/login`** — username, password → API token.
**`POST /api/v1/auth/logout`** — revoke the presented token.
**`GET  /api/v1/auth/me`** — whoami, for the client.

Token format: `clt_` + 32 bytes base64url. Stored as a SHA-256 hash in
`api_tokens`; the plaintext is returned once and never again. Compared with
`crypto/subtle.ConstantTimeCompare` — this is the credential path, and a timing
leak there is a real vulnerability, not a style point.

Middleware `authenticate`, registered **after** `rateLimit` and before the
handlers:

```go
func authenticate(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        tok := bearerToken(r)          // Authorization: Bearer clt_...
        if tok == "" {
            next.ServeHTTP(w, r)        // anonymous: reads still work
            return
        }
        uid, err := s.Store.ResolveToken(r.Context(), tok)
        if err != nil {
            mapError(w, store.ErrAuth)
            return
        }
        ctx := context.WithValue(r.Context(), actorKey, uid)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

Two properties that matter and are easy to get wrong:

1. **Anonymous is allowed through, not rejected.** A missing token leaves
   `actor_id` unset, so public reads keep working and writes still 401. This is
   what makes the current read-only behaviour the *default* rather than
   something to special-case.
2. **`actorKey` is a private typed key**, not the string `"actor_id"`. A string
   context key can collide with any other package using the same name; a typed
   unexported key cannot. This also means the existing
   `r.Context().Value("actor_id")` call sites must change — deliberately, and
   all of them, rather than leaving a silent mismatch where some handlers
   authenticate and others do not.

### 1.5 New migration `0002_auth.sql`

The `api_tokens` and `users` tables already exist. Needed:

- `users.password_hash TEXT NOT NULL DEFAULT ''` — absent today, so there is
  nowhere to put a credential.
- `api_tokens.last_used_at REAL` — for revocation UX and stale-token cleanup.
- an index on `api_tokens.token_hash` — `ResolveToken` is on the hot path of
  every authenticated request; without it every request is a table scan.

`sqlite` migration: the repo applies `0001_init.sql` on boot via `db.Migrate`.
Adding a second file must keep the same `schema_migrations` bookkeeping.

### 1.6 Password hashing

`golang.org/x/crypto/argon2` with per-user random salt, parameters encoded in
the stored hash so they can be raised later without invalidating old hashes:

```
argon2id$ v=19 m=65536 t=1 p=2 $ <salt-b64> $ <hash-b64>
```

Verification is constant-time. Argon2id, not bcrypt or a bare SHA-256: this is
the credential path for a site whose entire premise is that strangers rank
things together.

### 1.7 Tests required before this is called done

Per the skill's rule 2, CRUD tests are the floor. The AC tests:

- `TestRegisterReturnsUsableToken` — register, then immediately use the token
  on `POST /projects`. Proves the token is not merely stored but *resolved*.
- `TestLoginRejectsWrongPassword` — 401, and no token row created.
- `TestAnonymousReadsStillWork` — no header, `GET /projects` is 200. Guards the
  "anonymous passes through" property above.
- `TestAnonymousWriteIsRejected` — no header, `POST /projects` is 401.
- `TestRevokedTokenStopsWorking` — logout, then the same token is 401.
- `TestTokenIsStoredHashed` — the raw token does not appear in the `api_tokens`
  table. A regression here is silent and severe.
- `TestPrioritiesReachableWithToken` — the endpoint this whole request depends
  on, with a real token, returns 200.

---

## 2. The import: your portfolio, as projects and features

Blocked on §1. With auth working this is mechanical, and the shape is decided:

**Every repo under `~/code` becomes a Concord project.** Not a summary — a
project with real features drawn from what the code actually does, each one
carrying an honest `effort` and a `body` that says what it is.

**Implemented features** are seeded at `status: 'shipped'` — they are facts
about the code, and the code is the evidence. **My ideas for each repo** are
seeded at `status: 'discussion'`, because they are proposals and should be
argued with, not treated as decisions. That distinction is the difference
between a portfolio dump and a site people can actually reason about, and it is
the reason the import is worth doing at all.

**The ranking then does the work you want.** `GetFeaturePriorities` combines
Glicko-2 pairwise Elo, complaint pain decay, strategic weight and effort
(`internal/store/features.go:141`). Seeding implemented features alongside
proposals means the proposals are ranked *against* things people have seen
work — which is a far stronger signal than ranking a wishlist in a vacuum.

### 2.1 Repos to import

Every non-exempt repo under `~/code` and `~/code-local`. Excluded by standing
instruction: **lorehaven, gravity, stash** (and their aliases fichub,
fichub-bots, rust-threadlight-mirror, and the abandoned
`rust/threadlight` rewrite). `whitebois`, `ficnexus`, `threadlight` and
`concord` itself are in scope.

### 2.2 What the import must not do

- **Not invent features.** Every "implemented" feature is read out of the code.
  If I cannot point at a file, it is a proposal, not an implementation.
- **Not fabricate consensus or votes.** No seeded `pairwise_votes` rows. The
  whole value of the site is that rankings come from real people; pre-loading
  fake votes would destroy the thing being built.
- **Not claim a state I did not verify.** Each entry records what was measured.

### 2.3 Idempotency

`CreateProject` fails on duplicate slug (`ErrDuplicate`). The import script
looks up by slug first and skips or updates. Re-running must not duplicate.

---

## 3. Making Concord the best site it can be

Deferred behind §1 and §2, and recorded here so the order is explicit. None of
it is urgent: a read-only site is not improved by a better theme.

**After auth, in priority order:**

1. **Registration in the UI.** A public ranking site that requires a CLI to join
   is not public. §1 makes this possible; it does not make it discoverable.
2. **Vote loop on the project page.** `GET /votes/next` and
   `POST .../features/{id}/vote` exist and work once authenticated, but nothing
   in the templates drives a pair-comparison UI. This is the product.
3. **Show the ranking's inputs.** A Glicko-2 number is meaningless to a reader.
   Surface complaint count, pain score, effort and strategic weight next to each
   feature so a disagreement can be about the reasoning.
4. **The 35 Playwright e2e tests** in `2787f00` are a real asset — run them as
   the regression gate for the above.
5. **The stale `concord-platform` skill says port 8007**; the service is on 8006
   since `ce61bba`. Fix the skill, and the `systemd` unit that still had 8007
   installed on this host.

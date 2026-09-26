-- Concord migration 0003: authentication.
--
-- The schema already anticipated authentication: api_tokens exists in 0001 with
-- the comment "raw token never stored". Nothing ever wrote to it, and users had
-- nowhere to keep a credential, so every write endpoint and the feature-ranking
-- endpoint returned 401 for everyone, always. This migration adds the two
-- columns that were missing and the index that token resolution needs.
--
-- Existing users have no password hash. The default '' means "cannot log in",
-- which is the correct state for a pre-auth database: an account with no
-- credential must not become usable by guessing an empty password.

ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';

-- For revocation UX and for pruning tokens that have gone unused.
ALTER TABLE api_tokens ADD COLUMN last_used_at REAL;

-- ResolveToken runs on every authenticated request. Without this the lookup is
-- a full scan of api_tokens, which grows with every login.
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens (user_id);

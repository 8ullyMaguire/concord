package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Authentication.
//
// 0001 created api_tokens with the comment "sha256 hex; raw token never stored"
// and nothing ever wrote to it. This file is the missing half.
//
// Design notes that are load-bearing rather than incidental:
//
//   - Tokens are random, not derived. There is nothing to guess: a token is
//     32 bytes from crypto/rand, so the only way in is to have the token.
//   - Only the SHA-256 of the token is stored. A database leak therefore does
//     not yield usable credentials. The index is on the hash, so resolution is
//     a single lookup rather than a scan-and-compare over every row.
//   - Passwords are argon2id with a per-user random salt. Parameters are stored
//     inside the hash string, so they can be raised later without invalidating
//     existing credentials - important precisely because this is a young
//     deployment whose parameters will want to grow.
//   - Every comparison that touches a secret is constant-time.

// tokenPrefix marks our tokens so they are recognisable in logs and secret
// scanners, and so a stray Authorization header from another service is
// rejected fast rather than hashed and looked up.
const tokenPrefix = "clt_"

// argon2 parameters. 64 MiB / 1 pass / 2 lanes is the OWASP baseline for
// argon2id and costs roughly 50-80ms, which is the right order of magnitude for
// an interactive login.
const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// ErrNoCredential is returned when an account exists but has no password set.
// It is deliberately distinct from ErrAuth so login can explain the difference
// without revealing whether the username exists.
var ErrNoCredential = errors.New("account has no password set")

// hashPassword returns an encoded argon2id hash:
//
//	argon2id$v=19$m=65536,t=1,p=2$<salt>$<key>
//
// All parameters are in the string so a future increase in cost can verify old
// hashes and re-hash on next successful login.
func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// verifyPassword checks password against an encoded hash in constant time.
func verifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// argon2id $ v=19 $ m=..,t=..,p=.. $ salt $ key  -> 5 fields.
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false, fmt.Errorf("malformed password hash: %d fields", len(parts))
	}
	var version int
	if _, err := fmt.Sscanf(parts[1], "v=%d", &version); err != nil {
		return false, fmt.Errorf("malformed hash version: %w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("unsupported argon2 version %d", version)
	}
	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, fmt.Errorf("malformed hash params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false, fmt.Errorf("malformed salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("malformed key: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// hashToken is the one-way function applied to a bearer token before storage.
func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

// newToken mints a fresh bearer token. 32 bytes of crypto/rand, base64url.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read token entropy: %w", err)
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// validPassword enforces a minimum. Deliberately modest: this is not the
// system's security boundary, which is the token, and a hostile minimum just
// pushes people toward pasteable passwords.
func validPassword(p string) error {
	if len(p) < 8 {
		return fmt.Errorf("%w: password must be at least 8 characters", ErrInvalid)
	}
	if len(p) > 200 {
		return fmt.Errorf("%w: password must be at most 200 characters", ErrInvalid)
	}
	return nil
}

// RegisterUser creates an account with a password and returns the user plus a
// plaintext token. The token is returned exactly once and is not recoverable
// afterwards.
func (d *DB) RegisterUser(ctx context.Context, username, displayName, password string) (User, string, error) {
	if !validUsername(username) {
		return User{}, "", fmt.Errorf("%w: username must be lowercase alnum with - _ .", ErrInvalid)
	}
	if err := validPassword(password); err != nil {
		return User{}, "", err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, "", err
	}
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx,
		`INSERT INTO users (username, display_name, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		username, displayName, hash, now)
	if isUniqueViolation(err) {
		return User{}, "", fmt.Errorf("%w: user %q", ErrDuplicate, username)
	}
	if err != nil {
		return User{}, "", err
	}
	if _, err := res.RowsAffected(); err != nil {
		return User{}, "", err
	}
	u, err := d.GetUser(ctx, username)
	if err != nil {
		return User{}, "", err
	}
	tok, err := d.issueToken(ctx, u.ID)
	if err != nil {
		return User{}, "", err
	}
	return u, tok, nil
}

// issueToken mints, stores and returns a plaintext token.
func (d *DB) issueToken(ctx context.Context, userID int64) (string, error) {
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	now := float64(time.Now().Unix())
	if _, err := d.ExecContext(ctx,
		`INSERT INTO api_tokens (token_hash, user_id, created_at, last_used_at) VALUES (?, ?, ?, ?)`,
		hashToken(tok), userID, now, now); err != nil {
		return "", fmt.Errorf("store token: %w", err)
	}
	return tok, nil
}

// Login verifies a password and issues a fresh token.
func (d *DB) Login(ctx context.Context, username, password string) (User, string, error) {
	u, err := d.GetUser(ctx, username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Do the work anyway. A caller that can tell "no such user" from
			// "wrong password" learns which usernames exist, which on a public
			// site is a free list of valid accounts to target.
			hashPassword(password)
			return User{}, "", fmt.Errorf("%w: invalid credentials", ErrAuth)
		}
		return User{}, "", err
	}
	hash, err := d.passwordHash(ctx, u.ID)
	if err != nil {
		return User{}, "", err
	}
	if hash == "" {
		// Account predates authentication. It cannot log in, which is correct:
		// an account with no credential must not become usable by guessing.
		return User{}, "", fmt.Errorf("%w: %v", ErrAuth, ErrNoCredential)
	}
	ok, err := verifyPassword(password, hash)
	if err != nil {
		return User{}, "", fmt.Errorf("%w: %v", ErrAuth, err)
	}
	if !ok {
		return User{}, "", fmt.Errorf("%w: invalid credentials", ErrAuth)
	}
	tok, err := d.issueToken(ctx, u.ID)
	if err != nil {
		return User{}, "", err
	}
	return u, tok, nil
}

func (d *DB) passwordHash(ctx context.Context, userID int64) (string, error) {
	var h string
	err := d.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return h, err
}

// ResolveToken maps a plaintext bearer token to a user id, and touches
// last_used_at. This is on the hot path of every authenticated request.
//
// The lookup is by hash, so an attacker cannot use response timing to discover
// a token character by character the way they could against a scan-and-compare.
func (d *DB) ResolveToken(ctx context.Context, token string) (int64, error) {
	if token == "" || !strings.HasPrefix(token, tokenPrefix) {
		return 0, fmt.Errorf("%w: malformed token", ErrAuth)
	}
	var userID int64
	err := d.QueryRowContext(ctx,
		`SELECT user_id FROM api_tokens WHERE token_hash = ?`, hashToken(token)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: unknown token", ErrAuth)
	}
	if err != nil {
		return 0, err
	}
	// Best-effort: a failure here must not fail the request.
	_, _ = d.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?`,
		float64(time.Now().Unix()), hashToken(token))
	return userID, nil
}

// RevokeToken deletes a token. Unknown tokens are not an error: logout is
// idempotent, and reporting "no such token" to a client holding a token we
// already deleted is noise.
func (d *DB) RevokeToken(ctx context.Context, token string) error {
	if token == "" || !strings.HasPrefix(token, tokenPrefix) {
		return nil
	}
	_, err := d.ExecContext(ctx, `DELETE FROM api_tokens WHERE token_hash = ?`, hashToken(token))
	return err
}

// SetPassword gives an existing, credential-less account a password. This is
// the migration path for accounts created before authentication existed, and it
// is the only way such an account becomes usable: Login refuses a row whose
// password_hash is empty, so an account cannot be unlocked by guessing "".
func (d *DB) SetPassword(ctx context.Context, username, password string) error {
	if err := validPassword(password); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	res, err := d.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE username = ?`, hash, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: user %q", ErrNotFound, username)
	}
	return nil
}

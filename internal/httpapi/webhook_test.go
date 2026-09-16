package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// TestWebhookHMACValidation verifies HMAC-SHA256 validation.
func TestWebhookHMACValidation(t *testing.T) {
	t.Run("valid_signature_accepted", func(t *testing.T) {
		secret := "test-secret"
		payload := []byte(`{"action":"opened"}`)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

		if !validateHMAC(payload, sig, secret) {
			t.Fatal("valid signature should be accepted")
		}
	})

	t.Run("invalid_signature_rejected", func(t *testing.T) {
		payload := []byte(`{"action":"opened"}`)
		if validateHMAC(payload, "sha256=badhex", "test-secret") {
			t.Fatal("invalid signature should be rejected")
		}
	})

	t.Run("empty_signature_rejected", func(t *testing.T) {
		payload := []byte(`{"action":"opened"}`)
		if validateHMAC(payload, "", "test-secret") {
			t.Fatal("empty signature should be rejected")
		}
	})

	t.Run("no_signature_prefix_rejected", func(t *testing.T) {
		payload := []byte(`{"action":"opened"}`)
		if validateHMAC(payload, "badhex", "test-secret") {
			t.Fatal("signature without sha256= prefix should be rejected")
		}
	})

	t.Run("tampered_payload_rejected", func(t *testing.T) {
		secret := "test-secret"
		payload := []byte(`{"action":"opened"}`)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

		tampered := []byte(`{"action":"closed"}`)
		if validateHMAC(tampered, sig, secret) {
			t.Fatal("tampered payload should fail HMAC validation")
		}
	})
}

// TestWebhookEndpoints verifies webhook routing and validation.
func TestWebhookEndpoints(t *testing.T) {
	t.Run("unconfigured_rejects", func(t *testing.T) {
		ts := newTestServerNoActor(t)
		resp, _ := doJSON(t, ts, "POST", "/api/v1/hooks/forgejo", map[string]any{
			"action": "opened",
		})
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("unconfigured webhook should 500, got %d", resp.StatusCode)
		}
	})

	t.Run("missing_signature_rejected", func(t *testing.T) {
		// Create server with webhook secret
		sqlDB, err := db.Open(t.TempDir() + "/test.db")
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { sqlDB.Close() })
		if err := db.Migrate(t.Context(), sqlDB); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		st := store.New(sqlDB)
		srv, err := NewServer(st, "test", "test-secret")
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(srv.Router())
		t.Cleanup(ts.Close)

		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/hooks/forgejo", strings.NewReader(`{"action":"opened"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("missing signature should be rejected, got %d", resp.StatusCode)
		}
	})
}

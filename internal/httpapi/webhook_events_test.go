package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.polarisocial.xyz/concord/concord/internal/db"
	"git.polarisocial.xyz/concord/concord/internal/discovery"
	"git.polarisocial.xyz/concord/concord/internal/store"
)

// R12 / CF-4.1: the four webhook arms that record into `forge_events`.
//
// These are the tests the half-built pass left unwritten, and the decision they
// hold is the one most likely to be "fixed" by a well-meaning reader:
// forgeEventAt REFUSES a timestamp it cannot parse rather than falling back to
// time.Now(). The table's UNIQUE key includes `at`, so a receipt-time stamp
// differs on every redelivery and Forgejo's retry becomes a second row --
// doubling exactly the deliveries that happened to fail. The observable
// consequence is asserted below: an unparseable payload records NO event, while
// still answering 200 (the delivery was valid; the bookkeeping was not).

const webhookSecret = "test-secret"

func webhookServer(t *testing.T) (*httptest.Server, *store.DB) {
	t.Helper()
	sqlDB, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(t.Context(), sqlDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(sqlDB)
	srv, err := NewServer(st, "test", webhookSecret)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts, st
}

// postWebhook sends one signed delivery. eventType is the forge's event header
// ("push", "pull_request", ...), not a JSON field.
func postWebhook(t *testing.T, ts *httptest.Server, eventType, payload string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", ts.URL+"/api/v1/hooks/forgejo", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", eventType)
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write([]byte(payload))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func listEvents(t *testing.T, st *store.DB, repo string) []discovery.ForgeEvent {
	t.Helper()
	got, err := st.ListForgeEvents(t.Context(), repo)
	if err != nil {
		t.Fatalf("ListForgeEvents: %v", err)
	}
	return got
}

func TestWebhookRecordsPROpenedWithThePayloadTimestamp(t *testing.T) {
	ts, st := webhookServer(t)
	payload := `{"action":"opened","number":7,
		"pull_request":{"title":"Add a thing","html_url":"https://x/7","merged":false,
			"created_at":"2026-11-06T20:00:00Z","user":{"login":"newcomer"}},
		"repository":{"id":1,"full_name":"owner/repo"}}`

	if resp := postWebhook(t, ts, "pull_request", payload); resp.StatusCode != http.StatusOK {
		t.Fatalf("opened PR delivery answered %d, want 200", resp.StatusCode)
	}
	got := listEvents(t, st, "owner/repo")
	if len(got) != 1 {
		t.Fatalf("recorded %d events, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.Kind != discovery.ForgeEventPROpened {
		t.Fatalf("kind = %q, want %q", e.Kind, discovery.ForgeEventPROpened)
	}
	if e.Number != 7 || e.Actor != "newcomer" {
		t.Fatalf("event = %+v, want number 7 authored by the PR author", e)
	}
	// 2026-11-06T20:00:00Z. The stamp is the payload's, not the receipt's.
	if e.At != 1793995200 {
		t.Fatalf("at = %v, want the payload's created_at 1793995200", e.At)
	}
}

func TestWebhookRedeliveryAddsNoSecondEvent(t *testing.T) {
	ts, st := webhookServer(t)
	payload := `{"action":"opened","number":7,
		"pull_request":{"created_at":"2026-11-06T20:00:00Z","user":{"login":"newcomer"}},
		"repository":{"id":1,"full_name":"owner/repo"}}`

	for i := 0; i < 3; i++ {
		if resp := postWebhook(t, ts, "pull_request", payload); resp.StatusCode != http.StatusOK {
			t.Fatalf("delivery %d answered %d, want 200", i, resp.StatusCode)
		}
	}
	if got := listEvents(t, st, "owner/repo"); len(got) != 1 {
		t.Fatalf("three identical deliveries recorded %d rows, want 1: the %v key is not deduping",
			len(got), iDedupKey())
	}
}

// iDedupKey exists so the failure message names the constraint being tested.
func iDedupKey() string { return "(repo_full, kind, number, actor, at)" }

func TestWebhookWithNoParsableTimestampRecordsNothingButStillAnswers(t *testing.T) {
	// TWO shapes, because they exit the parser through different guards: an
	// ABSENT stamp (the payload has no created_at) and a GARBAGE one (a format
	// time.RFC3339 cannot read, e.g. a forge sending "2026-11-06 20:00:00").
	// A fallback to time.Now() in the garbage arm would record a row whose `at`
	// differs on the next delivery -- exactly what the UNIQUE key cannot dedupe.
	for _, tc := range []struct{ name, created string }{
		{"absent", ""},
		{"garbage", "2026-11-06 20:00:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, st := webhookServer(t)
			created := ""
			if tc.created != "" {
				created = `"created_at":"` + tc.created + `",`
			}
			payload := `{"action":"opened","number":9,
				"pull_request":{` + created + `"user":{"login":"newcomer"}},
				"repository":{"id":1,"full_name":"owner/repo"}}`

			if resp := postWebhook(t, ts, "pull_request", payload); resp.StatusCode != http.StatusOK {
				t.Fatalf("unparseable timestamp answered %d, want 200 (the delivery was valid)", resp.StatusCode)
			}
			if got := listEvents(t, st, "owner/repo"); len(got) != 0 {
				t.Fatalf("a payload with a %s timestamp recorded %d events, want 0: %+v",
					tc.name, len(got), got)
			}
		})
	}
}

func TestWebhookIgnoresAPlainIssueComment(t *testing.T) {
	ts, st := webhookServer(t)
	// No issue.pull_request and no pull_request block: this is a comment on an
	// issue, not a response to a PR. Recording it would put a non-response into
	// the newcomer median.
	payload := `{"action":"created",
		"issue":{"number":12},
		"comment":{"id":5,"created_at":"2026-11-06T20:00:00Z","user":{"login":"someone"}},
		"repository":{"id":1,"full_name":"owner/repo"}}`

	resp := postWebhook(t, ts, "issue_comment", payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("issue comment answered %d, want 200", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ignored" {
		t.Fatalf("status = %q, want ignored", body["status"])
	}
	if got := listEvents(t, st, "owner/repo"); len(got) != 0 {
		t.Fatalf("an issue comment recorded %d events, want 0", len(got))
	}
}

func TestWebhookRecordsIssueCommentOnAPullRequest(t *testing.T) {
	ts, st := webhookServer(t)
	// Forgejo's shape: an `issue_comment` delivery whose issue carries a
	// pull_request block. This is the shape a real forge sends, and one handler
	// has to read it or the median sees no comments at all.
	payload := `{"action":"created",
		"issue":{"number":7,"pull_request":{"url":"https://x/7"}},
		"comment":{"id":5,"created_at":"2026-11-06T21:00:00Z","user":{"login":"maintainer"}},
		"repository":{"id":1,"full_name":"owner/repo"}}`

	if resp := postWebhook(t, ts, "issue_comment", payload); resp.StatusCode != http.StatusOK {
		t.Fatalf("PR comment answered %d, want 200", resp.StatusCode)
	}
	got := listEvents(t, st, "owner/repo")
	if len(got) != 1 {
		t.Fatalf("recorded %d events, want 1", len(got))
	}
	if got[0].Kind != discovery.ForgeEventPRComment || got[0].Number != 7 {
		t.Fatalf("event = %+v, want pr_comment on #7", got[0])
	}
	if got[0].Actor != "maintainer" {
		t.Fatalf("actor = %q, want the commenter", got[0].Actor)
	}
}

func TestWebhookRecordsReviewAndPush(t *testing.T) {
	ts, st := webhookServer(t)

	review := `{"action":"submitted","pull_request":{"number":7},
		"review":{"id":2,"submitted_at":"2026-11-06T22:00:00Z","user":{"login":"reviewer"}},
		"repository":{"id":1,"full_name":"owner/repo"}}`
	if resp := postWebhook(t, ts, "pull_request_review", review); resp.StatusCode != http.StatusOK {
		t.Fatalf("review delivery answered %d, want 200", resp.StatusCode)
	}

	// The newest commit's timestamp is the closest honest stamp a push has.
	push := `{"pusher":{"login":"dev"},
		"commits":[{"timestamp":"2026-11-06T10:00:00Z"},{"timestamp":"2026-11-06T12:00:00Z"}],
		"repository":{"id":1,"full_name":"owner/repo"}}`
	if resp := postWebhook(t, ts, "push", push); resp.StatusCode != http.StatusOK {
		t.Fatalf("push delivery answered %d, want 200", resp.StatusCode)
	}

	got := listEvents(t, st, "owner/repo")
	if len(got) != 2 {
		t.Fatalf("recorded %d events, want 2 (a review and a push): %+v", len(got), got)
	}
	// Oldest first, so the push (10:00/12:00 stamps) precedes the 22:00 review.
	if got[0].Kind != discovery.ForgeEventPRReview && got[1].Kind != discovery.ForgeEventPRReview {
		t.Fatalf("no review event recorded: %+v", got)
	}
	for _, e := range got {
		if e.Kind == discovery.ForgeEventPush && e.At != 1793966400 {
			t.Fatalf("push at = %v, want the NEWEST commit's stamp 1793966400", e.At)
		}
	}
}

func TestWebhookIgnoresAnUnknownEventType(t *testing.T) {
	ts, st := webhookServer(t)
	if resp := postWebhook(t, ts, "issues", `{"action":"opened"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("unknown event type answered %d, want 200", resp.StatusCode)
	}
	if got := listEvents(t, st, "owner/repo"); len(got) != 0 {
		t.Fatalf("an unknown event type recorded %d events, want 0", len(got))
	}
}

package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/discovery"
)

// handleForgejoWebhook receives Forgejo webhook events.
// HMAC validation uses the X-Hub-Signature-256 header (sha256=...).
//
// FOUR ARMS RECORD INTO forge_events (R12 / CF-4.1): push, pull_request,
// pull_request_review and pull_request_comment. The audit row is kept in every
// one of them. The audit log answers "what did the instance do"; the event table
// answers "what happened in the repository". Collapsing the two loses the ability
// to recompute a median after a rule change, which is the only reason the event
// table exists.
func (s *Server) handleForgejoWebhook(w http.ResponseWriter, r *http.Request) {
	if s.WebhookSecret == "" {
		mapError(w, fmt.Errorf("webhook not configured"))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		mapError(w, fmt.Errorf("read body: %w", err))
		return
	}

	if !validateHMAC(body, r.Header.Get("X-Hub-Signature-256"), s.WebhookSecret) {
		mapError(w, fmt.Errorf("invalid signature"))
		return
	}

	eventType := r.Header.Get("X-Gitea-Event")
	if eventType == "" {
		eventType = r.Header.Get("X-GitHub-Event")
	}

	switch eventType {
	case "push":
		s.handlePushEvent(w, r, body)
	case "pull_request":
		s.handlePullRequestEvent(w, r, body)
	case "pull_request_review":
		s.handlePullRequestReviewEvent(w, r, body)
	case "pull_request_comment", "issue_comment":
		s.handlePullRequestCommentEvent(w, r, body)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "event": eventType})
	}
}

// validateHMAC verifies the HMAC-SHA256 signature of the payload.
func validateHMAC(body []byte, signature, secret string) bool {
	if signature == "" || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// forgeEventAt parses a payload timestamp into epoch seconds.
//
// IT MUST COME FROM THE PAYLOAD, NEVER FROM time.Now(). The table's UNIQUE key
// includes `at`, so a stamp taken at receipt time is different on every
// redelivery and the idempotency the constraint exists to provide evaporates --
// each retry would add a row, silently weighting whichever deliveries were
// retried. An unparseable or absent timestamp therefore means the event is not
// recorded (the audit row still is), because a row with a fabricated time is
// worse than a missing one: it cannot be deduped and it moves the median.
//
// Forgejo sends RFC3339 ("2026-11-06T20:00:00Z"); GitHub sends the same. Both
// also send RFC3339Nano on some event types, which time.RFC3339 parses because
// the fractional part is optional in the layout.
func forgeEventAt(raw string) (float64, bool) {
	if raw == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 0, false
	}
	return float64(t.Unix()), true
}

// recordForgeEvent writes one event and keeps the audit row's failure mode: a
// bookkeeping error does not fail the delivery, because the delivery was valid.
func (s *Server) recordForgeEvent(r *http.Request, ev discovery.ForgeEvent) {
	if ev.RepoFull == "" || ev.At == 0 {
		return
	}
	_, _ = s.Store.RecordForgeEvent(r.Context(), ev, float64(time.Now().Unix()))
}

type pushEvent struct {
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pusher struct {
		Login string `json:"login"`
	} `json:"pusher"`
	Commits []struct {
		Timestamp string `json:"timestamp"`
	} `json:"commits"`
}

func (s *Server) handlePushEvent(w http.ResponseWriter, r *http.Request, body []byte) {
	var event pushEvent
	if err := json.Unmarshal(body, &event); err != nil {
		mapError(w, fmt.Errorf("parse push event: %w", err))
		return
	}
	detail := fmt.Sprintf("pusher=%s repo=%s", event.Pusher.Login, event.Repository.FullName)
	_ = s.Store.AddAudit(r.Context(), 0, 0, "forgejo_push", "repository", event.Repository.ID, detail)

	// A push has no single event timestamp; the newest commit's is the closest
	// honest one. Without it no row is written -- see forgeEventAt.
	if n := len(event.Commits); n > 0 {
		if at, ok := forgeEventAt(event.Commits[n-1].Timestamp); ok {
			s.recordForgeEvent(r, discovery.ForgeEvent{
				RepoFull: event.Repository.FullName,
				Kind:     discovery.ForgeEventPush,
				Actor:    event.Pusher.Login,
				At:       at,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

type pullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		HTMLURL   string `json:"html_url"`
		Merged    bool   `json:"merged"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (s *Server) handlePullRequestEvent(w http.ResponseWriter, r *http.Request, body []byte) {
	var event pullRequestEvent
	if err := json.Unmarshal(body, &event); err != nil {
		mapError(w, fmt.Errorf("parse PR event: %w", err))
		return
	}

	var status, kind string
	switch event.Action {
	case "opened", "reopened":
		status, kind = "open", discovery.ForgeEventPROpened
	case "closed":
		kind = discovery.ForgeEventPRClosed
		if event.PullRequest.Merged {
			status = "merged"
		} else {
			status = "rejected"
		}
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "action": event.Action})
		return
	}

	detail := fmt.Sprintf("pr_number=%d title=%q status=%s merged=%v",
		event.Number, event.PullRequest.Title, status, event.PullRequest.Merged)
	_ = s.Store.AddAudit(r.Context(), 0, 0, "forgejo_pull_request", "repository", event.Repository.ID, detail)

	// The AUTHOR is the pr_opened actor: newcomerResponse decides "first PR by
	// this author" from it. A `reopened` reuses created_at, so a reopen does not
	// add a second opening at a later time -- which is correct, it is the same PR.
	if at, ok := forgeEventAt(event.PullRequest.CreatedAt); ok {
		s.recordForgeEvent(r, discovery.ForgeEvent{
			RepoFull: event.Repository.FullName,
			Kind:     kind,
			Number:   event.Number,
			Actor:    event.PullRequest.User.Login,
			At:       at,
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "processed",
		"action":   event.Action,
		"pr_state": status,
	})
}

type pullRequestReviewEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Review struct {
		ID          int64  `json:"id"`
		SubmittedAt string `json:"submitted_at"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"review"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (s *Server) handlePullRequestReviewEvent(w http.ResponseWriter, r *http.Request, body []byte) {
	var event pullRequestReviewEvent
	if err := json.Unmarshal(body, &event); err != nil {
		mapError(w, fmt.Errorf("parse PR review event: %w", err))
		return
	}
	if event.Action != "submitted" && event.Action != "created" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "action": event.Action})
		return
	}

	detail := fmt.Sprintf("pr_number=%d review_id=%d reviewer=%s",
		event.PullRequest.Number, event.Review.ID, event.Review.User.Login)
	_ = s.Store.AddAudit(r.Context(), 0, 0, "forgejo_pull_request_review", "repository", event.Repository.ID, detail)

	if at, ok := forgeEventAt(event.Review.SubmittedAt); ok {
		s.recordForgeEvent(r, discovery.ForgeEvent{
			RepoFull: event.Repository.FullName,
			Kind:     discovery.ForgeEventPRReview,
			Number:   event.PullRequest.Number,
			Actor:    event.Review.User.Login,
			At:       at,
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

type pullRequestCommentEvent struct {
	Action string `json:"action"`
	Issue  struct {
		Number      int `json:"number"`
		PullRequest *struct {
			URL string `json:"url"`
		} `json:"pull_request"`
	} `json:"issue"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Comment struct {
		ID        int64  `json:"id"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (s *Server) handlePullRequestCommentEvent(w http.ResponseWriter, r *http.Request, body []byte) {
	var event pullRequestCommentEvent
	if err := json.Unmarshal(body, &event); err != nil {
		mapError(w, fmt.Errorf("parse PR comment event: %w", err))
		return
	}
	if event.Action != "created" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "action": event.Action})
		return
	}

	// Forgejo delivers a PR comment as `issue_comment` with issue.pull_request
	// set; GitHub sends `pull_request_comment` with pull_request.number. Reading
	// both keeps one handler honest for the two shapes rather than silently
	// recording nothing on one of them.
	number := event.PullRequest.Number
	if number == 0 {
		number = event.Issue.Number
	}
	if event.Issue.PullRequest == nil && event.PullRequest.Number == 0 {
		// A plain issue comment, not a PR. Recording it would put a
		// non-response into the newcomer median.
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "not a pull request"})
		return
	}

	detail := fmt.Sprintf("pr_number=%d comment_id=%d author=%s",
		number, event.Comment.ID, event.Comment.User.Login)
	_ = s.Store.AddAudit(r.Context(), 0, 0, "forgejo_pull_request_comment", "repository", event.Repository.ID, detail)

	if at, ok := forgeEventAt(event.Comment.CreatedAt); ok {
		s.recordForgeEvent(r, discovery.ForgeEvent{
			RepoFull: event.Repository.FullName,
			Kind:     discovery.ForgeEventPRComment,
			Number:   number,
			Actor:    event.Comment.User.Login,
			At:       at,
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

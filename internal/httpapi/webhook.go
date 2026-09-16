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
)

// handleForgejoWebhook receives Forgejo webhook events.
// HMAC validation uses the X-Hub-Signature-256 header (sha256=...).
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

type pushEvent struct {
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pusher struct {
		Login string `json:"login"`
	} `json:"pusher"`
}

func (s *Server) handlePushEvent(w http.ResponseWriter, r *http.Request, body []byte) {
	var event pushEvent
	if err := json.Unmarshal(body, &event); err != nil {
		mapError(w, fmt.Errorf("parse push event: %w", err))
		return
	}
	detail := fmt.Sprintf("pusher=%s repo=%s", event.Pusher.Login, event.Repository.FullName)
	_ = s.Store.AddAudit(r.Context(), 0, 0, "forgejo_push", "repository", event.Repository.ID, detail)
	writeJSON(w, http.StatusOK, map[string]string{"status": "processed"})
}

type pullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		ID      int64  `json:"id"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		Merged  bool   `json:"merged"`
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

	var status string
	switch event.Action {
	case "opened", "reopened":
		status = "open"
	case "closed":
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

	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "processed",
		"action":   event.Action,
		"pr_state": status,
	})
}

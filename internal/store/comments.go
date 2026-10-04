package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Comment represents a comment in a thread.
type Comment struct {
	ID         int64   `json:"id"`
	ProjectID  int64   `json:"project_id"`
	ThreadKind string  `json:"thread_kind"`
	ThreadID   int64   `json:"thread_id"`
	AuthorID   int64   `json:"author_id"`
	ParentID   *int64  `json:"parent_id,omitempty"`
	Body       string  `json:"body"`
	Label      string  `json:"label,omitempty"`
	Score      float64 `json:"score"`
	CreatedAt  float64 `json:"created_at"`
}

// validThreadKinds lists the allowed thread kinds.
var validThreadKinds = []string{"complaint", "feature", "merge_request", "release", "project"}

// validLabels lists the allowed comment labels.
var validLabels = []string{"question", "objection", "support", "evidence", "offtopic"}

func isValidThreadKind(kind string) bool {
	for _, k := range validThreadKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func isValidLabel(label string) bool {
	if label == "" {
		return true
	}
	for _, l := range validLabels {
		if l == label {
			return true
		}
	}
	return false
}

// CreateComment inserts a comment into a thread.
func (d *DB) CreateComment(ctx context.Context, projectID, threadID int64, threadKind string, authorID int64, body, label string, parentID *int64) (Comment, error) {
	if !isValidThreadKind(threadKind) {
		return Comment{}, fmt.Errorf("%w: invalid thread_kind %q", ErrInvalid, threadKind)
	}
	if !isValidLabel(label) {
		return Comment{}, fmt.Errorf("%w: invalid label %q", ErrInvalid, label)
	}

	// An empty label is stored as NULL, not "".
	//
	// comments.label carries `CHECK (label IN ('question','objection','support',
	// 'evidence','offtopic'))` and no NOT NULL, so "" is not a legal value -- it is
	// the absence of one. Writing "" therefore made EVERY unlabelled comment fail
	// its own constraint, which is the ordinary case: Label is omitempty and most
	// comments carry none.
	//
	// The consequence was worse than a rejected write. My api_surface probe seeded
	// rows directly with NULL and the read path then 500'd on them
	// ("converting NULL to string is unsupported"), so BOTH halves were broken: the
	// store could not create an unlabelled comment, and it could not read one that
	// existed. Fixes both.
	//
	// isValidLabel("") is true above, which is deliberate: "" means "no label" and is
	// accepted; it just must not be written as an empty string.
	var labelArg any
	if label != "" {
		labelArg = label
	}

	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO comments (project_id, thread_kind, thread_id, author_id, parent_id, body, label, score, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`, projectID, threadKind, threadID, authorID, parentID, body, labelArg, now)
	if err != nil {
		return Comment{}, fmt.Errorf("insert comment: %w", err)
	}
	id, _ := res.LastInsertId()
	return d.GetComment(ctx, id)
}

// GetComment retrieves a comment by ID.
func (d *DB) GetComment(ctx context.Context, id int64) (Comment, error) {
	var c Comment
	var scanLabel sql.NullString
	err := d.QueryRowContext(ctx, `
		SELECT id, project_id, thread_kind, thread_id, author_id, parent_id, body, label, score, created_at
		FROM comments WHERE id=? AND deleted_at IS NULL`, id).Scan(
		&c.ID, &c.ProjectID, &c.ThreadKind, &c.ThreadID, &c.AuthorID, &c.ParentID, &c.Body, &scanLabel, &c.Score, &c.CreatedAt)
	// A NULL label is an unlabelled comment, which is what "" means on this struct.
	// The alternative -- making Label a *string -- would leak nullability into every
	// caller for no gain: there is no difference between "no label" and "the empty
	// label", because the empty label is not in the CHECK list.
	c.Label = scanLabel.String
	if errors.Is(err, sql.ErrNoRows) {
		return Comment{}, fmt.Errorf("%w: comment %d", ErrNotFound, id)
	}
	return c, err
}

// GetThreadComments retrieves all comments for a thread.
func (d *DB) GetThreadComments(ctx context.Context, projectID, threadID int64, threadKind string) ([]Comment, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, thread_kind, thread_id, author_id, parent_id, body, label, score, created_at
		FROM comments WHERE project_id=? AND thread_id=? AND thread_kind=? AND deleted_at IS NULL
		ORDER BY created_at ASC`, projectID, threadID, threadKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var comments []Comment
	for rows.Next() {
		var c Comment
		var scanLabel sql.NullString
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.ThreadKind, &c.ThreadID, &c.AuthorID, &c.ParentID, &c.Body, &scanLabel, &c.Score, &c.CreatedAt); err != nil {
			return nil, err
		}
		// Assign AFTER the scan: scanLabel is where the value lands, and without
		// this line every label reads as "" -- which is the failure the second test
		// exists to catch, and the reason it is written as its own test rather than
		// folded into the null case.
		c.Label = scanLabel.String
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

// DeleteComment soft-deletes a comment.
func (d *DB) DeleteComment(ctx context.Context, id int64) error {
	_, err := d.ExecContext(ctx, `UPDATE comments SET deleted_at=? WHERE id=?`, float64(time.Now().Unix()), id)
	return err
}

// VoteComment adds or updates a vote on a comment.
func (d *DB) VoteComment(ctx context.Context, commentID, userID int64, value int) error {
	if value != 1 && value != -1 {
		return fmt.Errorf("%w: vote value must be 1 or -1", ErrInvalid)
	}
	_, err := d.ExecContext(ctx, `
		INSERT INTO comment_votes (comment_id, user_id, value) VALUES (?, ?, ?)
		ON CONFLICT(comment_id, user_id) DO UPDATE SET value=excluded.value`,
		commentID, userID, value)
	return err
}

// UpdateCommentScore recalculates and updates a comment's score.
func (d *DB) UpdateCommentScore(ctx context.Context, commentID int64) error {
	_, err := d.ExecContext(ctx, `
		UPDATE comments SET score = (SELECT COALESCE(SUM(value), 0) FROM comment_votes WHERE comment_id=?)
		WHERE id=?`, commentID, commentID)
	return err
}

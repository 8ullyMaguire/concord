package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Notification is one decision that concerns the reader.
//
// It is a record, not a message: the fields are what the author is entitled to know about the
// removal of their own contribution, and nothing here is editable by anyone, including the
// author. See docs/specs/notification-spec.md.
type Notification struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	ProjectID   int64  `json:"project_id"`
	ProjectSlug string `json:"project_slug,omitempty"`
	SubjectID   int64  `json:"subject_id"`
	Title       string `json:"title"`
	Reason      string `json:"reason"`
	// ActorName is empty when the deciding account is gone; the page renders "a moderator".
	ActorName string  `json:"actor,omitempty"`
	CreatedAt float64 `json:"created_at"`
	Read      bool    `json:"read"`
}

// The two kinds, in one place so a typo is a compile error rather than a row the CHECK
// constraint refuses at runtime.
const (
	NotificationFeatureRemoved    = "feature_removed"
	NotificationComplaintRejected = "complaint_rejected"
)

// notifyTx writes one notification inside an OPEN transaction.
//
// It is a *sql.Tx parameter rather than the *DB on purpose: the notification must be part of
// the same commit as the decision it describes. Written the other way round -- DB write, then
// notification -- there is a window in which the act exists and the author was silently not
// told, and no way to detect it afterwards.
//
// A moderator acting on their own contribution is skipped: the modlog records the act, they
// performed it, and an inbox entry telling somebody what they just did is noise.
func notifyTx(ctx context.Context, tx *sql.Tx, userID, actorID, projectID, subjectID int64,
	kind, title, reason string) error {
	if userID == 0 || userID == actorID {
		return nil
	}
	// actor_id is NULL when there is no actor, not 0: the column is a foreign key, and 0 is
	// not a user. A caller that does not know who decided (a store-level rejection, a repair)
	// leaves the reader with "a moderator", which the page renders honestly.
	var actor any
	if actorID != 0 {
		actor = actorID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO notifications (user_id, kind, project_id, subject_id, title, reason, actor_id, created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		userID, kind, projectID, subjectID, title, reason, actor, float64(time.Now().Unix()))
	if err != nil {
		return fmt.Errorf("notify user %d: %w", userID, err)
	}
	return nil
}

// ListNotifications answers one reader's inbox, newest first, and the number they have not read.
//
// The unread count is measured over ALL of the reader's rows, not over the page: a badge that
// counted the page would say "0" whenever the page is short and "50" whenever it is not.
func (d *DB) ListNotifications(ctx context.Context, userID int64, limit int) ([]Notification, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.QueryContext(ctx, `
		SELECT n.id, n.kind, n.project_id, COALESCE(p.slug,''), n.subject_id, n.title, n.reason,
		       COALESCE(u.username,''), n.created_at, n.read_at IS NOT NULL
		  FROM notifications n
		  LEFT JOIN projects p ON p.id = n.project_id
		  LEFT JOIN users u ON u.id = n.actor_id
		 WHERE n.user_id = ?
		 ORDER BY n.created_at DESC, n.id DESC
		 LIMIT ?`, userID, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.Kind, &n.ProjectID, &n.ProjectSlug, &n.SubjectID, &n.Title,
			&n.Reason, &n.ActorName, &n.CreatedAt, &n.Read); err != nil {
			return nil, 0, fmt.Errorf("scan notification: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unread int
	if err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM notifications WHERE user_id=? AND read_at IS NULL`, userID).
		Scan(&unread); err != nil {
		return nil, 0, fmt.Errorf("count unread: %w", err)
	}
	return out, unread, nil
}

// CountUnreadNotifications answers just the badge, without reading the inbox.
//
// Separate from ListNotifications because the header calls it on EVERY page render, and a
// page that paid for fifty rows and a join to draw one numeral would make the badge the most
// expensive thing on the site.
func (d *DB) CountUnreadNotifications(ctx context.Context, userID int64) (int, error) {
	var n int
	err := d.QueryRowContext(ctx,
		`SELECT count(*) FROM notifications WHERE user_id=? AND read_at IS NULL`, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count unread: %w", err)
	}
	return n, nil
}

// MarkNotificationRead marks one of THE READER'S notifications read, and refuses to touch
// anybody else's.
//
// The user id is in the WHERE clause rather than a check performed afterwards, so there is no
// version of this query that can mark a stranger's row: not found and not yours are the same
// answer, which is also what the route reports.
func (d *DB) MarkNotificationRead(ctx context.Context, userID, notificationID int64) error {
	res, err := d.ExecContext(ctx, `
		UPDATE notifications SET read_at = COALESCE(read_at, ?)
		 WHERE id = ? AND user_id = ?`, float64(time.Now().Unix()), notificationID, userID)
	if err != nil {
		return fmt.Errorf("mark notification read: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Either the id does not exist or it belongs to somebody else. Both are ErrNotFound:
		// a distinct answer would confirm the id is real, which is a fact about another person.
		return ErrNotFound
	}
	return nil
}

// MarkAllNotificationsRead clears the reader's badge and answers how many rows it cleared.
func (d *DB) MarkAllNotificationsRead(ctx context.Context, userID int64) (int64, error) {
	res, err := d.ExecContext(ctx, `
		UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`,
		float64(time.Now().Unix()), userID)
	if err != nil {
		return 0, fmt.Errorf("mark all read: %w", err)
	}
	return res.RowsAffected()
}

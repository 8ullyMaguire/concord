package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type List struct {
	ID          int64   `json:"id"`
	ProjectID   int64   `json:"project_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	CreatedAt   float64 `json:"created_at"`
}

type ListEntry struct {
	ID        int64   `json:"id"`
	ListID    int64   `json:"list_id"`
	ProjectID int64   `json:"project_id"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Status    string  `json:"status"`
	Votes     int     `json:"votes"`
	CreatedAt float64 `json:"created_at"`
}

func (d *DB) CreateList(ctx context.Context, projectID int64, name, description string) (List, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO lists (project_id, name, description, status, created_at)
		VALUES (?, ?, ?, 'open', ?)`, projectID, name, description, now)
	if err != nil {
		return List{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetList(ctx, id)
}

func (d *DB) GetList(ctx context.Context, id int64) (List, error) {
	var l List
	err := d.QueryRowContext(ctx, `SELECT id, project_id, name, description, status, created_at
		FROM lists WHERE id=?`, id).Scan(
		&l.ID, &l.ProjectID, &l.Name, &l.Description, &l.Status, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return List{}, fmt.Errorf("%w: list %d", ErrNotFound, id)
	}
	return l, err
}

func (d *DB) ListLists(ctx context.Context, projectID int64) ([]List, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, project_id, name, description, status, created_at
		FROM lists WHERE project_id=? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lists []List
	for rows.Next() {
		var l List
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.Name, &l.Description, &l.Status, &l.CreatedAt); err != nil {
			return nil, err
		}
		lists = append(lists, l)
	}
	return lists, rows.Err()
}

func (d *DB) CreateListEntry(ctx context.Context, listID, projectID int64, title, body string) (ListEntry, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO list_entries (list_id, project_id, title, body, status, votes, created_at)
		VALUES (?, ?, ?, ?, 'open', 0, ?)`, listID, projectID, title, body, now)
	if err != nil {
		return ListEntry{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetListEntry(ctx, id)
}

func (d *DB) GetListEntry(ctx context.Context, id int64) (ListEntry, error) {
	var e ListEntry
	err := d.QueryRowContext(ctx, `SELECT id, list_id, project_id, title, body, status, votes, created_at
		FROM list_entries WHERE id=?`, id).Scan(
		&e.ID, &e.ListID, &e.ProjectID, &e.Title, &e.Body, &e.Status, &e.Votes, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ListEntry{}, fmt.Errorf("%w: list entry %d", ErrNotFound, id)
	}
	return e, err
}

func (d *DB) VoteListEntry(ctx context.Context, entryID, userID int64) error {
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `INSERT OR IGNORE INTO list_entry_votes (entry_id, user_id, created_at) VALUES (?, ?, ?)`, entryID, userID, now)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `UPDATE list_entries SET votes = votes + 1 WHERE id = ?`, entryID)
	return err
}

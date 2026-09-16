package store

import (
	"context"
	"time"
)

type List struct {
	ID          int64   `json:"id"`
	ProjectID   int64   `json:"project_id"`
	Slug      string  `json:"slug"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	CreatedAt   float64 `json:"created_at"`
	UpdatedAt   float64 `json:"updated_at"`
}

type ListEntry struct {
	ID        int64   `json:"id"`
	ListID    int64   `json:"list_id"`
	URL       string  `json:"url"`
	Title     string  `json:"title"`
	Description string  `json:"description"`
	Category  *string  `json:"category"`
	Status    string  `json:"status"`
	ER        float64 `json:"elo_r"`
	RD        float64 `json:"elo_rd"`
	Vol       float64 `json:"elo_vol"`
	ProposedBy int64  `json:"proposed_by"`
	CreatedAt float64 `json:"created_at"`
	UpdatedAt float64 `json:"updated_at"`
}

func (d *DB) CreateList(ctx context.Context, projectID int64, slug, title, description string) (List, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO lists (project_id, slug, title, description, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'open', 1, ?, ?)`, projectID, slug, title, description, now, now)
	if err != nil {
		return List{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetList(ctx, id)
}

func (d *DB) GetList(ctx context.Context, id int64) (List, error) {
	var l List
	err := d.QueryRowContext(ctx, `SELECT id, project_id, slug, title, description, status, created_at, updated_at FROM lists WHERE id=?`, id).Scan(
		&l.ID, &l.ProjectID, &l.Slug, &l.Name, &l.Description, &l.Status, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return List{}, err
	}
	return l, nil
}

func (d *DB) GetListsByProject(ctx context.Context, projectID int64) ([]List, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, project_id, slug, title, description, status, created_at, updated_at FROM lists WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lists []List
	for rows.Next() {
		var l List
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.Slug, &l.Name, &l.Description, &l.Status, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		lists = append(lists, l)
	}
	return lists, rows.Err()
}

func (d *DB) CreateListEntry(ctx context.Context, listID, projectID int64, title, body string) (ListEntry, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO list_entries (list_id, url, title, description, status, proposed_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'proposed', 1, ?, ?)`, listID, projectID, title, body, now, now)
	if err != nil {
		return ListEntry{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetListEntry(ctx, id)
}

func (d *DB) GetListEntry(ctx context.Context, id int64) (ListEntry, error) {
	var le ListEntry
	err := d.QueryRowContext(ctx, `SELECT id, list_id, url, title, description, category, status, elo_r, elo_rd, elo_vol, proposed_by, created_at, updated_at FROM list_entries WHERE id=?`, id).Scan(
		&le.ID, &le.ListID, &le.URL, &le.Title, &le.Description, &le.Category, &le.Status, &le.ER, &le.RD, &le.Vol, &le.ProposedBy, &le.CreatedAt, &le.UpdatedAt)
	if err != nil {
		return ListEntry{}, err
	}
	return le, nil
}

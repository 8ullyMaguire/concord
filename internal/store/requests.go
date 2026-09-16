package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Request struct {
	ID        int64   `json:"id"`
	ProjectID int64   `json:"project_id"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Status    string  `json:"status"`
	CreatedAt float64 `json:"created_at"`
}

type RequestAnswer struct {
	ID        int64   `json:"id"`
	RequestID int64   `json:"request_id"`
	AuthorID  int64   `json:"author_id"`
	Body      string  `json:"body"`
	Score     float64 `json:"score"`
	Status    string  `json:"status"`
	CreatedAt float64 `json:"created_at"`
}

type RequestAnswerVote struct {
	ID        int64   `json:"id"`
	AnswerID  int64   `json:"answer_id"`
	UserID    int64   `json:"user_id"`
	Direction int     `json:"direction"` // +1 or -1
	CreatedAt float64 `json:"created_at"`
}

func (d *DB) CreateRequest(ctx context.Context, projectID int64, title, body string) (Request, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO requests (project_id, title, body, status, created_at)
		VALUES (?, ?, ?, 'open', ?)`, projectID, title, body, now)
	if err != nil {
		return Request{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetRequest(ctx, id)
}

func (d *DB) GetRequest(ctx context.Context, id int64) (Request, error) {
	var r Request
	err := d.QueryRowContext(ctx, `SELECT id, project_id, title, body, status, created_at
		FROM requests WHERE id=?`, id).Scan(
		&r.ID, &r.ProjectID, &r.Title, &r.Body, &r.Status, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, fmt.Errorf("%w: request %d", ErrNotFound, id)
	}
	return r, err
}

func (d *DB) AnswerRequest(ctx context.Context, requestID, authorID int64, body string) (RequestAnswer, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO request_answers (request_id, author_id, body, score, status, created_at)
		VALUES (?, ?, ?, 0, 'open', ?)`, requestID, authorID, body, now)
	if err != nil {
		return RequestAnswer{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetRequestAnswer(ctx, id)
}

func (d *DB) GetRequestAnswer(ctx context.Context, id int64) (RequestAnswer, error) {
	var a RequestAnswer
	err := d.QueryRowContext(ctx, `SELECT id, request_id, author_id, body, score, status, created_at
		FROM request_answers WHERE id=?`, id).Scan(
		&a.ID, &a.RequestID, &a.AuthorID, &a.Body, &a.Score, &a.Status, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestAnswer{}, fmt.Errorf("%w: answer %d", ErrNotFound, id)
	}
	return a, err
}

func (d *DB) VoteAnswer(ctx context.Context, answerID, userID int64, direction int) error {
	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `INSERT OR IGNORE INTO request_answer_votes (answer_id, user_id, direction, created_at) VALUES (?, ?, ?, ?)`, answerID, userID, direction, now)
	if err != nil {
		return err
	}
	_, err = d.ExecContext(ctx, `UPDATE request_answers SET score = score + ? WHERE id = ?`, direction, answerID)
	return err
}

func (d *DB) GetRequestAnswers(ctx context.Context, requestID int64) ([]RequestAnswer, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, request_id, author_id, body, score, status, created_at
		FROM request_answers WHERE request_id=? ORDER BY score DESC`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var answers []RequestAnswer
	for rows.Next() {
		var a RequestAnswer
		if err := rows.Scan(&a.ID, &a.RequestID, &a.AuthorID, &a.Body, &a.Score, &a.Status, &a.CreatedAt); err != nil {
			return nil, err
		}
		answers = append(answers, a)
	}
	return answers, rows.Err()
}

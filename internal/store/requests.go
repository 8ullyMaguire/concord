package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"git.polarisocial.xyz/concord/concord/internal/governance"
	"git.polarisocial.xyz/concord/concord/internal/ranking"
)

type Request struct {
	ID int64 `json:"id"`
	// ProjectID is 0 for an INSTANCE-WIDE request. The column is nullable and the
	// migration says why ("NULL = instance-wide board"), so a plain int64 that
	// scanned it directly would fail on exactly the rows that make the feature work:
	//
	//	Scan error on column index 1, name "project_id": converting NULL to int64
	//
	// 0 is the right sentinel rather than a pointer: no valid project has id 0, and
	// every caller wants a number it can compare, not a nil check. The JSON omits it
	// when 0, so a client can tell "instance-wide" from "project 0" without the field
	// ever appearing.
	ProjectID int64   `json:"project_id,omitempty"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Status    string  `json:"status"`
	CreatedAt float64 `json:"created_at"`
}

// RequestAnswer is one answer to a request.
//
// It carries a GLICKO rating (elo_r/elo_rd/elo_vol) and NOT a vote score. The
// schema says so, and the queries said otherwise: every method here selected
// `author_id` and `score`, neither of which exists on request_answers. The column
// is proposed_by, and the ranking is a rating -- which is the same shape as
// features and solutions, deliberately.
//
// Every read and write of this table 500'd until 2026-10-05:
//
//	no such column: author_id (1)
//
// so the whole answers feature -- POST /answers, the vote endpoint, the requests
// page -- was dead on arrival. The struct kept the wrong names too, so even a
// corrected query would have scanned into fields that do not correspond to columns.
type RequestAnswer struct {
	ID         int64   `json:"id"`
	RequestID  int64   `json:"request_id"`
	ProjectID  int64   `json:"project_id"`
	Body       string  `json:"body"`
	Status     string  `json:"status"`
	EloR       float64 `json:"elo_r"`
	EloRD      float64 `json:"elo_rd"`
	EloVol     float64 `json:"elo_vol"`
	ProposedBy int64   `json:"proposed_by"`
	CreatedAt  float64 `json:"created_at"`
	UpdatedAt  float64 `json:"updated_at"`
}

type RequestAnswerVote struct {
	ID        int64   `json:"id"`
	AnswerID  int64   `json:"answer_id"`
	UserID    int64   `json:"user_id"`
	Direction int     `json:"direction"` // +1 or -1
	CreatedAt float64 `json:"created_at"`
}

func (d *DB) CreateRequest(ctx context.Context, projectID int64, authorID int64, title, body string) (Request, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO requests (project_id, author_id, title, body, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'open', ?, ?)`, projectID, authorID, title, body, now, now)
	if err != nil {
		return Request{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetRequest(ctx, id)
}

// GetRequestsByProject returns all requests for a project.
func (d *DB) GetRequestsByProject(ctx context.Context, projectID int64) ([]Request, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, title, body, status, created_at
		FROM requests WHERE project_id=? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := []Request{}
	for rows.Next() {
		var r Request
		var projectID sql.NullInt64
		if err := rows.Scan(&r.ID, &projectID, &r.Title, &r.Body, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.ProjectID = projectID.Int64
		requests = append(requests, r)
	}
	return requests, rows.Err()
}

// CreateInstanceRequest opens a question on the INSTANCE-WIDE board.
//
//	requests.project_id INTEGER REFERENCES projects(id)  -- NULL = instance-wide board
//
// The nullable column and that comment are the whole design: a request is either
// one project's question or the instance's, and only the second kind can be
// answered by several projects -- which is what the pairwise answer vote compares.
//
// Nothing reached this state. CreateRequest requires a project id and every caller
// passes one, so the feature the schema was built for was unreachable through the
// API: you could not ask the instance a question, and therefore could not get two
// projects' answers onto the same request to compare.
//
// projectID is written as a genuine SQL NULL rather than 0, because the column
// REFERENCES projects(id) and 0 is not a project. Writing 0 fails the foreign key.
func (d *DB) CreateInstanceRequest(ctx context.Context, authorID int64, title, body string) (Request, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO requests (project_id, author_id, title, body, status, created_at, updated_at)
		VALUES (NULL, ?, ?, ?, 'open', ?, ?)`, authorID, title, body, now, now)
	if err != nil {
		return Request{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetRequest(ctx, id)
}

func (d *DB) GetRequest(ctx context.Context, id int64) (Request, error) {
	var r Request
	var projectID sql.NullInt64
	err := d.QueryRowContext(ctx, `SELECT id, project_id, title, body, status, created_at
		FROM requests WHERE id=?`, id).Scan(
		&r.ID, &projectID, &r.Title, &r.Body, &r.Status, &r.CreatedAt)
	r.ProjectID = projectID.Int64
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, fmt.Errorf("%w: request %d", ErrNotFound, id)
	}
	return r, err
}

// AnswerRequest adds an answer to a request.
//
// The table carries UNIQUE (request_id, project_id) and project_id is NOT NULL, so
// both are supplied here. The previous INSERT omitted project_id and wrote
// author_id, so it could not have succeeded: the first half failed the constraint
// and the second named a column that does not exist.
//
// One answer per request per project is the schema's decision, not an accident --
// a project states one position on a question and then compares it against others.
func (d *DB) AnswerRequest(ctx context.Context, requestID, projectID, authorID int64, body string) (RequestAnswer, error) {
	now := float64(time.Now().Unix())
	res, err := d.ExecContext(ctx, `
		INSERT INTO request_answers (request_id, project_id, body, status, proposed_by, created_at, updated_at)
		VALUES (?, ?, ?, 'active', ?, ?, ?)`, requestID, projectID, body, authorID, now, now)
	if err != nil {
		return RequestAnswer{}, err
	}
	id, _ := res.LastInsertId()
	return d.GetRequestAnswer(ctx, id)
}

func (d *DB) GetRequestAnswer(ctx context.Context, id int64) (RequestAnswer, error) {
	var a RequestAnswer
	err := d.QueryRowContext(ctx, `SELECT id, request_id, project_id, body, status,
		elo_r, elo_rd, elo_vol, proposed_by, created_at, updated_at
		FROM request_answers WHERE id=?`, id).Scan(
		&a.ID, &a.RequestID, &a.ProjectID, &a.Body, &a.Status,
		&a.EloR, &a.EloRD, &a.EloVol, &a.ProposedBy, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestAnswer{}, fmt.Errorf("%w: answer %d", ErrNotFound, id)
	}
	return a, err
}

// VoteAnswer records a PAIRWISE comparison between two answers to the same request,
// and advances both ratings.
//
// The schema settles the design and the previous code disagreed with all of it:
//
//	request_answer_votes: request_id, answer_a, answer_b, voter_id, outcome, weight
//
// A vote names TWO answers and an outcome ('a', 'b', 'both', 'neither', 'skip') --
// it is "which of these two fits the request better", the same comparison features
// and solutions use. The old VoteAnswer wrote `answer_id`, `user_id` and a
// `direction` column, none of which exist, so every vote on an answer errored with
// "no such column named answer_id".
//
// The rating update reuses ranking.ApplyPairwiseVote, the same Glicko-2 call
// RecordVote uses for features, rather than summing vote directions into a score.
// There is no score column, and a bare `elo_r += 8` would have been the wrong
// arithmetic even if there were: Glicko weights a result by each side's deviation,
// so a confident favourite moves less than an unknown when both win.
//
// §5.1's rule applies here too, and it was not applied before: you cannot vote on
// an answer you proposed.
func (d *DB) VoteAnswer(ctx context.Context, requestID, answerA, answerB, voterID int64, outcome string, weight float64, charter governance.Charter) error {
	switch outcome {
	case "a", "b", "both", "neither", "skip":
	default:
		return fmt.Errorf("%w: outcome must be a, b, both, neither or skip", ErrInvalid)
	}
	if answerA == answerB {
		return fmt.Errorf("%w: cannot compare an answer with itself", ErrInvalid)
	}

	// Both answers must belong to this request, or a vote could move a rating on a
	// different question entirely -- which is the same class of mistake as the
	// ownership checks elsewhere in this package.
	var authorA, authorB int64
	for _, pair := range []struct {
		id     int64
		author *int64
	}{{answerA, &authorA}, {answerB, &authorB}} {
		var reqID int64
		if err := d.QueryRowContext(ctx,
			`SELECT proposed_by, request_id FROM request_answers WHERE id=?`, pair.id).
			Scan(pair.author, &reqID); err != nil {
			return fmt.Errorf("fetch answer %d: %w", pair.id, err)
		}
		if reqID != requestID {
			return fmt.Errorf("%w: answer %d belongs to request %d, not %d",
				ErrInvalid, pair.id, reqID, requestID)
		}
	}
	if voterID == authorA || voterID == authorB {
		return fmt.Errorf("%w: you cannot vote on an answer you proposed", ErrPerm)
	}

	// Current ratings, in the shape ApplyPairwiseVote expects.
	var fa, fb ranking.Feature
	load := func(id int64, out *ranking.Feature) error {
		return d.QueryRowContext(ctx,
			`SELECT elo_r, elo_rd, elo_vol FROM request_answers WHERE id=?`, id).
			Scan(&out.R, &out.RD, &out.Vol)
	}
	if err := load(answerA, &fa); err != nil {
		return fmt.Errorf("fetch answer A rating: %w", err)
	}
	if err := load(answerB, &fb); err != nil {
		return fmt.Errorf("fetch answer B rating: %w", err)
	}

	out := ranking.Outcome(outcome)
	a2, b2 := ranking.ApplyPairwiseVote(fa, fb, out, weight, charter.GlickoTau)

	now := float64(time.Now().Unix())
	_, err := d.ExecContext(ctx, `
		INSERT INTO request_answer_votes
			(request_id, answer_a, answer_b, voter_id, outcome, weight, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		requestID, answerA, answerB, voterID, outcome, weight, now)
	if err != nil {
		return err
	}

	if _, err := d.ExecContext(ctx,
		`UPDATE request_answers SET elo_r=?, elo_rd=?, elo_vol=?, updated_at=? WHERE id=?`,
		a2.R, a2.RD, a2.Vol, now, answerA); err != nil {
		return fmt.Errorf("update answer A rating: %w", err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE request_answers SET elo_r=?, elo_rd=?, elo_vol=?, updated_at=? WHERE id=?`,
		b2.R, b2.RD, b2.Vol, now, answerB); err != nil {
		return fmt.Errorf("update answer B rating: %w", err)
	}
	return nil
}

// GetRequestAnswers returns every answer to a request, best rating first.
//
// Ordered by elo_r DESC, not by insertion order: an answer is a position to be
// compared, and the page's job is to put the strongest first. elo_rd travels with
// it -- an answer that has been compared once (1500 +/- 350) is not the same claim
// as one compared ten times, and a page showing only elo_r makes them look alike.
//
// Empty, never nil: nil marshals to null and the client's Array.isArray check then
// reports "no answers" for a request that has some.
func (d *DB) GetRequestAnswers(ctx context.Context, requestID int64) ([]RequestAnswer, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, request_id, project_id, body, status,
		elo_r, elo_rd, elo_vol, proposed_by, created_at, updated_at
		FROM request_answers WHERE request_id=? ORDER BY elo_r DESC, id`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	answers := []RequestAnswer{}
	for rows.Next() {
		var a RequestAnswer
		if err := rows.Scan(&a.ID, &a.RequestID, &a.ProjectID, &a.Body, &a.Status,
			&a.EloR, &a.EloRD, &a.EloVol, &a.ProposedBy, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		answers = append(answers, a)
	}
	return answers, rows.Err()
}

// GetInstanceRequests returns the requests on the instance-wide board.
//
//	WHERE project_id IS NULL
//
// Which is the whole definition of that board, and getting it wrong in either
// direction is bad: omitting the clause lists EVERY project-scoped request on a
// board meant for cross-project questions, and inverting it lists nothing.
//
// Empty, never nil -- nil marshals to null.
func (d *DB) GetInstanceRequests(ctx context.Context) ([]Request, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, project_id, title, body, status, created_at
		FROM requests WHERE project_id IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := []Request{}
	for rows.Next() {
		var r Request
		var projectID sql.NullInt64
		if err := rows.Scan(&r.ID, &projectID, &r.Title, &r.Body, &r.Status,
			&r.CreatedAt); err != nil {
			return nil, err
		}
		r.ProjectID = projectID.Int64
		requests = append(requests, r)
	}
	return requests, rows.Err()
}

package store

import (
	"context"
)

type BoardColumn struct {
	Name     string `json:"name"`
	WIP      *int   `json:"wip"`
	Position int    `json:"position"`
}

type BoardCard struct {
	ID        int64   `json:"id"`
	ProjectID int64   `json:"project_id"`
	FeatureID int64   `json:"feature_id"`
	Column    string  `json:"column"`
	EnteredAt float64 `json:"entered_at"`
	ColumnID  int64   `json:"column_id"`
	Title     string  `json:"title,omitempty"`

	// Derived marks a card that has no row in board_cards and was synthesised
	// from a feature's own workflow status at read time. It exists so the board
	// shows the work a project actually has. Such a card has no row: ID and
	// ColumnID are zero and it cannot be moved. Moving it is what creates the
	// row, at which point the card stops being derived. See GetBoard.
	Derived   bool `json:"derived,omitempty"`
}

// GetBoard returns the columns plus the cards for a project.
//
// Cards come from two places. A row in board_cards is an explicit placement --
// somebody put that feature in that phase -- and always wins. Anything the
// project has but nobody has placed is derived from the feature's own status,
// placed in the phase that status names.
//
// This is why the board shows anything at all. board_cards was empty for all 70
// projects and no code path had ever inserted a row: MoveCard and GetBoardCard
// both operated on rows that could not exist, and every board rendered nine
// columns of "0". A user's first reaction to an all-zero kanban cannot
// distinguish "no work" from "broken", and here it was the second.
//
// Deriving rather than backfilling is a deliberate choice. A migration would put
// ~889 rows into a table nobody had ever written to, and the placements it wrote
// would be guesses dressed as data -- nobody had decided that draft belongs in
// "inbox". Deriving keeps the table meaning "explicit placement" and keeps the
// guess visible as a guess: a derived card carries Derived=true and no row, so
// the first time somebody moves it, that movement is recorded as the decision it
// actually is.
//
// A feature whose status matches no phase is omitted rather than forced into a
// column. Guessing a phase would be the same fabrication one level down.
func (d *DB) GetBoard(ctx context.Context, projectID int64) ([]BoardColumn, []BoardCard, error) {
	columns, err := d.GetBoardColumns(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	cards, err := d.GetBoardCards(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	placed := make(map[int64]bool, len(cards))
	for _, c := range cards {
		if c.FeatureID != 0 {
			placed[c.FeatureID] = true
		}
	}
	derived, err := d.deriveBoardCards(ctx, projectID, columns, placed)
	if err != nil {
		return nil, nil, err
	}
	return columns, append(cards, derived...), nil
}

// featureStatusToPhase maps a feature's workflow status onto the board phase it
// belongs in.
//
// The two vocabularies are not the same and an identity match gets it badly
// wrong. The live database has 7 feature statuses and 9 board phases, and only
// four names appear in both -- consensus, ready, in_progress, rejected. Matching
// by name would place 60% of all features nowhere, which is how this looked like
// a broken board when it was a vocabulary mismatch.
//
// The mapping is spelled out rather than derived, because each pair is a decision
// about what the board means: a draft feature is work that has not been triaged,
// so it belongs in inbox, not in a column called "draft" that does not exist.
//
// A status absent from this table is omitted. Adding one is a product decision
// about where that state belongs, and guessing it is the same fabrication as
// guessing any other placement.
var featureStatusToPhase = map[string]string{
	"draft":      "inbox",
	"discussion": "triaged",
	"consensus":  "consensus",
	"ready":      "ready",
	"in_progress": "in_progress",
	"review":     "review",
	"shipped":    "done",
	"rejected":   "rejected",
}

// deriveBoardCards synthesises cards for unplaced features.
func (d *DB) deriveBoardCards(ctx context.Context, projectID int64, columns []BoardColumn, placed map[int64]bool) ([]BoardCard, error) {
	known := make(map[string]bool, len(columns))
	for _, c := range columns {
		known[c.Name] = true
	}
	if len(known) == 0 {
		return nil, nil
	}

	rows, err := d.QueryContext(ctx, `
		SELECT id, status, COALESCE(title, '') FROM features
		WHERE project_id=? AND status IS NOT NULL AND status <> ''
		ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BoardCard
	for rows.Next() {
		var id int64
		var status, title string
		if err := rows.Scan(&id, &status, &title); err != nil {
			return nil, err
		}
		phase, ok := featureStatusToPhase[status]
		if placed[id] || !ok || !known[phase] {
			continue
		}
		out = append(out, BoardCard{
			ProjectID: projectID,
			FeatureID: id,
			Column:    phase,
			Title:     title,
			Derived:   true,
		})
	}
	return out, rows.Err()
}

func (d *DB) GetBoardColumns(ctx context.Context, projectID int64) ([]BoardColumn, error) {
	rows, err := d.QueryContext(ctx, `SELECT bc.phase, bc.wip_limit, bc.position FROM board_columns bc WHERE bc.project_id=? ORDER BY bc.position`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []BoardColumn
	for rows.Next() {
		var col BoardColumn
		if err := rows.Scan(&col.Name, &col.WIP, &col.Position); err != nil {
			return nil, err
		}
		columns = append(columns, col)
	}
	return columns, rows.Err()
}

func (d *DB) GetBoardCards(ctx context.Context, projectID int64) ([]BoardCard, error) {
	rows, err := d.QueryContext(ctx, `SELECT bc.id, bc.project_id, bc.feature_id, bc.entered_at, bc.column_id, b.phase FROM board_cards bc LEFT JOIN board_columns b ON bc.column_id=b.id WHERE bc.project_id=? ORDER BY bc.entered_at ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []BoardCard
	for rows.Next() {
		var c BoardCard
		var phase string
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.FeatureID, &c.EnteredAt, &c.ColumnID, &phase); err != nil {
			return nil, err
		}
		c.Column = phase
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

func (d *DB) MoveCard(ctx context.Context, cardID int64, newColumn string, projectID int64) error {
	_, err := d.ExecContext(ctx, `
		UPDATE board_cards SET column_id=(SELECT id FROM board_columns WHERE project_id=? AND phase=?) WHERE id=?`,
		projectID, newColumn, cardID)
	return err
}

func (d *DB) GetBoardCard(ctx context.Context, cardID int64) (BoardCard, error) {
	var c BoardCard
	err := d.QueryRowContext(ctx, `
		SELECT bc.id, bc.project_id, bc.feature_id, b.phase, bc.entered_at
		FROM board_cards bc LEFT JOIN board_columns b ON bc.column_id=b.id
		WHERE bc.id=?`, cardID).Scan(
		&c.ID, &c.ProjectID, &c.FeatureID, &c.Column, &c.EnteredAt)
	if err != nil {
		return BoardCard{}, err
	}
	return c, nil
}

package store

import (
	"context"
)

type BoardColumn struct {
	Name     string `json:"name"`
	WIP      *int  `json:"wip"`
	Position int    `json:"position"`
}

type BoardCard struct {
	ID        int64   `json:"id"`
	ProjectID int64   `json:"project_id"`
	FeatureID int64   `json:"feature_id"`
	Column    string  `json:"column"`
	EnteredAt float64 `json:"entered_at"`
	ColumnID  int64   `json:"column_id"`
}

func (d *DB) GetBoard(ctx context.Context, projectID int64) ([]BoardColumn, []BoardCard, error) {
	columns, err := d.GetBoardColumns(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	cards, err := d.GetBoardCards(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	return columns, cards, nil
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

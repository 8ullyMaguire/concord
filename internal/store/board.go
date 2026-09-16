package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type BoardColumn struct {
	Name string `json:"name"`
	WIP  int    `json:"wip"`
}

type BoardCard struct {
	ID         int64  `json:"id"`
	ProjectID  int64  `json:"project_id"`
	FeatureID  int64  `json:"feature_id"`
	Column     string `json:"column"`
	Position   int    `json:"position"`
	AssignedTo *int64 `json:"assigned_to,omitempty"`
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
	rows, err := d.QueryContext(ctx, `SELECT bc.column_name, bc.wip_limit FROM board_columns bc WHERE bc.project_id=? ORDER BY bc.position`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []BoardColumn
	for rows.Next() {
		var col BoardColumn
		if err := rows.Scan(&col.Name, &col.WIP); err != nil {
			return nil, err
		}
		columns = append(columns, col)
	}
	return columns, rows.Err()
}

func (d *DB) GetBoardCards(ctx context.Context, projectID int64) ([]BoardCard, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, project_id, feature_id, column_name, position, assigned_to FROM board_cards WHERE project_id=? ORDER BY position ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []BoardCard
	for rows.Next() {
		var c BoardCard
		var assigned sql.NullInt64
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.FeatureID, &c.Column, &c.Position, &assigned); err != nil {
			return nil, err
		}
		if assigned.Valid {
			c.AssignedTo = &assigned.Int64
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

func (d *DB) MoveCard(ctx context.Context, cardID int64, newColumn string, projectID int64) error {
	_, err := d.ExecContext(ctx, `UPDATE board_cards SET column_name=?, position=(SELECT COALESCE(MAX(position),0)+1 FROM board_cards WHERE project_id=? AND column_name=?) WHERE id=?`, newColumn, projectID, newColumn, cardID)
	if err != nil {
		return err
	}
	status := boardColumnToStatus(newColumn)
	if status != "" {
		rows, _ := d.QueryContext(ctx, `SELECT feature_id FROM board_cards WHERE id=? AND project_id=?`, cardID, projectID)
		var featureID int64
		if rows.Next() {
			rows.Scan(&featureID)
		}
		rows.Close()
		if featureID != 0 {
			_ = d.UpdateFeatureStatus(ctx, featureID, status)
		}
	}
	return d.AddAudit(ctx, projectID, 0, "board_move", "card", cardID, fmt.Sprintf("moved to %s", newColumn))
}

func (d *DB) GetBoardCard(ctx context.Context, cardID int64) (BoardCard, error) {
	var c BoardCard
	err := d.QueryRowContext(ctx, `SELECT id, project_id, feature_id, column_name, position, assigned_to FROM board_cards WHERE id=?`, cardID).Scan(
		&c.ID, &c.ProjectID, &c.FeatureID, &c.Column, &c.Position, &c.AssignedTo)
	if errors.Is(err, sql.ErrNoRows) {
		return BoardCard{}, fmt.Errorf("%w: card %d", ErrNotFound, cardID)
	}
	return c, err
}

func boardColumnToStatus(column string) string {
	switch column {
	case "solution_draft":
		return "draft"
	case "consensus":
		return "consensus"
	case "ready":
		return "ready"
	case "in_progress":
		return "in_progress"
	case "review":
		return "review"
	case "done":
		return "shipped"
	case "rejected":
		return "rejected"
	}
	return ""
}

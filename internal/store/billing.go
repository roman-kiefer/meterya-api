package store

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/domain"
)

func replaceAnnualProjections(ctx context.Context, tx pgx.Tx, p domain.Point) error {
	summaries, err := p.AnnualSummaries()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM annual_summaries WHERE point_id=$1", p.ID); err != nil {
		return err
	}
	for _, summary := range summaries {
		state, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO annual_summaries(point_id,year,point_version,calculation_version,state) VALUES($1,$2,$3,$4,$5)", p.ID, summary.Year, p.Version, domain.CalculationVersion, state); err != nil {
			return err
		}
	}
	return nil
}

// Backfill existing readings after migration or a change to calculation rules.
// Holding the point locks makes this safe alongside other API instances.
func (s *Store) warmAnnualProjections(ctx context.Context) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "LOCK TABLE points,annual_summaries IN EXCLUSIVE MODE"); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT state FROM points p WHERE NOT EXISTS(SELECT 1 FROM annual_summaries a WHERE a.point_id=p.id) OR EXISTS(SELECT 1 FROM annual_summaries a WHERE a.point_id=p.id AND (a.point_version<>p.version OR a.calculation_version<>$1))", domain.CalculationVersion)
	if err != nil {
		return err
	}
	points := []domain.Point{}
	for rows.Next() {
		var state []byte
		var p domain.Point
		if err = rows.Scan(&state); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(state, &p); err != nil {
			rows.Close()
			return err
		}
		points = append(points, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range points {
		if err = replaceAnnualProjections(ctx, tx, p); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) AnnualSummaries(ctx context.Context, workspace, point string) ([]domain.AnnualSummary, error) {
	rows, err := s.Pool.Query(ctx, "SELECT a.state FROM annual_summaries a JOIN points p ON p.id=a.point_id WHERE p.workspace_id=$1 AND p.id=$2 ORDER BY a.year DESC", workspace, point)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.AnnualSummary{}
	for rows.Next() {
		var state []byte
		var summary domain.AnnualSummary
		if err = rows.Scan(&state); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(state, &summary); err != nil {
			return nil, err
		}
		result = append(result, summary)
	}
	return result, rows.Err()
}

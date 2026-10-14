package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/domain"
	"sort"
)

type transitionQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func transitions(ctx context.Context, q transitionQuery, ws string) ([]domain.MeterTransition, error) {
	rows, err := q.Query(ctx, "SELECT payload FROM events WHERE workspace_id=$1 AND event_type='MeterTransitionRecorded' ORDER BY sequence", ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.MeterTransition{}
	for rows.Next() {
		var b []byte
		var v domain.MeterTransition
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func (s *Store) MeterTransitions(ctx context.Context, ws string) ([]domain.MeterTransition, error) {
	return transitions(ctx, s.Pool, ws)
}

func readingWithinMeterPeriod(ctx context.Context, tx pgx.Tx, ws, point, date string) error {
	records, err := transitions(ctx, tx, ws)
	if err != nil {
		return err
	}
	for _, v := range records {
		if v.OldPoint == point && date > v.Date {
			return domain.Fail("READING_OUTSIDE_METER_PERIOD")
		}
		for _, id := range v.NewPoints {
			if id == point && date < v.Date {
				return domain.Fail("READING_OUTSIDE_METER_PERIOD")
			}
		}
	}
	return nil
}

func (s *Store) RecordMeterTransition(ctx context.Context, c Command, v domain.MeterTransition, expected map[string]int64) (domain.MeterTransition, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return v, err
	}
	defer tx.Rollback(ctx)
	var version int64
	if err = tx.QueryRow(ctx, "SELECT version FROM workspaces WHERE id=$1 FOR UPDATE", c.Workspace).Scan(&version); err != nil {
		return v, mapError(err)
	}
	var role string
	if err = tx.QueryRow(ctx, roleQuery+" FOR SHARE", c.Workspace, c.Subject).Scan(&role); err != nil || !CanManage(role) {
		return v, domain.Fail("PERMISSION_DENIED")
	}
	var saved domain.MeterTransition
	if found, e := receipt(ctx, tx, c, &saved); found || e != nil {
		return saved, e
	}
	if version != c.Expected {
		return v, domain.Fail("AGGREGATE_VERSION_CONFLICT")
	}
	if len(v.NewPoints) < 1 || len(v.NewPoints) > 2 {
		return v, domain.Fail("VALIDATION_FAILED")
	}
	if err = validateVirtualContinuation(ctx, tx, c.Workspace, v); err != nil {
		return v, err
	}
	ids := append([]string{v.OldPoint}, v.NewPoints...)
	sort.Strings(ids)
	if len(expected) != len(ids) {
		return v, domain.Fail("VALIDATION_FAILED")
	}
	points := map[string]domain.Point{}
	for _, id := range ids {
		if _, err = uuid.Parse(id); err != nil {
			return v, domain.Fail("VALIDATION_FAILED")
		}
		var b []byte
		if err = tx.QueryRow(ctx, "SELECT state FROM points WHERE workspace_id=$1 AND id=$2 FOR UPDATE", c.Workspace, id).Scan(&b); err != nil {
			return v, mapError(err)
		}
		var p domain.Point
		if err = json.Unmarshal(b, &p); err != nil {
			return v, err
		}
		if p.Version != expected[id] {
			return v, domain.Fail("AGGREGATE_VERSION_CONFLICT")
		}
		points[id] = p
	}
	if err = v.Validate(points, c.Today); err != nil {
		return v, err
	}
	records, err := transitions(ctx, tx, c.Workspace)
	if err != nil {
		return v, err
	}
	for _, old := range records {
		if old.OldPoint == v.OldPoint {
			return v, domain.Fail("TRANSITION_ALREADY_LINKED")
		}
		for _, id := range old.NewPoints {
			if id == v.OldPoint && v.Date <= old.Date {
				return v, domain.Fail("READING_OUTSIDE_METER_PERIOD")
			}
		}
		for _, id := range v.NewPoints {
			if old.OldPoint == id {
				return v, domain.Fail("TRANSITION_ALREADY_LINKED")
			}
			for _, linked := range old.NewPoints {
				if linked == id {
					return v, domain.Fail("TRANSITION_ALREADY_LINKED")
				}
			}
		}
	}
	old := points[v.OldPoint]
	if !old.Archived {
		old.Archived = true
		old.Version++
		if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$4,'PointArchived',$5,'{}')", uuid.NewString(), old.ID, old.Version, c.Workspace, c.Subject); err != nil {
			return v, err
		}
		b, _ := json.Marshal(old)
		if _, err = tx.Exec(ctx, "UPDATE points SET state=$1,version=$2,archived=true WHERE id=$3", b, old.Version, old.ID); err != nil {
			return v, err
		}
		if err = replaceAnnualProjections(ctx, tx, old); err != nil {
			return v, err
		}
	}
	v.ID = uuid.NewString()
	b, _ := json.Marshal(v)
	if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$2,'MeterTransitionRecorded',$4,$5)", v.ID, c.Workspace, version+1, c.Subject, b); err != nil {
		return v, err
	}
	if _, err = tx.Exec(ctx, "UPDATE workspaces SET version=version+1 WHERE id=$1", c.Workspace); err != nil {
		return v, err
	}
	if err = saveReceipt(ctx, tx, c, v); err != nil {
		return v, err
	}
	return v, tx.Commit(ctx)
}

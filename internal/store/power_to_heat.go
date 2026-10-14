package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"time"
)

func heatConfigurations(ctx context.Context, q transitionQuery, ws string) ([]domain.PowerToHeat, error) {
	rows, err := q.Query(ctx, "SELECT payload FROM events WHERE workspace_id=$1 AND event_type IN ('PowerToHeatConfigured','PowerToHeatTariffsSaved','PowerToHeatSourceChoicesSaved') ORDER BY sequence", ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.PowerToHeat{}
	for rows.Next() {
		var b []byte
		var c domain.PowerToHeat
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		index := -1
		for i := range result {
			if result[i].ID == c.ID {
				index = i
				break
			}
		}
		if index < 0 {
			result = append(result, c)
		} else {
			result[index] = c
		}
	}
	return result, rows.Err()
}
func (s *Store) ConfigureHeat(ctx context.Context, c Command, v domain.PowerToHeat, expected map[string]int64) (domain.PowerToHeat, error) {
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
	var saved domain.PowerToHeat
	if found, e := receipt(ctx, tx, c, &saved); found || e != nil {
		return saved, e
	}
	if version != c.Expected {
		return v, domain.Fail("AGGREGATE_VERSION_CONFLICT")
	}
	d, e := time.Parse(time.DateOnly, v.From)
	if !domain.ValidName(v.Name) || e != nil || d.Year() < 1900 || d.Year() > 9998 || v.From > c.Today || v.NetPoint == v.HousePoint || len(expected) != 2 {
		return v, domain.Fail("VALIDATION_FAILED")
	}
	records, err := heatConfigurations(ctx, tx, c.Workspace)
	if err != nil {
		return v, err
	}
	for _, old := range records {
		if old.Name == v.Name || old.NetPoint == v.NetPoint || old.HousePoint == v.NetPoint || old.NetPoint == v.HousePoint || old.HousePoint == v.HousePoint {
			return v, domain.Fail("CONFLICT")
		}
	}
	points := map[string]domain.Point{}
	ids := []string{v.NetPoint, v.HousePoint}
	if ids[0] > ids[1] {
		ids[0], ids[1] = ids[1], ids[0]
	}
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
		if p.Archived {
			return v, domain.Fail("POINT_ARCHIVED")
		}
		if err = readingWithinMeterPeriod(ctx, tx, c.Workspace, p.ID, v.From); err != nil {
			return v, err
		}
		points[id] = p
	}
	if _, err = v.Report(points[v.NetPoint], points[v.HousePoint]); err != nil {
		return v, err
	}
	v.ID = uuid.NewString()
	v.Version = 1
	body, _ := json.Marshal(v)
	if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$2,'PowerToHeatConfigured',$4,$5)", v.ID, c.Workspace, version+1, c.Subject, body); err != nil {
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
func (s *Store) HeatReports(ctx context.Context, ws string) ([]domain.HeatReport, error) {
	snap, err := s.Snapshot(ctx, ws)
	if err != nil {
		return nil, err
	}
	result := []domain.HeatReport{}
	for _, config := range snap.Heat {
		report, _, _, err := domain.ResolveHeat(config, config.Choices, snap.PointMap(), snap.Transitions)
		if err != nil {
			switch domain.Code(err) {
			case "VIRTUAL_AMBIGUOUS", "VIRTUAL_INCOMPATIBLE", "SOURCE_MISSING":
				report = domain.HeatReport{PowerToHeat: config, OverviewVersion: domain.OverviewVersion, Flags: []string{domain.Code(err)}, GridPeriods: []domain.HeatPeriod{}, TotalPeriods: []domain.HeatPeriod{}, Annual: []domain.AnnualSummary{}, Yearly: []domain.HeatYear{}}
			default:
				return nil, err
			}
		}
		result = append(result, report)
	}
	return result, nil
}

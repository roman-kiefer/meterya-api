package store

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/domain"
)

type WorkspaceSnapshot struct {
	CurrentYear int
	Points      []domain.Point
	Annual      map[string][]domain.AnnualSummary
	Definitions []domain.VirtualPoint
	Transitions []domain.MeterTransition
	Heat        []domain.PowerToHeat
	Photos      map[string][]domain.Photo
}

func snapshot(ctx context.Context, tx pgx.Tx, ws string) (WorkspaceSnapshot, error) {
	out := WorkspaceSnapshot{Points: []domain.Point{}, Annual: map[string][]domain.AnnualSummary{}, Definitions: []domain.VirtualPoint{}}
	var timezone string
	if err := tx.QueryRow(ctx, "SELECT timezone FROM workspaces WHERE id=$1", ws).Scan(&timezone); err != nil {
		return out, mapError(err)
	}
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return out, err
	}
	out.CurrentYear = time.Now().In(zone).Year()
	rows, err := tx.Query(ctx, "SELECT state FROM points WHERE workspace_id=$1 ORDER BY state->>'name'", ws)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var b []byte
		var p domain.Point
		if err = rows.Scan(&b); err != nil {
			break
		}
		if err = json.Unmarshal(b, &p); err != nil {
			break
		}
		sort.SliceStable(p.Readings, func(i, j int) bool { return p.Readings[i].Date > p.Readings[j].Date })
		out.Points = append(out.Points, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, "SELECT a.point_id,a.state FROM annual_summaries a JOIN points p ON p.id=a.point_id WHERE p.workspace_id=$1 ORDER BY a.year DESC", ws)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id string
		var b []byte
		var s domain.AnnualSummary
		if err = rows.Scan(&id, &b); err != nil {
			break
		}
		if err = json.Unmarshal(b, &s); err != nil {
			break
		}
		out.Annual[id] = append(out.Annual[id], s)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, "SELECT state FROM virtual_points WHERE workspace_id=$1 AND NOT deleted ORDER BY state->>'name'", ws)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var b []byte
		var v domain.VirtualPoint
		if err = rows.Scan(&b); err != nil {
			break
		}
		if err = json.Unmarshal(b, &v); err != nil {
			break
		}
		out.Definitions = append(out.Definitions, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	out.Transitions, err = transitions(ctx, tx, ws)
	if err != nil {
		return out, err
	}
	out.Heat, err = heatConfigurations(ctx, tx, ws)
	if err == nil {
		out.Photos, err = photos(ctx, tx, ws)
	}
	return out, err
}
func (s *Store) Snapshot(ctx context.Context, ws string) (WorkspaceSnapshot, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	out, err := snapshot(ctx, tx, ws)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s WorkspaceSnapshot) PointMap() map[string]domain.Point {
	out := map[string]domain.Point{}
	for _, p := range s.Points {
		out[p.ID] = p
	}
	return out
}
func (s WorkspaceSnapshot) VirtualReports() ([]domain.VirtualReport, error) {
	out := []domain.VirtualReport{}
	points := s.PointMap()
	for _, v := range s.Definitions {
		report, err := domain.EvaluateVirtual(v, points, s.Transitions, s.Heat)
		if err != nil {
			switch domain.Code(err) {
			case "VIRTUAL_AMBIGUOUS", "VIRTUAL_INCOMPATIBLE", "VIRTUAL_CYCLE", "SOURCE_MISSING":
				report = domain.UnavailableVirtual(v, domain.Code(err), points, s.Transitions, s.Heat)
			default:
				return nil, err
			}
		}
		report.Enrich()
		out = append(out, report)
	}
	return out, nil
}

func (s *Store) SaveVirtual(ctx context.Context, c Command, v domain.VirtualPoint, remove bool) (domain.VirtualPoint, error) {
	var result domain.VirtualPoint
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	var wsVersion int64
	if err = tx.QueryRow(ctx, "SELECT version FROM workspaces WHERE id=$1 FOR UPDATE", c.Workspace).Scan(&wsVersion); err != nil {
		return result, mapError(err)
	}
	var role string
	if err = tx.QueryRow(ctx, roleQuery+" FOR SHARE", c.Workspace, c.Subject).Scan(&role); err != nil || !CanManage(role) {
		return result, domain.Fail("PERMISSION_DENIED")
	}
	if found, e := receipt(ctx, tx, c, &result); found || e != nil {
		return result, e
	}
	if c.PointID != "" {
		var b []byte
		if err = tx.QueryRow(ctx, "SELECT state FROM virtual_points WHERE id=$1 AND workspace_id=$2 AND NOT deleted FOR UPDATE", c.PointID, c.Workspace).Scan(&b); err != nil {
			return result, mapError(err)
		}
		if err = json.Unmarshal(b, &result); err != nil {
			return result, err
		}
		if result.Version != c.Expected {
			return result, domain.Fail("AGGREGATE_VERSION_CONFLICT")
		}
		v.ID, v.Version, v.WorkspaceID = result.ID, result.Version+1, c.Workspace
		if v.Type != result.Type || v.Root != result.Root || v.HeatID != result.HeatID {
			return result, domain.Fail("VALIDATION_FAILED")
		}
	} else {
		if remove || c.Expected != 0 {
			return result, domain.Fail("VALIDATION_FAILED")
		}
		v.ID, v.Version, v.WorkspaceID = uuid.NewString(), 1, c.Workspace
	}
	v.Deleted = remove
	if !domain.ValidName(v.Name) || (v.Type != "SEQUENCE" && v.Type != "HEAT") {
		return result, domain.Fail("VALIDATION_FAILED")
	}
	if v.Choices == nil {
		v.Choices = map[string]string{}
	}
	snap, err := snapshot(ctx, tx, c.Workspace)
	if err != nil {
		return result, err
	}
	if v.Type == "HEAT" {
		inherited := map[string]string{}
		for _, config := range snap.Heat {
			if config.ID == v.HeatID {
				for old, next := range config.Choices {
					inherited[old] = next
				}
			}
		}
		for old, next := range v.Choices {
			inherited[old] = next
		}
		v.Choices = inherited
	}
	// One named logical meter per source definition avoids competing heat choices.
	for _, other := range snap.Definitions {
		if other.ID != v.ID && other.Type == v.Type && ((v.Type == "SEQUENCE" && other.Root == v.Root) || (v.Type == "HEAT" && other.HeatID == v.HeatID)) {
			return result, domain.Fail("VIRTUAL_ALREADY_EXISTS")
		}
	}
	if !remove && !(c.PointID != "" && reflect.DeepEqual(result.Choices, v.Choices)) {
		report, e := domain.EvaluateVirtual(v, snap.PointMap(), snap.Transitions, snap.Heat)
		if e != nil {
			return result, e
		}
		for _, flag := range report.Info.Flags {
			if flag == "SOURCE_MISSING" {
				return result, domain.Fail("SOURCE_MISSING")
			}
		}
		if v.Type == "SEQUENCE" && len(report.Info.Sources) < 2 {
			return result, domain.Fail("VIRTUAL_TRANSITION_REQUIRED")
		}
		for old, next := range v.Choices {
			valid := false
			for _, change := range snap.Transitions {
				if change.OldPoint == old {
					for _, id := range change.NewPoints {
						if id == next {
							valid = true
						}
					}
				}
			}
			if !valid {
				return result, domain.Fail("VALIDATION_FAILED")
			}
		}
	}
	if v.Type == "HEAT" && !remove {
		for _, config := range snap.Heat {
			if config.ID != v.HeatID || (len(config.Choices) == 0 && len(v.Choices) == 0) || reflect.DeepEqual(config.Choices, v.Choices) {
				continue
			}
			config.Choices = v.Choices
			wsVersion++
			config.Version = wsVersion
			payload, _ := json.Marshal(config)
			if _, err = tx.Exec(ctx, "UPDATE workspaces SET version=$1 WHERE id=$2", wsVersion, c.Workspace); err != nil {
				return result, err
			}
			if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$2,'PowerToHeatSourceChoicesSaved',$4,$5)", uuid.NewString(), c.Workspace, wsVersion, c.Subject, payload); err != nil {
				return result, err
			}
		}
	}
	b, _ := json.Marshal(v)
	if _, err = tx.Exec(ctx, "INSERT INTO virtual_points(id,workspace_id,version,state,archived,deleted) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET version=excluded.version,state=excluded.state,archived=excluded.archived,deleted=excluded.deleted", v.ID, c.Workspace, v.Version, b, v.Archived, v.Deleted); err != nil {
		return result, err
	}
	kind := "VirtualPointSaved"
	if remove {
		kind = "VirtualPointRemoved"
	}
	if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$4,$5,$6,$7)", uuid.NewString(), v.ID, v.Version, c.Workspace, kind, c.Subject, b); err != nil {
		return result, err
	}
	if err = saveReceipt(ctx, tx, c, v); err != nil {
		return result, err
	}
	return v, tx.Commit(ctx)
}

// Used with a workspace lock before moving or permanently deleting a source.
func referencedVirtual(ctx context.Context, tx pgx.Tx, ws, id string) error {
	snap, err := snapshot(ctx, tx, ws)
	if err != nil {
		return err
	}
	for _, v := range snap.Definitions {
		roots := []string{v.Root}
		effectiveChoices := map[string]string{}
		if v.Type == "HEAT" {
			roots = nil
			for _, config := range snap.Heat {
				if config.ID == v.HeatID {
					roots = []string{config.NetPoint, config.HousePoint}
					for old, next := range config.Choices {
						effectiveChoices[old] = next
					}
				}
			}
		}
		for old, next := range v.Choices {
			effectiveChoices[old] = next
		}
		seen := map[string]bool{}
		var contains func(string) bool
		contains = func(point string) bool {
			if point == id {
				return true
			}
			if point == "" || seen[point] {
				return false
			}
			seen[point] = true
			for _, change := range snap.Transitions {
				if change.OldPoint != point {
					continue
				}
				next := effectiveChoices[point]
				if next == "" {
					next = change.Continuation
				}
				if next != "" {
					return contains(next)
				}
				for _, child := range change.NewPoints {
					if contains(child) {
						return true
					}
				}
			}
			return false
		}
		for _, root := range roots {
			if contains(root) {
				return domain.Fail("VIRTUAL_SOURCE_IN_USE")
			}
		}
	}
	return nil
}

// A branching replacement affecting an existing logical meter needs an explicit continuation.
func validateVirtualContinuation(ctx context.Context, tx pgx.Tx, ws string, change domain.MeterTransition) error {
	if change.Continuation != "" {
		found := false
		for _, id := range change.NewPoints {
			if id == change.Continuation {
				found = true
			}
		}
		if !found {
			return domain.Fail("VALIDATION_FAILED")
		}
	}
	if len(change.NewPoints) < 2 || change.Continuation != "" {
		return nil
	}
	snap, err := snapshot(ctx, tx, ws)
	if err != nil {
		return err
	}
	reports, err := snap.VirtualReports()
	if err != nil {
		return err
	}
	for _, r := range reports {
		for _, source := range r.Info.Sources {
			if source.PointID == change.OldPoint {
				return domain.Fail("VIRTUAL_CONTINUATION_REQUIRED")
			}
		}
	}
	return nil
}

func (s *Store) VirtualCandidates(ctx context.Context, ws string) (map[string]any, error) {
	snap, err := s.Snapshot(ctx, ws)
	if err != nil {
		return nil, err
	}
	// Client selection uses existing links; source compatibility remains server enforced.
	branches := []domain.MeterTransition{}
	for _, c := range snap.Transitions {
		if len(c.NewPoints) > 1 {
			branches = append(branches, c)
		}
	}
	sort.Slice(branches, func(i, j int) bool { return branches[i].Date < branches[j].Date })
	catalog := []map[string]any{}
	for _, p := range snap.Points {
		catalog = append(catalog, map[string]any{"id": p.ID, "name": p.Name, "serial": p.Meter.Serial, "archived": p.Archived, "registers": p.Meter.Registers})
	}
	return map[string]any{"points": catalog, "definitions": snap.Definitions, "transitions": snap.Transitions, "branches": branches, "heat": snap.Heat}, nil
}

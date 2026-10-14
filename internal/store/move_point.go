package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"meterya.local/api/internal/domain"
)

// MovePoint keeps the aggregate identity and its immutable history while changing
// ownership. Both workspaces are locked before checking references and access.
func (s *Store) MovePoint(ctx context.Context, c Command, target string) (domain.Point, error) {
	var p domain.Point
	if _, err := uuid.Parse(target); err != nil || target == c.Workspace {
		return p, domain.Fail("VALIDATION_FAILED")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT id FROM workspaces WHERE id=ANY($1::uuid[]) ORDER BY id FOR UPDATE", []string{c.Workspace, target})
	if err != nil {
		return p, err
	}
	count := 0
	for rows.Next() {
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	if count != 2 {
		return p, domain.Fail("RESOURCE_NOT_FOUND")
	}
	for _, ws := range []string{c.Workspace, target} {
		var role string
		if err = tx.QueryRow(ctx, roleQuery+" FOR SHARE", ws, c.Subject).Scan(&role); err != nil || !CanManage(role) {
			return p, domain.Fail("PERMISSION_DENIED")
		}
	}
	if found, e := receipt(ctx, tx, c, &p); found || e != nil {
		if e != nil {
			return p, e
		}
		var present bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM points WHERE id=$1 AND workspace_id=$2)", c.PointID, target).Scan(&present); err != nil {
			return p, err
		}
		if !present {
			return p, domain.Fail("RESOURCE_NOT_FOUND")
		}
		return p, nil
	}
	var state []byte
	if err = tx.QueryRow(ctx, "SELECT state FROM points WHERE id=$1 AND workspace_id=$2 FOR UPDATE", c.PointID, c.Workspace).Scan(&state); err != nil {
		return p, mapError(err)
	}
	if err = json.Unmarshal(state, &p); err != nil {
		return p, err
	}
	if p.Version != c.Expected {
		return p, domain.Fail("AGGREGATE_VERSION_CONFLICT")
	}
	transitions, err := transitions(ctx, tx, c.Workspace)
	if err != nil {
		return p, err
	}
	for _, transition := range transitions {
		if transition.OldPoint == p.ID {
			return p, domain.Fail("POINT_LINKED")
		}
		for _, id := range transition.NewPoints {
			if id == p.ID {
				return p, domain.Fail("POINT_LINKED")
			}
		}
	}
	heat, err := heatConfigurations(ctx, tx, c.Workspace)
	if err != nil {
		return p, err
	}
	for _, configuration := range heat {
		if configuration.NetPoint == p.ID || configuration.HousePoint == p.ID {
			return p, domain.Fail("POINT_LINKED")
		}
	}
	if err = referencedVirtual(ctx, tx, c.Workspace, p.ID); err != nil {
		return p, err
	}
	p.WorkspaceID = target
	p.Version++
	state, err = json.Marshal(p)
	if err != nil {
		return p, err
	}
	if _, err = tx.Exec(ctx, "UPDATE points SET workspace_id=$1,version=$2,state=$3 WHERE id=$4", target, p.Version, state, p.ID); err != nil {
		var duplicate *pgconn.PgError
		if errors.As(err, &duplicate) && duplicate.Code == "23505" && duplicate.ConstraintName == "points_active_serial" {
			return p, domain.Fail("SERIAL_ALREADY_ACTIVE")
		}
		return p, err
	}
	payload, _ := json.Marshal(map[string]string{"from_workspace_id": c.Workspace, "to_workspace_id": target})
	if _, err = tx.Exec(ctx, "UPDATE photos SET workspace_id=$1 WHERE point_id=$2", target, p.ID); err != nil {
		return p, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$4,'PointWorkspaceMoved',$5,$6)", uuid.NewString(), p.ID, p.Version, target, c.Subject, payload); err != nil {
		return p, err
	}
	if err = replaceAnnualProjections(ctx, tx, p); err != nil {
		return p, err
	}
	if err = saveReceipt(ctx, tx, c, p); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

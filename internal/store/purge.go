package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/domain"
)

// PurgePoint removes the explicitly selected point's data, including event
// payloads and saved idempotent responses. A data-free deletion marker prevents
// accidental resurrection during replay without retaining readings or metadata.
func (s *Store) PurgePoint(ctx context.Context, c Command, confirmation string) error {
	if confirmation != c.PointID || c.PointID == "" {
		return domain.Fail("VALIDATION_FAILED")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var workspaceID string
	if err = tx.QueryRow(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", c.Workspace).Scan(&workspaceID); err != nil {
		return mapError(err)
	}
	var role string
	if err = tx.QueryRow(ctx, roleQuery+" FOR SHARE", c.Workspace, c.Subject).Scan(&role); err != nil || !CanManage(role) {
		return domain.Fail("PERMISSION_DENIED")
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", c.Subject+":"+c.Key); err != nil {
		return err
	}
	var fingerprint string
	err = tx.QueryRow(ctx, "SELECT fingerprint FROM idempotency WHERE subject=$1 AND key=$2", c.Subject, c.Key).Scan(&fingerprint)
	if err == nil {
		if fingerprint != c.Fingerprint {
			return domain.Fail("IDEMPOTENCY_CONFLICT")
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var p domain.Point
	var state []byte
	if err = tx.QueryRow(ctx, "SELECT state FROM points WHERE workspace_id=$1 AND id=$2 FOR UPDATE", c.Workspace, c.PointID).Scan(&state); err != nil {
		return mapError(err)
	}
	if err = json.Unmarshal(state, &p); err != nil {
		return err
	}
	if p.Version != c.Expected {
		return domain.Fail("AGGREGATE_VERSION_CONFLICT")
	}
	if err = referencedVirtual(ctx, tx, c.Workspace, p.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('meterya.purge_point',$1,true)", p.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM events WHERE aggregate_id=$1", p.ID); err != nil {
		return err
	}
	// Keep only a payload-free receipt so an old create retry cannot recreate it.
	if _, err = tx.Exec(ctx, "UPDATE idempotency SET response='{\"purged\":true}'::jsonb WHERE response->>'id'=$1 OR response->>'id' IN(SELECT id::text FROM photos WHERE point_id=$1::uuid)", p.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM readings WHERE point_id=$1", p.ID); err != nil {
		return err
	}
	// Annual summaries cascade with the point. Its embedded tariffs are removed too.
	if _, err = tx.Exec(ctx, "DELETE FROM points WHERE workspace_id=$1 AND id=$2", c.Workspace, p.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$4,'PointPurged',$5,'{}'::jsonb)", uuid.NewString(), p.ID, p.Version+1, c.Workspace, c.Subject); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO idempotency(subject,key,fingerprint,response) VALUES($1,$2,$3,'{\"deleted\":true}'::jsonb)", c.Subject, c.Key, c.Fingerprint); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

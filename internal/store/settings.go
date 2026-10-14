package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/domain"
	"strings"
)

func (s *Store) RenameWorkspace(ctx context.Context, c Command, name string) (Member, error) {
	var result Member
	name = strings.TrimSpace(name)
	if !domain.ValidName(name) {
		return result, domain.Fail("VALIDATION_FAILED")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, "SELECT id,name,version FROM workspaces WHERE id=$1 FOR UPDATE", c.Workspace).Scan(&result.ID, &result.Name, &result.Version); err != nil {
		return result, mapError(err)
	}
	var role string
	if err = tx.QueryRow(ctx, roleQuery+" FOR SHARE", c.Workspace, c.Subject).Scan(&role); err != nil || !CanManage(role) {
		return result, domain.Fail("PERMISSION_DENIED")
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", c.Subject+":"+c.Key); err != nil {
		return result, err
	}
	var fingerprint string
	var saved []byte
	err = tx.QueryRow(ctx, "SELECT fingerprint,response FROM idempotency WHERE subject=$1 AND key=$2", c.Subject, c.Key).Scan(&fingerprint, &saved)
	if err == nil {
		if fingerprint != c.Fingerprint {
			return result, domain.Fail("IDEMPOTENCY_CONFLICT")
		}
		err = json.Unmarshal(saved, &result)
		return result, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if result.Version != c.Expected {
		return result, domain.Fail("AGGREGATE_VERSION_CONFLICT")
	}
	result.Name, result.Role = name, role
	result.Version++
	body, _ := json.Marshal(map[string]string{"name": name})
	if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$2,'WorkspaceRenamed',$4,$5)", uuid.NewString(), c.Workspace, result.Version, c.Subject, body); err != nil {
		return result, mapError(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE workspaces SET name=$1,version=$2 WHERE id=$3", name, result.Version, c.Workspace); err != nil {
		return result, err
	}
	response, _ := json.Marshal(result)
	if _, err = tx.Exec(ctx, "INSERT INTO idempotency(subject,key,fingerprint,response) VALUES($1,$2,$3,$4)", c.Subject, c.Key, c.Fingerprint, response); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

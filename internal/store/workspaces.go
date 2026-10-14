package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/domain"
)

type WorkspaceMember struct {
	Subject     string `json:"subject"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	SystemAdmin bool   `json:"system_admin"`
	Active      bool   `json:"active"`
}

type Memberships struct {
	Version int64             `json:"version"`
	Members []WorkspaceMember `json:"members"`
}

// receipt serializes retries. Permissions must be checked before reading it.
func receipt(ctx context.Context, tx pgx.Tx, c Command, result any) (bool, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", c.Subject+":"+c.Key); err != nil {
		return false, err
	}
	var fingerprint string
	var body []byte
	err := tx.QueryRow(ctx, "SELECT fingerprint,response FROM idempotency WHERE subject=$1 AND key=$2", c.Subject, c.Key).Scan(&fingerprint, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if fingerprint != c.Fingerprint {
		return false, domain.Fail("IDEMPOTENCY_CONFLICT")
	}
	return true, json.Unmarshal(body, result)
}

func saveReceipt(ctx context.Context, tx pgx.Tx, c Command, result any) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO idempotency(subject,key,fingerprint,response) VALUES($1,$2,$3,$4)", c.Subject, c.Key, c.Fingerprint, body)
	return err
}

func (s *Store) CreateWorkspace(ctx context.Context, c Command, name, email string) (Member, error) {
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
	// Creating additional workspaces is reserved for an existing administrator.
	var admin string
	if err = tx.QueryRow(ctx, "SELECT subject FROM members WHERE active AND subject=$1 AND role='ADMIN' ORDER BY workspace_id LIMIT 1 FOR SHARE", c.Subject).Scan(&admin); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return result, domain.Fail("PERMISSION_DENIED")
		}
		return result, err
	}
	if found, e := receipt(ctx, tx, c, &result); found || e != nil {
		return result, e
	}
	result = Member{ID: uuid.NewString(), Name: name, Role: "ADMIN", Version: 1}
	if _, err = tx.Exec(ctx, "INSERT INTO workspaces(id,name) VALUES($1,$2)", result.ID, name); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,$2,$3,'ADMIN')", result.ID, c.Subject, email); err != nil {
		return result, err
	}
	body, _ := json.Marshal(map[string]string{"id": result.ID, "name": name, "subject": c.Subject, "email": email})
	if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,1,$2,'WorkspaceCreated',$3,$4)", uuid.NewString(), result.ID, c.Subject, body); err != nil {
		return result, err
	}
	if err = saveReceipt(ctx, tx, c, result); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) WorkspaceMembers(ctx context.Context, ws, subject string) (Memberships, error) {
	result := Memberships{Members: []WorkspaceMember{}}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	var role string
	if err = tx.QueryRow(ctx, roleQuery, ws, subject).Scan(&role); err != nil || !CanManage(role) {
		return result, domain.Fail("PERMISSION_DENIED")
	}
	if err = tx.QueryRow(ctx, "SELECT version FROM workspaces WHERE id=$1", ws).Scan(&result.Version); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, "SELECT m.subject,m.email,m.role,EXISTS(SELECT 1 FROM members a WHERE a.subject=m.subject AND a.active AND a.role='ADMIN'),m.active FROM members m WHERE m.workspace_id=$1 ORDER BY m.email,m.subject", ws)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var m WorkspaceMember
		if err = rows.Scan(&m.Subject, &m.Email, &m.Role, &m.SystemAdmin, &m.Active); err != nil {
			rows.Close()
			return result, err
		}
		result.Members = append(result.Members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) ManageMember(ctx context.Context, c Command, target, role string, remove bool) error {
	return s.manageMember(ctx, c, target, role, remove, nil)
}
func (s *Store) SetMemberActive(ctx context.Context, c Command, target string, active bool) error {
	return s.manageMember(ctx, c, target, "", false, &active)
}
func (s *Store) manageMember(ctx context.Context, c Command, target, role string, remove bool, active *bool) error {
	if target == "" || len(target) > 255 || (!remove && active == nil && !ValidRole(role)) {
		return domain.Fail("VALIDATION_FAILED")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serialize global admin changes across all workspaces as well.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('system-admin-memberships',0))"); err != nil {
		return err
	}
	// Serialize membership changes, including changes to the last administrator.
	var version int64
	if err = tx.QueryRow(ctx, "SELECT version FROM workspaces WHERE id=$1 FOR UPDATE", c.Workspace).Scan(&version); err != nil {
		return mapError(err)
	}
	var actorRole string
	if err = tx.QueryRow(ctx, roleQuery, c.Workspace, c.Subject).Scan(&actorRole); err != nil || !CanManage(actorRole) {
		return domain.Fail("PERMISSION_DENIED")
	}
	if role == "ADMIN" && actorRole != "ADMIN" {
		return domain.Fail("PERMISSION_DENIED")
	}
	var saved map[string]bool
	if found, e := receipt(ctx, tx, c, &saved); found || e != nil {
		return e
	}
	if version != c.Expected {
		return domain.Fail("AGGREGATE_VERSION_CONFLICT")
	}
	var previous string
	var previousActive bool
	if err = tx.QueryRow(ctx, "SELECT role,active FROM members WHERE workspace_id=$1 AND subject=$2 FOR UPDATE", c.Workspace, target).Scan(&previous, &previousActive); err != nil {
		return mapError(err)
	}
	var globalTarget bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM members WHERE active AND subject=$1 AND role='ADMIN')", target).Scan(&globalTarget); err != nil {
		return err
	}
	if (globalTarget || previous == "ADMIN") && actorRole != "ADMIN" {
		return domain.Fail("PERMISSION_DENIED")
	}
	if active != nil {
		role = previous
	}
	deactivating := active != nil && !*active
	if previousActive && previous == "ADMIN" && (remove || role != "ADMIN" || deactivating) {
		var remaining bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM members WHERE active AND role='ADMIN' AND NOT (workspace_id=$1 AND subject=$2))", c.Workspace, target).Scan(&remaining); err != nil {
			return err
		}
		if !remaining {
			return domain.Fail("LAST_WORKSPACE_ADMIN")
		}
	}
	if previousActive && CanManage(previous) && (remove || !CanManage(role) || deactivating) {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM members WHERE active AND workspace_id=$1 AND role IN ('ADMIN','MODERATOR')", c.Workspace).Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return domain.Fail("LAST_WORKSPACE_ADMIN")
		}
	}
	kind := "WorkspaceMemberRoleChanged"
	if active != nil {
		kind = "WorkspaceMemberActivityChanged"
		_, err = tx.Exec(ctx, "UPDATE members SET active=$1 WHERE workspace_id=$2 AND subject=$3", *active, c.Workspace, target)
	} else if remove {
		kind = "WorkspaceMemberRemoved"
		_, err = tx.Exec(ctx, "DELETE FROM members WHERE workspace_id=$1 AND subject=$2", c.Workspace, target)
	} else {
		_, err = tx.Exec(ctx, "UPDATE members SET role=$1 WHERE workspace_id=$2 AND subject=$3", role, c.Workspace, target)
	}
	if err != nil {
		return err
	}
	payload := map[string]any{"workspace_id": c.Workspace, "subject": target, "role": role}
	if active != nil {
		payload["active"] = *active
	}
	body, _ := json.Marshal(payload)
	if _, err = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$2,$4,$5,$6)", uuid.NewString(), c.Workspace, version+1, kind, c.Subject, body); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE workspaces SET version=version+1 WHERE id=$1", c.Workspace); err != nil {
		return err
	}
	if err = saveReceipt(ctx, tx, c, map[string]bool{"updated": true}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

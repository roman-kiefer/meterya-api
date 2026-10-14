package httpapi

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/auth"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

type invitation struct {
	ID      string    `json:"id"`
	Email   string    `json:"email"`
	Role    string    `json:"role"`
	Status  string    `json:"status"`
	Expires time.Time `json:"expires_at"`
	Version int64     `json:"version"`
}

func (s *Server) admin(r *http.Request, v store.Session) error {
	role, err := s.Store.Role(r.Context(), r.PathValue("ws"), v.Subject)
	if err != nil {
		return err
	}
	if !store.CanManage(role) {
		return domain.Fail("PERMISSION_DENIED")
	}
	if v.ACR != "2" {
		return domain.Fail("MFA_REQUIRED")
	}
	return nil
}
func (s *Server) listInvites(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.admin(r, v); err != nil {
		return err
	}
	rows, err := s.Store.Pool.Query(r.Context(), "SELECT id,email,role,status,expires_at,version FROM invitations WHERE workspace_id=$1 AND status<>'ACCEPTED' ORDER BY expires_at DESC", r.PathValue("ws"))
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []invitation{}
	for rows.Next() {
		var i invitation
		if err = rows.Scan(&i.ID, &i.Email, &i.Role, &i.Status, &i.Expires, &i.Version); err != nil {
			return err
		}
		items = append(items, i)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	write(w, 200, items)
	return nil
}
func (s *Server) invite(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.admin(r, v); err != nil {
		return err
	}
	var input struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	parsed, err := mail.ParseAddress(input.Email)
	if err != nil || parsed.Address != input.Email || len(input.Email) > 254 || !store.ValidRole(input.Role) {
		return domain.Fail("VALIDATION_FAILED")
	}
	if input.Role == "ADMIN" {
		role, err := s.Store.Role(r.Context(), r.PathValue("ws"), v.Subject)
		if err != nil || role != "ADMIN" {
			return domain.Fail("PERMISSION_DENIED")
		}
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", v.Subject+":"+k)
	if err != nil {
		return err
	}
	fp := store.Hash(r.URL.Path + string(body))
	var oldfp string
	var old []byte
	err = tx.QueryRow(r.Context(), "SELECT fingerprint,response FROM idempotency WHERE subject=$1 AND key=$2", v.Subject, k).Scan(&oldfp, &old)
	if err == nil {
		if oldfp != fp {
			return domain.Fail("IDEMPOTENCY_CONFLICT")
		}
		var saved invitation
		if err = json.Unmarshal(old, &saved); err != nil {
			return err
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		write(w, 201, saved)
		return nil
	}
	if err != pgx.ErrNoRows {
		return err
	}
	i := invitation{ID: uuid.NewString(), Email: input.Email, Role: input.Role, Status: "PENDING", Expires: time.Now().Add(7 * 24 * time.Hour), Version: 1}
	var exists bool
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM members WHERE workspace_id=$1 AND email=$2)", r.PathValue("ws"), input.Email).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return domain.Fail("CONFLICT")
	}
	token := auth.Random()
	_, err = tx.Exec(r.Context(), "INSERT INTO invitations(id,workspace_id,email,role,token_hash,status,expires_at,actor,version) VALUES($1,$2,$3,$4,$5,'PENDING',$6,$7,1)", i.ID, r.PathValue("ws"), i.Email, i.Role, store.Hash(token), i.Expires, v.Subject)
	if err != nil {
		return domain.Fail("CONFLICT")
	}
	payload, _ := json.Marshal(i)
	_, err = tx.Exec(r.Context(), "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,1,$3,'MemberInvited',$4,$5)", uuid.NewString(), i.ID, r.PathValue("ws"), v.Subject, payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), "INSERT INTO idempotency(subject,key,fingerprint,response) VALUES($1,$2,$3,$4)", v.Subject, k, fp, payload)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	if err = s.deliver(r, &i, token); err != nil {
		return err
	}
	write(w, 201, i)
	return nil
}
func (s *Server) deliver(r *http.Request, i *invitation, token string) error {
	redirect := s.Auth.Origin + "/invitations/accept?token=" + url.QueryEscape(token)
	sub, err := s.Identity.SendInvite(r.Context(), i.Email, i.Role, redirect, i.Expires)
	i.Status = "SENT"
	if err != nil {
		i.Status = "FAILED"
	}
	_, err = s.Store.Pool.Exec(r.Context(), "UPDATE invitations SET status=$1,subject=NULLIF($2,'') WHERE id=$3 AND status='PENDING' AND token_hash=$4", i.Status, sub, i.ID, store.Hash(token))
	if err != nil {
		return err
	}
	return s.Store.Pool.QueryRow(r.Context(), "SELECT status,version FROM invitations WHERE id=$1", i.ID).Scan(&i.Status, &i.Version)
}
func (s *Server) retryInvite(w http.ResponseWriter, r *http.Request, v store.Session) error {
	return s.changeInvite(w, r, v, false, false)
}
func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request, v store.Session) error {
	return s.changeInvite(w, r, v, true, false)
}
func (s *Server) deleteInvite(w http.ResponseWriter, r *http.Request, v store.Session) error {
	return s.changeInvite(w, r, v, false, true)
}
func (s *Server) changeInvite(w http.ResponseWriter, r *http.Request, v store.Session, revoke, remove bool) error {
	if err := s.admin(r, v); err != nil {
		return err
	}
	var input struct {
		Expected int64 `json:"expected_version"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	fp := store.Hash(r.URL.Path + string(body))
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", v.Subject+":"+k); err != nil {
		return err
	}
	var savedfp string
	var saved []byte
	err = tx.QueryRow(r.Context(), "SELECT fingerprint,response FROM idempotency WHERE subject=$1 AND key=$2", v.Subject, k).Scan(&savedfp, &saved)
	if err == nil {
		if savedfp != fp {
			return domain.Fail("IDEMPOTENCY_CONFLICT")
		}
		write(w, 200, json.RawMessage(saved))
		return nil
	}
	if err != pgx.ErrNoRows {
		return err
	}
	var i invitation
	err = tx.QueryRow(r.Context(), "SELECT id,email,role,status,expires_at,version FROM invitations WHERE id=$1 AND workspace_id=$2 FOR UPDATE", r.PathValue("invite"), r.PathValue("ws")).Scan(&i.ID, &i.Email, &i.Role, &i.Status, &i.Expires, &i.Version)
	if err == pgx.ErrNoRows {
		return domain.Fail("RESOURCE_NOT_FOUND")
	}
	if err != nil {
		return err
	}
	actorRole, roleErr := s.Store.Role(r.Context(), r.PathValue("ws"), v.Subject)
	if roleErr != nil {
		return roleErr
	}
	if !remove && i.Role == "ADMIN" && actorRole != "ADMIN" {
		return domain.Fail("PERMISSION_DENIED")
	}
	if i.Version != input.Expected {
		return domain.Fail("AGGREGATE_VERSION_CONFLICT")
	}
	if (remove && i.Status != "REVOKED") || (!remove && (i.Status == "ACCEPTED" || i.Status == "REVOKED" || (!revoke && time.Now().After(i.Expires)))) {
		return domain.Fail("INVITATION_UNAVAILABLE")
	}
	kind := "InvitationRetried"
	i.Version++
	i.Status = "PENDING"
	token := auth.Random()
	if revoke {
		kind = "InvitationRevoked"
		i.Status = "REVOKED"
	}
	if remove {
		kind = "InvitationDeleted"
		i.Status = "REVOKED"
	}
	payload, _ := json.Marshal(i)
	if remove {
		_, err = tx.Exec(r.Context(), "DELETE FROM invitations WHERE id=$1", i.ID)
	} else {
		_, err = tx.Exec(r.Context(), "UPDATE invitations SET version=$1,status=$2,token_hash=$3 WHERE id=$4", i.Version, i.Status, store.Hash(token), i.ID)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$4,$5,$6,$7)", uuid.NewString(), i.ID, i.Version, r.PathValue("ws"), kind, v.Subject, payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), "INSERT INTO idempotency(subject,key,fingerprint,response) VALUES($1,$2,$3,$4)", v.Subject, k, fp, payload)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	if !revoke && !remove {
		if err = s.deliver(r, &i, token); err != nil {
			return err
		}
	}
	write(w, 200, i)
	return nil
}
func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request, v store.Session) error {
	var input struct {
		Token string `json:"token"`
	}
	_, err := decode(r, &input)
	if err != nil {
		return err
	}
	if len(input.Token) != 43 {
		return domain.Fail("INVITATION_UNAVAILABLE")
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var id, ws, email, role, status, subject string
	var expiry time.Time
	var version int64
	err = tx.QueryRow(r.Context(), "SELECT id,workspace_id,email,role,status,expires_at,COALESCE(subject,''),version FROM invitations WHERE token_hash=$1 FOR UPDATE", store.Hash(input.Token)).Scan(&id, &ws, &email, &role, &status, &expiry, &subject, &version)
	if err == pgx.ErrNoRows {
		return domain.Fail("INVITATION_UNAVAILABLE")
	}
	if err != nil {
		return err
	}
	if status == "ACCEPTED" && subject == v.Subject {
		write(w, 200, map[string]string{"workspace_id": ws})
		return nil
	}
	if status != "SENT" || time.Now().After(expiry) {
		return domain.Fail("INVITATION_UNAVAILABLE")
	}
	if email != strings.ToLower(v.Email) || subject != v.Subject {
		return domain.Fail("PERMISSION_DENIED")
	}
	if store.CanManage(role) && v.ACR != "2" {
		return domain.Fail("MFA_REQUIRED")
	}
	var workspaceVersion int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM workspaces WHERE id=$1 FOR UPDATE", ws).Scan(&workspaceVersion); err != nil {
		return err
	}
	// Do not silently change an existing membership through an invitation.
	tag, err := tx.Exec(r.Context(), "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", ws, v.Subject, email, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.Fail("CONFLICT")
	}
	_, err = tx.Exec(r.Context(), "UPDATE invitations SET status='ACCEPTED',version=version+1 WHERE id=$1", id)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"workspace_id": ws, "subject": v.Subject, "email": email, "role": role})
	_, err = tx.Exec(r.Context(), "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$4,'InvitationAccepted',$5,$6)", uuid.NewString(), id, version+1, ws, v.Subject, payload)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$2,'WorkspaceMemberAdded',$4,$5)", uuid.NewString(), ws, workspaceVersion+1, v.Subject, payload); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), "UPDATE workspaces SET version=version+1 WHERE id=$1", ws); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	write(w, 200, map[string]string{"workspace_id": ws})
	return nil
}

package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
)

func TestDeleteRevokedInvitation(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := "invite_test_" + uuid.NewString()[:8]
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE"); conn.Close(ctx) })
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Pool.Close)
	ws, err := db.Bootstrap(ctx, "Home", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.Bootstrap(ctx, "Other", "other-admin", "other@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'mod','mod@example.test','MODERATOR'),($1,'guest','guest@example.test','GUEST')", ws); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: db}
	insert := func(status string) string {
		t.Helper()
		id := uuid.NewString()
		_, err := db.Pool.Exec(ctx, "INSERT INTO invitations(id,workspace_id,email,role,token_hash,status,expires_at,actor) VALUES($1,$2,$3,'ADMIN',$5,$4,now()+interval '1 day','admin')", id, ws, id+"@example.test", status, id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	call := func(id, workspace, subject, acr, version, key string) error {
		r := httptest.NewRequest("POST", "/api/v1/workspaces/"+workspace+"/invitations/"+id+"/delete", strings.NewReader(`{"expected_version":`+version+`}`))
		r.SetPathValue("ws", workspace)
		r.SetPathValue("invite", id)
		r.Header.Set("Idempotency-Key", key)
		return server.deleteInvite(httptest.NewRecorder(), r, store.Session{Subject: subject, ACR: acr})
	}
	id := insert("REVOKED")
	for _, tc := range []struct{ ws, subject, acr, version, code string }{
		{ws, "guest", "2", "1", "PERMISSION_DENIED"},
		{ws, "mod", "1", "1", "MFA_REQUIRED"},
		{other, "mod", "2", "1", "RESOURCE_NOT_FOUND"},
		{ws, "mod", "2", "2", "AGGREGATE_VERSION_CONFLICT"},
	} {
		if err := call(id, tc.ws, tc.subject, tc.acr, tc.version, uuid.NewString()); domain.Code(err) != tc.code {
			t.Fatalf("expected %s, got %v", tc.code, err)
		}
	}
	for _, status := range []string{"SENT", "ACCEPTED", "FAILED"} {
		if err := call(insert(status), ws, "admin", "2", "1", uuid.NewString()); domain.Code(err) != "INVITATION_UNAVAILABLE" {
			t.Fatalf("deleted %s: %v", status, err)
		}
	}
	key := uuid.NewString()
	for range 2 {
		if err := call(id, ws, "mod", "2", "1", key); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM invitations WHERE id=$1", id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invitation remains: %d, %v", count, err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE aggregate_id=$1 AND event_type='InvitationDeleted'", id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit: %d, %v", count, err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM members WHERE workspace_id=$1 AND role='ADMIN'", ws).Scan(&count); err != nil || count != 1 {
		t.Fatalf("active admin changed: %d, %v", count, err)
	}
}

func TestInvitationListExcludesAcceptedHistory(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := "invite_list_" + uuid.NewString()[:8]
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE"); conn.Close(ctx) })
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Pool.Close)
	ws, err := db.Bootstrap(ctx, "Home", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.Bootstrap(ctx, "Other", "other-admin", "other@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'mod','mod@example.test','MODERATOR'),($1,'guest','guest@example.test','GUEST')", ws); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"SENT", "FAILED", "REVOKED", "ACCEPTED"} {
		id := uuid.NewString()
		if _, err = db.Pool.Exec(ctx, "INSERT INTO invitations(id,workspace_id,email,role,token_hash,status,expires_at,actor) VALUES($1,$2,$3,'USER',$5,$4,now()-interval '1 day','admin')", id, ws, status+"@example.test", status, id); err != nil {
			t.Fatal(err)
		}
	}
	server := &Server{Store: db}
	for _, subject := range []string{"admin", "mod"} {
		r := httptest.NewRequest("GET", "/api/v1/workspaces/"+ws+"/invitations", nil)
		r.SetPathValue("ws", ws)
		w := httptest.NewRecorder()
		if err := server.listInvites(w, r, store.Session{Subject: subject, ACR: "2"}); err != nil {
			t.Fatal(err)
		}
		var items []invitation
		if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != 3 {
			t.Fatalf("expected 3 actionable invitations, got %d", len(items))
		}
		seen := map[string]bool{}
		for _, item := range items {
			seen[item.Status] = true
		}
		if seen["ACCEPTED"] || !seen["SENT"] || !seen["FAILED"] || !seen["REVOKED"] {
			t.Fatalf("wrong statuses: %v", seen)
		}
	}
	for _, tc := range []struct{ workspace, subject, acr, code string }{
		{ws, "guest", "2", "PERMISSION_DENIED"},
		{ws, "mod", "1", "MFA_REQUIRED"},
		{other, "mod", "2", "RESOURCE_NOT_FOUND"},
	} {
		r := httptest.NewRequest("GET", "/api/v1/workspaces/"+tc.workspace+"/invitations", nil)
		r.SetPathValue("ws", tc.workspace)
		err := server.listInvites(httptest.NewRecorder(), r, store.Session{Subject: tc.subject, ACR: tc.acr})
		if domain.Code(err) != tc.code {
			t.Fatalf("expected %s, got %v", tc.code, err)
		}
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM invitations WHERE workspace_id=$1 AND status='ACCEPTED'", ws).Scan(&count); err != nil || count != 1 {
		t.Fatalf("accepted history changed: %d, %v", count, err)
	}
}

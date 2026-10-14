package httpapi

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/auth"
	"meterya.local/api/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAuditHTTPRequiresMFAAndScopedManagementRights(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := "audit_http_" + uuid.NewString()[:8]
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
	other, err := db.Bootstrap(ctx, "Other", "other", "other@example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'mod','mod@example.test','MODERATOR'),($1,'user','user@example.test','USER'),($1,'guest','guest@example.test','GUEST')", ws)
	if err != nil {
		t.Fatal(err)
	}
	for token, session := range map[string]store.Session{
		"admin": {Subject: "admin", ACR: "2"}, "weak": {Subject: "admin", ACR: "1"},
		"mod": {Subject: "mod", ACR: "2"}, "mod-weak": {Subject: "mod", ACR: "1"},
		"user": {Subject: "user", ACR: "2"}, "guest": {Subject: "guest", ACR: "2"},
	} {
		session.Expires = time.Now().Add(time.Hour)
		if err = db.SaveSession(ctx, token, session); err != nil {
			t.Fatal(err)
		}
	}
	server := (&Server{Store: db, Auth: &auth.Auth{Store: db, Origin: "http://localhost:8080"}}).Handler()
	for _, test := range []struct {
		token, path string
		status      int
	}{
		{"admin", "/api/v1/audit", 200},
		{"mod", "/api/v1/workspaces/" + ws + "/audit", 200},
		{"mod", "/api/v1/audit", 403},
		{"mod", "/api/v1/workspaces/" + other + "/audit", 404},
		{"weak", "/api/v1/audit", 403},
		{"mod-weak", "/api/v1/workspaces/" + ws + "/audit", 403},
		{"user", "/api/v1/workspaces/" + ws + "/audit", 403},
		{"guest", "/api/v1/workspaces/" + ws + "/audit", 403},
		{"admin", "/api/v1/workspaces/" + ws + "/audit?cursor=broken", 400},
		{"admin", "/api/v1/workspaces/" + ws + "/audit?event_type=WorkspaceCreated", 200},
	} {
		t.Run(test.token+test.path, func(t *testing.T) {
			r := httptest.NewRequest("GET", test.path, nil)
			r.AddCookie(&http.Cookie{Name: "meterya_session", Value: test.token})
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if w.Code == 200 && (strings.Contains(w.Body.String(), "payload") || !strings.Contains(w.Body.String(), "event_types")) {
				t.Fatal(w.Body.String())
			}
		})
	}
}

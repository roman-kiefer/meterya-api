package httpapi

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/auth"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestVirtualAPIReadPermissionsAndExportSourceVersions(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := "virtual_http_" + uuid.NewString()[:8]
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
	if _, err = db.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'guest','guest@example.test','GUEST'),($2,'foreign','foreign@example.test','MODERATOR')", ws, other); err != nil {
		t.Fatal(err)
	}
	makePoint := func(serial, from, until, opening, closing string) domain.Point {
		t.Helper()
		p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: serial, Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: serial, Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
		p, e := db.Execute(ctx, store.Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: serial, NewPoint: &p})
		if e != nil {
			t.Fatal(e)
		}
		for _, value := range []struct{ d, v string }{{from, opening}, {until, closing}} {
			r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: value.d, Value: value.v, Actor: "admin", RecordedAt: time.Now().UTC()}
			p, e = db.Execute(ctx, store.Command{Workspace: ws, Subject: "admin", PointID: p.ID, Expected: p.Version, Key: uuid.NewString(), Fingerprint: value.d, Reading: &r, Today: "2027-01-01"})
			if e != nil {
				t.Fatal(e)
			}
		}
		return p
	}
	old := makePoint("old", "2026-01-01", "2026-06-01", "100", "110")
	next := makePoint("new", "2026-06-01", "2027-01-01", "0", "20")
	if _, err = db.RecordMeterTransition(ctx, store.Command{Workspace: ws, Subject: "admin", Expected: 1, Key: uuid.NewString(), Fingerprint: "change", Today: "2027-01-01"}, domain.MeterTransition{Date: "2026-06-01", OldPoint: old.ID, NewPoints: []string{next.ID}}, map[string]int64{old.ID: old.Version, next.ID: next.Version}); err != nil {
		t.Fatal(err)
	}
	for token, session := range map[string]store.Session{"admin": {Subject: "admin", ACR: "2", CSRF: "csrf"}, "weak": {Subject: "admin", ACR: "1", CSRF: "csrf"}, "guest": {Subject: "guest", ACR: "1", CSRF: "csrf"}, "foreign": {Subject: "foreign", ACR: "2", CSRF: "csrf"}} {
		session.Expires = time.Now().Add(time.Hour)
		if err = db.SaveSession(ctx, token, session); err != nil {
			t.Fatal(err)
		}
	}
	server := (&Server{Store: db, Auth: &auth.Auth{Store: db, Origin: "http://localhost:8080"}}).Handler()
	call := func(method, path, token string, body any, keys ...string) *httptest.ResponseRecorder {
		t.Helper()
		var payload string
		if body != nil {
			b, _ := json.Marshal(body)
			payload = string(b)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(payload))
		r.AddCookie(&http.Cookie{Name: "meterya_session", Value: token})
		r.Header.Set("Origin", "http://localhost:8080")
		r.Header.Set("X-CSRF-Token", "csrf")
		commandKey := uuid.NewString()
		if len(keys) > 0 {
			commandKey = keys[0]
		}
		r.Header.Set("Idempotency-Key", commandKey)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	definition := map[string]any{"definition": map[string]any{"name": "House", "type": "SEQUENCE", "root_point": old.ID, "choices": map[string]string{}}, "expected_version": 0}
	endpoint := "/api/v1/workspaces/" + ws + "/virtual-points"
	for _, tc := range []struct {
		token  string
		status int
	}{{"weak", 403}, {"guest", 403}, {"foreign", 404}} {
		if w := call("POST", endpoint, tc.token, definition); w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	created := call("POST", endpoint, "admin", definition)
	if created.Code != 200 {
		t.Fatal(created.Body.String())
	}
	var v domain.VirtualPoint
	if err = json.Unmarshal(created.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	response := call("GET", "/api/v1/workspaces/"+ws+"/points?include_archived=true", "guest", nil)
	var points []struct {
		ID       string                 `json:"id"`
		Kind     string                 `json:"kind"`
		Virtual  *domain.VirtualInfo    `json:"virtual"`
		Readings []domain.Reading       `json:"readings"`
		Annual   []domain.AnnualSummary `json:"annual"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &points) != nil || len(points) != 3 || points[0].ID != v.ID || points[0].Kind != "VIRTUAL" || points[0].Virtual.Actions["edit"] || points[0].Virtual.Actions["record"] || len(points[0].Readings) != 0 {
		t.Fatal(response.Code, response.Body.String())
	}
	if points[0].Annual[0].Positions[0].Quantity != "30.000000" {
		t.Fatal(points[0])
	}
	export := "/api/v1/workspaces/" + ws + "/points/" + v.ID + "/reports/2026/download"
	report := map[string]any{"expected_version": v.Version, "calculation_version": domain.CalculationVersion, "format": "json", "content": "{}", "data_version": points[0].Virtual.DataVersion}
	saved := call("POST", export, "guest", report)
	if saved.Code != 201 {
		t.Fatal(saved.Code, saved.Body.String())
	}
	var download map[string]string
	json.Unmarshal(saved.Body.Bytes(), &download)
	if fetched := call("GET", download["download_url"], "guest", nil); fetched.Code != 200 {
		t.Fatal(fetched.Code, fetched.Body.String())
	}
	change := domain.ReadingChange{ID: next.Readings[1].ID, Date: next.Readings[1].Date, Value: "21"}
	if _, err = db.Execute(ctx, store.Command{Workspace: ws, Subject: "admin", PointID: next.ID, Expected: next.Version, Key: uuid.NewString(), Fingerprint: "correction", ReadingChange: &change, Today: "2027-01-01"}); err != nil {
		t.Fatal(err)
	}
	if stale := call("POST", export, "guest", report); stale.Code != 409 {
		t.Fatal("stale source export accepted", stale.Code, stale.Body.String())
	}
	if stale := call("GET", download["download_url"], "guest", nil); stale.Code != 409 {
		t.Fatal("cached source snapshot not checked", stale.Code, stale.Body.String())
	}
	if forbidden := call("POST", "/api/v1/workspaces/"+ws+"/points/"+v.ID+"/readings", "admin", map[string]any{"register_id": v.ID + "-IMPORT", "date": "2026-07-01", "value": "100", "expected_version": v.Version}); forbidden.Code == 200 || forbidden.Code == 201 {
		t.Fatal("virtual meter accepted manual reading")
	}
	deleteKey := uuid.NewString()
	for range 2 {
		removed := call("POST", endpoint+"/"+v.ID+"/remove", "admin", map[string]any{"expected_version": v.Version}, deleteKey)
		if removed.Code != 200 {
			t.Fatal("definition removal is not idempotent", removed.Code, removed.Body.String())
		}
	}

}

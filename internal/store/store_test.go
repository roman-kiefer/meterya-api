package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/domain"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL not set; PostgreSQL integration not executed")
	}
	testURL, err := url.Parse(raw)
	if err != nil || (testURL.Scheme != "postgres" && testURL.Scheme != "postgresql") {
		t.Fatal("TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := "meterya_test_" + uuid.New().String()[:8]
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE"); conn.Close(ctx) })
	query := testURL.Query()
	query.Set("search_path", schema)
	testURL.RawQuery = query.Encode()
	s, err := Open(ctx, testURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Pool.Close)
	return s
}

func TestReleaseBaselineRejectsDevelopmentDatabase(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	reopened, err := Open(ctx, s.Pool.Config().ConnString())
	if err != nil {
		t.Fatal("release database did not reopen", err)
	}
	reopened.Pool.Close()

	raw := os.Getenv("TEST_DATABASE_URL")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	schema := "meterya_legacy_" + uuid.New().String()[:8]
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = conn.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE")
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	if _, err = conn.Exec(ctx, "CREATE TABLE "+identifier+".goose_db_version (version_id integer)"); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(ctx, u.String()); err == nil || !strings.Contains(err.Error(), "unsupported development database") {
		t.Fatal("legacy database was not rejected", err)
	}
}

func TestCommandsAreAtomicIsolatedAndReplayable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Test", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Bootstrap(ctx, "Other", "other", "other@example.test")
	if err != nil {
		t.Fatal(err)
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: "Haus", Kind: "PHYSICAL",
		Meter: domain.Meter{ID: uuid.NewString(), Serial: "serial", Type: "ELECTRICITY",
			Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
	create := Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "create", NewPoint: &p}
	p, err = s.Execute(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	reading := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: "2026-01-01", Value: "12345678901234.123456", Actor: "admin", RecordedAt: time.Now().UTC()}
	command := Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "reading", Expected: 1, PointID: p.ID, Reading: &reading, Today: "2026-10-04"}
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		group.Go(func() { _, e := s.Execute(ctx, command); failures <- e })
	}
	group.Wait()
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	got, err := s.Point(ctx, ws, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || len(got.Readings) != 1 {
		t.Fatal("duplicate event or projection", got)
	}
	var exact string
	if err = s.Pool.QueryRow(ctx, "SELECT value::text FROM readings WHERE id=$1", reading.ID).Scan(&exact); err != nil || exact != reading.Value {
		t.Fatal(exact, err)
	}
	changed := command
	changed.Fingerprint = "changed"
	if _, err = s.Execute(ctx, changed); domain.Code(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(err)
	}
	stale := command
	stale.Key = uuid.NewString()
	if _, err = s.Execute(ctx, stale); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal(err)
	}
	foreign := command
	foreign.Workspace = other
	foreign.Subject = "other"
	foreign.Key = uuid.NewString()
	if _, err = s.Execute(ctx, foreign); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
	denied := create
	denied.Key = uuid.NewString()
	denied.Subject = "outsider"
	if _, err = s.Execute(ctx, denied); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE events SET actor='tampered'"); err == nil {
		t.Fatal("events mutable")
	}
	before, _ := json.Marshal(got)
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = s.Point(ctx, ws, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(got)
	if string(before) != string(after) {
		t.Fatal("replay differs")
	}
	if role, e := s.Role(ctx, ws, "admin"); e != nil || role != "ADMIN" {
		t.Fatal(role, e)
	}
}

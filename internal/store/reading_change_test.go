package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
	"time"
)

func TestReadingChangesPreserveEventsAndRebuildProjections(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Changes", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'user','user@example.test','USER')", ws); err != nil {
		t.Fatal(err)
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: "Meter", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "change", Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "create", NewPoint: &p})
	if err != nil {
		t.Fatal(err)
	}
	r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: "2026-01-01", Value: "10.000000", Actor: "user", RecordedAt: time.Now().UTC()}
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "user", PointID: p.ID, Expected: p.Version, Key: uuid.NewString(), Fingerprint: "record", Reading: &r, Today: "2026-10-07"})
	if err != nil {
		t.Fatal(err)
	}
	c := Command{Workspace: ws, Subject: "user", PointID: p.ID, Expected: p.Version, Key: uuid.NewString(), Fingerprint: "correct", ReadingChange: &domain.ReadingChange{ID: r.ID, Date: r.Date, Value: "11.000001"}, Today: "2026-10-07"}
	p, err = s.Execute(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, c); err != nil {
		t.Fatal("idempotent retry", err)
	}
	stale := c
	stale.Key = uuid.NewString()
	if _, err = s.Execute(ctx, stale); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal("stale", err)
	}
	before, _ := json.Marshal(p)
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Point(ctx, ws, p.ID)
	after, _ := json.Marshal(got)
	if err != nil || string(before) != string(after) {
		t.Fatal("correction replay differs", err)
	}
	// The manually inserted test user has no membership event and is removed
	// by replay. Use the event-backed administrator for the next command.
	c.Subject = "admin"
	c.Expected = p.Version
	c.Key = uuid.NewString()
	c.Fingerprint = "retract"
	c.ReadingChange = &domain.ReadingChange{ID: r.ID, Retract: true}
	p, err = s.Execute(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = s.Point(ctx, ws, p.ID)
	if err != nil || len(got.Readings) != 0 || got.Version != p.Version {
		t.Fatal("retraction replay", err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE aggregate_id=$1 AND event_type IN ('ReadingRecorded','ReadingCorrected','ReadingRetracted')", p.ID).Scan(&count); err != nil || count != 3 {
		t.Fatal("original events lost", count, err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM readings WHERE point_id=$1", p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("projection not removed", count, err)
	}
}

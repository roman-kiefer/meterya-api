package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
	"time"
)

func TestPurgeRemovesDataAndCannotBeUndoneByReplayOrCreateRetry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Haushalt", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Bootstrap(ctx, "Anderer Workspace", "other", "other@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'user','user@example.test','USER')", ws); err != nil {
		t.Fatal(err)
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: "SENSITIVE-METER", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "SENSITIVE-SERIAL", Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
	create := Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "create", NewPoint: &p}
	p, err = s.Execute(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ date, value string }{{"2026-01-01", "10.000000"}, {"2026-01-02", "20.000000"}} {
		r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: item.date, Value: item.value, Actor: "admin", RecordedAt: time.Now().UTC()}
		p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: item.date, PointID: p.ID, Expected: p.Version, Reading: &r, Today: "2026-10-05"})
		if err != nil {
			t.Fatal(err)
		}
	}
	name, serial := "Corrected", "CORRECT-SERIAL"
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "details", PointID: p.ID, Expected: p.Version, Name: &name, Serial: &serial})
	if err != nil || len(p.Readings) != 2 || p.Meter.Serial != serial {
		t.Fatal(p, err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'user','user@example.test','USER') ON CONFLICT DO NOTHING", ws); err != nil {
		t.Fatal(err)
	}
	replayed, err := s.Point(ctx, ws, p.ID)
	if err != nil || replayed.Meter.Serial != serial || replayed.Name != name {
		t.Fatal(replayed, err)
	}
	command := Command{Workspace: ws, Subject: "user", Key: uuid.NewString(), Fingerprint: "purge", PointID: p.ID, Expected: p.Version}
	if err = s.PurgePoint(ctx, command, p.ID); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	command.Subject = "admin"
	if err = s.PurgePoint(ctx, command, uuid.NewString()); domain.Code(err) != "VALIDATION_FAILED" {
		t.Fatal(err)
	}
	command.Expected--
	if err = s.PurgePoint(ctx, command, p.ID); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal(err)
	}
	command.Expected++
	foreign := command
	foreign.Workspace, foreign.Subject = other, "other"
	if err = s.PurgePoint(ctx, foreign, p.ID); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
	// Ordinary deletion remains forbidden, including after the purge transaction.
	if _, err = s.Pool.Exec(ctx, "DELETE FROM events WHERE aggregate_id=$1", p.ID); err == nil {
		t.Fatal("unprotected events")
	}
	if err = s.PurgePoint(ctx, command, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.PurgePoint(ctx, command, p.ID); err != nil {
		t.Fatal("repeat failed", err)
	}
	if _, err = s.Point(ctx, ws, p.ID); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
	for _, table := range []string{"points", "readings", "annual_summaries"} {
		var count int
		column := "point_id"
		if table == "points" {
			column = "id"
		}
		if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+column+"=$1", p.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE aggregate_id=$1 AND (event_type<>'PointPurged' OR payload<>'{}'::jsonb)", p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("original events remain", count, err)
	}
	var receipt []byte
	if err = s.Pool.QueryRow(ctx, "SELECT response FROM idempotency WHERE subject=$1 AND key=$2", create.Subject, create.Key).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	var got struct{ Purged bool }
	if err = json.Unmarshal(receipt, &got); err != nil || !got.Purged {
		t.Fatal(string(receipt), err)
	}
	if _, err = s.Execute(ctx, create); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal("create retry resurrected point", err)
	}
	if _, err = s.Pool.Exec(ctx, "DELETE FROM events WHERE aggregate_id=$1", p.ID); err == nil {
		t.Fatal("purge scope leaked beyond transaction")
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if points, err := s.Points(ctx, ws); err != nil || len(points) != 0 {
		t.Fatal("replay resurrected point", points, err)
	}
	// Its number is now available for a fresh meter with a fresh command.
	p.ID, p.Version, p.Readings = uuid.NewString(), 0, []domain.Reading{}
	if _, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "fresh", NewPoint: &p}); err != nil {
		t.Fatal(err)
	}
}

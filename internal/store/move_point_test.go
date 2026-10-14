package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
)

func TestMovePointPreservesDataPermissionsReplayAndPurge(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source, err := s.Bootstrap(ctx, "Source", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.Bootstrap(ctx, "Target", "other-admin", "other@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'mod','mod@example.test','MODERATOR'),($1,'user','user@example.test','USER'),($2,'user','user@example.test','USER')", source, target); err != nil {
		t.Fatal(err)
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: source, Name: "Meter", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "MOVE-1", Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
	create := Command{Workspace: source, Subject: "admin", Key: uuid.NewString(), Fingerprint: "create", NewPoint: &p}
	p, err = s.Execute(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	for _, reading := range []struct{ date, value string }{{"2026-01-01", "0.000000"}, {"2026-01-03", "2.123456"}} {
		r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: reading.date, Value: reading.value, Actor: "admin", RecordedAt: time.Now().UTC()}
		p, err = s.Execute(ctx, Command{Workspace: source, Subject: "admin", Key: uuid.NewString(), Fingerprint: reading.date, PointID: p.ID, Expected: p.Version, Reading: &r, Today: "2026-10-12"})
		if err != nil {
			t.Fatal(err)
		}
	}
	tariffs := domain.Tariffs{BasePrices: []domain.BasePrice{{PricePeriod: domain.PricePeriod{From: "2026-01-01", Cents: 0}, Period: "YEAR"}}, UnitPrices: []domain.UnitPrice{{PricePeriod: domain.PricePeriod{From: "2026-01-01", Cents: 3590}, Direction: "IMPORT"}}}
	p, err = s.Execute(ctx, Command{Workspace: source, Subject: "admin", Key: uuid.NewString(), Fingerprint: "tariffs", PointID: p.ID, Expected: p.Version, Tariffs: &tariffs})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(p)
	command := Command{Workspace: source, Subject: "user", Key: uuid.NewString(), Fingerprint: "move", PointID: p.ID, Expected: p.Version}
	if _, err = s.MovePoint(ctx, command, target); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	command.Subject = "mod"
	if _, err = s.MovePoint(ctx, command, target); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	command.Subject = "admin"
	if _, err = s.MovePoint(ctx, command, source); domain.Code(err) != "VALIDATION_FAILED" {
		t.Fatal(err)
	}
	if _, err = s.MovePoint(ctx, command, uuid.NewString()); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
	command.Expected--
	if _, err = s.MovePoint(ctx, command, target); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal(err)
	}
	command.Expected++
	if _, err = s.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'mod','mod@example.test','MODERATOR')", target); err != nil {
		t.Fatal(err)
	}
	command.Subject = "mod"
	moved, err := s.MovePoint(ctx, command, target)
	if err != nil || moved.WorkspaceID != target || moved.Version != p.Version+1 {
		t.Fatal(moved, err)
	}
	expected := p
	expected.WorkspaceID, expected.Version = target, p.Version+1
	got, _ := json.Marshal(moved)
	want, _ := json.Marshal(expected)
	if string(got) != string(want) {
		t.Fatal("data changed during move")
	}
	if repeated, err := s.MovePoint(ctx, command, target); err != nil || repeated.Version != moved.Version {
		t.Fatal(repeated, err)
	}
	conflicting := command
	conflicting.Fingerprint = "different"
	if _, err = s.MovePoint(ctx, conflicting, target); domain.Code(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(err)
	}
	if _, err = s.Point(ctx, source, p.ID); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, create); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal("old receipt exposed moved point", err)
	}
	summaries, err := s.AnnualSummaries(ctx, target, p.ID)
	if err != nil || len(summaries) != 1 || summaries[0].PointVersion != moved.Version || summaries[0].Positions[0].Quantity != "2.123456" {
		t.Fatal(summaries, err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE aggregate_id=$1 AND event_type='PointWorkspaceMoved'", p.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := s.Point(ctx, target, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(rebuilt)
	if string(body) != string(want) {
		t.Fatalf("move lost during replay; original %s", before)
	}
	if _, err = s.Point(ctx, source, p.ID); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
	if err = s.PurgePoint(ctx, Command{Workspace: target, Subject: "admin", Key: uuid.NewString(), Fingerprint: "purge", PointID: p.ID, Expected: moved.Version}, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE aggregate_id=$1 AND event_type<>'PointPurged'", p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("old workspace history not purged", count, err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Point(ctx, target, p.ID); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal("purged moved point resurrected", err)
	}
}

func TestMovePointRejectsCollisionsAndLinkedMetersAtomically(t *testing.T) {
	for _, blocked := range []string{"serial", "transition-old", "transition-new", "heat"} {
		t.Run(blocked, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			source, err := s.Bootstrap(ctx, "Source", "admin", "admin@example.test")
			if err != nil {
				t.Fatal(err)
			}
			target, err := s.Bootstrap(ctx, "Target", "other", "other@example.test")
			if err != nil {
				t.Fatal(err)
			}
			p := domain.Point{ID: uuid.NewString(), WorkspaceID: source, Name: "Meter", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "shared", Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
			p, err = s.Execute(ctx, Command{Workspace: source, Subject: "admin", Key: uuid.NewString(), Fingerprint: "create", NewPoint: &p})
			if err != nil {
				t.Fatal(err)
			}
			code := "POINT_LINKED"
			if blocked == "serial" {
				other := p
				other.ID, other.WorkspaceID = uuid.NewString(), target
				if _, err = s.Execute(ctx, Command{Workspace: target, Subject: "admin", Key: uuid.NewString(), Fingerprint: "other", NewPoint: &other}); err != nil {
					t.Fatal(err)
				}
				code = "SERIAL_ALREADY_ACTIVE"
			} else {
				kind := "MeterTransitionRecorded"
				payload := map[string]any{"old_point": p.ID, "new_points": []string{uuid.NewString()}}
				if blocked == "transition-new" {
					payload = map[string]any{"old_point": uuid.NewString(), "new_points": []string{p.ID}}
				}
				if blocked == "heat" {
					kind = "PowerToHeatConfigured"
					payload = map[string]any{"net_point": p.ID, "house_point": uuid.NewString()}
				}
				body, _ := json.Marshal(payload)
				if _, err = s.Pool.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,2,$2,$3,'admin',$4)", uuid.NewString(), source, kind, body); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.MovePoint(ctx, Command{Workspace: source, Subject: "admin", Key: uuid.NewString(), Fingerprint: "move", PointID: p.ID, Expected: p.Version}, target); domain.Code(err) != code {
				t.Fatal(err)
			}
			current, err := s.Point(ctx, source, p.ID)
			if err != nil || current.Version != p.Version {
				t.Fatal("failed move changed point", current, err)
			}
		})
	}
}

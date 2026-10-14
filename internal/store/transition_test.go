package store

import (
	"context"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
)

func TestMeterTransitionIsAtomicScopedAndReplayable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Home", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	var points []domain.Point
	for _, name := range []string{"Old", "Z1", "Z2"} {
		p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: name, Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: name, Type: "BIDIRECTIONAL", Registers: []domain.Register{{ID: uuid.NewString(), Unit: "kWh", Direction: "IMPORT"}, {ID: uuid.NewString(), Unit: "kWh", Direction: "EXPORT"}}}, Readings: []domain.Reading{}}
		p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: name, NewPoint: &p})
		if err != nil {
			t.Fatal(err)
		}
		for _, reg := range p.Meter.Registers {
			r := domain.Reading{ID: uuid.NewString(), RegisterID: reg.ID, Date: "2026-10-01", Value: "0.000000", Actor: "admin"}
			p, err = s.Execute(ctx, Command{Workspace: ws, PointID: p.ID, Subject: "admin", Expected: p.Version, Key: uuid.NewString(), Fingerprint: reg.ID, Reading: &r, Today: "2026-10-06"})
			if err != nil {
				t.Fatal(err)
			}
		}
		points = append(points, p)
	}
	v := domain.MeterTransition{Date: "2026-10-01", OldPoint: points[0].ID, NewPoints: []string{points[1].ID, points[2].ID}}
	versions := map[string]int64{}
	for _, p := range points {
		versions[p.ID] = p.Version
	}
	c := Command{Workspace: ws, Subject: "admin", Expected: 1, Key: uuid.NewString(), Fingerprint: "transition", Today: "2026-10-06"}
	denied := c
	denied.Subject = "outside"
	if _, err = s.RecordMeterTransition(ctx, denied, v, versions); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	stale := c
	stale.Expected = 0
	if _, err = s.RecordMeterTransition(ctx, stale, v, versions); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal(err)
	}
	old, _ := s.Point(ctx, ws, points[0].ID)
	if old.Archived {
		t.Fatal("failed transition archived old meter")
	}
	saved, err := s.RecordMeterTransition(ctx, c, v, versions)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.RecordMeterTransition(ctx, c, v, versions)
	if err != nil || retry.ID != saved.ID {
		t.Fatal(retry, err)
	}
	old, err = s.Point(ctx, ws, old.ID)
	if err != nil || !old.Archived || len(old.Readings) != 2 {
		t.Fatal(old, err)
	}
	// Restoring the archived old point does not permit readings after its end.
	flag := false
	old, err = s.Execute(ctx, Command{Workspace: ws, PointID: old.ID, Subject: "admin", Expected: old.Version, Key: uuid.NewString(), Fingerprint: "restore", Archived: &flag})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		p    domain.Point
		date string
	}{{old, "2026-10-02"}, {points[1], "2026-09-30"}} {
		r := domain.Reading{ID: uuid.NewString(), RegisterID: item.p.Meter.Registers[0].ID, Date: item.date, Value: "0.000000", Actor: "admin"}
		if _, err = s.Execute(ctx, Command{Workspace: ws, PointID: item.p.ID, Subject: "admin", Expected: item.p.Version, Key: uuid.NewString(), Fingerprint: item.date, Reading: &r, Today: "2026-10-06"}); domain.Code(err) != "READING_OUTSIDE_METER_PERIOD" {
			t.Fatal(err)
		}
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	history, err := s.MeterTransitions(ctx, ws)
	if err != nil || len(history) != 1 || history[0].ID != saved.ID {
		t.Fatal(history, err)
	}
	workspaces, err := s.Workspaces(ctx, "admin")
	if err != nil || workspaces[0].Version != 2 {
		t.Fatal(workspaces, err)
	}
}

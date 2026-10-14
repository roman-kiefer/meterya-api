package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
	"time"
)

func TestVirtualDefinitionsAreScopedReplayableAndKeepSources(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Home", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Bootstrap(ctx, "Other", "other", "other@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'mod','mod@example.test','MODERATOR'),($1,'user','user@example.test','USER'),($2,'foreign','foreign@example.test','MODERATOR')", ws, other); err != nil {
		t.Fatal(err)
	}
	makePoint := func(serial string, dates, values []string) domain.Point {
		t.Helper()
		p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: serial, Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: serial, Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
		p, e := s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: serial, NewPoint: &p})
		if e != nil {
			t.Fatal(e)
		}
		for i, date := range dates {
			r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: date, Value: values[i], Actor: "admin", RecordedAt: time.Now().UTC()}
			p, e = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", PointID: p.ID, Expected: p.Version, Key: uuid.NewString(), Fingerprint: date, Reading: &r, Today: "2027-01-01"})
			if e != nil {
				t.Fatal(e)
			}
		}
		return p
	}
	old := makePoint("old", []string{"2026-01-01", "2026-06-01"}, []string{"9000", "9010"})
	next := makePoint("new", []string{"2026-06-01", "2026-09-01"}, []string{"4", "24"})
	if _, err = s.RecordMeterTransition(ctx, Command{Workspace: ws, Subject: "admin", Expected: 1, Key: uuid.NewString(), Fingerprint: "transition", Today: "2027-01-01"}, domain.MeterTransition{Date: "2026-06-01", OldPoint: old.ID, NewPoints: []string{next.ID}}, map[string]int64{old.ID: old.Version, next.ID: next.Version}); err != nil {
		t.Fatal(err)
	}
	old, _ = s.Point(ctx, ws, old.ID)
	definition := domain.VirtualPoint{Name: "House", Type: "SEQUENCE", Root: old.ID}
	c := Command{Workspace: ws, Subject: "user", Key: uuid.NewString(), Fingerprint: "virtual"}
	if _, err = s.SaveVirtual(ctx, c, definition, false); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	c.Subject = "foreign"
	if _, err = s.SaveVirtual(ctx, c, definition, false); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	c.Subject = "mod"
	v, err := s.SaveVirtual(ctx, c, definition, false)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.SaveVirtual(ctx, c, definition, false)
	if err != nil || repeated.ID != v.ID {
		t.Fatal(repeated, err)
	}
	snap, err := s.Snapshot(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	reports, err := snap.VirtualReports()
	if err != nil || len(reports) != 1 || reports[0].Annual[0].Positions[0].Quantity != "30.000000" {
		t.Fatal(reports, err)
	}
	token := reports[0].Info.DataVersion
	if len(reports[0].Point.Readings) != 0 || reports[0].Point.Kind != "VIRTUAL" {
		t.Fatal("virtual readings persisted")
	}
	if _, err = s.SaveVirtual(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "duplicate"}, definition, false); domain.Code(err) != "VIRTUAL_ALREADY_EXISTS" {
		t.Fatal(err)
	}
	if err = s.PurgePoint(ctx, Command{Workspace: ws, Subject: "admin", PointID: old.ID, Expected: old.Version, Key: uuid.NewString(), Fingerprint: "purge"}, old.ID); domain.Code(err) != "VIRTUAL_SOURCE_IN_USE" {
		t.Fatal(err)
	}
	third := makePoint("third", []string{"2026-09-01", "2027-01-01"}, []string{"700", "705"})
	extra := makePoint("overlapping", []string{"2026-09-01", "2027-01-01"}, []string{"20", "1000"})
	change := domain.MeterTransition{Date: "2026-09-01", OldPoint: next.ID, NewPoints: []string{third.ID, extra.ID}}
	transition := Command{Workspace: ws, Subject: "admin", Expected: 2, Key: uuid.NewString(), Fingerprint: "branch", Today: "2027-01-01"}
	versions := map[string]int64{next.ID: next.Version, third.ID: third.Version, extra.ID: extra.Version}
	if _, err = s.RecordMeterTransition(ctx, transition, change, versions); domain.Code(err) != "VIRTUAL_CONTINUATION_REQUIRED" {
		t.Fatal(err)
	}
	change.Continuation = third.ID
	if _, err = s.RecordMeterTransition(ctx, transition, change, versions); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(ctx, ws)
	reports, err = snap.VirtualReports()
	if err != nil || reports[0].Point.ID != v.ID || reports[0].Info.DataVersion == token || reports[0].Annual[0].Positions[0].Quantity != "35.000000" {
		t.Fatal(reports, err)
	}
	before, _ := json.Marshal(reports[0])
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(ctx, ws)
	reports, err = snap.VirtualReports()
	after, _ := json.Marshal(reports[0])
	if err != nil || string(before) != string(after) {
		t.Fatal("virtual projection changed after rebuild", err)
	}
	backup, err := s.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Source definitions cannot silently change kind or root through an edit.
	c = Command{Workspace: ws, Subject: "admin", PointID: v.ID, Expected: v.Version, Key: uuid.NewString(), Fingerprint: "edit"}
	wrong := v
	wrong.Root = next.ID
	if _, err = s.SaveVirtual(ctx, c, wrong, false); domain.Code(err) != "VALIDATION_FAILED" {
		t.Fatal(err)
	}
	v.Name = "Renamed"
	v, err = s.SaveVirtual(ctx, c, v, false)
	if err != nil {
		t.Fatal(err)
	}
	c.Key, c.Fingerprint = uuid.NewString(), "archive"
	if _, err = s.SaveVirtual(ctx, c, v, false); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal(err)
	}
	c.Expected = v.Version
	v.Archived = true
	v, err = s.SaveVirtual(ctx, c, v, false)
	if err != nil {
		t.Fatal(err)
	}
	c.Expected, c.Key, c.Fingerprint = v.Version, uuid.NewString(), "remove"
	if _, err = s.SaveVirtual(ctx, c, v, true); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(ctx, ws)
	if len(snap.Definitions) != 0 || len(snap.Points) != 4 {
		t.Fatal("definition removal changed physical sources")
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(ctx, ws)
	if len(snap.Definitions) != 0 {
		t.Fatal("removed definition resurrected")
	}
	if _, err = s.RestoreBackup(ctx, backup, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RestoreBackup(ctx, backup, false); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(ctx, ws)
	if len(snap.Definitions) != 1 || snap.Definitions[0].ID != v.ID {
		t.Fatal("virtual definitions missing from backup")
	}
}

func TestHeatContinuationSurvivesRemovalAndRecreationOfVirtualDefinition(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Heat", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	makePoint := func(serial string, dates, imports, exports []string) domain.Point {
		t.Helper()
		p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: serial, Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: serial, Type: "BIDIRECTIONAL", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}, {ID: uuid.NewString(), Direction: "EXPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
		p, e := s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: serial, NewPoint: &p})
		if e != nil {
			t.Fatal(e)
		}
		for i, date := range dates {
			for j, value := range []string{imports[i], exports[i]} {
				r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[j].ID, Date: date, Value: value, Actor: "admin", RecordedAt: time.Now().UTC()}
				p, e = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", PointID: p.ID, Expected: p.Version, Key: uuid.NewString(), Fingerprint: date + value, Reading: &r, Today: "2027-01-01"})
				if e != nil {
					t.Fatal(e)
				}
			}
		}
		return p
	}
	net := makePoint("net", []string{"2026-01-01", "2026-06-01"}, []string{"0", "20"}, []string{"0", "10"})
	house := makePoint("house", []string{"2026-01-01", "2026-06-01", "2027-01-01"}, []string{"0", "10", "20"}, []string{"0", "15", "40"})
	cfg, err := s.ConfigureHeat(ctx, Command{Workspace: ws, Subject: "admin", Expected: 1, Key: uuid.NewString(), Fingerprint: "heat", Today: "2027-01-01"}, domain.PowerToHeat{Name: "WP", From: "2026-01-01", NetPoint: net.ID, HousePoint: house.ID}, map[string]int64{net.ID: net.Version, house.ID: house.Version})
	if err != nil {
		t.Fatal(err)
	}
	good := makePoint("good", []string{"2026-06-01", "2027-01-01"}, []string{"0", "30"}, []string{"0", "20"})
	overlap := makePoint("overlap", []string{"2026-06-01", "2027-01-01"}, []string{"0", "300"}, []string{"0", "200"})
	if _, err = s.RecordMeterTransition(ctx, Command{Workspace: ws, Subject: "admin", Expected: 2, Key: uuid.NewString(), Fingerprint: "branch", Today: "2027-01-01"}, domain.MeterTransition{Date: "2026-06-01", OldPoint: net.ID, NewPoints: []string{good.ID, overlap.ID}}, map[string]int64{net.ID: net.Version, good.ID: good.Version, overlap.ID: overlap.Version}); err != nil {
		t.Fatal(err)
	}
	definition := domain.VirtualPoint{Name: "WP virtual", Type: "HEAT", HeatID: cfg.ID, Choices: map[string]string{net.ID: good.ID}}
	v, err := s.SaveVirtual(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "virtual"}, definition, false)
	if err != nil {
		t.Fatal(err)
	}
	assertHeat := func() {
		t.Helper()
		reports, e := s.HeatReports(ctx, ws)
		if e != nil || len(reports) != 1 || len(reports[0].Flags) > 0 || len(reports[0].Sources) != 3 {
			t.Fatal(reports, e)
		}
		var total string
		for _, y := range reports[0].Yearly {
			if y.Year == 2026 && y.TotalTotals.Total != nil {
				total = *y.TotalTotals.Total
			}
		}
		if total != "40.000000" {
			t.Fatal(total)
		}
	}
	assertHeat()
	if _, err = s.SaveVirtual(ctx, Command{Workspace: ws, Subject: "admin", PointID: v.ID, Expected: v.Version, Key: uuid.NewString(), Fingerprint: "remove"}, v, true); err != nil {
		t.Fatal(err)
	}
	assertHeat()
	definition.Choices = nil
	recreated, err := s.SaveVirtual(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "recreate"}, definition, false)
	if err != nil || recreated.Choices[net.ID] != good.ID {
		t.Fatal(recreated, err)
	}
	assertHeat()
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	assertHeat()
}

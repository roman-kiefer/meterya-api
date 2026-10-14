package store

import (
	"context"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
)

func TestHeatConfigurationScopeReplayAndFreshSources(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Heat", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	var points []domain.Point
	for _, name := range []string{"Z1", "Z2"} {
		p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: name, Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: name, Type: "BIDIRECTIONAL", Registers: []domain.Register{{ID: uuid.NewString(), Unit: "kWh", Direction: "IMPORT"}, {ID: uuid.NewString(), Unit: "kWh", Direction: "EXPORT"}}}}
		p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: name, NewPoint: &p})
		if err != nil {
			t.Fatal(err)
		}
		for _, reg := range p.Meter.Registers {
			r := domain.Reading{ID: uuid.NewString(), RegisterID: reg.ID, Date: "2026-10-01", Value: "0", Actor: "admin"}
			p, err = s.Execute(ctx, Command{Workspace: ws, PointID: p.ID, Subject: "admin", Expected: p.Version, Key: uuid.NewString(), Fingerprint: reg.ID, Reading: &r, Today: "2026-10-06"})
			if err != nil {
				t.Fatal(err)
			}
		}
		points = append(points, p)
	}
	v := domain.PowerToHeat{Name: "Wärmepumpe", From: "2026-10-01", NetPoint: points[0].ID, HousePoint: points[1].ID}
	versions := map[string]int64{points[0].ID: points[0].Version, points[1].ID: points[1].Version}
	c := Command{Workspace: ws, Subject: "admin", Expected: 1, Key: uuid.NewString(), Fingerprint: "heat", Today: "2026-10-06"}
	bad := c
	bad.Subject = "outside"
	if _, err = s.ConfigureHeat(ctx, bad, v, versions); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	bad = c
	bad.Expected = 0
	if _, err = s.ConfigureHeat(ctx, bad, v, versions); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal(err)
	}
	stale := map[string]int64{points[0].ID: 0, points[1].ID: points[1].Version}
	if _, err = s.ConfigureHeat(ctx, c, v, stale); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal(err)
	}
	saved, err := s.ConfigureHeat(ctx, c, v, versions)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.ConfigureHeat(ctx, c, v, versions)
	if err != nil || retry.ID != saved.ID {
		t.Fatal(retry, err)
	}
	reports, err := s.HeatReports(ctx, ws)
	if err != nil || len(reports) != 1 || len(reports[0].TotalPeriods) != 0 {
		t.Fatal(reports, err)
	}
	// Adding readings changes the report without changing configuration or tariffs.
	for i, p := range points {
		for j, reg := range p.Meter.Registers {
			values := [2][2]string{{"100", "10"}, {"60", "30"}}
			r := domain.Reading{ID: uuid.NewString(), RegisterID: reg.ID, Date: "2026-10-06", Value: values[i][j], Actor: "admin"}
			p, err = s.Execute(ctx, Command{Workspace: ws, PointID: p.ID, Subject: "admin", Expected: p.Version, Key: uuid.NewString(), Fingerprint: reg.ID + "end", Reading: &r, Today: "2026-10-06"})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	reports, err = s.HeatReports(ctx, ws)
	if err != nil || len(reports) != 1 || len(reports[0].TotalPeriods) != 1 {
		t.Fatal(reports, err)
	}
	if *reports[0].TotalPeriods[0].Total != "60.000000" {
		t.Fatal(reports)
	}
	workspaces, err := s.Workspaces(ctx, "admin")
	if err != nil || workspaces[0].Version != 2 {
		t.Fatal(workspaces, err)
	}
	prices := domain.Tariffs{BasePrices: []domain.BasePrice{{PricePeriod: domain.PricePeriod{From: "2026-10-01", Cents: 3100}, Period: "MONTH"}}, UnitPrices: []domain.UnitPrice{{PricePeriod: domain.PricePeriod{From: "2026-10-01", Cents: 30000}, Direction: "IMPORT"}}}
	source, err := s.Point(ctx, ws, points[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	tc := Command{Workspace: ws, PointID: source.ID, Subject: "admin", Expected: source.Version, Key: uuid.NewString(), Fingerprint: "z1-tariff", Tariffs: &prices}
	source, err = s.Execute(ctx, tc)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	reports, err = s.HeatReports(ctx, ws)
	if err != nil || len(reports) != 1 || *reports[0].Annual[0].NetCents != "1700" {
		t.Fatal(reports, err)
	}
	prices.UnitPrices[0].Cents = 40000
	tc.Expected = source.Version
	tc.Key = uuid.NewString()
	tc.Fingerprint = "changed-z1-tariff"
	if _, err = s.Execute(ctx, tc); err != nil {
		t.Fatal(err)
	}
	reports, err = s.HeatReports(ctx, ws)
	if err != nil || *reports[0].Annual[0].NetCents != "2100" {
		t.Fatal(reports, err)
	}
	other, err := s.Bootstrap(ctx, "Other", "other", "other@example.test")
	if err != nil {
		t.Fatal(err)
	}
	reports, err = s.HeatReports(ctx, other)
	if err != nil || len(reports) != 0 {
		t.Fatal(reports, err)
	}
}

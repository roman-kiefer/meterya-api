package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
	"time"
)

func TestTariffProjectionIsAtomicRestrictedAndReplayable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Billing", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'user','user@example.test','USER')", ws); err != nil {
		t.Fatal(err)
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: "Haus", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "billing", Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "create", NewPoint: &p})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ date, value string }{{"2026-01-01", "0.000000"}, {"2026-01-03", "1.000000"}} {
		r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: item.date, Value: item.value, Actor: "admin", RecordedAt: time.Now().UTC()}
		p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: item.date, PointID: p.ID, Expected: p.Version, Reading: &r, Today: "2026-10-05"})
		if err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.AnnualSummaries(ctx, ws, p.ID)
	if err != nil || len(before) != 1 || before[0].NetCents != nil {
		t.Fatal(before, err)
	}
	if len(before[0].Months) != 12 || before[0].Months[0].Positions[0].Quantity != "1.000000" || before[0].Months[0].NetCents != nil {
		t.Fatal("monthly quantity without tariff", before)
	}
	tariffs := domain.Tariffs{BasePrices: []domain.BasePrice{{PricePeriod: domain.PricePeriod{From: "2026-01-01", Cents: 0}, Period: "YEAR"}}, UnitPrices: []domain.UnitPrice{{PricePeriod: domain.PricePeriod{From: "2026-01-01", Until: "2026-01-02", Cents: 500}, Direction: "IMPORT"}, {PricePeriod: domain.PricePeriod{From: "2026-01-02", Cents: 5000}, Direction: "IMPORT"}}}
	command := Command{Workspace: ws, Subject: "user", Key: uuid.NewString(), Fingerprint: "tariff", PointID: p.ID, Expected: p.Version, Tariffs: &tariffs}
	if _, err = s.Execute(ctx, command); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	command.Subject = "admin"
	p, err = s.Execute(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	// The repeated request returns its response without another event/version.
	repeated, err := s.Execute(ctx, command)
	if err != nil || repeated.Version != p.Version {
		t.Fatal(err)
	}
	got, err := s.AnnualSummaries(ctx, ws, p.ID)
	if err != nil || got[0].PointVersion != p.Version || *got[0].NetCents != "3" || !got[0].Estimated {
		t.Fatal(got, err)
	}
	if len(got[0].Months) != 12 || got[0].Months[0].NetCents == nil || *got[0].Months[0].NetCents != "3" {
		t.Fatal("tariff change must update monthly cache", got)
	}
	stale := command
	stale.Key = uuid.NewString()
	if _, err = s.Execute(ctx, stale); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal(err)
	}
	invalid := tariffs
	invalid.UnitPrices = append(append([]domain.UnitPrice{}, tariffs.UnitPrices...), tariffs.UnitPrices[0])
	bad := command
	bad.Key = uuid.NewString()
	bad.Expected = p.Version
	bad.Tariffs = &invalid
	if _, err = s.Execute(ctx, bad); domain.Code(err) != "TARIFF_OVERLAP" {
		t.Fatal(err)
	}
	var eventCount int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE aggregate_id=$1", p.ID).Scan(&eventCount); err != nil || int64(eventCount) != p.Version {
		t.Fatal(eventCount, err)
	}
	// USER may still insert a retroactive reading; it changes the cached costs.
	r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: "2026-01-02", Value: "0.250000", Actor: "user", RecordedAt: time.Now().UTC()}
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "user", Key: uuid.NewString(), Fingerprint: "retroactive", PointID: p.ID, Expected: p.Version, Reading: &r, Today: "2026-10-05"})
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.AnnualSummaries(ctx, ws, p.ID)
	if err != nil || *got[0].NetCents != "4" || got[0].Estimated {
		t.Fatal(got, err)
	}
	if hidden, err := s.AnnualSummaries(ctx, uuid.NewString(), p.ID); err != nil || len(hidden) != 0 {
		t.Fatal("workspace leak", err)
	}
	serialized, _ := json.Marshal(got)
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := s.AnnualSummaries(ctx, ws, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(replayed)
	if string(serialized) != string(after) {
		t.Fatal("replay differs", string(after))
	}
	// Existing points also receive projections on startup after the migration.
	if _, err = s.Pool.Exec(ctx, "DELETE FROM annual_summaries"); err != nil {
		t.Fatal(err)
	}
	if err = s.warmAnnualProjections(ctx); err != nil {
		t.Fatal(err)
	}
	backfilled, err := s.AnnualSummaries(ctx, ws, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ = json.Marshal(backfilled)
	if string(serialized) != string(after) {
		t.Fatal("backfill differs")
	}
}

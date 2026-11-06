package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
	"time"
)

func TestAdvancePaymentScheduleIsRestrictedReplayableAndPurgeable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Advance", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'user','user@example.test','USER')", ws); err != nil {
		t.Fatal(err)
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: "Haus", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "advance", Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
	if p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "create", NewPoint: &p}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ date, value string }{{"2026-01-01", "0.000000"}, {"2027-01-01", "3000.000000"}} {
		r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: item.date, Value: item.value, Actor: "admin", RecordedAt: time.Now().UTC()}
		if p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: item.date, PointID: p.ID, Expected: p.Version, Reading: &r, Today: "2027-01-05"}); err != nil {
			t.Fatal(err)
		}
	}
	tariffs := domain.Tariffs{BasePrices: []domain.BasePrice{{PricePeriod: domain.PricePeriod{From: "2026-01-01", Cents: 150000}, Period: "YEAR"}}, UnitPrices: []domain.UnitPrice{{PricePeriod: domain.PricePeriod{From: "2026-01-01", Cents: 0}, Direction: "IMPORT"}}}
	if p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "tariffs", PointID: p.ID, Expected: p.Version, Tariffs: &tariffs}); err != nil {
		t.Fatal(err)
	}
	schedule := []domain.AdvancePayment{{From: "2026-08-15", Cents: 11500}, {From: "2026-01-01", Cents: 12356}, {From: "2026-05-01", Cents: 13265}}
	command := Command{Workspace: ws, Subject: "user", Key: uuid.NewString(), Fingerprint: "advance", PointID: p.ID, Expected: p.Version, AdvancePayments: &schedule}
	if _, err = s.Execute(ctx, command); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	command.Subject = "admin"
	if p, err = s.Execute(ctx, command); err != nil {
		t.Fatal(err)
	}
	if len(p.AdvancePayments) != 3 || p.AdvancePayments[0].From != "2026-01-01" || p.AdvancePayments[2].From != "2026-08-15" {
		t.Fatal("schedule must be stored sorted", p.AdvancePayments)
	}
	if repeated, err := s.Execute(ctx, command); err != nil || repeated.Version != p.Version {
		t.Fatal("idempotent retry", err)
	}
	got, err := s.AnnualSummaries(ctx, ws, p.ID)
	// The closing reading on 2027-01-01 also yields an uncovered year 2027.
	if err != nil || len(got) != 2 || got[1].Year != 2026 {
		t.Fatal(got, err)
	}
	year := got[1]
	if year.AdvancePaymentCents == nil || *year.AdvancePaymentCents != "147516" || year.EstimatedBalanceCents == nil || *year.EstimatedBalanceCents != "2484" {
		t.Fatal("2026 estimate", year.AdvancePaymentCents, year.EstimatedBalanceCents)
	}
	if *year.Months[7].AdvancePaymentCents != "12297" {
		t.Fatal("monthly projection", year.Months[7])
	}
	if got[0].AdvancePaymentCents == nil || *got[0].AdvancePaymentCents != "0" || got[0].EstimatedBalanceCents != nil {
		t.Fatal("uncovered 2027 has neither payments nor a balance", got[0])
	}
	duplicate := []domain.AdvancePayment{{From: "2026-01-01", Cents: 1}, {From: "2026-01-01", Cents: 2}}
	bad := Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "duplicate", PointID: p.ID, Expected: p.Version, AdvancePayments: &duplicate}
	if _, err = s.Execute(ctx, bad); domain.Code(err) != "ADVANCE_PAYMENT_DUPLICATE" {
		t.Fatal(err)
	}
	stale := command
	stale.Key = uuid.NewString()
	if _, err = s.Execute(ctx, stale); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal(err)
	}
	water := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: "Wasser", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "water", Type: "WATER", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "CONSUMPTION", Unit: "m3"}}}, Readings: []domain.Reading{}}
	if water, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "water", NewPoint: &water}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "water-advance", PointID: water.ID, Expected: water.Version, AdvancePayments: &schedule}); domain.Code(err) != "ADVANCE_PAYMENTS_UNSUPPORTED" {
		t.Fatal(err)
	}
	var eventType string
	if err = s.Pool.QueryRow(ctx, "SELECT event_type FROM events WHERE aggregate_id=$1 ORDER BY sequence DESC LIMIT 1", p.ID).Scan(&eventType); err != nil || eventType != "AdvancePaymentScheduleSaved" {
		t.Fatal(eventType, err)
	}
	serialized, _ := json.Marshal(got)
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := s.AnnualSummaries(ctx, ws, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := json.Marshal(replayed); string(after) != string(serialized) {
		t.Fatal("replay differs", string(after))
	}
	rebuilt, err := s.Point(ctx, ws, p.ID)
	if err != nil || len(rebuilt.AdvancePayments) != 3 || rebuilt.Version != p.Version {
		t.Fatal("replayed point", rebuilt, err)
	}
	// The authorized purge must be able to remove the new event type as well.
	if err = s.PurgePoint(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "purge", PointID: p.ID, Expected: p.Version}, p.ID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM events WHERE aggregate_id=$1 AND event_type<>'PointPurged'", p.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal(remaining, err)
	}
}

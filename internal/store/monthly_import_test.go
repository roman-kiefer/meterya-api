package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
)

func monthlyFixture() MonthlyImport {
	return MonthlyImport{PointName: "Meter", Serial: "001", Tariffs: domain.Tariffs{BasePrices: []domain.BasePrice{{PricePeriod: domain.PricePeriod{From: "2024-01-01", Cents: 1390}, Period: "MONTH"}}, UnitPrices: []domain.UnitPrice{{PricePeriod: domain.PricePeriod{From: "2024-01-01", Cents: 35900}, Direction: "IMPORT"}}}, Statements: []domain.MonthlyStatement{{Year: 2024, OpeningDate: "2023-12-31", OpeningValue: "992", Months: []domain.StatementMonth{{Date: "2024-01-31", Value: "1162", UnitCents: 35900, BaseCents: 1390, PaymentCents: 7600}}}}}
}

func TestMonthlyImportRejectsFutureWithoutDatabase(t *testing.T) {
	input := monthlyFixture()
	input.Statements[0].Year = 2026
	input.Statements[0].OpeningDate = "2025-12-31"
	input.Statements[0].Months[0].Date = "2026-01-31"
	if _, err := input.Readings("reg", "admin", "2026-01-01"); domain.Code(err) != "INVALID_DATE" {
		t.Fatal(err)
	}
}

func TestMonthlyImportIsAtomicIdempotentReplayableAndPurgeable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Home", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: "Meter", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "001", Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Unit: "kWh", Direction: "IMPORT"}}}, Readings: []domain.Reading{}}
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "new", NewPoint: &p})
	if err != nil {
		t.Fatal(err)
	}
	input := monthlyFixture()
	c := Command{Workspace: ws, PointID: p.ID, Subject: "admin", Expected: p.Version, Key: uuid.NewString(), Fingerprint: "import", Today: "2026-10-06"}
	denied := c
	denied.Subject = "outsider"
	if _, err = s.ImportMonthly(ctx, denied, input); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	p, err = s.ImportMonthly(ctx, c, input)
	if err != nil || len(p.Readings) != 2 || len(p.MonthlyStatements) != 1 {
		t.Fatal(p, err)
	}
	repeated, err := s.ImportMonthly(ctx, c, input)
	if err != nil || repeated.Version != p.Version || len(repeated.Readings) != 2 {
		t.Fatal(repeated, err)
	}
	before, _ := json.Marshal(p)
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := s.Point(ctx, ws, p.ID)
	after, _ := json.Marshal(replayed)
	if err != nil || string(before) != string(after) {
		t.Fatal("replay differs", err)
	}
	// A second package with a conflicting reading must not append events or change payments.
	input.Statements[0].Months[0].Value = "1163"
	c.Expected = p.Version
	c.Key = uuid.NewString()
	c.Fingerprint = "changed"
	if _, err = s.ImportMonthly(ctx, c, input); domain.Code(err) != "READING_ALREADY_EXISTS" {
		t.Fatal(err)
	}
	unchanged, err := s.Point(ctx, ws, p.ID)
	current, _ := json.Marshal(unchanged)
	if err != nil || string(before) != string(current) {
		t.Fatal("failed import changed point", err)
	}
	if err = s.PurgePoint(ctx, Command{Workspace: ws, PointID: p.ID, Subject: "admin", Expected: p.Version, Key: uuid.NewString(), Fingerprint: "purge"}, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Point(ctx, ws, p.ID); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
}

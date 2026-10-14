package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
)

func bidirectionalMonthlyFixture() MonthlyImport {
	i := monthlyFixture()
	i.Tariffs.UnitPrices = append(i.Tariffs.UnitPrices, domain.UnitPrice{PricePeriod: domain.PricePeriod{From: "2024-01-01", Cents: 8200}, Direction: "EXPORT"})
	i.Statements = append(i.Statements, domain.MonthlyStatement{Year: 2024, Direction: "EXPORT", OpeningDate: "2023-12-31", OpeningValue: "992", Months: []domain.StatementMonth{{Date: "2024-01-31", Value: "1004", UnitCents: 8200, PaymentCents: 2100}}})
	return i
}

func TestMonthlyImportKeepsSameDatesAndValuesSeparateByRegister(t *testing.T) {
	i := bidirectionalMonthlyFixture()
	registers := []domain.Register{{ID: "import", Unit: "kWh", Direction: "IMPORT"}, {ID: "export", Unit: "kWh", Direction: "EXPORT"}}
	r, err := i.ReadingsFor(registers, "admin", "2026-10-06")
	if err != nil || len(r) != 4 {
		t.Fatal(r, err)
	}
	counts := map[string]int{}
	for _, reading := range r {
		counts[reading.RegisterID]++
	}
	if counts["import"] != 2 || counts["export"] != 2 {
		t.Fatal(counts)
	}
	if _, err = i.Readings("import", "admin", "2026-10-06"); domain.Code(err) != "REGISTER_NOT_FOUND" {
		t.Fatal(err)
	}
	i.Statements = append(i.Statements, i.Statements[1])
	if _, err = i.ReadingsFor(registers, "admin", "2026-10-06"); domain.Code(err) != "CONFLICT" {
		t.Fatal(err)
	}
}

func TestBidirectionalMonthlyImportIsAtomicIdempotentAndReplayable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Test", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: "Meter", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "001", Type: "BIDIRECTIONAL", Registers: []domain.Register{{ID: uuid.NewString(), Unit: "kWh", Direction: "IMPORT"}, {ID: uuid.NewString(), Unit: "kWh", Direction: "EXPORT"}}}, Readings: []domain.Reading{}}
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "create", NewPoint: &p})
	if err != nil {
		t.Fatal(err)
	}
	// The explicit development option replaces an existing target tariff plan.
	testTariffs := monthlyFixture().Tariffs
	p, err = s.Execute(ctx, Command{Workspace: ws, PointID: p.ID, Subject: "admin", Key: uuid.NewString(), Fingerprint: "tariff", Expected: p.Version, Tariffs: &testTariffs})
	if err != nil {
		t.Fatal(err)
	}
	c := Command{Workspace: ws, PointID: p.ID, Subject: "admin", Key: uuid.NewString(), Fingerprint: "import", Expected: p.Version, Today: "2026-10-06"}
	i := bidirectionalMonthlyFixture()
	if _, err = s.ImportMonthly(ctx, c, i); domain.Code(err) != "TARIFF_OVERLAP" {
		t.Fatal(err)
	}
	unchanged, err := s.Point(ctx, ws, p.ID)
	if err != nil || len(unchanged.Readings) != 0 {
		t.Fatal("partial import", unchanged, err)
	}
	c.ReplaceImportTariffs = true
	p, err = s.ImportMonthly(ctx, c, i)
	if err != nil || len(p.Readings) != 4 || len(p.MonthlyStatements) != 2 {
		t.Fatal(p, err)
	}
	if i.Statements[0].RegisterID != "" {
		t.Fatal("caller input mutated")
	}
	bills, err := p.MonthlyBills()
	if err != nil || len(bills) != 2 || bills[1].Direction != "EXPORT" || bills[1].RegisterID != p.Meter.Registers[1].ID || bills[1].BalanceCents != "-2002" {
		t.Fatal(bills, err)
	}
	before, _ := json.Marshal(p)
	repeated, err := s.ImportMonthly(ctx, c, i)
	after, _ := json.Marshal(repeated)
	if err != nil || string(before) != string(after) {
		t.Fatal("retry changed data", err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := s.Point(ctx, ws, p.ID)
	after, _ = json.Marshal(replayed)
	if err != nil || string(before) != string(after) {
		t.Fatal("replay differs", err)
	}
}

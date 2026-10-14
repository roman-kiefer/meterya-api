package domain

import "testing"

func billingPoint() Point {
	return Point{Version: 3, Meter: Meter{Registers: []Register{{ID: "i", Direction: "IMPORT", Unit: "kWh"}, {ID: "e", Direction: "EXPORT", Unit: "kWh"}}}}
}
func read(register, date, value string) Reading {
	normalized, _ := Decimal(value)
	return Reading{RegisterID: register, Date: date, Value: normalized}
}
func summaryFor(t *testing.T, p Point, year int) AnnualSummary {
	t.Helper()
	summaries, err := p.AnnualSummaries()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range summaries {
		if s.Year == year {
			return s
		}
	}
	t.Fatal("missing year")
	return AnnualSummary{}
}
func requireCents(t *testing.T, value *string, want string) {
	t.Helper()
	if value == nil || *value != want {
		t.Fatalf("cents %v, want %s", value, want)
	}
}

func TestAnnualBillingLeapYearAndExport(t *testing.T) {
	p := billingPoint()
	p.Readings = []Reading{read("i", "2024-01-01", "0"), read("i", "2025-01-01", "3660"), read("e", "2024-01-01", "0"), read("e", "2025-01-01", "366")}
	p.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2024-01-01", Cents: 12000}, Period: "YEAR"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2024-01-01", Cents: 31000}, Direction: "IMPORT"}, {PricePeriod: PricePeriod{From: "2024-01-01", Cents: 8200}, Direction: "EXPORT"}}}
	s := summaryFor(t, p, 2024)
	if !s.Complete || s.Estimated || len(s.Warnings) != 0 {
		t.Fatal(s)
	}
	requireCents(t, s.BaseCents, "12000")
	requireCents(t, s.Positions[0].Cents, "113460")
	requireCents(t, s.Positions[1].Cents, "3001")
	requireCents(t, s.NetCents, "122459")
	if s.Positions[0].Quantity != "3660.000000" || s.PointVersion != 3 {
		t.Fatal(s)
	}
}
func TestMonthlyBaseUsesActualCalendarDays(t *testing.T) {
	p := billingPoint()
	p.Meter.Registers = p.Meter.Registers[:1]
	p.Readings = []Reading{read("i", "2024-02-28", "0"), read("i", "2024-03-01", "2")}
	p.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2024-02-01", Cents: 1000}, Period: "MONTH"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2024-02-01", Cents: 0}, Direction: "IMPORT"}}}
	s := summaryFor(t, p, 2024)
	requireCents(t, s.BaseCents, "69")
	requireCents(t, s.Positions[0].Cents, "0")
	if s.Complete || s.Estimated || s.Positions[0].Until != "2024-03-01" {
		t.Fatal(s)
	}
}
func TestTariffChangeInterpolationAndAnnualRounding(t *testing.T) {
	p := billingPoint()
	p.Meter.Registers = p.Meter.Registers[:1]
	p.Readings = []Reading{read("i", "2026-01-01", "0"), read("i", "2026-01-03", "1")}
	p.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2026-01-01", Cents: 0}, Period: "YEAR"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2026-01-01", Until: "2026-01-02", Cents: 500}, Direction: "IMPORT"}, {PricePeriod: PricePeriod{From: "2026-01-02", Cents: 500}, Direction: "IMPORT"}}}
	s := summaryFor(t, p, 2026)
	requireCents(t, s.Positions[0].Cents, "1") // 0.25 + 0.25 cent: round once.
	if !s.Estimated || s.Complete {
		t.Fatal(s)
	}
	// A retroactive real reading changes allocation across the tariff boundary.
	p.Tariffs.UnitPrices[1].Cents = 5000
	s = summaryFor(t, p, 2026)
	requireCents(t, s.Positions[0].Cents, "3")
	p.Readings = append(p.Readings, read("i", "2026-01-02", "0.25"))
	s = summaryFor(t, p, 2026)
	requireCents(t, s.Positions[0].Cents, "4")
	if s.Estimated {
		t.Fatal("all price boundaries have real readings", s)
	}
}
func TestYearBoundaryAndMissingTariffAreNotExtrapolated(t *testing.T) {
	p := billingPoint()
	p.Meter.Registers = p.Meter.Registers[:1]
	p.Readings = []Reading{read("i", "2025-12-30", "99999999999998.999998"), read("i", "2026-01-03", "99999999999999.999999")}
	p.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2025-12-30", Cents: 36500}, Period: "YEAR"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2026-01-02", Cents: 1000}, Direction: "IMPORT"}}}
	a, b := summaryFor(t, p, 2025), summaryFor(t, p, 2026)
	if a.Positions[0].Quantity != "0.500001" || b.Positions[0].Quantity != "0.500001" {
		t.Fatal(a, b)
	}
	if a.Positions[0].Cents != nil || a.Complete || !b.Estimated {
		t.Fatal(a, b)
	}
	requireCents(t, a.BaseCents, "200")
	requireCents(t, b.BaseCents, "200")
	requireCents(t, b.Positions[0].Cents, "0")
	if b.Positions[0].From != "2026-01-01" || b.Positions[0].Until != "2026-01-03" {
		t.Fatal(b)
	}
}
func TestNoPricesAreUnknownNotZero(t *testing.T) {
	p := billingPoint()
	p.Readings = []Reading{read("i", "2026-01-01", "0"), read("i", "2026-01-02", "1")}
	s := summaryFor(t, p, 2026)
	if s.NetCents != nil || s.BaseCents != nil || s.Positions[0].Cents != nil || s.Complete {
		t.Fatal(s)
	}
	if s.Positions[1].Quantity != "0.000000" || s.Positions[1].Complete {
		t.Fatal(s)
	}
	p.Readings = p.Readings[:1]
	s = summaryFor(t, p, 2026)
	if s.Positions[0].From != "" || s.NetCents != nil {
		t.Fatal(s)
	}
}

func TestExportCanProduceNegativeNetCosts(t *testing.T) {
	p := billingPoint()
	p.Readings = []Reading{read("i", "2026-01-01", "0"), read("i", "2026-01-02", "0"), read("e", "2026-01-01", "0"), read("e", "2026-01-02", "100")}
	p.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2026-01-01", Cents: 0}, Period: "MONTH"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2026-01-01", Cents: 31000}, Direction: "IMPORT"}, {PricePeriod: PricePeriod{From: "2026-01-01", Cents: 8200}, Direction: "EXPORT"}}}
	s := summaryFor(t, p, 2026)
	requireCents(t, s.NetCents, "-820")
	requireCents(t, s.Positions[0].Cents, "0")
	requireCents(t, s.BaseCents, "0")
}

func TestWaterCostAndBaseChange(t *testing.T) {
	p := Point{Meter: Meter{Registers: []Register{{ID: "w", Direction: "CONSUMPTION", Unit: "m³"}}}, Readings: []Reading{read("w", "2026-01-01", "0"), read("w", "2027-01-01", "36.5")}}
	p.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2026-01-01", Until: "2026-02-01", Cents: 1000}, Period: "MONTH"}, {PricePeriod: PricePeriod{From: "2026-02-01", Cents: 24000}, Period: "YEAR"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2026-01-01", Cents: 200000}, Direction: "CONSUMPTION"}}}
	s := summaryFor(t, p, 2026)
	if !s.Complete || s.Estimated || s.Positions[0].Unit != "m³" {
		t.Fatal(s)
	}
	requireCents(t, s.Positions[0].Cents, "7300")
	requireCents(t, s.BaseCents, "22962")
	requireCents(t, s.NetCents, "30262")
}
func TestTariffValidationIndependentDirectionsAndExclusiveEnd(t *testing.T) {
	p := billingPoint()
	period := PricePeriod{From: "2026-01-01", Until: "2026-02-01", Cents: 8200}
	valid := Tariffs{UnitPrices: []UnitPrice{{PricePeriod: period, Direction: "IMPORT"}, {PricePeriod: period, Direction: "EXPORT"}, {PricePeriod: PricePeriod{From: "2026-02-01", Cents: 31000}, Direction: "IMPORT"}}}
	if err := p.ValidateTariffs(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.UnitPrices = append(append([]UnitPrice{}, valid.UnitPrices...), UnitPrice{PricePeriod: PricePeriod{From: "2026-01-31"}, Direction: "IMPORT"})
	if Code(p.ValidateTariffs(invalid)) != "TARIFF_OVERLAP" {
		t.Fatal("overlap accepted")
	}
	for _, period := range []PricePeriod{{From: "2026-02-30"}, {From: "2026-01-01", Until: "2026-01-01"}, {From: "2026-01-01", Cents: -1}, {From: "2026-01-01", Cents: 1_000_000_000_001}} {
		if Code(p.ValidateTariffs(Tariffs{UnitPrices: []UnitPrice{{PricePeriod: period, Direction: "IMPORT"}}})) != "INVALID_TARIFF" {
			t.Fatal(period)
		}
	}
	if Code(p.ValidateTariffs(Tariffs{UnitPrices: []UnitPrice{{PricePeriod: period, Direction: "CONSUMPTION"}}})) != "INVALID_TARIFF" {
		t.Fatal("unknown direction accepted")
	}
}

package domain

import "testing"

func TestCalculatedMonthsSplitLeapYearWithoutExtrapolation(t *testing.T) {
	p := billingPoint()
	p.Meter.Registers = p.Meter.Registers[:1]
	p.Readings = []Reading{read("i", "2024-02-28", "0"), read("i", "2024-03-02", "3")}
	p.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2024-01-01", Cents: 2900}, Period: "MONTH"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2024-01-01", Cents: 100000}, Direction: "IMPORT"}}}
	s := summaryFor(t, p, 2024)
	if len(s.Months) != 12 {
		t.Fatal(s.Months)
	}
	feb, mar := s.Months[1], s.Months[2]
	if feb.Positions[0].Quantity != "2.000000" || mar.Positions[0].Quantity != "1.000000" || !feb.Estimated || !mar.Estimated || feb.Complete {
		t.Fatal(feb, mar)
	}
	requireCents(t, feb.BaseCents, "200")
	requireCents(t, feb.Positions[0].Cents, "200")
	requireCents(t, mar.BaseCents, "94")
	if feb.Positions[0].From != "2024-02-28" || mar.Positions[0].Until != "2024-03-02" {
		t.Fatal(feb, mar)
	}
	if s.Months[0].NetCents != nil || s.Months[0].Positions[0].From != "" || s.Months[3].NetCents != nil {
		t.Fatal("unmeasured months must not be priced")
	}
}

func TestCalculatedMonthsRoundIndependentlyFromAnnualTotal(t *testing.T) {
	p := billingPoint()
	p.Meter.Registers = p.Meter.Registers[:1]
	p.Readings = []Reading{read("i", "2026-01-01", "0"), read("i", "2026-02-01", "1"), read("i", "2026-03-01", "2")}
	p.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2026-01-01", Cents: 0}, Period: "MONTH"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2026-01-01", Cents: 500}, Direction: "IMPORT"}}}
	s := summaryFor(t, p, 2026)
	requireCents(t, s.NetCents, "1")
	for _, m := range s.Months[:2] {
		requireCents(t, m.NetCents, "1")
		if !m.Complete || m.Estimated {
			t.Fatal(m)
		}
	}
}

func TestCalculatedMonthsPriceGapAndExportRemainSeparate(t *testing.T) {
	p := billingPoint()
	p.Readings = []Reading{read("i", "2026-01-01", "0"), read("i", "2026-02-01", "31"), read("e", "2026-01-01", "0"), read("e", "2026-02-01", "62")}
	p.Tariffs = Tariffs{UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2026-01-01", Cents: 8200}, Direction: "EXPORT"}}}
	s := summaryFor(t, p, 2026)
	m := s.Months[0]
	if m.Positions[0].Cents != nil || m.BaseCents != nil || m.Complete || len(m.Warnings) < 2 {
		t.Fatal(m)
	}
	requireCents(t, m.Positions[1].Cents, "508")
	requireCents(t, m.NetCents, "-508")
}

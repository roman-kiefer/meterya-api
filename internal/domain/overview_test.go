package domain

import (
	"encoding/json"
	"fmt"
	"math/big"
	"testing"
	"time"
)

func historyPosition(y, m int, q string, partial bool) AnnualPosition {
	start := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	if m != 1 {
		end = start.AddDate(0, 1, 0)
	}
	if partial {
		end = time.Date(y, 4, 1, 0, 0, 0, 0, time.UTC)
	}
	return AnnualPosition{RegisterID: "i", Direction: "IMPORT", Unit: "kWh", Quantity: q, From: start.Format(time.DateOnly), Until: end.Format(time.DateOnly), Complete: !partial}
}
func historyFixture() (Point, []AnnualSummary) {
	p := Point{Meter: Meter{Registers: []Register{{ID: "i", Direction: "IMPORT", Unit: "kWh"}, {ID: "e", Direction: "EXPORT", Unit: "kWh"}}}}
	all := []AnnualSummary{}
	for y, q := range map[int]string{2024: "1000", 2025: "1200", 2026: "180"} {
		all = append(all, AnnualSummary{Year: y, Positions: []AnnualPosition{historyPosition(y, 1, q, y == 2026)}, BaseCents: textPtr("50")})
	}
	for y, values := range map[int][]string{2025: {"100", "200", "90"}, 2026: {"50", "100", "30"}} {
		s := findYear(all, y)
		for i, q := range values {
			start := time.Date(y, time.Month(i+1), 1, 0, 0, 0, 0, time.UTC)
			end := start.AddDate(0, 1, 0)
			if y == 2026 && i == 2 {
				end = start.AddDate(0, 0, 9)
			}
			s.Months = append(s.Months, MonthlySummary{Month: i + 1, AnnualSummary: AnnualSummary{Year: y, Positions: []AnnualPosition{{RegisterID: "i", Direction: "IMPORT", Unit: "kWh", Quantity: q, From: start.Format(time.DateOnly), Until: end.Format(time.DateOnly)}}}})
		}
	}
	return p, all
}
func TestHistoryMatchedMonthsAndWindowTrend(t *testing.T) {
	p, all := historyFixture()
	c, r := change(all, "i", 2026)
	if c == nil || *c.Percent != "-50.00" || fmt.Sprint(c.Months) != "[1 2]" || c.Earlier != "300.000000" || c.Later != "150.000000" {
		t.Fatalf("matched months: %+v", c)
	}
	if r.Cmp(big.NewRat(-1, 2)) != 0 {
		t.Fatal(r)
	}
	var w HistoryWindow
	for _, v := range p.Histories(all) {
		if v.RegisterID == "i" && v.UntilYear == 2026 {
			w = v
		}
	}
	if w.TrendCount != 2 || *w.TrendPercent != "-15.00" || w.TrendDirection != -1 || *w.QuantityMean != "793.333333" || w.QuantityCount != 3 || w.CostMean != nil {
		t.Fatalf("window: %+v", w)
	}
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HistoryWindow
	if err = json.Unmarshal(b, &decoded); err != nil || *decoded.TrendPercent != "-15.00" {
		t.Fatalf("wire: %s %v", b, err)
	}
}
func TestHistoryMissingMonthZeroAndRegisterIsolation(t *testing.T) {
	p, all := historyFixture()
	findYear(all, 2025).Months = findYear(all, 2025).Months[:1]
	if c, _ := change(all, "i", 2026); c != nil {
		t.Fatalf("missing matching month: %+v", c)
	}
	if c, _ := change(all, "e", 2026); c != nil {
		t.Fatal(c)
	}
	_, all = historyFixture()
	for i := range findYear(all, 2025).Months {
		findYear(all, 2025).Months[i].Positions[0].Quantity = "0"
	}
	c, r := change(all, "i", 2026)
	if c == nil || c.Percent != nil || r != nil {
		t.Fatal(c, r)
	}
	for _, v := range p.Histories(all) {
		if v.RegisterID == "e" && (v.QuantityMean != nil || v.TrendPercent != nil) {
			t.Fatal(v)
		}
	}
}
func TestOverviewCostsBaseOnceAndTariffGaps(t *testing.T) {
	p, _ := historyFixture()
	s := AnnualSummary{Year: 2025, BaseCents: textPtr("50"), Positions: []AnnualPosition{{RegisterID: "i", Cents: textPtr("100")}, {RegisterID: "e", Cents: textPtr("20")}}}
	rows := p.Overview(&s, 0)
	if *rows[0].TotalCents != "150" || *rows[1].TotalCents != "20" {
		t.Fatal(rows)
	}
	s.Warnings = []string{"BASE_PRICE_GAP"}
	rows = p.Overview(&s, 0)
	if rows[0].TotalCents != nil || *rows[1].TotalCents != "20" {
		t.Fatal(rows)
	}
	s.Warnings = []string{"UNIT_PRICE_GAP"}
	if p.Overview(&s, 0)[1].TotalCents != nil {
		t.Fatal("priced gap")
	}
	s.Warnings = nil
	s.BaseCents = nil
	if p.Overview(&s, 0)[0].TotalCents != nil {
		t.Fatal("missing base")
	}
}
func TestOverviewClosingPriorityAndChangingRates(t *testing.T) {
	p, _ := historyFixture()
	p.Readings = []Reading{{RegisterID: "i", Date: "2026-02-01", Value: "25101"}, {RegisterID: "i", Date: "2026-01-31", Value: "25100"}}
	p.Tariffs.UnitPrices = []UnitPrice{{Direction: "IMPORT", PricePeriod: PricePeriod{From: "2026-01-01", Cents: 31500}}}
	s := AnnualSummary{Year: 2026}
	r := p.Overview(&s, 1)[0]
	if r.Closing == nil || r.Closing.Value != "25100" || r.Rate == nil || r.Rate.Cents != 31500 {
		t.Fatal(r)
	}
	p.Readings = []Reading{p.Readings[1], p.Readings[0]}
	if p.Overview(&s, 1)[0].Closing.Value != "25100" {
		t.Fatal("order dependent")
	}
	p.Tariffs.UnitPrices = []UnitPrice{{Direction: "IMPORT", PricePeriod: PricePeriod{From: "2026-01-01", Until: "2026-01-15", Cents: 30000}}, {Direction: "IMPORT", PricePeriod: PricePeriod{From: "2026-01-15", Cents: 32000}}}
	r = p.Overview(&s, 1)[0]
	if !r.ChangingRate || r.Rate != nil {
		t.Fatal(r)
	}
}
func TestHistoryExactLargeValuesAndRounding(t *testing.T) {
	p, all := historyFixture()
	all = all[:0]
	for y, q := range map[int]string{2025: "99999999999999.999999", 2026: "0.000001"} {
		pos := historyPosition(y, 1, q, false)
		pos.Cents = textPtr("101")
		all = append(all, AnnualSummary{Year: y, BaseCents: textPtr("0"), Positions: []AnnualPosition{pos}})
	}
	for _, w := range p.Histories(all) {
		if w.RegisterID == "i" && w.UntilYear == 2026 && (*w.QuantityMean != "50000000000000.000000" || *w.CostMean != "101") {
			t.Fatal(w)
		}
	}
	if *percent(big.NewRat(1, 100000000)) != "+0.00" || *percent(big.NewRat(-1, 100000000)) != "-0.00" {
		t.Fatal("lost sign")
	}
	// Rounding happens once after averaging exact ratios, not rounded percentages.
	if *percent(big.NewRat(1, 3)) != "+33.33" {
		t.Fatal("rounding")
	}
}

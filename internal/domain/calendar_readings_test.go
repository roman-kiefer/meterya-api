package domain

import (
	"reflect"
	"testing"
)

func TestCalendarBoundaryExactYearAndMonthEndPriority(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		p := billingPoint()
		p.Meter.Registers = p.Meter.Registers[:1]
		p.Readings = []Reading{read("i", "2024-12-31", "100"), read("i", "2025-01-01", "101"), read("i", "2025-01-31", "110"), read("i", "2025-02-01", "111"), read("i", "2025-12-31", "200"), read("i", "2026-01-01", "201")}
		if reverse {
			for i, j := 0, len(p.Readings)-1; i < j; i, j = i+1, j-1 {
				p.Readings[i], p.Readings[j] = p.Readings[j], p.Readings[i]
			}
		}
		original := append([]Reading(nil), p.Readings...)
		p.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2025-01-01", Cents: 0}, Period: "YEAR"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2025-01-01", Cents: 1000}, Direction: "IMPORT"}}}
		s := summaryFor(t, p, 2025)
		if s.Estimated || !s.Complete || s.Positions[0].Quantity != "100.000000" || s.Months[0].Positions[0].Quantity != "10.000000" {
			t.Fatal(s)
		}
		if !reflect.DeepEqual(original, p.Readings) {
			t.Fatal("original dates changed")
		}
	}
}

func TestCalendarBoundaryFirstOnlyAndLeapDay(t *testing.T) {
	p := billingPoint()
	p.Meter.Registers = p.Meter.Registers[:1]
	p.Readings = []Reading{read("i", "2024-01-01", "0"), read("i", "2024-02-01", "31"), read("i", "2024-02-29", "60"), read("i", "2025-01-01", "366")}
	s := summaryFor(t, p, 2024)
	if s.Estimated || s.Positions[0].Quantity != "366.000000" || s.Months[1].Positions[0].Quantity != "29.000000" {
		t.Fatal(s)
	}
	if ReadingBoundary("2025-02-28") != "2025-03-01" || ReadingBoundary("2024-02-28") != "2024-02-28" {
		t.Fatal("calendar error")
	}
}

func TestFirstDaySecondaryReadingCannotOverrideMonthEnd(t *testing.T) {
	p := billingPoint()
	p.Meter.Registers = p.Meter.Registers[:1]
	p.Readings = []Reading{read("i", "2026-01-01", "0"), read("i", "2026-01-31", "10"), read("i", "2026-03-01", "20")}
	for _, value := range []string{"9", "11", "30"} {
		if err := p.ValidateReading(read("i", "2026-02-01", value), "2026-10-07"); err != nil {
			t.Fatal(err)
		}
	}
	p.Readings = append(p.Readings, read("i", "2026-02-01", "30"))
	intervals, err := p.Consumption()
	if err != nil || len(intervals) != 2 || intervals[0].Quantity != "10.000000" {
		t.Fatal(intervals, err)
	}
	p.Readings = append(p.Readings, read("i", "2026-03-02", "24"))
	if err = p.ValidateReading(read("i", "2026-02-28", "25"), "2026-10-07"); Code(err) != "READING_DECREASES" {
		t.Fatal("effective neighbor validation", err)
	}
}

func TestHeatSourcesShareAliasedCalendarBoundary(t *testing.T) {
	net := heatSource("n", [][3]string{{"2026-10-01", "0", "0"}, {"2026-10-31", "100", "10"}, {"2026-11-01", "110", "11"}})
	house := heatSource("h", [][3]string{{"2026-10-01", "0", "0"}, {"2026-11-01", "40", "20"}})
	r, err := (PowerToHeat{From: "2026-10-01"}).Report(net, house)
	if err != nil || len(r.TotalPeriods) != 1 || r.TotalPeriods[0].Grid != "60.000000" || r.TotalPeriods[0].Until != "2026-11-01" {
		t.Fatal(r, err)
	}
}

package domain

import "testing"

func heatSource(id string, values [][3]string) Point {
	p := Point{ID: id, Kind: "PHYSICAL", Meter: Meter{Type: "BIDIRECTIONAL", Registers: []Register{{ID: id + "i", Unit: "kWh", Direction: "IMPORT"}, {ID: id + "e", Unit: "kWh", Direction: "EXPORT"}}}}
	for _, row := range values {
		for j, suffix := range []string{"i", "e"} {
			if row[j+1] != "" {
				p.Readings = append(p.Readings, Reading{RegisterID: id + suffix, Date: row[0], Value: row[j+1]})
			}
		}
	}
	return p
}

func TestHeatExactBalancesAndRealBoundaries(t *testing.T) {
	c := PowerToHeat{From: "2026-10-01"}
	net := heatSource("n", [][3]string{{"2026-10-01", "9999999999999.000001", "0"}, {"2026-10-03", "9999999999999.100002", ""}, {"2026-10-06", "9999999999999.300004", "0.100001"}})
	house := heatSource("h", [][3]string{{"2026-10-01", "0", "0"}, {"2026-10-03", "0.010001", ""}, {"2026-10-06", "0.100001", "0.300003"}})
	r, err := c.Report(net, house)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.GridPeriods) != 2 || len(r.TotalPeriods) != 1 || len(r.Flags) != 0 {
		t.Fatal(r)
	}
	v := r.TotalPeriods[0]
	if v.Grid != "0.200002" || *v.HousePV != "0.200002" || *v.Total != "0.400004" || !v.Valid || v.From != "2026-10-01" || v.Until != "2026-10-06" {
		t.Fatal(v)
	}
	// A missing export boundary must not be interpolated from other dates.
	house.Readings = house.Readings[:len(house.Readings)-1]
	r, err = c.Report(net, house)
	if err != nil || len(r.TotalPeriods) != 0 || len(r.GridPeriods) != 2 {
		t.Fatal(r, err)
	}
	// The initial readings alone do not claim zero consumption.
	r, err = c.Report(heatSource("n", [][3]string{{"2026-10-01", "0", "0"}}), heatSource("h", [][3]string{{"2026-10-01", "0", "0"}}))
	if err != nil || len(r.GridPeriods) != 0 || len(r.TotalPeriods) != 0 || len(r.Flags) != 2 {
		t.Fatal(r, err)
	}
}

func TestHeatRetainsNegativeBalance(t *testing.T) {
	n := heatSource("n", [][3]string{{"2026-10-01", "0", "0"}, {"2026-10-06", "10", "5"}})
	h := heatSource("h", [][3]string{{"2026-10-01", "0", "0"}, {"2026-10-06", "12", "3"}})
	r, err := (PowerToHeat{From: "2026-10-01"}).Report(n, h)
	if err != nil {
		t.Fatal(err)
	}
	v := r.TotalPeriods[0]
	if v.Valid || v.Grid != "-2.000000" || *v.HousePV != "-2.000000" || *v.Total != "-4.000000" || len(r.Flags) != 1 || r.Flags[0] != "NEGATIVE_BALANCE" {
		t.Fatal(r)
	}
}

func TestHeatCostsRoundOnceAndUseZ1Tariff(t *testing.T) {
	c := PowerToHeat{ID: "heat", From: "2026-10-01"}
	n := heatSource("n", [][3]string{{"2026-10-01", "0", "0"}, {"2026-10-02", "1", "0"}, {"2026-10-06", "2", "1"}})
	h := heatSource("h", [][3]string{{"2026-10-01", "0", "0"}, {"2026-10-02", "0", "5"}, {"2026-10-06", "0", "10"}})
	// Z1 supplies the full base price and its import rate; Z2 is ignored.
	n.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2026-10-01", Cents: 3100}, Period: "MONTH"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2026-10-01", Cents: 32900}, Direction: "IMPORT"}, {PricePeriod: PricePeriod{From: "2026-10-01", Cents: 8200}, Direction: "EXPORT"}}}
	h.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2026-10-01", Cents: 999999}, Period: "MONTH"}}}
	r, err := c.Report(n, h)
	if err != nil || len(r.Annual) != 1 {
		t.Fatal(r, err)
	}
	y := r.Annual[0]
	if *y.Positions[0].Cents != "66" || *y.BaseCents != "500" || *y.NetCents != "566" || y.Complete || y.Positions[0].Quantity != "2.000000" {
		t.Fatal(y)
	}
	// A tariff boundary within measured coverage is allocated and marked estimated.
	n.Tariffs.UnitPrices = []UnitPrice{{PricePeriod: PricePeriod{From: "2026-10-01", Until: "2026-10-04", Cents: 10000}, Direction: "IMPORT"}, {PricePeriod: PricePeriod{From: "2026-10-04", Cents: 20000}, Direction: "IMPORT"}}
	r, err = c.Report(n, h)
	if err != nil || *r.Annual[0].Positions[0].Cents != "25" || !r.Annual[0].Estimated {
		t.Fatal(r, err)
	}
	n.Tariffs = Tariffs{}
	r, err = c.Report(n, h)
	if err != nil || r.Annual[0].NetCents != nil || r.Annual[0].Positions[0].Cents != nil || r.Annual[0].Complete {
		t.Fatal(r, err)
	}
	n.Tariffs.UnitPrices = []UnitPrice{{PricePeriod: PricePeriod{From: "2026-10-01", Cents: 8200}, Direction: "EXPORT"}}
	r, err = c.Report(n, h)
	if err != nil || r.Annual[0].NetCents != nil || len(r.Tariffs.UnitPrices) != 0 {
		t.Fatal(r, err)
	}
}

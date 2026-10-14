package domain

import "testing"

func virtualTestPoint(id string, dates []string, imports, exports []string) Point {
	p := Point{ID: id, WorkspaceID: "ws", Name: id, Kind: "PHYSICAL", Version: 1, Meter: Meter{ID: id, Type: "BIDIRECTIONAL", Registers: []Register{{ID: id + "i", Direction: "IMPORT", Unit: "kWh"}, {ID: id + "e", Direction: "EXPORT", Unit: "kWh"}}}, Readings: []Reading{}, Tariffs: Tariffs{BasePrices: []BasePrice{}, UnitPrices: []UnitPrice{}}}
	for i, d := range dates {
		p.Readings = append(p.Readings, Reading{RegisterID: id + "i", Date: d, Value: imports[i]}, Reading{RegisterID: id + "e", Date: d, Value: exports[i]})
	}
	return p
}
func TestVirtualSequenceUsesDeltasTemporalTariffsAndOneBasePrice(t *testing.T) {
	old := virtualTestPoint("old", []string{"2026-01-01", "2026-06-15"}, []string{"10000", "10100"}, []string{"5000", "5020"})
	next := virtualTestPoint("new", []string{"2026-06-15", "2027-01-01"}, []string{"12", "212"}, []string{"3", "53"})
	for _, p := range []*Point{&old, &next} {
		p.Tariffs.BasePrices = []BasePrice{{PricePeriod: PricePeriod{From: "2026-01-01", Cents: 36500}, Period: "YEAR"}}
		p.Tariffs.UnitPrices = []UnitPrice{{PricePeriod: PricePeriod{From: "2026-01-01", Cents: 1000}, Direction: "IMPORT"}, {PricePeriod: PricePeriod{From: "2026-01-01", Cents: 500}, Direction: "EXPORT"}}
	}
	next.Tariffs.UnitPrices[0].Cents = 2000
	definition := VirtualPoint{ID: "logical", WorkspaceID: "ws", Name: "Haus", Type: "SEQUENCE", Root: old.ID, Version: 1}
	points := map[string]Point{old.ID: old, next.ID: next}
	changes := []MeterTransition{{OldPoint: old.ID, NewPoints: []string{next.ID}, Date: "2026-06-15"}}
	report, err := EvaluateVirtual(definition, points, changes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Point.Readings) != 0 || len(report.Info.Periods) != 4 || len(report.Annual) != 1 {
		t.Fatal("derived measurements saved or missing", report)
	}
	year := report.Annual[0]
	if !year.Complete || year.Positions[0].Quantity != "300.000000" || year.Positions[1].Quantity != "70.000000" || *year.BaseCents != "36500" || *year.NetCents != "36965" {
		t.Fatal(year)
	}
	if !year.Months[5].Estimated {
		t.Fatal("mid-month split must be marked estimated")
	}
	originalToken := report.Info.DataVersion
	next.Version++
	next.Readings[2].Value = "213"
	points[next.ID] = next
	changed, err := EvaluateVirtual(definition, points, changes, nil)
	if err != nil || changed.Info.DataVersion == originalToken || changed.Annual[0].Positions[0].Quantity != "301.000000" {
		t.Fatal(changed, err)
	}
	report.Enrich()
	history := report.Point.Histories(report.Annual)
	if !history[0].Rows[4].Complete {
		t.Fatal("continuous virtual year not complete", history)
	}
}
func TestVirtualSequenceBranchCycleAndSourceGaps(t *testing.T) {
	old := virtualTestPoint("old", []string{"2026-01-01", "2026-06-01"}, []string{"10", "20"}, []string{"0", "5"})
	next := virtualTestPoint("next", []string{"2026-06-01", "2027-01-01"}, []string{"0", "40"}, []string{"0", "4"})
	other := virtualTestPoint("other", []string{"2026-06-01", "2027-01-01"}, []string{"0", "200"}, []string{"0", "30"})
	points := map[string]Point{old.ID: old, next.ID: next, other.ID: other}
	changes := []MeterTransition{{OldPoint: old.ID, NewPoints: []string{next.ID, other.ID}, Date: "2026-06-01"}}
	v := VirtualPoint{ID: "virtual", WorkspaceID: "ws", Name: "Logical", Type: "SEQUENCE", Root: old.ID, Version: 1}
	if _, err := EvaluateVirtual(v, points, changes, nil); Code(err) != "VIRTUAL_AMBIGUOUS" {
		t.Fatal(err)
	}
	v.Choices = map[string]string{old.ID: next.ID}
	result, err := EvaluateVirtual(v, points, changes, nil)
	if err != nil || result.Annual[0].Positions[0].Quantity != "50.000000" {
		t.Fatal(result, err)
	}
	changes = append(changes, MeterTransition{OldPoint: next.ID, NewPoints: []string{old.ID}, Date: "2027-01-01"})
	if _, err := EvaluateVirtual(v, points, changes, nil); Code(err) != "VIRTUAL_CYCLE" {
		t.Fatal(err)
	}
	changes = changes[:1]
	old.Readings = old.Readings[:2]
	points[old.ID] = old
	partial, err := EvaluateVirtual(v, points, changes, nil)
	if err != nil || partial.Annual[0].Complete || partial.Annual[0].Positions[0].Quantity != "40.000000" {
		t.Fatal(partial, err)
	}
	next.Meter.Registers[0].Unit = "m³"
	points[next.ID] = next
	if _, err := EvaluateVirtual(v, points, changes, nil); Code(err) != "VIRTUAL_INCOMPATIBLE" {
		t.Fatal(err)
	}
}
func TestVirtualHeatShowsTotalButPricesOnlyGrid(t *testing.T) {
	net := virtualTestPoint("net", []string{"2026-01-01", "2026-02-01"}, []string{"100", "120"}, []string{"500", "550"})
	house := virtualTestPoint("house", []string{"2026-01-01", "2026-02-01"}, []string{"20", "30"}, []string{"700", "760"})
	net.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "2026-01-01", Cents: 1000}, Period: "MONTH"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "2026-01-01", Cents: 35900}, Direction: "IMPORT"}}}
	config := PowerToHeat{ID: "heat", Name: "WP", From: "2026-01-01", NetPoint: net.ID, HousePoint: house.ID, Version: 1}
	virtual := VirtualPoint{ID: "logical", WorkspaceID: "ws", Name: "WP", Type: "HEAT", HeatID: config.ID, Version: 1}
	result, err := EvaluateVirtual(virtual, map[string]Point{net.ID: net, house.ID: house}, nil, []PowerToHeat{config})
	if err != nil {
		t.Fatal(err)
	}
	year := result.Annual[0]
	if year.Positions[0].Quantity != "20.000000" || year.Positions[1].Quantity != "10.000000" || year.Positions[2].Quantity != "10.000000" || *year.NetCents != "1359" || *year.Positions[0].Cents != "359" {
		t.Fatal(year)
	}
	if year.Positions[1].Cents != nil || year.Positions[2].Cents != nil || len(result.Point.Tariffs.UnitPrices) != 0 {
		t.Fatal("total/PV consumption was repriced", year)
	}
	if result.Info.CostBasis != "GRID" || len(result.Info.Periods) != 3 || result.Info.Periods[1].HousePV == nil && result.Info.Periods[2].HousePV == nil {
		t.Fatal(result.Info)
	}
	house.Readings = []Reading{house.Readings[0], house.Readings[2]}
	partial, err := EvaluateVirtual(virtual, map[string]Point{net.ID: net, house.ID: house}, nil, []PowerToHeat{config})
	if err != nil || len(partial.Annual) == 0 || partial.Annual[0].Positions[0].From != "" || partial.Annual[0].Positions[1].Quantity != "10.000000" {
		t.Fatal("missing total fabricated", partial, err)
	}
}
func TestHeatContinuationUsesBothPhysicalChainsWithoutInventingBoundaries(t *testing.T) {
	net := virtualTestPoint("net", []string{"2026-01-01", "2026-01-15"}, []string{"100", "104"}, []string{"500", "505"})
	next := virtualTestPoint("next", []string{"2026-01-15", "2026-02-01"}, []string{"0", "6"}, []string{"0", "5"})
	house := virtualTestPoint("house", []string{"2026-01-01", "2026-02-01"}, []string{"20", "23"}, []string{"700", "714"})
	config := PowerToHeat{ID: "heat", From: "2026-01-01", NetPoint: net.ID, HousePoint: house.ID, Version: 1}
	result, sources, versions, err := ResolveHeat(config, nil, map[string]Point{net.ID: net, next.ID: next, house.ID: house}, []MeterTransition{{OldPoint: net.ID, NewPoints: []string{next.ID}, Date: "2026-01-15"}})
	if err != nil || len(sources) != 3 || len(versions) != 3 || len(result.TotalPeriods) != 1 || result.TotalPeriods[0].Grid != "7.000000" || *result.TotalPeriods[0].Total != "11.000000" {
		t.Fatal(result, err)
	}
}

func TestVirtualHistoryWindowExcludesAnEmptyCurrentYearFromMeans(t *testing.T) {
	p := Point{ID: "v", Meter: Meter{Registers: []Register{{ID: "i", Direction: "IMPORT", Unit: "kWh"}}}}
	position := AnnualPosition{RegisterID: "i", Direction: "IMPORT", Unit: "kWh", Quantity: "10.000000", From: "2025-01-01", Until: "2026-01-01"}
	windows := p.HistoriesThrough([]AnnualSummary{{Year: 2025, Positions: []AnnualPosition{position}}}, 2026)
	if len(windows) != 2 || windows[1].UntilYear != 2026 || windows[1].QuantityCount != 1 || *windows[1].QuantityMean != "10.000000" || windows[1].Rows[4].Position != nil {
		t.Fatal(windows)
	}
}

func TestUnavailableVirtualStillVersionsItsSourceSnapshot(t *testing.T) {
	v := VirtualPoint{ID: "v", Type: "SEQUENCE", Root: "root", Version: 1}
	points := map[string]Point{"root": {ID: "root", Version: 1}}
	before := UnavailableVirtual(v, "VIRTUAL_AMBIGUOUS", points, nil, nil)
	points["root"] = Point{ID: "root", Version: 2}
	after := UnavailableVirtual(v, "VIRTUAL_AMBIGUOUS", points, nil, nil)
	if before.Info.DataVersion == after.Info.DataVersion || len(after.Annual) != 0 {
		t.Fatal("unavailable report kept stale source version", before, after)
	}
}

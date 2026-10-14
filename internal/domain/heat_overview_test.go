package domain

import "testing"

func TestHeatYearTotalsSharesAndInvalidPeriods(t *testing.T) {
	r := HeatReport{
		GridPeriods:  []HeatPeriod{{From: "2026-10-01", Until: "2026-10-06", Grid: "26", Valid: true}, {From: "2026-10-06", Until: "2026-10-07", Grid: "4", Valid: true}, {From: "2026-10-07", Until: "2026-10-08", Grid: "-2", Valid: false}},
		TotalPeriods: []HeatPeriod{{From: "2026-10-01", Until: "2026-10-06", Grid: "26", HousePV: textPtr("6"), Total: textPtr("32"), Valid: true}, {From: "2026-10-06", Until: "2026-10-07", Grid: "4", HousePV: textPtr("2"), Total: textPtr("6"), Valid: true}},
	}
	y := r.Years()[0]
	if *y.GridTotals.Grid != "30.000000" || *y.TotalTotals.HousePV != "8.000000" || *y.TotalTotals.Total != "38.000000" || y.GridTotals.Days != 6 || *y.GridShare != 789 || *y.PVShare != 211 || !y.SameCoverage || y.HasGaps || *y.Until != "2026-10-07" || len(y.GridPeriods) != 3 || y.GridPeriods[0].Days != 5 {
		t.Fatalf("year: %+v", y)
	}
	r.TotalPeriods = r.TotalPeriods[1:]
	y = r.Years()[0]
	if y.SameCoverage || y.GridShare != nil || *y.TotalTotals.Total != "6.000000" {
		t.Fatal(y)
	}
}
func TestHeatGapsYearBoundaryAndMissingValues(t *testing.T) {
	r := HeatReport{GridPeriods: []HeatPeriod{{From: "2025-12-30", Until: "2026-01-02", Grid: "100", Valid: true}, {From: "2026-01-02", Until: "2026-01-03", Grid: "0.000001", Valid: true}, {From: "2026-02-01", Until: "2026-02-02", Grid: "99999999999999.999999", Valid: true}}}
	y := r.Years()[0]
	if y.Year != 2026 || !y.HasGaps || *y.GridTotals.Grid != "100000000000000.000000" || y.GridTotals.Days != 2 || y.TotalTotals.Total != nil || y.GridShare != nil {
		t.Fatal(y)
	}
	if r.Years()[1].GridTotals.Grid != nil {
		t.Fatal("split cross-year period")
	}
	for _, values := range [][3]string{{"0", "0", "0"}, {"-1", "2", "1"}, {"4", "2", "7"}} {
		g, p := heatShares(&values[0], &values[1], &values[2])
		if g != nil || p != nil {
			t.Fatal(values)
		}
	}
	g, p := heatShares(textPtr("99999999999999.999999"), textPtr("0"), textPtr("99999999999999.999999"))
	if *g != 1000 || *p != 0 {
		t.Fatal(g, p)
	}
}

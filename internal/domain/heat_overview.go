package domain

import (
	"fmt"
	"math/big"
	"slices"
	"sort"
)

type HeatTotals struct {
	Days    int64   `json:"days"`
	Grid    *string `json:"grid_kwh"`
	HousePV *string `json:"house_pv_kwh"`
	Total   *string `json:"total_kwh"`
}
type HeatYear struct {
	Year         int          `json:"year"`
	GridPeriods  []HeatPeriod `json:"grid_periods"`
	TotalPeriods []HeatPeriod `json:"total_periods"`
	GridTotals   HeatTotals   `json:"grid_totals"`
	TotalTotals  HeatTotals   `json:"total_totals"`
	From         *string      `json:"covered_from"`
	Until        *string      `json:"covered_until"`
	SameCoverage bool         `json:"same_coverage"`
	HasGaps      bool         `json:"has_gaps"`
	GridShare    *int64       `json:"grid_share_per_mille"`
	PVShare      *int64       `json:"pv_share_per_mille"`
}

func heatTotals(periods []HeatPeriod) HeatTotals {
	t := HeatTotals{}
	selected := []HeatPeriod{}
	for _, p := range periods {
		if p.Valid {
			selected = append(selected, p)
			t.Days += days(date(p.From), date(p.Until))
		}
	}
	sum := func(value func(HeatPeriod) *string) *string {
		if len(selected) == 0 {
			return nil
		}
		total := new(big.Rat)
		for _, p := range selected {
			s := value(p)
			if s == nil {
				return nil
			}
			v := exact(*s)
			if v == nil {
				return nil
			}
			total.Add(total, v)
		}
		return textPtr(total.FloatString(6))
	}
	t.Grid = sum(func(p HeatPeriod) *string { return &p.Grid })
	t.HousePV = sum(func(p HeatPeriod) *string { return p.HousePV })
	t.Total = sum(func(p HeatPeriod) *string { return p.Total })
	return t
}
func heatCoverage(periods []HeatPeriod) []string {
	selected := []HeatPeriod{}
	for _, p := range periods {
		if p.Valid {
			selected = append(selected, p)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].From < selected[j].From })
	ranges := [][2]string{}
	for _, p := range selected {
		i := len(ranges) - 1
		if i >= 0 && p.From <= ranges[i][1] {
			if p.Until > ranges[i][1] {
				ranges[i][1] = p.Until
			}
		} else {
			ranges = append(ranges, [2]string{p.From, p.Until})
		}
	}
	result := []string{}
	for _, r := range ranges {
		result = append(result, r[0]+"/"+r[1])
	}
	return result
}
func heatShares(grid, pv, total *string) (*int64, *int64) {
	if grid == nil || pv == nil || total == nil {
		return nil, nil
	}
	g, p, t := exact(*grid), exact(*pv), exact(*total)
	if g == nil || p == nil || t == nil || g.Sign() < 0 || p.Sign() < 0 || t.Sign() <= 0 || new(big.Rat).Add(g, p).Cmp(t) != 0 {
		return nil, nil
	}
	share := rounded(new(big.Rat).Mul(new(big.Rat).Quo(g, t), big.NewRat(1000, 1))).Int64()
	other := 1000 - share
	return &share, &other
}

// Years never split unsynchronised periods or extrapolate missing boundaries.
// Invalid rows remain visible, but do not enter accounting totals or shares.
func (r HeatReport) Years() []HeatYear {
	years := map[int]bool{}
	for _, ps := range [][]HeatPeriod{r.GridPeriods, r.TotalPeriods} {
		for _, p := range ps {
			for y := date(p.From).Year(); y <= date(p.Until).Year(); y++ {
				years[y] = true
			}
		}
	}
	result := []HeatYear{}
	for y := range years {
		row := HeatYear{Year: y, GridPeriods: []HeatPeriod{}, TotalPeriods: []HeatPeriod{}}
		selectPeriods := func(ps []HeatPeriod) []HeatPeriod {
			out := []HeatPeriod{}
			for _, p := range ps {
				if p.From >= fmt.Sprintf("%04d-01-01", y) && p.Until <= fmt.Sprintf("%04d-01-01", y+1) {
					p.Days = days(date(p.From), date(p.Until))
					out = append(out, p)
				}
			}
			return out
		}
		row.GridPeriods = selectPeriods(r.GridPeriods)
		row.TotalPeriods = selectPeriods(r.TotalPeriods)
		row.GridTotals = heatTotals(row.GridPeriods)
		row.TotalTotals = heatTotals(row.TotalPeriods)
		a, b := heatCoverage(row.GridPeriods), heatCoverage(row.TotalPeriods)
		row.SameCoverage = slices.Equal(a, b)
		row.HasGaps = len(a) > 1 || len(b) > 1
		for _, ps := range [][]HeatPeriod{row.GridPeriods, row.TotalPeriods} {
			for _, p := range ps {
				if !p.Valid {
					continue
				}
				if row.From == nil || p.From < *row.From {
					row.From = textPtr(p.From)
				}
				if row.Until == nil || p.Until > *row.Until {
					row.Until = textPtr(p.Until)
				}
			}
		}
		if row.SameCoverage {
			row.GridShare, row.PVShare = heatShares(row.GridTotals.Grid, row.TotalTotals.HousePV, row.TotalTotals.Total)
		}
		result = append(result, row)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Year > result[j].Year })
	return result
}

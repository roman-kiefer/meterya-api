package domain

import (
	"fmt"
	"math/big"
	"slices"
	"time"
)

const OverviewVersion = 1

// RegisterOverview adds display-ready accounting values to existing summaries.
// Quantities remain decimal strings and monetary amounts remain integer cents.
type RegisterOverview struct {
	RegisterID   string     `json:"register_id"`
	TotalCents   *string    `json:"total_cents"`
	Closing      *Reading   `json:"closing"`
	Rate         *UnitPrice `json:"rate"`
	ChangingRate bool       `json:"changing_rate"`
}
type YearChange struct {
	Earlier   string  `json:"earlier_quantity"`
	Later     string  `json:"later_quantity"`
	Percent   *string `json:"percent"`
	Months    []int   `json:"months"`
	Estimated bool    `json:"estimated"`
}
type HistoryRow struct {
	Year      int             `json:"year"`
	Position  *AnnualPosition `json:"position"`
	CostCents *string         `json:"cost_cents"`
	Complete  bool            `json:"complete_year"`
	Change    *YearChange     `json:"change"`
}
type HistoryWindow struct {
	RegisterID     string       `json:"register_id"`
	FromYear       int          `json:"from_year"`
	UntilYear      int          `json:"until_year"`
	Rows           []HistoryRow `json:"rows"`
	QuantityMean   *string      `json:"quantity_mean"`
	CostMean       *string      `json:"cost_mean_cents"`
	QuantityCount  int          `json:"quantity_count"`
	CostCount      int          `json:"cost_count"`
	TrendPercent   *string      `json:"trend_percent"`
	TrendDirection int          `json:"trend_direction"`
	TrendCount     int          `json:"trend_count"`
}

func exact(s string) *big.Rat {
	v, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil
	}
	return v
}
func textPtr(s string) *string { return &s }
func covered(p *AnnualPosition, from, until string) bool {
	return p != nil && !p.HasGaps && p.From != "" && p.Until != "" && p.From <= from && p.Until >= until
}
func findPosition(s *AnnualSummary, id string) *AnnualPosition {
	if s == nil {
		return nil
	}
	for i := range s.Positions {
		if s.Positions[i].RegisterID == id {
			return &s.Positions[i]
		}
	}
	return nil
}
func findYear(all []AnnualSummary, y int) *AnnualSummary {
	for i := range all {
		if all[i].Year == y {
			return &all[i]
		}
	}
	return nil
}
func monthPosition(s *AnnualSummary, id string, m int) *AnnualPosition {
	if s != nil {
		for i := range s.Months {
			if s.Months[i].Month == m {
				return findPosition(&s.Months[i].AnnualSummary, id)
			}
		}
	}
	return nil
}
func fullYear(p *AnnualPosition, y int) bool {
	return covered(p, fmt.Sprintf("%04d-01-01", y), fmt.Sprintf("%04d-01-01", y+1))
}
func percent(ratio *big.Rat) *string {
	v := new(big.Rat).Mul(ratio, big.NewRat(10000, 1))
	n := rounded(v)
	a := new(big.Int).Abs(n)
	whole, fraction := new(big.Int), new(big.Int)
	whole.QuoRem(a, big.NewInt(100), fraction)
	sign := ""
	if ratio.Sign() < 0 {
		sign = "-"
	} else if ratio.Sign() > 0 {
		sign = "+"
	}
	return textPtr(fmt.Sprintf("%s%s.%02d", sign, whole.String(), fraction.Int64()))
}
func change(all []AnnualSummary, id string, y int) (*YearChange, *big.Rat) {
	now, before := findYear(all, y), findYear(all, y-1)
	a, b := findPosition(before, id), findPosition(now, id)
	if a == nil || b == nil || a.From == "" || b.From == "" || a.Unit != b.Unit || a.Direction != b.Direction {
		return nil, nil
	}
	earlier, later := new(big.Rat), new(big.Rat)
	months := []int{}
	estimated := false
	if fullYear(a, y-1) && fullYear(b, y) {
		earlier, later = exact(a.Quantity), exact(b.Quantity)
		estimated = a.Estimated || b.Estimated
	} else {
		if fullYear(b, y) {
			return nil, nil
		}
		for m := 1; m <= 12; m++ {
			start := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
			end := start.AddDate(0, 1, 0)
			n := monthPosition(now, id, m)
			if !covered(n, start.Format(time.DateOnly), end.Format(time.DateOnly)) {
				continue
			}
			p := monthPosition(before, id, m)
			oldStart := time.Date(y-1, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
			if !covered(p, oldStart.Format(time.DateOnly), oldStart.AddDate(0, 1, 0).Format(time.DateOnly)) || p.Unit != n.Unit || p.Direction != n.Direction {
				return nil, nil
			}
			av, bv := exact(p.Quantity), exact(n.Quantity)
			if av == nil || bv == nil {
				return nil, nil
			}
			earlier.Add(earlier, av)
			later.Add(later, bv)
			months = append(months, m)
			estimated = estimated || p.Estimated || n.Estimated
		}
		if len(months) == 0 {
			return nil, nil
		}
	}
	if earlier == nil || later == nil || earlier.Sign() < 0 || later.Sign() < 0 {
		return nil, nil
	}
	result := &YearChange{Earlier: earlier.FloatString(6), Later: later.FloatString(6), Months: months, Estimated: estimated}
	if earlier.Sign() == 0 {
		return result, nil
	}
	ratio := new(big.Rat).Quo(new(big.Rat).Sub(later, earlier), earlier)
	result.Percent = percent(ratio)
	return result, ratio
}
func (p Point) Overview(s *AnnualSummary, month int) []RegisterOverview {
	result := []RegisterOverview{}
	baseID := ""
	for _, r := range p.Meter.Registers {
		if r.Direction != "EXPORT" {
			baseID = r.ID
			break
		}
	}
	for _, r := range p.Meter.Registers {
		row := RegisterOverview{RegisterID: r.ID}
		pos := findPosition(s, r.ID)
		if pos != nil && pos.Cents != nil {
			amount := exact(*pos.Cents)
			base := big.NewRat(0, 1)
			if r.ID == baseID {
				if s.BaseCents == nil {
					base = nil
				} else {
					base = exact(*s.BaseCents)
				}
			}
			if amount != nil && base != nil && !slices.Contains(s.Warnings, "UNIT_PRICE_GAP") && (r.ID != baseID || !slices.Contains(s.Warnings, "BASE_PRICE_GAP")) {
				row.TotalCents = cents(new(big.Rat).Add(amount, base))
			}
		}
		if month > 0 {
			start := time.Date(s.Year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
			end := start.AddDate(0, 1, 0).Format(time.DateOnly)
			last := start.AddDate(0, 1, -1).Format(time.DateOnly)
			for i := range p.Readings {
				v := &p.Readings[i]
				if v.RegisterID == r.ID && (v.Date == last || v.Date == end && row.Closing == nil) {
					row.Closing = v
				}
			}
			prices := []UnitPrice{}
			for _, v := range p.Tariffs.UnitPrices {
				if v.Direction == r.Direction && v.From < end && (v.Until == "" || v.Until > start.Format(time.DateOnly)) {
					prices = append(prices, v)
				}
			}
			row.ChangingRate = len(prices) > 1
			if len(prices) == 1 && prices[0].From <= start.Format(time.DateOnly) && (prices[0].Until == "" || prices[0].Until >= end) {
				row.Rate = &prices[0]
			}
		}
		result = append(result, row)
	}
	return result
}

// Histories uses only available data in each five-year window. Partial-year
// comparisons require all matching fully covered prior-year months.
func (p Point) Histories(all []AnnualSummary) []HistoryWindow {
	result := []HistoryWindow{}
	for _, last := range all {
		for _, reg := range p.Meter.Registers {
			w := HistoryWindow{RegisterID: reg.ID, FromYear: last.Year - 4, UntilYear: last.Year, Rows: []HistoryRow{}}
			quantities, costs, ratios := new(big.Rat), new(big.Rat), new(big.Rat)
			for y := w.FromYear; y <= w.UntilYear; y++ {
				s := findYear(all, y)
				pos := findPosition(s, reg.ID)
				row := HistoryRow{Year: y, Complete: fullYear(pos, y)}
				if pos != nil && pos.From != "" && pos.Until != "" {
					v := exact(pos.Quantity)
					if v != nil && v.Sign() >= 0 {
						row.Position = pos
						quantities.Add(quantities, v)
						w.QuantityCount++
						for _, o := range p.Overview(s, 0) {
							if o.RegisterID == reg.ID {
								row.CostCents = o.TotalCents
							}
						}
						if row.CostCents != nil {
							costs.Add(costs, exact(*row.CostCents))
							w.CostCount++
						}
					}
				}
				var ratio *big.Rat
				row.Change, ratio = change(all, reg.ID, y)
				if y > w.FromYear && ratio != nil {
					ratios.Add(ratios, ratio)
					w.TrendCount++
				}
				w.Rows = append(w.Rows, row)
			}
			if w.QuantityCount > 0 {
				w.QuantityMean = textPtr(new(big.Rat).Quo(quantities, big.NewRat(int64(w.QuantityCount), 1)).FloatString(6))
			}
			if w.CostCount > 0 {
				w.CostMean = cents(new(big.Rat).Quo(costs, big.NewRat(int64(w.CostCount), 1)))
			}
			if w.TrendCount > 0 {
				w.TrendDirection = ratios.Sign()
				w.TrendPercent = percent(new(big.Rat).Quo(ratios, big.NewRat(int64(w.TrendCount), 1)))
			}
			result = append(result, w)
		}
	}
	return result
}

// ReadingView attaches the measured difference and calendar-boundary decision
// to each original reading; suppressed month-start readings remain visible.
type ReadingView struct {
	Reading
	Difference       *string `json:"difference_quantity"`
	BoundaryPriority bool    `json:"boundary_priority"`
}

func (p Point) ReadingViews(intervals []ConsumptionInterval) ([]ReadingView, error) {
	selected, err := p.calendarReadings()
	if err != nil {
		return nil, err
	}
	chosen := map[string]bool{}
	for _, r := range selected {
		chosen[r.RegisterID+"/"+r.Date] = true
	}
	differences := map[string]string{}
	for _, v := range intervals {
		differences[v.RegisterID+"/"+v.EndDate] = v.Quantity
	}
	result := []ReadingView{}
	for _, r := range p.Readings {
		key := r.RegisterID + "/" + r.Date
		row := ReadingView{Reading: r, BoundaryPriority: !chosen[key]}
		if q, ok := differences[key]; ok {
			row.Difference = textPtr(q)
		}
		result = append(result, row)
	}
	return result, nil
}

// Include an empty display window without inventing measurements or annual projections.
func (p Point) HistoriesThrough(all []AnnualSummary, year int) []HistoryWindow {
	for _, s := range all {
		if s.Year == year {
			return p.Histories(all)
		}
	}
	windows := append([]AnnualSummary{}, all...)
	windows = append(windows, AnnualSummary{Year: year, Positions: []AnnualPosition{}})
	return p.Histories(windows)
}

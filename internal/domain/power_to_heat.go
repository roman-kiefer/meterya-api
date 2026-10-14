package domain

import (
	"math/big"
	"sort"
)

type PowerToHeat struct {
	Choices    map[string]string `json:"choices,omitempty"`
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	From       string            `json:"valid_from"`
	NetPoint   string            `json:"net_point"`
	HousePoint string            `json:"house_point"`
	Version    int64             `json:"version"`
}
type HeatPeriod struct {
	Days    int64   `json:"days"`
	From    string  `json:"start_date"`
	Until   string  `json:"end_date"`
	Grid    string  `json:"grid_kwh"`
	HousePV *string `json:"house_pv_kwh"`
	Total   *string `json:"total_kwh"`
	Valid   bool    `json:"valid"`
}
type HeatReport struct {
	Sources         []VirtualSource  `json:"sources,omitempty"`
	SourceVersions  map[string]int64 `json:"source_versions,omitempty"`
	OverviewVersion int              `json:"overview_version"`
	Yearly          []HeatYear       `json:"yearly"`
	PowerToHeat
	Tariffs      Tariffs         `json:"tariffs"`
	NetVersion   int64           `json:"net_version"`
	HouseVersion int64           `json:"house_version"`
	GridPeriods  []HeatPeriod    `json:"grid_periods"`
	TotalPeriods []HeatPeriod    `json:"total_periods"`
	Flags        []string        `json:"flags"`
	Annual       []AnnualSummary `json:"annual"`
}

// Only real common boundary readings are used for these accounting periods.
// No estimate, extrapolation or clamp hides missing or negative balances.
func (c PowerToHeat) Report(net, house Point) (HeatReport, error) {
	r := HeatReport{PowerToHeat: c, NetVersion: net.Version, HouseVersion: house.Version, GridPeriods: []HeatPeriod{}, TotalPeriods: []HeatPeriod{}, Flags: []string{}}
	r.Tariffs = Tariffs{BasePrices: net.Tariffs.BasePrices, UnitPrices: []UnitPrice{}}
	for _, price := range net.Tariffs.UnitPrices {
		if price.Direction == "IMPORT" {
			r.Tariffs.UnitPrices = append(r.Tariffs.UnitPrices, price)
		}
	}
	sources := []Point{net, house}
	data := make([]map[string]*big.Rat, 4)
	for i, p := range sources {
		calendar, err := p.calendarReadings()
		if err != nil {
			return r, err
		}
		p.Readings = calendar
		for j := range p.Readings {
			p.Readings[j].Date = ReadingBoundary(p.Readings[j].Date)
		}
		if p.Kind != "PHYSICAL" || p.Meter.Type != "BIDIRECTIONAL" {
			return r, Fail("VALIDATION_FAILED")
		}
		for j, dir := range []string{"IMPORT", "EXPORT"} {
			k := i*2 + j
			data[k] = map[string]*big.Rat{}
			register := ""
			for _, reg := range p.Meter.Registers {
				if reg.Direction == dir && reg.Unit == "kWh" {
					if register != "" {
						return r, Fail("VALIDATION_FAILED")
					}
					register = reg.ID
				}
			}
			if register == "" {
				return r, Fail("REGISTER_NOT_FOUND")
			}
			for _, v := range p.Readings {
				if v.RegisterID == register && v.Date >= c.From {
					d, err := p.measurementDecimal(v.Value)
					if err != nil {
						return r, err
					}
					q := new(big.Rat)
					q.SetString(d)
					if _, ok := data[k][v.Date]; ok {
						return r, Fail("READING_ALREADY_EXISTS")
					}
					data[k][v.Date] = q
				}
			}
		}
	}
	common := func(all bool) []string {
		dates := []string{}
		for d := range data[0] {
			if data[2][d] != nil && (!all || (data[1][d] != nil && data[3][d] != nil)) {
				dates = append(dates, d)
			}
		}
		sort.Strings(dates)
		return dates
	}
	periods := func(dates []string, all bool) []HeatPeriod {
		result := []HeatPeriod{}
		for i := 1; i < len(dates); i++ {
			start, end := dates[i-1], dates[i]
			delta := func(k int) *big.Rat { return new(big.Rat).Sub(data[k][end], data[k][start]) }
			grid := new(big.Rat).Sub(delta(0), delta(2))
			v := HeatPeriod{From: start, Until: end, Grid: grid.FloatString(6), Valid: grid.Sign() >= 0}
			if all {
				pv := new(big.Rat).Sub(delta(3), delta(1))
				total := new(big.Rat).Add(grid, pv)
				ps, ts := pv.FloatString(6), total.FloatString(6)
				v.HousePV = &ps
				v.Total = &ts
				v.Valid = v.Valid && pv.Sign() >= 0 && total.Sign() >= 0
			}
			if !v.Valid && len(r.Flags) == 0 {
				r.Flags = append(r.Flags, "NEGATIVE_BALANCE")
			}
			result = append(result, v)
		}
		return result
	}
	r.GridPeriods = periods(common(false), false)
	r.TotalPeriods = periods(common(true), true)
	if len(r.GridPeriods) == 0 {
		r.Flags = append(r.Flags, "INSUFFICIENT_READINGS")
	}
	if len(r.TotalPeriods) == 0 {
		r.Flags = append(r.Flags, "MISSING_COMMON_EXPORT_READINGS")
	}
	var err error
	r.Annual, err = r.Costs()
	r.OverviewVersion = OverviewVersion
	r.Yearly = r.Years()
	if err != nil {
		return r, err
	}
	return r, nil
}

func (r HeatReport) ValidateTariffs(t Tariffs) error {
	p := Point{Meter: Meter{Registers: []Register{{ID: r.ID, Unit: "kWh", Direction: "IMPORT"}}}}
	return p.ValidateTariffs(t)
}

// Reuse the exact annual billing rules on a cumulative derived import register.
// This register exists only during calculation; it is never stored as a reading.
func (r HeatReport) Costs() ([]AnnualSummary, error) {
	if err := r.ValidateTariffs(r.Tariffs); err != nil {
		return nil, err
	}
	for _, flag := range r.Flags {
		if flag == "NEGATIVE_BALANCE" || flag == "SOURCE_MISSING" {
			return []AnnualSummary{}, nil
		}
	}
	if len(r.GridPeriods) == 0 {
		return []AnnualSummary{}, nil
	}
	p := Point{ID: r.ID, Version: r.Version, Tariffs: r.Tariffs, Meter: Meter{Registers: []Register{{ID: r.ID, Unit: "kWh", Direction: "IMPORT"}}}}
	intervals := []ConsumptionInterval{}
	for _, v := range r.GridPeriods {
		if !v.Valid {
			continue
		}
		intervals = append(intervals, ConsumptionInterval{RegisterID: r.ID, StartDate: v.From, EndDate: v.Until, Quantity: v.Grid, Unit: "kWh"})
	}
	return p.SummariesFromIntervals(intervals)
}

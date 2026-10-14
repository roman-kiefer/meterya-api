package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
)

type VirtualPoint struct {
	ID          string            `json:"id"`
	WorkspaceID string            `json:"workspace_id"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Root        string            `json:"root_point,omitempty"`
	HeatID      string            `json:"heat_id,omitempty"`
	Choices     map[string]string `json:"choices"`
	Version     int64             `json:"version"`
	Archived    bool              `json:"archived"`
	Deleted     bool              `json:"deleted"`
}
type VirtualSource struct {
	PointID string `json:"point_id"`
	Name    string `json:"name"`
	From    string `json:"valid_from"`
	Until   string `json:"valid_until"`
	Role    string `json:"role,omitempty"`
}
type VirtualPeriod struct {
	ConsumptionInterval
	SourceIDs []string `json:"source_ids"`
	Grid      *string  `json:"grid_quantity,omitempty"`
	HousePV   *string  `json:"house_pv_quantity,omitempty"`
	Valid     bool     `json:"valid"`
}
type VirtualInfo struct {
	Definition     VirtualPoint     `json:"definition"`
	Sources        []VirtualSource  `json:"sources"`
	SourceVersions map[string]int64 `json:"source_versions"`
	DataVersion    string           `json:"data_version"`
	Flags          []string         `json:"flags"`
	Periods        []VirtualPeriod  `json:"periods"`
	CostBasis      string           `json:"cost_basis"`
	Actions        map[string]bool  `json:"actions"`
}
type VirtualReport struct {
	Point     Point
	Info      VirtualInfo
	Annual    []AnnualSummary
	Intervals []ConsumptionInterval
}
type SourceChain struct {
	Sources   []VirtualSource
	Points    []Point
	Intervals []ConsumptionInterval
	Tariffs   Tariffs
	Registers []Register
	Versions  map[string]int64
	Flags     []string
}

func clipPrice(p PricePeriod, from, until string) (PricePeriod, bool) {
	if from != "" && p.From < from {
		p.From = from
	}
	if until != "" && (p.Until == "" || p.Until > until) {
		p.Until = until
	}
	return p, p.Until == "" || p.From < p.Until
}

// ResolveChain follows one temporal continuation, never a sum of overlapping successors.
func ResolveChain(root string, choices map[string]string, points map[string]Point, changes []MeterTransition) (SourceChain, error) {
	out := SourceChain{Sources: []VirtualSource{}, Points: []Point{}, Intervals: []ConsumptionInterval{}, Versions: map[string]int64{}, Flags: []string{}, Tariffs: Tariffs{BasePrices: []BasePrice{}, UnitPrices: []UnitPrice{}}}
	seen := map[string]bool{}
	current, from := root, ""
	for current != "" {
		if seen[current] {
			return out, Fail("VIRTUAL_CYCLE")
		}
		seen[current] = true
		p, ok := points[current]
		if !ok {
			out.Flags = append(out.Flags, "SOURCE_MISSING")
			break
		}
		if p.Kind != "PHYSICAL" {
			return out, Fail("VALIDATION_FAILED")
		}
		if len(out.Registers) == 0 {
			seenDirections := map[string]bool{}
			for _, reg := range p.Meter.Registers {
				if seenDirections[reg.Direction] {
					return out, Fail("VIRTUAL_INCOMPATIBLE")
				}
				seenDirections[reg.Direction] = true
			}
			out.Registers = append([]Register{}, p.Meter.Registers...)
		} else {
			if len(p.Meter.Registers) != len(out.Registers) || p.Meter.Type != out.Points[0].Meter.Type {
				return out, Fail("VIRTUAL_INCOMPATIBLE")
			}
			for _, reg := range out.Registers {
				count := 0
				for _, r := range p.Meter.Registers {
					if reg.Direction == r.Direction && reg.Unit == r.Unit {
						count++
					}
				}
				if count != 1 {
					return out, Fail("VIRTUAL_INCOMPATIBLE")
				}
			}
		}
		until, next := "", ""
		for _, change := range changes {
			if change.OldPoint != current {
				continue
			}
			if until != "" {
				return out, Fail("VIRTUAL_AMBIGUOUS")
			}
			until = ReadingBoundary(change.Date)
			if from != "" && until <= from {
				return out, Fail("VIRTUAL_CYCLE")
			}
			if len(change.NewPoints) == 1 {
				next = change.NewPoints[0]
			} else {
				next = choices[current]
				if next == "" {
					next = change.Continuation
				}
				found := false
				for _, id := range change.NewPoints {
					if next == id {
						found = true
					}
				}
				if !found {
					return out, Fail("VIRTUAL_AMBIGUOUS")
				}
			}
		}
		out.Sources = append(out.Sources, VirtualSource{PointID: p.ID, Name: p.Name, From: from, Until: until})
		out.Points = append(out.Points, p)
		out.Versions[p.ID] = p.Version
		intervals, err := p.Consumption()
		if err != nil {
			return out, err
		}
		for _, v := range intervals {
			v.StartDate, v.EndDate = ReadingBoundary(v.StartDate), ReadingBoundary(v.EndDate)
			// A missing transition reading must not be bridged or apportioned silently.
			if from != "" && v.StartDate < from || until != "" && v.EndDate > until {
				continue
			}
			if v.StartDate < v.EndDate {
				out.Intervals = append(out.Intervals, v)
			}
		}
		for _, price := range p.Tariffs.UnitPrices {
			if clipped, ok := clipPrice(price.PricePeriod, from, until); ok {
				price.PricePeriod = clipped
				out.Tariffs.UnitPrices = append(out.Tariffs.UnitPrices, price)
			}
		}
		for _, price := range p.Tariffs.BasePrices {
			if clipped, ok := clipPrice(price.PricePeriod, from, until); ok {
				price.PricePeriod = clipped
				out.Tariffs.BasePrices = append(out.Tariffs.BasePrices, price)
			}
		}
		current, from = next, until
	}
	return out, nil
}

func virtualFingerprint(v VirtualPoint, versions map[string]int64, changes []MeterTransition, heat *PowerToHeat) string {
	b, _ := json.Marshal(struct {
		Definition  VirtualPoint
		Sources     map[string]int64
		Transitions []MeterTransition
		Heat        *PowerToHeat
		Calculation int
	}{v, versions, changes, heat, CalculationVersion})
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}
func virtualBase(v VirtualPoint) VirtualReport {
	return VirtualReport{Point: Point{ID: v.ID, WorkspaceID: v.WorkspaceID, Name: v.Name, Kind: "VIRTUAL", Version: v.Version, Archived: v.Archived, Meter: Meter{ID: v.ID, Serial: "", Registers: []Register{}}, Readings: []Reading{}, Tariffs: Tariffs{BasePrices: []BasePrice{}, UnitPrices: []UnitPrice{}}}, Info: VirtualInfo{Definition: v, Sources: []VirtualSource{}, SourceVersions: map[string]int64{}, Flags: []string{}, Periods: []VirtualPeriod{}, CostBasis: "SOURCE", Actions: map[string]bool{"record": false, "tariffs": false, "move": false, "edit": true, "archive": true, "delete": true}}, Annual: []AnnualSummary{}, Intervals: []ConsumptionInterval{}}
}

func EvaluateVirtual(v VirtualPoint, points map[string]Point, changes []MeterTransition, configs []PowerToHeat) (VirtualReport, error) {
	out := virtualBase(v)
	if v.Type == "SEQUENCE" {
		chain, err := ResolveChain(v.Root, v.Choices, points, changes)
		if err != nil {
			return out, err
		}
		out.Info.Sources, out.Info.SourceVersions, out.Info.Flags = chain.Sources, chain.Versions, chain.Flags
		out.Point.Meter.Type = "VIRTUAL"
		out.Point.Tariffs = chain.Tariffs
		ids := map[string]string{}
		for _, r := range chain.Registers {
			id := v.ID + "-" + r.Direction
			out.Point.Meter.Registers = append(out.Point.Meter.Registers, Register{ID: id, Direction: r.Direction, Unit: r.Unit})
		}
		for _, p := range chain.Points {
			for _, reg := range p.Meter.Registers {
				ids[reg.ID] = v.ID + "-" + reg.Direction
			}
		}
		for _, interval := range chain.Intervals {
			source := ""
			for _, p := range chain.Points {
				for _, reg := range p.Meter.Registers {
					if reg.ID == interval.RegisterID {
						source = p.ID
					}
				}
			}
			interval.RegisterID = ids[interval.RegisterID]
			out.Intervals = append(out.Intervals, interval)
			out.Info.Periods = append(out.Info.Periods, VirtualPeriod{ConsumptionInterval: interval, SourceIDs: []string{source}, Valid: true})
		}
		out.Annual, err = out.Point.SummariesFromIntervals(out.Intervals)
		if err != nil {
			return out, err
		}
	} else if v.Type == "HEAT" {
		var config *PowerToHeat
		for i := range configs {
			if configs[i].ID == v.HeatID {
				config = &configs[i]
				break
			}
		}
		if config == nil {
			out.Info.Flags = append(out.Info.Flags, "SOURCE_MISSING")
		} else {
			report, sources, versions, err := ResolveHeat(*config, v.Choices, points, changes)
			if err != nil {
				return out, err
			}
			out.Info.Sources, out.Info.SourceVersions, out.Info.Flags = sources, versions, report.Flags
			out.Info.CostBasis = "GRID"
			out.Point.Meter.Type = "VIRTUAL_HEAT"
			out.Point.Meter.Registers = []Register{{ID: v.ID + "-TOTAL", Direction: "CONSUMPTION", Unit: "kWh"}, {ID: v.ID + "-GRID", Direction: "IMPORT", Unit: "kWh"}, {ID: v.ID + "-PV", Direction: "SELF", Unit: "kWh"}}
			// Costs stay with grid consumption; rates must never be applied to total demand.
			sourceIDs := func(from, until string) []string {
				ids := []string{}
				for _, source := range sources {
					if (source.From == "" || source.From < until) && (source.Until == "" || source.Until > from) {
						ids = append(ids, source.PointID)
					}
				}
				return ids
			}
			for _, period := range report.GridPeriods {
				if !period.Valid {
					continue
				}
				interval := ConsumptionInterval{RegisterID: v.ID + "-GRID", StartDate: period.From, EndDate: period.Until, Quantity: period.Grid, Unit: "kWh"}
				out.Intervals = append(out.Intervals, interval)
				out.Info.Periods = append(out.Info.Periods, VirtualPeriod{ConsumptionInterval: interval, SourceIDs: sourceIDs(period.From, period.Until), Valid: true})
			}
			for _, period := range report.TotalPeriods {
				if !period.Valid || period.Total == nil || period.HousePV == nil {
					continue
				}
				total := ConsumptionInterval{RegisterID: v.ID + "-TOTAL", StartDate: period.From, EndDate: period.Until, Quantity: *period.Total, Unit: "kWh"}
				pv := total
				pv.RegisterID = v.ID + "-PV"
				pv.Quantity = *period.HousePV
				out.Intervals = append(out.Intervals, total, pv)
				out.Info.Periods = append(out.Info.Periods, VirtualPeriod{ConsumptionInterval: pv, SourceIDs: sourceIDs(period.From, period.Until), Valid: true})
				periodSourceIDs := []string{}
				for _, source := range sources {
					if (source.From == "" || source.From < period.Until) && (source.Until == "" || source.Until > period.From) {
						periodSourceIDs = append(periodSourceIDs, source.PointID)
					}
				}
				out.Info.Periods = append(out.Info.Periods, VirtualPeriod{ConsumptionInterval: total, SourceIDs: periodSourceIDs, Grid: textPtr(period.Grid), HousePV: period.HousePV, Valid: true})
			}
			var errAnnual error
			out.Annual, errAnnual = out.Point.SummariesFromIntervals(out.Intervals)
			if errAnnual != nil {
				return out, errAnnual
			}
			applyCosts := func(summary *AnnualSummary, cost *AnnualSummary) {
				// Quantity completeness is independent of the availability of a tariff.
				warnings := []string{}
				for _, warning := range summary.Warnings {
					if warning != "UNIT_PRICE_GAP" && warning != "BASE_PRICE_GAP" {
						warnings = append(warnings, warning)
					}
				}
				summary.Warnings = warnings
				summary.Complete = true
				for i := range summary.Positions {
					p := &summary.Positions[i]
					p.Complete = p.From != "" && !p.HasGaps && (p.Until == fmt.Sprintf("%04d-01-01", summary.Year+1) && p.From == fmt.Sprintf("%04d-01-01", summary.Year))
					// Monthly positions cover their entire calendar month when the matching cost month does.
					if cost != nil && cost.Complete && !p.HasGaps && p.From != "" {
						p.Complete = p.From == cost.Positions[0].From && p.Until == cost.Positions[0].Until
					}
					summary.Complete = summary.Complete && p.Complete
				}
				if len(summary.Positions) > 1 {
					total, grid := summary.Positions[0], summary.Positions[1]
					if total.From != grid.From || total.Until != grid.Until || total.HasGaps != grid.HasGaps {
						warn(summary, "COVERAGE_DIFFERS")
					}
				}
				if cost == nil || !cost.Complete {
					summary.Complete = false
				}
				if cost != nil {
					summary.BaseCents, summary.NetCents = cost.BaseCents, cost.NetCents
					for _, warning := range cost.Warnings {
						if warning == "UNIT_PRICE_GAP" || warning == "BASE_PRICE_GAP" {
							warn(summary, warning)
						}
					}
				}
				for i := range summary.Positions {
					p := &summary.Positions[i]
					p.Cents = nil
					if p.RegisterID == v.ID+"-TOTAL" && cost != nil && len(cost.Positions) > 0 {
						p.Cents = cost.Positions[0].Cents
					}
				}
			}
			for i := range out.Annual {
				year := &out.Annual[i]
				cost := findYear(report.Annual, year.Year)
				applyCosts(year, cost)
				for j := range year.Months {
					var monthly *AnnualSummary
					if cost != nil {
						for k := range cost.Months {
							if cost.Months[k].Month == year.Months[j].Month {
								monthly = &cost.Months[k].AnnualSummary
							}
						}
					}
					applyCosts(&year.Months[j].AnnualSummary, monthly)
				}
			}
			out.Info.DataVersion = virtualFingerprint(v, versions, changes, config)
		}
	} else {
		return out, Fail("VALIDATION_FAILED")
	}
	if out.Info.DataVersion == "" {
		out.Info.DataVersion = virtualFingerprint(v, out.Info.SourceVersions, changes, nil)
	}
	sort.SliceStable(out.Info.Periods, func(i, j int) bool { return out.Info.Periods[i].EndDate > out.Info.Periods[j].EndDate })
	return out, nil
}

// A logical series is used only inside the calculator; never saved as readings.
func (c SourceChain) logicalPoint(id string) Point {
	p := Point{DerivedSeries: true, ID: id, Kind: "PHYSICAL", Meter: Meter{ID: id, Type: "BIDIRECTIONAL", Registers: []Register{}}, Tariffs: c.Tariffs, Readings: []Reading{}}
	for _, reg := range c.Registers {
		logicalID := id + "-" + reg.Direction
		p.Meter.Registers = append(p.Meter.Registers, Register{ID: logicalID, Direction: reg.Direction, Unit: reg.Unit})
		intervals := []ConsumptionInterval{}
		for _, source := range c.Points {
			for _, r := range source.Meter.Registers {
				if r.Direction == reg.Direction {
					for _, interval := range c.Intervals {
						if interval.RegisterID == r.ID {
							intervals = append(intervals, interval)
						}
					}
				}
			}
		}
		sort.Slice(intervals, func(i, j int) bool { return intervals[i].StartDate < intervals[j].StartDate })
		sum := new(big.Rat)
		seen := map[string]bool{}
		for _, interval := range intervals {
			if !seen[interval.StartDate] {
				p.Readings = append(p.Readings, Reading{RegisterID: logicalID, Date: interval.StartDate, Value: sum.FloatString(6)})
				seen[interval.StartDate] = true
			}
			sum.Add(sum, exact(interval.Quantity))
			p.Readings = append(p.Readings, Reading{RegisterID: logicalID, Date: interval.EndDate, Value: sum.FloatString(6)})
			seen[interval.EndDate] = true
		}
	}
	return p
}
func chainCovers(c SourceChain, from, until, direction string) bool {
	spans := []ConsumptionInterval{}
	for _, p := range c.Points {
		for _, reg := range p.Meter.Registers {
			if reg.Direction == direction {
				for _, v := range c.Intervals {
					if reg.ID == v.RegisterID {
						spans = append(spans, v)
					}
				}
			}
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].StartDate < spans[j].StartDate })
	cursor := from
	for _, v := range spans {
		if v.StartDate <= cursor && v.EndDate > cursor {
			cursor = v.EndDate
		}
	}
	return cursor >= until
}

func ResolveHeat(config PowerToHeat, choices map[string]string, points map[string]Point, changes []MeterTransition) (HeatReport, []VirtualSource, map[string]int64, error) {
	report := HeatReport{PowerToHeat: config, OverviewVersion: OverviewVersion, Flags: []string{}, Annual: []AnnualSummary{}, GridPeriods: []HeatPeriod{}, TotalPeriods: []HeatPeriod{}, Yearly: []HeatYear{}}
	merged := map[string]string{}
	for id, next := range config.Choices {
		merged[id] = next
	}
	for id, next := range choices {
		merged[id] = next
	}
	choices = merged
	net, err := ResolveChain(config.NetPoint, choices, points, changes)
	if err != nil {
		return report, nil, nil, err
	}
	house, err := ResolveChain(config.HousePoint, choices, points, changes)
	if err != nil {
		return report, nil, nil, err
	}
	versions := map[string]int64{}
	sources := []VirtualSource{}
	for _, item := range []struct {
		chain SourceChain
		role  string
	}{{net, "NET"}, {house, "HOUSE_PV"}} {
		for _, source := range item.chain.Sources {
			source.Role = item.role
			sources = append(sources, source)
		}
		for id, version := range item.chain.Versions {
			versions[id] = version
		}
	}
	if len(net.Flags) > 0 || len(house.Flags) > 0 {
		report.Flags = []string{"SOURCE_MISSING"}
		return report, sources, versions, nil
	}
	a, b := net.logicalPoint(config.NetPoint), house.logicalPoint(config.HousePoint)
	a.Version, b.Version = versions[config.NetPoint], versions[config.HousePoint]
	// Keep the source meter tariff rule, including time-dependent successor prices.
	report, err = config.Report(a, b)
	if err != nil {
		return report, sources, versions, err
	}
	filter := func(periods []HeatPeriod, all bool) []HeatPeriod {
		for i := range periods {
			v := &periods[i]
			if !chainCovers(net, v.From, v.Until, "IMPORT") || !chainCovers(house, v.From, v.Until, "IMPORT") || all && (!chainCovers(net, v.From, v.Until, "EXPORT") || !chainCovers(house, v.From, v.Until, "EXPORT")) {
				v.Valid = false
				report.Flags = append(report.Flags, "MEASUREMENT_GAP")
			}
		}
		return periods
	}
	report.Sources, report.SourceVersions = sources, versions
	report.GridPeriods = filter(report.GridPeriods, false)
	report.TotalPeriods = filter(report.TotalPeriods, true)
	report.Annual, err = report.Costs()
	report.Yearly = report.Years()
	return report, sources, versions, err
}

func (r *VirtualReport) Enrich() {
	for i := range r.Annual {
		year := &r.Annual[i]
		year.Overview = r.Point.Overview(year, 0)
		for j := range year.Months {
			m := &year.Months[j]
			m.Overview = r.Point.Overview(&m.AnnualSummary, m.Month)
		}
	}
}

func virtualErrorReport(v VirtualPoint, code string) VirtualReport {
	out := virtualBase(v)
	out.Info.Flags = []string{code}
	out.Info.DataVersion = fmt.Sprint(v.Version)
	return out
}

// Failed dependencies are exposed as unavailable, never converted to zero.
func UnavailableVirtual(v VirtualPoint, code string, points map[string]Point, changes []MeterTransition, heat []PowerToHeat) VirtualReport {
	out := virtualErrorReport(v, code)
	// A broken or ambiguous chain cannot resolve its final dependencies safely.
	// Conservatively invalidate the unavailable report on workspace source changes.
	versions := map[string]int64{}
	for id, point := range points {
		versions[id] = point.Version
	}
	var config *PowerToHeat
	for i := range heat {
		if heat[i].ID == v.HeatID {
			config = &heat[i]
			break
		}
	}
	out.Info.SourceVersions = versions
	out.Info.DataVersion = virtualFingerprint(v, versions, changes, config)
	return out
}

package domain

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
)

const CalculationVersion = 3

type AnnualPosition struct {
	RegisterID string  `json:"register_id"`
	Direction  string  `json:"direction"`
	Unit       string  `json:"unit"`
	Quantity   string  `json:"quantity"`
	Cents      *string `json:"cents"`
	From       string  `json:"covered_from"`
	Until      string  `json:"covered_until"`
	Estimated  bool    `json:"estimated"`
	Complete   bool    `json:"complete"`
	HasGaps    bool    `json:"has_gaps,omitempty"`
}
type MonthlySummary struct {
	AnnualSummary
	Month int `json:"month"`
}

type AnnualSummary struct {
	Overview     []RegisterOverview `json:"overview,omitempty"`
	Months       []MonthlySummary   `json:"months,omitempty"`
	Year         int                `json:"year"`
	Version      int                `json:"calculation_version"`
	PointVersion int64              `json:"point_version"`
	Complete     bool               `json:"complete"`
	Estimated    bool               `json:"estimated"`
	Warnings     []string           `json:"warnings"`
	BaseCents    *string            `json:"base_cents"`
	NetCents     *string            `json:"net_cents"`
	Positions    []AnnualPosition   `json:"positions"`
}

func date(s string) time.Time   { d, _ := time.Parse(time.DateOnly, s); return d }
func days(a, b time.Time) int64 { return (b.Unix() - a.Unix()) / 86400 }
func minDate(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func maxDate(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func applies(p PricePeriod, d time.Time) bool {
	s := d.Format(time.DateOnly)
	return p.From <= s && (p.Until == "" || s < p.Until)
}

// Money is rounded only once per period position. Negative values round away
// from zero at a half, although export revenue itself is stored positive.
func rounded(v *big.Rat) *big.Int {
	n := new(big.Int).Abs(v.Num())
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(n, v.Denom(), rem)
	if new(big.Int).Lsh(rem, 1).Cmp(v.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if v.Sign() < 0 {
		q.Neg(q)
	}
	return q
}
func cents(v *big.Rat) *string { s := rounded(v).String(); return &s }
func quantity(v *big.Rat) string {
	digits := rounded(v).String() // v is in millionths of a unit; display only.
	if len(digits) < 7 {
		digits = strings.Repeat("0", 7-len(digits)) + digits
	}
	return digits[:len(digits)-6] + "." + digits[len(digits)-6:]
}
func warn(s *AnnualSummary, code string) {
	for _, existing := range s.Warnings {
		if existing == code {
			return
		}
	}
	s.Warnings = append(s.Warnings, code)
	s.Complete = false
}
func priceCuts(start, end time.Time, periods []PricePeriod) []time.Time {
	cuts := []time.Time{start, end}
	for _, p := range periods {
		for _, boundary := range []string{p.From, p.Until} {
			if boundary == "" {
				continue
			}
			d := date(boundary)
			if d.After(start) && d.Before(end) {
				cuts = append(cuts, d)
			}
		}
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].Before(cuts[j]) })
	unique := cuts[:0]
	for _, d := range cuts {
		if len(unique) == 0 || !d.Equal(unique[len(unique)-1]) {
			unique = append(unique, d)
		}
	}
	return unique
}

// AnnualSummaries never extrapolates. Intermediate quantities and costs are
// rational numbers; displayed quantities cannot influence money calculations.
func (p Point) AnnualSummaries() ([]AnnualSummary, error) {
	if err := p.ValidateTariffs(p.Tariffs); err != nil {
		return nil, err
	}
	calendar, err := p.calendarReadings()
	if err != nil {
		return nil, err
	}
	p.Readings = calendar
	for i := range p.Readings {
		p.Readings[i].Date = ReadingBoundary(p.Readings[i].Date)
	}
	intervals, err := p.Consumption()
	if err != nil {
		return nil, err
	}
	byYear := map[int]bool{}
	for _, reading := range p.Readings {
		d, e := time.Parse(time.DateOnly, reading.Date)
		if e != nil || d.Year() < 1900 || d.Year() > 9998 {
			return nil, Fail("INVALID_DATE")
		}
		byYear[d.Year()] = true
	}
	for _, interval := range intervals {
		for year := date(interval.StartDate).Year(); year <= date(interval.EndDate).AddDate(0, 0, -1).Year(); year++ {
			byYear[year] = true
		}
	}
	years := []int{}
	for year := range byYear {
		years = append(years, year)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))
	result := []AnnualSummary{}
	for _, year := range years {
		start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(1, 0, 0)
		summary := p.periodSummary(intervals, start, end)
		for month := 1; month <= 12; month++ {
			monthStart := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
			summary.Months = append(summary.Months, MonthlySummary{AnnualSummary: p.periodSummary(intervals, monthStart, monthStart.AddDate(0, 1, 0)), Month: month})
		}
		result = append(result, summary)
	}
	return result, nil
}

// periodSummary uses rational arithmetic within a half-open measured period.
// Monthly display totals are rounded independently; annual totals are never
// reconstructed by summing these rounded monthly values.
func (p Point) periodSummary(intervals []ConsumptionInterval, start, end time.Time) AnnualSummary {
	year := start.Year()
	summary := AnnualSummary{Year: year, Version: CalculationVersion, PointVersion: p.Version, Complete: true, Warnings: []string{}, Positions: []AnnualPosition{}}
	net := new(big.Int)
	priced := false
	for _, register := range p.Meter.Registers {
		pos := AnnualPosition{RegisterID: register.ID, Direction: register.Direction, Unit: register.Unit, Complete: true}
		qty, cost := new(big.Rat), new(big.Rat)
		pricedPosition := false
		coveredDays := int64(0)
		periods := []PricePeriod{}
		for _, price := range p.Tariffs.UnitPrices {
			if price.Direction == register.Direction {
				periods = append(periods, price.PricePeriod)
			}
		}
		for _, interval := range intervals {
			if interval.RegisterID != register.ID {
				continue
			}
			a, b := date(interval.StartDate), date(interval.EndDate)
			lo, hi := maxDate(a, start), minDate(b, end)
			if !hi.After(lo) {
				continue
			}
			if pos.From == "" || lo.Format(time.DateOnly) < pos.From {
				pos.From = lo.Format(time.DateOnly)
			}
			if hi.Format(time.DateOnly) > pos.Until {
				pos.Until = hi.Format(time.DateOnly)
			}
			coveredDays += days(lo, hi)
			delta, _ := new(big.Int).SetString(strings.ReplaceAll(interval.Quantity, ".", ""), 10)
			perDay := new(big.Rat).SetFrac(delta, big.NewInt(days(a, b)))
			qty.Add(qty, new(big.Rat).Mul(perDay, new(big.Rat).SetInt64(days(lo, hi))))
			cuts := priceCuts(lo, hi, periods)
			for i := 1; i < len(cuts); i++ {
				left, right := cuts[i-1], cuts[i]
				if !left.Equal(a) || !right.Equal(b) {
					pos.Estimated = true
				}
				var rate *PricePeriod
				for j := range periods {
					if applies(periods[j], left) {
						rate = &periods[j]
						break
					}
				}
				if rate == nil {
					pos.Complete = false
					warn(&summary, "UNIT_PRICE_GAP")
					continue
				}
				segment := new(big.Rat).Mul(perDay, new(big.Rat).SetInt64(days(left, right)))
				segment.Mul(segment, new(big.Rat).SetFrac(big.NewInt(rate.Cents), big.NewInt(1_000_000_000)))
				cost.Add(cost, segment)
				pricedPosition = true
			}
		}
		if pos.From != "" && coveredDays < days(date(pos.From), date(pos.Until)) {
			pos.HasGaps = true
		}
		pos.Quantity = quantity(qty)
		if coveredDays != days(start, end) {
			pos.Complete = false
			warn(&summary, "MEASUREMENT_GAP")
		}
		if pricedPosition {
			pos.Cents = cents(cost)
			value, _ := new(big.Int).SetString(*pos.Cents, 10)
			if register.Direction == "EXPORT" {
				net.Sub(net, value)
			} else {
				net.Add(net, value)
			}
			priced = true
		}
		if pos.Estimated {
			summary.Estimated = true
		}
		summary.Positions = append(summary.Positions, pos)
	}
	// The shared base price follows the import/consumption register's measured
	// coverage and is never charged again for the export register.
	baseCost := new(big.Rat)
	basePriced := false
	baseDays := int64(0)
	primary := ""
	for _, reg := range p.Meter.Registers {
		if reg.Direction != "EXPORT" {
			primary = reg.ID
			break
		}
	}
	basePeriods := []PricePeriod{}
	for _, price := range p.Tariffs.BasePrices {
		basePeriods = append(basePeriods, price.PricePeriod)
	}
	for _, interval := range intervals {
		if interval.RegisterID != primary {
			continue
		}
		lo, hi := maxDate(date(interval.StartDate), start), minDate(date(interval.EndDate), end)
		if !hi.After(lo) {
			continue
		}
		cuts := priceCuts(lo, hi, basePeriods)
		for i := 1; i < len(cuts); i++ {
			left, right := cuts[i-1], cuts[i]
			var price *BasePrice
			for j := range p.Tariffs.BasePrices {
				if applies(p.Tariffs.BasePrices[j].PricePeriod, left) {
					price = &p.Tariffs.BasePrices[j]
					break
				}
			}
			if price == nil {
				warn(&summary, "BASE_PRICE_GAP")
				continue
			}
			for cursor := left; cursor.Before(right); {
				periodStart := time.Date(cursor.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
				periodEnd := periodStart.AddDate(1, 0, 0)
				if price.Period == "MONTH" {
					periodStart = time.Date(cursor.Year(), cursor.Month(), 1, 0, 0, 0, 0, time.UTC)
					periodEnd = periodStart.AddDate(0, 1, 0)
				}
				next := minDate(right, periodEnd)
				n := days(cursor, next)
				baseCost.Add(baseCost, new(big.Rat).SetFrac(new(big.Int).Mul(big.NewInt(price.Cents), big.NewInt(n)), big.NewInt(days(periodStart, periodEnd))))
				baseDays += n
				basePriced = true
				cursor = next
			}
		}
	}
	if baseDays != days(start, end) {
		summary.Complete = false
	}
	if basePriced {
		summary.BaseCents = cents(baseCost)
		value, _ := new(big.Int).SetString(*summary.BaseCents, 10)
		net.Add(net, value)
		priced = true
	}
	if priced {
		s := net.String()
		summary.NetCents = &s
	}
	return summary
}

// SummariesFromIntervals projects derived measurements without creating readings.
func (p Point) SummariesFromIntervals(intervals []ConsumptionInterval) ([]AnnualSummary, error) {
	if err := p.ValidateTariffs(p.Tariffs); err != nil {
		return nil, err
	}
	years := map[int]bool{}
	for _, v := range intervals {
		if v.StartDate >= v.EndDate {
			return nil, Fail("INVALID_DATE")
		}
		for y := date(v.StartDate).Year(); y <= date(v.EndDate).AddDate(0, 0, -1).Year(); y++ {
			years[y] = true
		}
	}
	ordered := []int{}
	for y := range years {
		ordered = append(ordered, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ordered)))
	out := []AnnualSummary{}
	for _, y := range ordered {
		start := date(fmt.Sprintf("%04d-01-01", y))
		row := p.periodSummary(intervals, start, start.AddDate(1, 0, 0))
		for m := 1; m <= 12; m++ {
			a := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
			row.Months = append(row.Months, MonthlySummary{AnnualSummary: p.periodSummary(intervals, a, a.AddDate(0, 1, 0)), Month: m})
		}
		out = append(out, row)
	}
	return out, nil
}

package domain

import (
	"math/big"
	"sort"
	"time"
)

// AdvancePayment is a monthly amount charged by the electricity provider from
// ValidFrom until the day before the next entry's ValidFrom. The last entry
// remains valid until further notice.
type AdvancePayment struct {
	From  string `json:"valid_from"`
	Cents int64  `json:"cents"`
}

// SupportsAdvancePayments reports whether the meter measures electricity.
func (p Point) SupportsAdvancePayments() bool {
	return p.Kind == "PHYSICAL" && (p.Meter.Type == "ELECTRICITY" || p.Meter.Type == "BIDIRECTIONAL")
}

// ValidateAdvancePayments checks a complete advance payment schedule.
func (p Point) ValidateAdvancePayments(schedule []AdvancePayment) error {
	if !p.SupportsAdvancePayments() {
		return Fail("ADVANCE_PAYMENTS_UNSUPPORTED")
	}
	if len(schedule) > 200 {
		return Fail("INVALID_ADVANCE_PAYMENT")
	}
	seen := map[string]bool{}
	for _, entry := range schedule {
		d, err := time.Parse(time.DateOnly, entry.From)
		if err != nil || d.Format(time.DateOnly) != entry.From || d.Year() < 1900 || d.Year() > 9998 || entry.Cents < 0 || entry.Cents > 1_000_000_000_000 {
			return Fail("INVALID_ADVANCE_PAYMENT")
		}
		if seen[entry.From] {
			return Fail("ADVANCE_PAYMENT_DUPLICATE")
		}
		seen[entry.From] = true
	}
	return nil
}

// SortedAdvancePayments returns a copy ordered by validity start.
func SortedAdvancePayments(schedule []AdvancePayment) []AdvancePayment {
	sorted := append([]AdvancePayment{}, schedule...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].From < sorted[j].From })
	return sorted
}

// advancePaymentPeriods converts the schedule into half-open price periods.
func advancePaymentPeriods(schedule []AdvancePayment) []PricePeriod {
	sorted := SortedAdvancePayments(schedule)
	periods := make([]PricePeriod, len(sorted))
	for i, entry := range sorted {
		periods[i] = PricePeriod{From: entry.From, Cents: entry.Cents}
		if i+1 < len(sorted) {
			periods[i].Until = sorted[i+1].From
		}
	}
	return periods
}

// addAdvancePayments adds the exact advance payments applicable to the
// half-open range [lo, hi). Monthly amounts are prorated by calendar days of
// the respective month.
func addAdvancePayments(total *big.Rat, periods []PricePeriod, lo, hi time.Time) {
	cuts := priceCuts(lo, hi, periods)
	for i := 1; i < len(cuts); i++ {
		left, right := cuts[i-1], cuts[i]
		var rate *PricePeriod
		for j := range periods {
			if applies(periods[j], left) {
				rate = &periods[j]
				break
			}
		}
		if rate == nil {
			continue
		}
		for cursor := left; cursor.Before(right); {
			monthStart := time.Date(cursor.Year(), cursor.Month(), 1, 0, 0, 0, 0, time.UTC)
			monthEnd := monthStart.AddDate(0, 1, 0)
			next := minDate(right, monthEnd)
			total.Add(total, new(big.Rat).SetFrac(new(big.Int).Mul(big.NewInt(rate.Cents), big.NewInt(days(cursor, next))), big.NewInt(days(monthStart, monthEnd))))
			cursor = next
		}
	}
}

package domain

import "time"

// Periods are calendar dates with inclusive starts and exclusive ends.
// An empty end means that the price remains valid until further notice.
type PricePeriod struct {
	From  string `json:"valid_from"`
	Until string `json:"valid_until"`
	Cents int64  `json:"cents"`
}
type BasePrice struct {
	PricePeriod
	Period string `json:"period"`
}
type UnitPrice struct {
	PricePeriod
	Direction string `json:"direction"`
}
type Tariffs struct {
	BasePrices []BasePrice `json:"base_prices"`
	UnitPrices []UnitPrice `json:"unit_prices"`
}

func validPricePeriod(p PricePeriod) bool {
	from, e := time.Parse(time.DateOnly, p.From)
	if e != nil || from.Year() < 1900 || from.Year() > 9998 || p.Cents < 0 || p.Cents > 1_000_000_000_000 {
		return false
	}
	if p.Until == "" {
		return true
	}
	until, e := time.Parse(time.DateOnly, p.Until)
	return e == nil && until.Year() <= 9999 && until.After(from)
}
func overlaps(a, b PricePeriod) bool {
	return (a.Until == "" || b.From < a.Until) && (b.Until == "" || a.From < b.Until)
}
func (p Point) ValidateTariffs(t Tariffs) error {
	if len(t.BasePrices) > 200 || len(t.UnitPrices) > 400 {
		return Fail("INVALID_TARIFF")
	}
	for i, price := range t.BasePrices {
		if !validPricePeriod(price.PricePeriod) || (price.Period != "MONTH" && price.Period != "YEAR") {
			return Fail("INVALID_TARIFF")
		}
		for _, other := range t.BasePrices[:i] {
			if overlaps(price.PricePeriod, other.PricePeriod) {
				return Fail("TARIFF_OVERLAP")
			}
		}
	}
	for i, price := range t.UnitPrices {
		found := false
		for _, register := range p.Meter.Registers {
			if register.Direction == price.Direction {
				found = true
			}
		}
		if !found || !validPricePeriod(price.PricePeriod) {
			return Fail("INVALID_TARIFF")
		}
		for _, other := range t.UnitPrices[:i] {
			if price.Direction == other.Direction && overlaps(price.PricePeriod, other.PricePeriod) {
				return Fail("TARIFF_OVERLAP")
			}
		}
	}
	return nil
}

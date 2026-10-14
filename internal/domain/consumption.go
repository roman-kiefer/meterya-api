package domain

import (
	"math/big"
	"sort"
	"strings"
)

// ConsumptionInterval describes a measured difference between adjacent readings.
type ConsumptionInterval struct {
	RegisterID string `json:"register_id"`
	StartDate  string `json:"start_date"`
	EndDate    string `json:"end_date"`
	Quantity   string `json:"quantity"`
	Unit       string `json:"unit"`
}

// Consumption uses real readings from the same register and device, choosing
// month-end over the following first day's reading at a shared boundary.
// It retains original dates and does not interpolate or extrapolate.
func (p Point) Consumption() ([]ConsumptionInterval, error) {
	calendar, err := p.calendarReadings()
	if err != nil {
		return nil, err
	}
	p.Readings = calendar
	result := []ConsumptionInterval{}
	for _, register := range p.Meter.Registers {
		readings := []Reading{}
		for _, reading := range p.Readings {
			if reading.RegisterID == register.ID {
				readings = append(readings, reading)
			}
		}
		sort.Slice(readings, func(i, j int) bool { return readings[i].Date < readings[j].Date })
		for i := 1; i < len(readings); i++ {
			previous, current := readings[i-1], readings[i]
			if previous.Date == current.Date {
				return nil, Fail("READING_ALREADY_EXISTS")
			}
			before, err := Decimal(previous.Value)
			if err != nil {
				return nil, err
			}
			after, err := Decimal(current.Value)
			if err != nil {
				return nil, err
			}
			a, _ := new(big.Int).SetString(strings.ReplaceAll(after, ".", ""), 10)
			b, _ := new(big.Int).SetString(strings.ReplaceAll(before, ".", ""), 10)
			difference := new(big.Int).Sub(a, b)
			if difference.Sign() < 0 {
				return nil, Fail("READING_DECREASES")
			}
			digits := difference.String()
			if len(digits) < 7 {
				digits = strings.Repeat("0", 7-len(digits)) + digits
			}
			quantity := digits[:len(digits)-6] + "." + digits[len(digits)-6:]
			result = append(result, ConsumptionInterval{register.ID, previous.Date, current.Date, quantity, register.Unit})
		}
	}
	// Latest intervals first; preserve register order when dates match.
	sort.SliceStable(result, func(i, j int) bool { return result[i].EndDate > result[j].EndDate })
	return result, nil
}

package domain

import (
	"fmt"
	"math/big"
	"time"
)

// MonthlyStatement retains the explicitly supplied accounting periods and payments.
// These are monthly closing statements, distinct from calendar-day interpolation.
type MonthlyStatement struct {
	Year         int              `json:"year"`
	RegisterID   string           `json:"register_id"`
	Direction    string           `json:"direction,omitempty"`
	OpeningDate  string           `json:"opening_date"`
	OpeningValue string           `json:"opening_value"`
	Months       []StatementMonth `json:"months"`
}
type StatementMonth struct {
	Date         string `json:"date"`
	Value        string `json:"value"`
	UnitCents    int64  `json:"unit_cents_per_1000"`
	BaseCents    int64  `json:"base_cents"`
	PaymentCents int64  `json:"payment_cents"`
}
type MonthlyBillRow struct {
	StatementMonth
	Quantity    string `json:"quantity"`
	EnergyEuros string `json:"energy_euros_exact"`
}
type MonthlyBill struct {
	Year         int              `json:"year"`
	RegisterID   string           `json:"register_id"`
	Direction    string           `json:"direction"`
	OpeningValue string           `json:"opening_value"`
	Complete     bool             `json:"complete"`
	Rows         []MonthlyBillRow `json:"rows"`
	Quantity     string           `json:"quantity"`
	EnergyCents  string           `json:"energy_cents"`
	BaseCents    string           `json:"base_cents"`
	PaymentCents string           `json:"payment_cents"`
	TotalCents   string           `json:"total_cents"`
	BalanceCents string           `json:"balance_cents"`
}

func (s MonthlyStatement) Bill() (MonthlyBill, error) {
	b := MonthlyBill{Year: s.Year, RegisterID: s.RegisterID, Direction: s.EffectiveDirection(), OpeningValue: s.OpeningValue, Complete: len(s.Months) == 12, Rows: []MonthlyBillRow{}}
	if b.Direction != "IMPORT" && b.Direction != "EXPORT" {
		return b, Fail("VALIDATION_FAILED")
	}
	if s.Year < 1900 || s.Year > 9998 || len(s.Months) == 0 || len(s.Months) > 12 || s.OpeningDate != fmt.Sprintf("%04d-12-31", s.Year-1) {
		return b, Fail("VALIDATION_FAILED")
	}
	previous, err := Decimal(s.OpeningValue)
	if err != nil {
		return b, err
	}
	b.OpeningValue = previous
	opening := new(big.Rat)
	opening.SetString(previous)
	energy := new(big.Rat)
	base, payments := new(big.Int), new(big.Int)
	for i, m := range s.Months {
		expected := time.Date(s.Year, time.Month(i+2), 0, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
		if m.Date != expected || m.UnitCents < 0 || m.UnitCents > 1_000_000_000_000 || m.BaseCents < 0 || m.BaseCents > 1_000_000_000_000 || m.PaymentCents < 0 || m.PaymentCents > 1_000_000_000_000 {
			return b, Fail("VALIDATION_FAILED")
		}
		if b.Direction == "EXPORT" && m.BaseCents != 0 {
			return b, Fail("INVALID_TARIFF")
		}
		value, e := Decimal(m.Value)
		if e != nil {
			return b, e
		}
		m.Value = value
		if Compare(value, previous) < 0 {
			return b, Fail("READING_DECREASES")
		}
		from, to := new(big.Rat), new(big.Rat)
		from.SetString(previous)
		to.SetString(value)
		quantity := new(big.Rat).Sub(to, from)
		cost := new(big.Rat).Mul(quantity, big.NewRat(m.UnitCents, 1000))
		energy.Add(energy, cost)
		base.Add(base, big.NewInt(m.BaseCents))
		payments.Add(payments, big.NewInt(m.PaymentCents))
		b.Rows = append(b.Rows, MonthlyBillRow{m, quantity.FloatString(6), new(big.Rat).Quo(cost, big.NewRat(100, 1)).FloatString(9)})
		previous = value
	}
	closing := new(big.Rat)
	closing.SetString(previous)
	b.Quantity = new(big.Rat).Sub(closing, opening).FloatString(6)
	e := rounded(energy)
	total := new(big.Int).Add(e, base)
	b.EnergyCents = e.String()
	b.BaseCents = base.String()
	b.PaymentCents = payments.String()
	b.TotalCents = total.String()
	b.BalanceCents = new(big.Int).Sub(total, payments).String()
	return b, nil
}

func (p Point) MonthlyBills() ([]MonthlyBill, error) {
	result := []MonthlyBill{}
	for _, s := range p.MonthlyStatements {
		for _, register := range p.Meter.Registers {
			if register.ID == s.RegisterID {
				if s.Direction != "" && s.Direction != register.Direction {
					return nil, Fail("REGISTER_NOT_FOUND")
				}
				s.Direction = register.Direction
			}
		}
		b, e := s.Bill()
		if e != nil {
			return nil, e
		}
		result = append(result, b)
	}
	return result, nil
}

// Older single-register imports omitted direction and continue to mean IMPORT.
func (s MonthlyStatement) EffectiveDirection() string {
	if s.Direction == "" {
		return "IMPORT"
	}
	return s.Direction
}

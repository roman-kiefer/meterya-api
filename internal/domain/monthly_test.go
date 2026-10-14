package domain

import (
	"fmt"
	"testing"
	"time"
)

func TestMonthlyBillsReproduceClosingStatements(t *testing.T) {
	cases := []struct {
		year                                       int
		opening                                    string
		values                                     []int
		rate, firstPayment, normalPayment          int64
		energy, total, payments, balance, quantity string
	}{
		{2024, "992", []int{1162, 1302, 1427, 1566, 1678, 1816, 1956, 2052, 2147, 2285, 2439, 2580}, 35900, 7600, 6000, "57009", "73689", "73600", "89", "1588.000000"},
		{2025, "2580", []int{2727, 2863, 3002, 3130, 3252, 3375, 3479, 3588, 3717, 3848, 3987, 4115}, 35900, 6000, 6000, "55107", "71787", "72000", "-213", "1535.000000"},
		{2026, "4115", []int{4267, 4399, 4539, 4661, 4760, 4855, 4959, 5085, 5199}, 32900, 5600, 5600, "35664", "48174", "50400", "-2226", "1084.000000"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.year), func(t *testing.T) {
			s := MonthlyStatement{Year: c.year, OpeningDate: fmt.Sprintf("%d-12-31", c.year-1), OpeningValue: c.opening}
			for i, value := range c.values {
				payment := c.normalPayment
				if i == 0 {
					payment = c.firstPayment
				}
				s.Months = append(s.Months, StatementMonth{Date: time.Date(c.year, time.Month(i+2), 0, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), Value: fmt.Sprint(value), UnitCents: c.rate, BaseCents: 1390, PaymentCents: payment})
			}
			b, e := s.Bill()
			if e != nil || b.Quantity != c.quantity || b.EnergyCents != c.energy || b.TotalCents != c.total || b.PaymentCents != c.payments || b.BalanceCents != c.balance || b.Complete != (len(c.values) == 12) {
				t.Fatal(b, e)
			}
			if c.year == 2025 && b.Rows[0].EnergyEuros != "52.773000000" {
				t.Fatal("row rounded prematurely", b.Rows[0])
			}
		})
	}
}
func TestMonthlyStatementRejectsDecreasesAndSkippedMonths(t *testing.T) {
	s := MonthlyStatement{Year: 2024, OpeningDate: "2023-12-31", OpeningValue: "100", Months: []StatementMonth{{Date: "2024-01-31", Value: "99"}}}
	if _, e := s.Bill(); Code(e) != "READING_DECREASES" {
		t.Fatal(e)
	}
	s.Months[0].Value = "110"
	s.Months[0].Date = "2024-02-29"
	if _, e := s.Bill(); Code(e) != "VALIDATION_FAILED" {
		t.Fatal(e)
	}
}

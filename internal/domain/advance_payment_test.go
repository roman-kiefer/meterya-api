package domain

import "testing"

func advancePoint(from, until string) Point {
	p := Point{Version: 2, Kind: "PHYSICAL", Meter: Meter{Type: "ELECTRICITY", Registers: []Register{{ID: "i", Direction: "IMPORT", Unit: "kWh"}}}}
	p.Readings = []Reading{read("i", from, "0"), read("i", until, "1000")}
	p.Tariffs = Tariffs{BasePrices: []BasePrice{{PricePeriod: PricePeriod{From: "1900-01-01", Cents: 0}, Period: "YEAR"}}, UnitPrices: []UnitPrice{{PricePeriod: PricePeriod{From: "1900-01-01", Cents: 0}, Direction: "IMPORT"}}}
	return p
}

func TestAdvancePaymentBalanceMatchesIssueExample(t *testing.T) {
	p := advancePoint("2025-01-01", "2026-01-01")
	p.AdvancePayments = []AdvancePayment{{From: "2025-01-01", Cents: 10000}}
	p.Tariffs.BasePrices[0].Cents = 120000
	s := summaryFor(t, p, 2025)
	requireCents(t, s.NetCents, "120000")
	requireCents(t, s.AdvancePaymentCents, "120000")
	requireCents(t, s.EstimatedBalanceCents, "0")
	p.Tariffs.BasePrices[0].Cents = 130000
	s = summaryFor(t, p, 2025)
	requireCents(t, s.EstimatedBalanceCents, "10000")
}

func TestAdvancePaymentScheduleProratesMidMonthChange(t *testing.T) {
	p := advancePoint("2026-01-01", "2027-01-01")
	// Unordered input must not matter.
	p.AdvancePayments = []AdvancePayment{{From: "2026-08-15", Cents: 11500}, {From: "2026-01-01", Cents: 12356}, {From: "2026-05-01", Cents: 13265}}
	s := summaryFor(t, p, 2026)
	// 4 x 123.56 + 3 x 132.65 + (14/31 x 132.65 + 17/31 x 115.00) + 4 x 115.00
	requireCents(t, s.AdvancePaymentCents, "147516")
	requireCents(t, s.EstimatedBalanceCents, "-147516")
	requireCents(t, s.Months[3].AdvancePaymentCents, "12356")
	requireCents(t, s.Months[4].AdvancePaymentCents, "13265")
	requireCents(t, s.Months[7].AdvancePaymentCents, "12297")
	requireCents(t, s.Months[8].AdvancePaymentCents, "11500")
}

func TestAdvancePaymentsFollowMeasuredCoverage(t *testing.T) {
	p := advancePoint("2026-01-01", "2026-01-17")
	p.AdvancePayments = []AdvancePayment{{From: "2025-12-01", Cents: 3100}}
	s := summaryFor(t, p, 2026)
	requireCents(t, s.AdvancePaymentCents, "1600")
	if s.Complete {
		t.Fatal("partial coverage must stay incomplete", s)
	}
	if s.Months[1].AdvancePaymentCents == nil || *s.Months[1].AdvancePaymentCents != "0" {
		t.Fatal("uncovered month has no advance payments", s.Months[1])
	}
}

func TestAdvancePaymentsInLeapFebruaryAndBeforeFirstEntry(t *testing.T) {
	p := advancePoint("2024-02-01", "2024-03-01")
	p.AdvancePayments = []AdvancePayment{{From: "2024-02-15", Cents: 2900}}
	s := summaryFor(t, p, 2024)
	requireCents(t, s.AdvancePaymentCents, "1500")

	p = advancePoint("2026-01-01", "2027-01-01")
	p.AdvancePayments = []AdvancePayment{{From: "2026-07-01", Cents: 10000}}
	s = summaryFor(t, p, 2026)
	requireCents(t, s.AdvancePaymentCents, "60000")
	requireCents(t, s.Months[5].AdvancePaymentCents, "0")
	requireCents(t, s.Months[6].AdvancePaymentCents, "10000")
}

func TestAdvancePaymentFieldsAreAbsentWithoutScheduleOrCosts(t *testing.T) {
	p := advancePoint("2026-01-01", "2027-01-01")
	s := summaryFor(t, p, 2026)
	if s.AdvancePaymentCents != nil || s.EstimatedBalanceCents != nil {
		t.Fatal("no schedule", s)
	}
	p.AdvancePayments = []AdvancePayment{{From: "2026-01-01", Cents: 10000}}
	p.Tariffs = Tariffs{}
	s = summaryFor(t, p, 2026)
	requireCents(t, s.AdvancePaymentCents, "120000")
	if s.NetCents != nil || s.EstimatedBalanceCents != nil {
		t.Fatal("balance requires costs", s)
	}
}

func TestValidateAdvancePayments(t *testing.T) {
	p := advancePoint("2026-01-01", "2027-01-01")
	if err := p.ValidateAdvancePayments([]AdvancePayment{}); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateAdvancePayments([]AdvancePayment{{From: "2026-01-01", Cents: 12356}, {From: "2026-08-15", Cents: 0}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		schedule []AdvancePayment
		code     string
	}{
		{[]AdvancePayment{{From: "2026-01-01", Cents: 1}, {From: "2026-01-01", Cents: 2}}, "ADVANCE_PAYMENT_DUPLICATE"},
		{[]AdvancePayment{{From: "2026-01-01", Cents: -1}}, "INVALID_ADVANCE_PAYMENT"},
		{[]AdvancePayment{{From: "2026-02-30", Cents: 1}}, "INVALID_ADVANCE_PAYMENT"},
		{[]AdvancePayment{{From: "2026-1-01", Cents: 1}}, "INVALID_ADVANCE_PAYMENT"},
		{[]AdvancePayment{{From: "", Cents: 1}}, "INVALID_ADVANCE_PAYMENT"},
	} {
		if err := p.ValidateAdvancePayments(tc.schedule); Code(err) != tc.code {
			t.Fatal(tc.schedule, err)
		}
	}
	p.Meter.Type = "BIDIRECTIONAL"
	if err := p.ValidateAdvancePayments(nil); err != nil {
		t.Fatal("bidirectional meters measure electricity", err)
	}
	p.Meter.Type = "WATER"
	if err := p.ValidateAdvancePayments(nil); Code(err) != "ADVANCE_PAYMENTS_UNSUPPORTED" {
		t.Fatal(err)
	}
	p.Meter.Type, p.Kind = "ELECTRICITY", "VIRTUAL"
	if err := p.ValidateAdvancePayments(nil); Code(err) != "ADVANCE_PAYMENTS_UNSUPPORTED" {
		t.Fatal(err)
	}
}

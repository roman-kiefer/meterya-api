package domain

import (
	"fmt"
	"testing"
	"time"
)

func TestImportAndExportAnnualStatements(t *testing.T) {
	cases := []struct {
		year                                      int
		direction, opening                        string
		values                                    []int
		rate, base, first, normal                 int64
		quantity, energy, payment, total, balance string
	}{
		{2024, "IMPORT", "1986", []int{2671, 3184, 3575, 3882, 4080, 4269, 4496, 4663, 4991, 5345, 6005, 6721}, 35900, 1390, 14400, 14400, "4735.000000", "169987", "172800", "186667", "13867"},
		{2024, "EXPORT", "1794", []int{1875, 1960, 2232, 2561, 2964, 3471, 4028, 4773, 5145, 5306, 5345, 5370}, 8200, 0, 311, 2100, "3576.000000", "29323", "23411", "29323", "5912"},
		{2025, "IMPORT", "6721", []int{7413, 7917, 8257, 8469, 8610, 8780, 8920, 9106, 9369, 9762, 10311, 10913}, 35900, 1390, 13900, 13900, "4192.000000", "150493", "166800", "167173", "373"},
		{2025, "EXPORT", "5370", []int{5446, 5588, 6019, 6510, 7091, 7624, 8040, 8603, 8798, 8872, 8914, 8934}, 8200, 0, 2100, 2100, "3564.000000", "29225", "25200", "29225", "4025"},
		{2026, "IMPORT", "10913", []int{11548, 12047, 12270, 12379, 12456, 12633, 12750, 12923, 13109}, 32900, 1390, 12800, 12800, "2196.000000", "72248", "115200", "84758", "-30442"},
		{2026, "EXPORT", "8934", []int{8946, 9003, 9334, 9764, 10164, 10626, 11192, 11619, 11980}, 8200, 0, 2100, 2100, "3046.000000", "24977", "18900", "24977", "6077"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d-%s", c.year, c.direction), func(t *testing.T) {
			s := MonthlyStatement{Year: c.year, Direction: c.direction, OpeningDate: fmt.Sprintf("%d-12-31", c.year-1), OpeningValue: c.opening}
			for i, v := range c.values {
				payment := c.normal
				if i == 0 {
					payment = c.first
				}
				s.Months = append(s.Months, StatementMonth{Date: time.Date(c.year, time.Month(i+2), 0, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), Value: fmt.Sprint(v), UnitCents: c.rate, BaseCents: c.base, PaymentCents: payment})
			}
			b, err := s.Bill()
			if err != nil || b.Direction != c.direction || b.Quantity != c.quantity || b.EnergyCents != c.energy || b.PaymentCents != c.payment || b.TotalCents != c.total || b.BalanceCents != c.balance || b.Complete != (c.year != 2026) {
				t.Fatal(b, err)
			}
		})
	}
}

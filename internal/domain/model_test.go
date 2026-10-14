package domain

import "testing"

func TestDecimal(t *testing.T) {
	for _, v := range []string{"0", "1.23", "99999999999999.999999"} {
		if _, err := Decimal(v); err != nil {
			t.Fatal(v, err)
		}
	}
	for _, v := range []string{"-1", "1e2", "NaN", "1,2", "01", "1.0000001", "100000000000000"} {
		if _, err := Decimal(v); err == nil {
			t.Fatal("accepted", v)
		}
	}
	a, _ := Decimal("99999999999999.999998")
	b, _ := Decimal("99999999999999.999999")
	if Compare(a, b) != -1 {
		t.Fatal("precision lost")
	}
}
func TestBackdatedReading(t *testing.T) {
	p := Point{Meter: Meter{Registers: []Register{{ID: "r"}}}, Readings: []Reading{{RegisterID: "r", Date: "2026-01-01", Value: "10.000000"}, {RegisterID: "r", Date: "2026-03-01", Value: "30.000000"}}}
	for _, tc := range []struct{ date, value, code string }{{"2026-02-01", "20.000000", ""}, {"2026-02-01", "9.000000", "READING_DECREASES"}, {"2026-02-01", "31.000000", "READING_DECREASES"}, {"2026-01-01", "10.000000", "READING_ALREADY_EXISTS"}, {"2027-01-01", "40.000000", "INVALID_DATE"}} {
		err := p.ValidateReading(Reading{RegisterID: "r", Date: tc.date, Value: tc.value}, "2026-10-04")
		if tc.code == "" && err != nil || tc.code != "" && (err == nil || Code(err) != tc.code) {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

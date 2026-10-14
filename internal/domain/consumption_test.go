package domain

import (
	"reflect"
	"testing"
)

func TestConsumptionIsExactAndSeparatesRegisters(t *testing.T) {
	p := Point{Meter: Meter{Registers: []Register{{ID: "import", Unit: "kWh"}, {ID: "export", Unit: "kWh"}}},
		Readings: []Reading{
			{RegisterID: "import", Date: "2026-10-05", Value: "99999999999999.999999"},
			{RegisterID: "export", Date: "2026-10-05", Value: "100.25"},
			{RegisterID: "import", Date: "2026-10-04", Value: "99999999999999.999998"},
			{RegisterID: "export", Date: "2026-10-04", Value: "100"},
		}}
	before := append([]Reading(nil), p.Readings...)
	got, err := p.Consumption()
	if err != nil {
		t.Fatal(err)
	}
	want := []ConsumptionInterval{{"import", "2026-10-04", "2026-10-05", "0.000001", "kWh"}, {"export", "2026-10-04", "2026-10-05", "0.250000", "kWh"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	if !reflect.DeepEqual(before, p.Readings) {
		t.Fatal("calculation changed readings")
	}
}

func TestBackdatedReadingSplitsConsumptionInterval(t *testing.T) {
	p := Point{Meter: Meter{Registers: []Register{{ID: "r", Unit: "m³"}}}, Readings: []Reading{
		{RegisterID: "r", Date: "2026-10-05", Value: "120"},
		{RegisterID: "r", Date: "2026-10-01", Value: "100"},
	}}
	got, err := p.Consumption()
	if err != nil || len(got) != 1 || got[0].Quantity != "20.000000" {
		t.Fatal(got, err)
	}
	p.Readings = append(p.Readings, Reading{RegisterID: "r", Date: "2026-10-03", Value: "107.5"})
	got, err = p.Consumption()
	if err != nil || len(got) != 2 || got[0].Quantity != "12.500000" || got[1].Quantity != "7.500000" {
		t.Fatal(got, err)
	}
	if got[0].StartDate != "2026-10-03" || got[1].EndDate != "2026-10-03" {
		t.Fatal(got)
	}
}

func TestConsumptionRequiresTwoReadingsAndAllowsZero(t *testing.T) {
	p := Point{Meter: Meter{Registers: []Register{{ID: "r", Unit: "kWh"}}}}
	for range 2 {
		got, err := p.Consumption()
		if err != nil || got == nil || len(got) != 0 {
			t.Fatal(got, err)
		}
		p.Readings = append(p.Readings, Reading{RegisterID: "r", Date: "2026-10-04", Value: "1"})
	}
	p.Readings[1].Date = "2026-10-05"
	got, err := p.Consumption()
	if err != nil || got[0].Quantity != "0.000000" {
		t.Fatal(got, err)
	}
	p.Readings[1].Value = "0.9"
	if _, err = p.Consumption(); Code(err) != "READING_DECREASES" {
		t.Fatal(err)
	}
}

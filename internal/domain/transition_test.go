package domain

import "testing"

func TestTransitionNeedsBothRegistersAtBoundary(t *testing.T) {
	makePoint := func(id string) Point {
		return Point{ID: id, WorkspaceID: "ws", Kind: "PHYSICAL", Meter: Meter{Type: "BIDIRECTIONAL", Registers: []Register{{ID: id + "i", Unit: "kWh", Direction: "IMPORT"}, {ID: id + "e", Unit: "kWh", Direction: "EXPORT"}}}, Readings: []Reading{{RegisterID: id + "i", Date: "2026-10-01", Value: "0"}, {RegisterID: id + "e", Date: "2026-10-01", Value: "0"}}}
	}
	points := map[string]Point{"old": makePoint("old"), "z1": makePoint("z1"), "z2": makePoint("z2")}
	v := MeterTransition{Date: "2026-10-01", OldPoint: "old", NewPoints: []string{"z1", "z2"}}
	if err := v.Validate(points, "2026-10-06"); err != nil {
		t.Fatal(err)
	}
	p := points["z2"]
	p.Readings = p.Readings[:1]
	points["z2"] = p
	if err := v.Validate(points, "2026-10-06"); Code(err) != "TRANSITION_READING_REQUIRED" {
		t.Fatal(err)
	}
	points["z2"] = makePoint("z2")
	p = points["old"]
	p.Readings = append(p.Readings, Reading{Date: "2026-10-02"})
	points["old"] = p
	if err := v.Validate(points, "2026-10-06"); Code(err) != "READING_OUTSIDE_METER_PERIOD" {
		t.Fatal(err)
	}
	points["old"] = makePoint("old")
	p = points["z1"]
	p.Readings = append(p.Readings, Reading{Date: "2026-09-30"})
	points["z1"] = p
	if err := v.Validate(points, "2026-10-06"); Code(err) != "READING_OUTSIDE_METER_PERIOD" {
		t.Fatal(err)
	}
}

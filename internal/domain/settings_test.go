package domain

import "testing"

func TestSerialCorrectionPreservesReadings(t *testing.T) {
	p := Point{Name: "Haus", Version: 3, Meter: Meter{ID: "meter", Serial: "wrong", Registers: []Register{{ID: "register"}}}, Readings: []Reading{{ID: "reading", Value: "123.000000"}}}
	if err := p.ChangeDetails("Haus", " right "); err != nil || p.Meter.Serial != "right" || p.Meter.ID != "meter" || p.Version != 4 || p.Readings[0].ID != "reading" {
		t.Fatal(p, err)
	}
	if err := p.ChangeDetails("Haus", " "); Code(err) != "VALIDATION_FAILED" || p.Version != 4 || p.Meter.Serial != "right" {
		t.Fatal(p, err)
	}
}

func TestManagementPreservesHistoryAndRejectsInvalidChanges(t *testing.T) {
	p := Point{Name: "Haus", Version: 2, Readings: []Reading{{ID: "r", Value: "1.000000"}}, Meter: Meter{Serial: "123"}}
	name := "  Wohnung  "
	if kind, err := p.Manage(&name, nil); err != nil || kind != "PointRenamed" || p.Name != "Wohnung" || p.Version != 3 {
		t.Fatal(p, kind, err)
	}
	archived := true
	if kind, err := p.Manage(nil, &archived); err != nil || kind != "PointArchived" || !p.Archived {
		t.Fatal(p, kind, err)
	}
	if len(p.Readings) != 1 || p.Readings[0].ID != "r" || p.Meter.Serial != "123" {
		t.Fatal("history lost", p)
	}
	version := p.Version
	if _, err := p.Manage(nil, &archived); Code(err) != "CONFLICT" || p.Version != version {
		t.Fatal(p, err)
	}
	archived = false
	if kind, err := p.Manage(nil, &archived); err != nil || kind != "PointRestored" || p.Archived {
		t.Fatal(p, kind, err)
	}
	name = " "
	version = p.Version
	if _, err := p.Manage(&name, nil); Code(err) != "VALIDATION_FAILED" || p.Version != version {
		t.Fatal(p, err)
	}
	if _, err := p.Manage(&name, &archived); Code(err) != "VALIDATION_FAILED" {
		t.Fatal(err)
	}
}

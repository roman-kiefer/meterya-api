package domain

import (
	"testing"
	"time"
)

func correctionPoint() Point {
	return Point{Version: 3, Meter: Meter{Registers: []Register{{ID: "r"}}}, Readings: []Reading{{ID: "a", RegisterID: "r", Date: "2026-01-01", Value: "10.000000", Actor: "alice", RecordedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}, {ID: "b", RegisterID: "r", Date: "2026-02-01", Value: "20.000000", Actor: "alice"}, {ID: "c", RegisterID: "r", Date: "2026-03-01", Value: "30.000000", Actor: "bob"}}}
}
func TestReadingCorrectionOwnershipValidationAndReplay(t *testing.T) {
	p := correctionPoint()
	original := p.Readings[1]
	if _, err := p.ChangeReading(ReadingChange{ID: "b", Date: "2026-02-01", Value: "22"}, "bob", "USER", "2026-10-07"); Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	for _, c := range []ReadingChange{{ID: "b", Date: "2026-02-01", Value: "31"}, {ID: "b", Date: "2026-02-01", Value: "9"}, {ID: "b", Date: "2026-01-01", Value: "20"}, {ID: "b", Date: "2027-01-01", Value: "20"}, {ID: "b", Date: "2026-02-01", Value: "-1"}} {
		if _, err := p.ChangeReading(c, "alice", "USER", "2026-10-07"); err == nil {
			t.Fatal("accepted invalid correction", c)
		}
		if p.Version != 3 || p.Readings[1] != original {
			t.Fatal("invalid correction mutated state")
		}
	}
	event, err := p.ChangeReading(ReadingChange{ID: "b", Date: "2026-02-02", Value: "22.000001", Reason: " typo "}, "admin", "ADMIN", "2026-10-07")
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != 4 || p.Readings[1].Actor != "alice" || event.Reason != "typo" || p.Readings[1].Value != "22.000001" {
		t.Fatal(p, event)
	}
	replay := correctionPoint()
	if err = replay.ApplyReadingChange(event); err != nil || replay.Readings[1] != p.Readings[1] {
		t.Fatal("replay differs", err)
	}
	event, err = p.ChangeReading(ReadingChange{ID: "b", Retract: true}, "alice", "USER", "2026-10-07")
	if err != nil {
		t.Fatal(err)
	}
	if err = replay.ApplyReadingChange(event); err != nil || len(replay.Readings) != 2 || len(p.Readings) != 2 {
		t.Fatal("retraction replay", err)
	}
	if _, err = p.ChangeReading(ReadingChange{ID: "b", Retract: true}, "alice", "USER", "2026-10-07"); Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
}

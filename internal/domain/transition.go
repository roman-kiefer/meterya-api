package domain

import "time"

type MeterTransition struct {
	Continuation string   `json:"continuation_point,omitempty"`
	ID           string   `json:"id"`
	Date         string   `json:"date"`
	OldPoint     string   `json:"old_point"`
	NewPoints    []string `json:"new_points"`
}

func (v MeterTransition) Validate(points map[string]Point, today string) error {
	d, err := time.Parse(time.DateOnly, v.Date)
	if err != nil || d.Year() < 1900 || d.Year() > 9998 || v.Date > today || len(v.NewPoints) < 1 || len(v.NewPoints) > 2 {
		return Fail("VALIDATION_FAILED")
	}
	old, ok := points[v.OldPoint]
	if !ok || old.Kind != "PHYSICAL" {
		return Fail("RESOURCE_NOT_FOUND")
	}
	seen := map[string]bool{v.OldPoint: true}
	check := func(p Point, old bool) error {
		if len(p.Meter.Registers) == 0 {
			return Fail("VALIDATION_FAILED")
		}
		for _, r := range p.Readings {
			if (old && r.Date > v.Date) || (!old && r.Date < v.Date) {
				return Fail("READING_OUTSIDE_METER_PERIOD")
			}
		}
		for _, reg := range p.Meter.Registers {
			found := false
			for _, r := range p.Readings {
				if r.RegisterID == reg.ID && r.Date == v.Date {
					found = true
				}
			}
			if !found {
				return Fail("TRANSITION_READING_REQUIRED")
			}
		}
		return nil
	}
	if err = check(old, true); err != nil {
		return err
	}
	for _, id := range v.NewPoints {
		p, ok := points[id]
		if !ok || p.WorkspaceID != old.WorkspaceID {
			return Fail("RESOURCE_NOT_FOUND")
		}
		if seen[id] || p.Archived || p.Kind != "PHYSICAL" || p.Meter.Type != old.Meter.Type {
			return Fail("VALIDATION_FAILED")
		}
		seen[id] = true
		if err = check(p, false); err != nil {
			return err
		}
	}
	return nil
}

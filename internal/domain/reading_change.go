package domain

import (
	"strings"
	"unicode/utf8"
)

type ReadingChange struct {
	ID      string `json:"id"`
	Date    string `json:"date"`
	Value   string `json:"value"`
	Reason  string `json:"reason"`
	Retract bool   `json:"retract"`
}
type ReadingChangeApplied struct {
	ID      string   `json:"id"`
	Reading *Reading `json:"reading"`
	Reason  string   `json:"reason,omitempty"`
}

// ChangeReading retains the original owner and recording time. The separate
// correction event records its own actor and time for audit and replay.
func (p *Point) ChangeReading(c ReadingChange, subject, role, today string) (ReadingChangeApplied, error) {
	event := ReadingChangeApplied{ID: c.ID, Reason: strings.TrimSpace(c.Reason)}
	if utf8.RuneCountInString(event.Reason) > 500 {
		return event, Fail("VALIDATION_FAILED")
	}
	var original *Reading
	for i := range p.Readings {
		if p.Readings[i].ID == c.ID {
			original = &p.Readings[i]
			break
		}
	}
	if original == nil {
		return event, Fail("RESOURCE_NOT_FOUND")
	}
	if role != "ADMIN" && role != "MODERATOR" && (role != "USER" || original.Actor != subject) {
		return event, Fail("PERMISSION_DENIED")
	}
	if !c.Retract {
		updated := *original
		value, err := Decimal(c.Value)
		if err != nil {
			return event, err
		}
		updated.Date, updated.Value = c.Date, value
		candidate := *p
		candidate.Readings = nil
		for _, r := range p.Readings {
			if r.ID != c.ID {
				candidate.Readings = append(candidate.Readings, r)
			}
		}
		if err = candidate.ValidateReading(updated, today); err != nil {
			return event, err
		}
		event.Reading = &updated
	}
	if err := p.ApplyReadingChange(event); err != nil {
		return event, err
	}
	p.Version++
	return event, nil
}

// ApplyReadingChange changes only the current projection, never earlier events.
func (p *Point) ApplyReadingChange(event ReadingChangeApplied) error {
	for i, r := range p.Readings {
		if r.ID != event.ID {
			continue
		}
		if event.Reading == nil {
			p.Readings = append(p.Readings[:i:i], p.Readings[i+1:]...)
		} else {
			if event.Reading.ID != event.ID || event.Reading.RegisterID != r.RegisterID {
				return Fail("VALIDATION_FAILED")
			}
			p.Readings[i] = *event.Reading
		}
		return nil
	}
	return Fail("RESOURCE_NOT_FOUND")
}

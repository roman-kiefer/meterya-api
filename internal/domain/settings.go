package domain

import "strings"

func ValidName(name string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= 200
}

func (p *Point) ChangeDetails(name, serial string) error {
	name, serial = strings.TrimSpace(name), strings.TrimSpace(serial)
	if !ValidName(name) || serial == "" || len(serial) > 100 {
		return Fail("VALIDATION_FAILED")
	}
	p.Name, p.Meter.Serial = name, serial
	p.Version++
	return nil
}

// Manage changes metadata and visibility without removing readings or tariffs.
func (p *Point) Manage(name *string, archived *bool) (string, error) {
	if (name == nil) == (archived == nil) {
		return "", Fail("VALIDATION_FAILED")
	}
	if name != nil {
		value := strings.TrimSpace(*name)
		if !ValidName(value) {
			return "", Fail("VALIDATION_FAILED")
		}
		p.Name = value
		p.Version++
		return "PointRenamed", nil
	}
	if p.Archived == *archived {
		return "", Fail("CONFLICT")
	}
	p.Archived = *archived
	p.Version++
	if p.Archived {
		return "PointArchived", nil
	}
	return "PointRestored", nil
}

package domain

import (
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"
)

type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }
func Fail(code string) error   { return &Error{Code: code} }
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "INTERNAL_ERROR"
}

var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,13})(\.[0-9]{1,6})?$`)

func Decimal(value string) (string, error) {
	if !decimalPattern.MatchString(value) {
		return "", Fail("INVALID_DECIMAL")
	}
	parts := strings.Split(value, ".")
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	return parts[0] + "." + fraction + strings.Repeat("0", 6-len(fraction)), nil
}
func Compare(a, b string) int {
	x, _ := new(big.Int).SetString(strings.ReplaceAll(a, ".", ""), 10)
	y, _ := new(big.Int).SetString(strings.ReplaceAll(b, ".", ""), 10)
	return x.Cmp(y)
}

type Register struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
	Unit      string `json:"unit"`
}
type Reading struct {
	ID         string    `json:"id"`
	RegisterID string    `json:"register_id"`
	Date       string    `json:"date"`
	Value      string    `json:"value"`
	Actor      string    `json:"actor"`
	RecordedAt time.Time `json:"recorded_at"`
}
type Meter struct {
	ID        string     `json:"id"`
	Serial    string     `json:"serial"`
	Type      string     `json:"type"`
	Registers []Register `json:"registers"`
}
type Point struct {
	DerivedSeries     bool               `json:"-"`
	ID                string             `json:"id"`
	WorkspaceID       string             `json:"workspace_id"`
	Name              string             `json:"name"`
	Kind              string             `json:"kind"`
	Version           int64              `json:"version"`
	Archived          bool               `json:"archived"`
	Meter             Meter              `json:"meter"`
	Readings          []Reading          `json:"readings"`
	Tariffs           Tariffs            `json:"tariffs"`
	MonthlyStatements []MonthlyStatement `json:"monthly_statements,omitempty"`
}

func (p Point) ValidateReading(r Reading, today string) error {
	if d, err := time.Parse("2006-01-02", r.Date); err != nil || d.Year() < 1900 || d.Year() > 9998 || r.Date > today {
		return Fail("INVALID_DATE")
	}
	found := false
	for _, reg := range p.Meter.Registers {
		if reg.ID == r.RegisterID {
			found = true
		}
	}
	if !found {
		return Fail("RESOURCE_NOT_FOUND")
	}
	for _, existing := range p.Readings {
		if existing.RegisterID != r.RegisterID {
			continue
		}
		if existing.Date == r.Date {
			return Fail("READING_ALREADY_EXISTS")
		}
	}
	candidate := p
	candidate.Readings = append(append([]Reading(nil), p.Readings...), r)
	calendar, err := candidate.calendarReadings()
	if err != nil {
		return err
	}
	boundary := ReadingBoundary(r.Date)
	for _, selected := range calendar {
		if selected.RegisterID == r.RegisterID && ReadingBoundary(selected.Date) == boundary && selected.Date != r.Date {
			return nil
		}
	}
	for _, existing := range calendar {
		if existing.RegisterID != r.RegisterID {
			continue
		}
		d := ReadingBoundary(existing.Date)
		if d < boundary && Compare(existing.Value, r.Value) > 0 || d > boundary && Compare(existing.Value, r.Value) < 0 {
			return Fail("READING_DECREASES")
		}
	}
	return nil
}

type Event struct {
	ID          string `json:"id"`
	AggregateID string `json:"aggregate_id"`
	WorkspaceID string `json:"workspace_id"`
	Version     int64  `json:"version"`
	Type        string `json:"type"`
	Actor       string `json:"actor"`
	Payload     []byte `json:"-"`
}

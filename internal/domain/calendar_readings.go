package domain

import "time"

// ReadingBoundary treats the last day of a month and the following first
// day as the same billing boundary. The recorded date is never rewritten.
func ReadingBoundary(value string) string {
	d, err := time.Parse(time.DateOnly, value)
	if err == nil && d.AddDate(0, 0, 1).Day() == 1 {
		return d.AddDate(0, 0, 1).Format(time.DateOnly)
	}
	return value
}

func (p Point) calendarReadings() ([]Reading, error) {
	result := []Reading{}
	type key struct{ register, date string }
	seen := map[key]int{}
	for _, original := range p.Readings {
		r := original
		_, err := p.measurementDecimal(r.Value)
		if err != nil {
			return nil, err
		}
		k := key{r.RegisterID, ReadingBoundary(r.Date)}
		if previous, ok := seen[k]; ok {
			if result[previous].Date == r.Date {
				return nil, Fail("READING_ALREADY_EXISTS")
			}
			// Month-end wins over the next day's reading, regardless of order.
			if r.Date != k.date {
				result[previous] = r
			}
			continue
		}
		seen[k] = len(result)
		result = append(result, r)
	}
	return result, nil
}

// Only calculator-local logical series may exceed physical counter limits.
func (p Point) measurementDecimal(value string) (string, error) {
	if !p.DerivedSeries {
		return Decimal(value)
	}
	q := exact(value)
	if q == nil || q.Sign() < 0 {
		return "", Fail("INVALID_DECIMAL")
	}
	return q.FloatString(6), nil
}

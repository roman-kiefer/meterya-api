package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"sort"
	"time"
)

type MonthlyImport struct {
	WorkspaceName string                    `json:"workspace_name"`
	PointName     string                    `json:"point_name"`
	Serial        string                    `json:"serial"`
	Tariffs       domain.Tariffs            `json:"tariffs"`
	Statements    []domain.MonthlyStatement `json:"statements"`
}

func (input MonthlyImport) Readings(register, subject, today string) ([]domain.Reading, error) {
	return input.ReadingsFor([]domain.Register{{ID: register, Unit: "kWh", Direction: "IMPORT"}}, subject, today)
}

func (input MonthlyImport) ReadingsFor(registers []domain.Register, subject, today string) ([]domain.Reading, error) {
	if len(input.Statements) == 0 || len(input.Statements) > 100 {
		return nil, domain.Fail("VALIDATION_FAILED")
	}
	byDirection := map[string]string{}
	registerIDs := map[string]bool{}
	for _, r := range registers {
		if r.Unit != "kWh" || (r.Direction != "IMPORT" && r.Direction != "EXPORT") || byDirection[r.Direction] != "" || r.ID == "" || registerIDs[r.ID] {
			return nil, domain.Fail("REGISTER_NOT_FOUND")
		}
		byDirection[r.Direction] = r.ID
		registerIDs[r.ID] = true
	}
	type readingKey struct{ Register, Date string }
	values := map[readingKey]string{}
	years := map[string]bool{}
	add := func(register, date, value string) error {
		v, e := domain.Decimal(value)
		if e != nil {
			return e
		}
		key := readingKey{register, date}
		if old, ok := values[key]; ok && old != v {
			return domain.Fail("CONFLICT")
		}
		values[key] = v
		return nil
	}
	for _, s := range input.Statements {
		register := byDirection[s.EffectiveDirection()]
		if register == "" {
			return nil, domain.Fail("REGISTER_NOT_FOUND")
		}
		year := fmt.Sprintf("%s:%d", register, s.Year)
		if years[year] {
			return nil, domain.Fail("CONFLICT")
		}
		years[year] = true
		if _, err := s.Bill(); err != nil {
			return nil, err
		}
		if err := add(register, s.OpeningDate, s.OpeningValue); err != nil {
			return nil, err
		}
		for _, m := range s.Months {
			if err := add(register, m.Date, m.Value); err != nil {
				return nil, err
			}
		}
	}
	readings := []domain.Reading{}
	for key, value := range values {
		readings = append(readings, domain.Reading{ID: uuid.NewString(), RegisterID: key.Register, Date: key.Date, Value: value, Actor: subject, RecordedAt: time.Now().UTC()})
	}
	sort.Slice(readings, func(i, j int) bool {
		if readings[i].Date == readings[j].Date {
			return readings[i].RegisterID < readings[j].RegisterID
		}
		return readings[i].Date < readings[j].Date
	})
	p := domain.Point{Meter: domain.Meter{Registers: registers}, Readings: []domain.Reading{}}
	for _, r := range readings {
		if err := p.ValidateReading(r, today); err != nil {
			return nil, err
		}
		p.Readings = append(p.Readings, r)
	}
	if err := p.ValidateTariffs(input.Tariffs); err != nil {
		return nil, err
	}
	return readings, nil
}

func (s *Store) ImportMonthly(ctx context.Context, c Command, input MonthlyImport) (domain.Point, error) {
	var p domain.Point
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	var role string
	if err = tx.QueryRow(ctx, roleQuery+" FOR SHARE", c.Workspace, c.Subject).Scan(&role); err != nil || !CanManage(role) {
		return p, domain.Fail("PERMISSION_DENIED")
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", c.Subject+":"+c.Key); err != nil {
		return p, err
	}
	if found, e := receipt(ctx, tx, c, &p); found || e != nil {
		if e == nil && (p.ID != c.PointID || p.WorkspaceID != c.Workspace) {
			return p, domain.Fail("RESOURCE_NOT_FOUND")
		}
		return p, e
	}
	var body []byte
	if err = tx.QueryRow(ctx, "SELECT state FROM points WHERE workspace_id=$1 AND id=$2 FOR UPDATE", c.Workspace, c.PointID).Scan(&body); err != nil {
		return p, mapError(err)
	}
	if err = json.Unmarshal(body, &p); err != nil {
		return p, err
	}
	if p.Version != c.Expected {
		return p, domain.Fail("AGGREGATE_VERSION_CONFLICT")
	}
	if p.Archived {
		return p, domain.Fail("POINT_ARCHIVED")
	}
	if p.Name != input.PointName || p.Meter.Serial != input.Serial || p.Kind != "PHYSICAL" {
		return p, domain.Fail("CONFLICT")
	}
	readings, err := input.ReadingsFor(p.Meter.Registers, c.Subject, c.Today)
	if err != nil {
		return p, err
	}
	appendEvent := func(kind string, payload any) error {
		b, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		p.Version++
		_, e = tx.Exec(ctx, "INSERT INTO events(id,aggregate_id,aggregate_version,workspace_id,event_type,actor,payload) VALUES($1,$2,$3,$4,$5,$6,$7)", uuid.NewString(), p.ID, p.Version, c.Workspace, kind, c.Subject, b)
		return e
	}
	for _, r := range readings {
		if err = readingWithinMeterPeriod(ctx, tx, c.Workspace, p.ID, r.Date); err != nil {
			return p, err
		}
		duplicate := false
		for _, old := range p.Readings {
			if old.RegisterID == r.RegisterID && old.Date == r.Date {
				if old.Value != r.Value {
					return p, domain.Fail("READING_ALREADY_EXISTS")
				}
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if err = p.ValidateReading(r, c.Today); err != nil {
			return p, err
		}
		if err = appendEvent("ReadingRecorded", r); err != nil {
			return p, err
		}
		if err = insertReading(ctx, tx, p.ID, r); err != nil {
			return p, err
		}
		p.Readings = append(p.Readings, r)
	}
	oldTariffs, _ := json.Marshal(p.Tariffs)
	newTariffs, _ := json.Marshal(input.Tariffs)
	if !c.ReplaceImportTariffs && (len(p.Tariffs.BasePrices) > 0 || len(p.Tariffs.UnitPrices) > 0) {
		if string(oldTariffs) != string(newTariffs) {
			return p, domain.Fail("TARIFF_OVERLAP")
		}
	} else {
		p.Tariffs = input.Tariffs
		if err = appendEvent("TariffScheduleSaved", p.Tariffs); err != nil {
			return p, err
		}
	}
	// Bind supplied directions to the actual existing registers, never file UUIDs.
	statements := append([]domain.MonthlyStatement(nil), input.Statements...)
	for i := range statements {
		statements[i].Direction = statements[i].EffectiveDirection()
		for _, r := range p.Meter.Registers {
			if r.Direction == statements[i].Direction {
				statements[i].RegisterID = r.ID
			}
		}
	}
	for _, incoming := range statements {
		for _, old := range p.MonthlyStatements {
			if old.Year == incoming.Year && old.RegisterID == incoming.RegisterID {
				return p, domain.Fail("CONFLICT")
			}
		}
	}
	if err = appendEvent("MonthlyStatementsImported", statements); err != nil {
		return p, err
	}
	p.MonthlyStatements = append(p.MonthlyStatements, statements...)
	state, _ := json.Marshal(p)
	if _, err = tx.Exec(ctx, "UPDATE points SET state=$1,version=$2 WHERE id=$3", state, p.Version, p.ID); err != nil {
		return p, err
	}
	if err = replaceAnnualProjections(ctx, tx, p); err != nil {
		return p, err
	}
	if err = saveReceipt(ctx, tx, c, p); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

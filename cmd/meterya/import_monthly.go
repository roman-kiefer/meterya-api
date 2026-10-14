package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/google/uuid"
	"io"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"os"
	"time"
)

func importMonthly(ctx context.Context, s *store.Store) error {
	f := flag.NewFlagSet("import-monthly", flag.ContinueOnError)
	email := f.String("admin-email", "", "Existing administrator email")
	replaceTariffs := f.Bool("replace-tariffs", false, "Replace the named target's tariff schedule with the import schedule")
	existingPoint := f.Bool("existing-point", false, "Require the named workspace and meter to exist")
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *email == "" {
		return fmt.Errorf("admin-email is required")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return domain.Fail("VALIDATION_FAILED")
	}
	var input store.MonthlyImport
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return domain.Fail("VALIDATION_FAILED")
	}
	if !domain.ValidName(input.WorkspaceName) || !domain.ValidName(input.PointName) || !domain.ValidName(input.Serial) || len(input.Serial) > 100 {
		return domain.Fail("VALIDATION_FAILED")
	}
	zone, _ := time.LoadLocation("Europe/Berlin")
	today := time.Now().In(zone).Format(time.DateOnly)
	if _, err = input.ReadingsFor([]domain.Register{{ID: uuid.NewString(), Unit: "kWh", Direction: "IMPORT"}, {ID: uuid.NewString(), Unit: "kWh", Direction: "EXPORT"}}, "validation", today); err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, "SELECT DISTINCT subject FROM members WHERE email=$1 AND role='ADMIN'", *email)
	if err != nil {
		return err
	}
	subjects := []string{}
	for rows.Next() {
		var subject string
		if err = rows.Scan(&subject); err != nil {
			rows.Close()
			return err
		}
		subjects = append(subjects, subject)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(subjects) != 1 {
		return fmt.Errorf("admin email must identify exactly one existing administrator")
	}
	subject := subjects[0]
	fingerprint := store.Hash(fmt.Sprintf("%s:replace=%t:existing=%t", raw, *replaceTariffs, *existingPoint))
	command := func(action string) store.Command {
		return store.Command{Subject: subject, Key: uuid.NewSHA1(uuid.NameSpaceURL, []byte("meterya-monthly:"+fingerprint+":"+action)).String(), Fingerprint: action + ":" + fingerprint, Today: today}
	}
	workspaces, err := s.Workspaces(ctx, subject)
	if err != nil {
		return err
	}
	var workspace *store.Member
	for i := range workspaces {
		if workspaces[i].Name == input.WorkspaceName {
			if workspace != nil {
				return fmt.Errorf("workspace name is ambiguous")
			}
			workspace = &workspaces[i]
		}
	}
	if workspace == nil {
		if *existingPoint {
			return domain.Fail("RESOURCE_NOT_FOUND")
		}
		created, e := s.CreateWorkspace(ctx, command("workspace"), input.WorkspaceName, *email)
		if e != nil {
			return e
		}
		workspace = &created
	}
	if workspace.Role != "ADMIN" {
		return domain.Fail("PERMISSION_DENIED")
	}
	points, err := s.Points(ctx, workspace.ID)
	if err != nil {
		return err
	}
	var point *domain.Point
	for i := range points {
		if points[i].Meter.Serial == input.Serial && !points[i].Archived {
			point = &points[i]
			break
		}
	}
	if point == nil {
		if *existingPoint {
			return domain.Fail("RESOURCE_NOT_FOUND")
		}
		p := domain.Point{ID: uuid.NewString(), WorkspaceID: workspace.ID, Name: input.PointName, Kind: "PHYSICAL", Readings: []domain.Reading{}, Meter: domain.Meter{ID: uuid.NewString(), Serial: input.Serial, Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Unit: "kWh", Direction: "IMPORT"}}}}
		for _, statement := range input.Statements {
			if statement.EffectiveDirection() == "EXPORT" {
				p.Meter.Type = "BIDIRECTIONAL"
				p.Meter.Registers = append(p.Meter.Registers, domain.Register{ID: uuid.NewString(), Unit: "kWh", Direction: "EXPORT"})
				break
			}
		}
		c := command("point")
		c.Workspace = workspace.ID
		c.NewPoint = &p
		created, e := s.Execute(ctx, c)
		if e != nil {
			return e
		}
		point = &created
	}
	c := command("import")
	c.Workspace = workspace.ID
	c.PointID = point.ID
	c.Expected = point.Version
	c.ReplaceImportTariffs = *replaceTariffs
	result, err := s.ImportMonthly(ctx, c, input)
	if err != nil {
		return err
	}
	bills, err := result.MonthlyBills()
	if err != nil {
		return err
	}
	summaries := []map[string]any{}
	for _, bill := range bills {
		summaries = append(summaries, map[string]any{"year": bill.Year, "direction": bill.Direction, "months": len(bill.Rows), "quantity_kwh": bill.Quantity, "total_cents": bill.TotalCents, "payment_cents": bill.PaymentCents, "balance_cents": bill.BalanceCents})
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"imported": true, "workspace": workspace.Name, "point": result.Name, "serial": result.Meter.Serial, "reading_count": len(result.Readings), "summaries": summaries})
}

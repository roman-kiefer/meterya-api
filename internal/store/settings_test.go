package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
	"time"
)

func TestSettingsAreRestrictedVersionedAndReplayable(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Haushalt", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,'user','user@example.test','USER')", ws); err != nil {
		t.Fatal(err)
	}
	c := Command{Workspace: ws, Subject: "user", Key: uuid.NewString(), Fingerprint: "workspace-name", Expected: 1}
	if _, err = s.RenameWorkspace(ctx, c, "Wohnung"); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	c.Subject = "admin"
	renamed, err := s.RenameWorkspace(ctx, c, "Wohnung")
	if err != nil || renamed.Version != 2 || renamed.Name != "Wohnung" {
		t.Fatal(renamed, err)
	}
	if repeated, err := s.RenameWorkspace(ctx, c, "Wohnung"); err != nil || repeated.Version != 2 {
		t.Fatal(repeated, err)
	}
	c.Key = uuid.NewString()
	if _, err = s.RenameWorkspace(ctx, c, "Stale"); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal(err)
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: "Alt", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "123", Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "create", NewPoint: &p})
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2026-01-01", "2026-01-02"} {
		r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: date, Value: "1.000000", Actor: "admin", RecordedAt: time.Now().UTC()}
		p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: date, PointID: p.ID, Expected: p.Version, Reading: &r, Today: "2026-10-05"})
		if err != nil {
			t.Fatal(err)
		}
	}
	name := "Neu"
	c = Command{Workspace: ws, Subject: "user", Key: uuid.NewString(), Fingerprint: "name", PointID: p.ID, Expected: p.Version, Name: &name}
	if _, err = s.Execute(ctx, c); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal(err)
	}
	c.Subject = "admin"
	p, err = s.Execute(ctx, c)
	if err != nil || p.Name != name {
		t.Fatal(p, err)
	}
	archive := true
	c = Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "archive", PointID: p.ID, Expected: p.Version, Archived: &archive}
	p, err = s.Execute(ctx, c)
	if err != nil || !p.Archived || len(p.Readings) != 2 {
		t.Fatal(p, err)
	}
	if repeat, err := s.Execute(ctx, c); err != nil || repeat.Version != p.Version {
		t.Fatal(repeat, err)
	}
	r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: "2026-01-03", Value: "2.000000", Actor: "admin", RecordedAt: time.Now().UTC()}
	if _, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "blocked", PointID: p.ID, Expected: p.Version, Reading: &r, Today: "2026-10-05"}); domain.Code(err) != "POINT_ARCHIVED" {
		t.Fatal(err)
	}
	// Serial reuse is allowed only after archival. Restoring a collision is atomic.
	second := p
	second.ID, second.Version, second.Archived, second.Readings = uuid.NewString(), 0, false, []domain.Reading{}
	second, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "second", NewPoint: &second})
	if err != nil {
		t.Fatal(err)
	}
	archive = false
	c = Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "restore", PointID: p.ID, Expected: p.Version, Archived: &archive}
	if _, err = s.Execute(ctx, c); domain.Code(err) != "SERIAL_ALREADY_ACTIVE" {
		t.Fatal(err)
	}
	current, err := s.Point(ctx, ws, p.ID)
	if err != nil || current.Version != p.Version || !current.Archived {
		t.Fatal(current, err)
	}
	archiveSecond := true
	if _, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "archive-second", PointID: second.ID, Expected: second.Version, Archived: &archiveSecond}); err != nil {
		t.Fatal(err)
	}
	p, err = s.Execute(ctx, c)
	if err != nil || p.Archived {
		t.Fatal(p, err)
	}
	before, _ := json.Marshal(p)
	summaryBefore, err := s.AnnualSummaries(ctx, ws, p.ID)
	if err != nil || len(summaryBefore) != 1 || summaryBefore[0].PointVersion != p.Version {
		t.Fatal(summaryBefore, err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	replayed, err := s.Point(ctx, ws, p.ID)
	after, _ := json.Marshal(replayed)
	if err != nil || string(before) != string(after) {
		t.Fatal("point replay differs", err)
	}
	workspaces, err := s.Workspaces(ctx, "admin")
	if err != nil || len(workspaces) != 1 || workspaces[0].Name != "Wohnung" || workspaces[0].Version != 2 {
		t.Fatal(workspaces, err)
	}
	if _, err = s.Point(ctx, uuid.NewString(), p.ID); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal("workspace leak", err)
	}
}

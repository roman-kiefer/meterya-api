package store

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"meterya.local/api/internal/domain"
	"testing"
	"time"
)

func encodeBackup(t *testing.T, p BackupPayload) []byte {
	t.Helper()
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(Backup{Format: "meterya-backup-v1", SHA256: Hash(string(payload)), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func TestBackupFormatRejectsCorruptionAndDifferentSchema(t *testing.T) {
	p := BackupPayload{CreatedAt: time.Now().UTC(), Schema: backupSchema(), Tables: map[string]json.RawMessage{}}
	for _, table := range backupTables {
		p.Tables[table] = json.RawMessage(`[]`)
	}
	p.Tables["events"] = json.RawMessage(`[{"sequence":9007199254740993}]`)
	body := encodeBackup(t, p)
	parsed, info, err := ParseBackup(body)
	if err != nil || info.Events != 1 || string(parsed.Tables["events"]) != string(p.Tables["events"]) {
		t.Fatal(info, err)
	}
	var b Backup
	json.Unmarshal(body, &b)
	b.SHA256 = "wrong"
	bad, _ := json.Marshal(b)
	if _, _, err = ParseBackup(bad); domain.Code(err) != "BACKUP_INVALID" {
		t.Fatal(err)
	}
	p.Schema = "old"
	if _, _, err = ParseBackup(encodeBackup(t, p)); domain.Code(err) != "BACKUP_SCHEMA_MISMATCH" {
		t.Fatal(err)
	}
	p.Schema = backupSchema()
	delete(p.Tables, "points")
	if _, _, err = ParseBackup(encodeBackup(t, p)); domain.Code(err) != "BACKUP_INVALID" {
		t.Fatal(err)
	}
}

func TestDevelopmentBackupSchemaIsRejected(t *testing.T) {
	p := BackupPayload{CreatedAt: time.Now().UTC(), Schema: "development-schema", Tables: map[string]json.RawMessage{}}
	for _, table := range backupTables {
		p.Tables[table] = json.RawMessage(`[]`)
	}
	_, _, err := ParseBackup(encodeBackup(t, p))
	if domain.Code(err) != "BACKUP_SCHEMA_MISMATCH" {
		t.Fatal(err)
	}
}
func TestBackupRestorePreservesMonthlyDataAndRollsBackFailure(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "Home", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: "Meter", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "001", Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Unit: "kWh", Direction: "IMPORT"}}}, Readings: []domain.Reading{}}
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "create", NewPoint: &p})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.ImportMonthly(ctx, Command{Workspace: ws, PointID: p.ID, Subject: "admin", Expected: p.Version, Key: uuid.NewString(), Fingerprint: "import", Today: "2026-10-06"}, monthlyFixture())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,subject,email,acr,csrf,expires_at) VALUES('session','admin','admin@example.test','2','csrf',now()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	body, err := s.Backup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Bootstrap(ctx, "Later", "other", "other@example.test"); err != nil {
		t.Fatal(err)
	}
	assertCurrent := func(workspaces, sessions int) {
		t.Helper()
		var w, n int
		if err := s.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM workspaces),(SELECT count(*) FROM sessions)").Scan(&w, &n); err != nil || w != workspaces || n != sessions {
			t.Fatal(w, n, err)
		}
		got, err := s.Point(ctx, ws, p.ID)
		before, _ := json.Marshal(p)
		after, _ := json.Marshal(got)
		if err != nil || string(before) != string(after) {
			t.Fatal("point data differs", err)
		}
	}
	if info, err := s.RestoreBackup(ctx, body, true); err != nil || info.Readings != 2 {
		t.Fatal(info, err)
	}
	assertCurrent(2, 1)
	parsed, _, _ := ParseBackup(body)
	parsed.Tables["members"] = json.RawMessage(`[{"workspace_id":"00000000-0000-4000-8000-000000000000","subject":"bad","email":"bad","role":"ADMIN"}]`)
	if _, err = s.RestoreBackup(ctx, encodeBackup(t, parsed), false); domain.Code(err) != "BACKUP_INVALID" {
		t.Fatal(err)
	}
	assertCurrent(2, 1)
	if _, err = s.RestoreBackup(ctx, body, false); err != nil {
		t.Fatal(err)
	}
	assertCurrent(1, 0)
	if _, err = s.CreateWorkspace(ctx, Command{Subject: "admin", Key: uuid.NewString(), Fingerprint: "after restore"}, "Restored new", "admin@example.test"); err != nil {
		t.Fatal("sequence not restored", err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Point(ctx, ws, p.ID)
	if err != nil || len(got.MonthlyStatements) != 1 || len(got.Readings) != 2 {
		t.Fatal(got, err)
	}
}

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"meterya.local/api/internal/domain"
	"time"
)

const MaxBackupBytes = 32 << 20

// The file can only supply data for these tables, never SQL or schema objects.
var backupTables = []string{"workspaces", "members", "events", "points", "readings", "annual_summaries", "invitations", "idempotency", "virtual_points"}

type BackupPayload struct {
	CreatedAt time.Time                  `json:"created_at"`
	Schema    string                     `json:"schema"`
	Tables    map[string]json.RawMessage `json:"tables"`
}
type Backup struct {
	Format  string          `json:"format"`
	SHA256  string          `json:"sha256"`
	Payload json.RawMessage `json:"payload"`
}
type BackupInfo struct {
	Photos         int       `json:"photos"`
	ContainsPhotos bool      `json:"contains_photos"`
	CreatedAt      time.Time `json:"created_at"`
	SHA256         string    `json:"sha256"`
	Workspaces     int       `json:"workspaces"`
	Points         int       `json:"points"`
	Readings       int       `json:"readings"`
	Events         int       `json:"events"`
}

// Bump this only when the serialized backup tables become incompatible.
const backupSchemaVersion = "meterya-release-schema-v1"

func backupSchema() string { return backupSchemaVersion }
func (s *Store) Backup(ctx context.Context) ([]byte, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var photos int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM photos WHERE point_id IS NOT NULL").Scan(&photos); err != nil {
		return nil, err
	}
	if photos > 0 {
		return nil, domain.Fail("PHOTO_ARCHIVE_REQUIRED")
	}
	result, err := backupTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
func backupTx(ctx context.Context, tx pgx.Tx) ([]byte, error) {
	var err error
	p := BackupPayload{CreatedAt: time.Now().UTC(), Schema: backupSchema(), Tables: map[string]json.RawMessage{}}
	size := 0
	for _, table := range backupTables {
		var body []byte
		query := "SELECT COALESCE(jsonb_agg(to_jsonb(t)), '[]'::jsonb) FROM " + table + " t"
		if table == "idempotency" {
			query += " WHERE NOT(t.response ? 'preview_width' AND NOT EXISTS(SELECT 1 FROM photos f WHERE f.point_id IS NOT NULL AND t.response->>'id'=f.id::text))"
		}
		if err = tx.QueryRow(ctx, query).Scan(&body); err != nil {
			return nil, err
		}
		size += len(body)
		if size > MaxBackupBytes-4096 {
			return nil, domain.Fail("BACKUP_TOO_LARGE")
		}
		p.Tables[table] = body
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	result, err := json.Marshal(Backup{Format: "meterya-backup-v1", SHA256: Hash(string(payload)), Payload: payload})
	if err != nil {
		return nil, err
	}
	if len(result) > MaxBackupBytes {
		return nil, domain.Fail("BACKUP_TOO_LARGE")
	}
	return result, nil
}
func strictJSON(b []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return domain.Fail("BACKUP_INVALID")
	}
	return nil
}
func ParseBackup(body []byte) (BackupPayload, BackupInfo, error) {
	var p BackupPayload
	info := BackupInfo{SHA256: Hash(string(body))}
	if len(body) > MaxBackupBytes {
		return p, info, domain.Fail("BACKUP_TOO_LARGE")
	}
	var b Backup
	if strictJSON(body, &b) != nil || b.Format != "meterya-backup-v1" || b.SHA256 != Hash(string(b.Payload)) {
		return p, info, domain.Fail("BACKUP_INVALID")
	}
	if strictJSON(b.Payload, &p) != nil || p.CreatedAt.IsZero() || len(p.Tables) != len(backupTables) {
		return p, info, domain.Fail("BACKUP_INVALID")
	}
	if p.Schema != backupSchema() {
		return p, info, domain.Fail("BACKUP_SCHEMA_MISMATCH")
	}
	counts := map[string]int{}
	for _, table := range backupTables {
		var rows []json.RawMessage
		b := bytes.TrimSpace(p.Tables[table])
		if len(b) == 0 || b[0] != '[' || json.Unmarshal(b, &rows) != nil {
			return p, info, domain.Fail("BACKUP_INVALID")
		}
		for _, row := range rows {
			row = bytes.TrimSpace(row)
			if len(row) == 0 || row[0] != '{' {
				return p, info, domain.Fail("BACKUP_INVALID")
			}
		}
		counts[table] = len(rows)
	}
	info.CreatedAt = p.CreatedAt
	info.Workspaces, info.Points, info.Readings, info.Events = counts["workspaces"], counts["points"], counts["readings"], counts["events"]
	return p, info, nil
}

// A preview performs the same import but rolls it back, including sequence
// changes. Constraints reject invalid data; failures preserve the old database.
func (s *Store) RestoreBackup(ctx context.Context, body []byte, validateOnly bool) (BackupInfo, error) {
	return s.restoreBackup(ctx, body, validateOnly, nil)
}
func (s *Store) restoreBackup(ctx context.Context, body []byte, validateOnly bool, photos func(pgx.Tx) error) (BackupInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	p, info, err := ParseBackup(body)
	if err != nil {
		return info, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return info, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout='5s'; SET LOCAL statement_timeout='120s'"); err != nil {
		return info, err
	}
	if _, err = tx.Exec(ctx, "TRUNCATE workspaces,members,events,points,readings,annual_summaries,invitations,idempotency,sessions,oidc_states,virtual_points,photos"); err != nil {
		return info, err
	}
	for _, table := range backupTables {
		if _, err = tx.Exec(ctx, "INSERT INTO "+table+" OVERRIDING SYSTEM VALUE SELECT * FROM jsonb_populate_recordset(NULL::"+table+",$1::jsonb)", []byte(p.Tables[table])); err != nil {
			return info, domain.Fail("BACKUP_INVALID")
		}
	}
	var next int64
	if photos != nil {
		if err = photos(tx); err != nil {
			return info, err
		}
	}
	var missing bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM events e LEFT JOIN photos f ON f.id::text=e.payload->>'id' WHERE e.event_type='ReadingPhotoAttached' AND f.id IS NULL)").Scan(&missing); err != nil {
		return info, err
	}
	if missing {
		return info, domain.Fail("PHOTO_MISSING")
	}
	if err = tx.QueryRow(ctx, "SELECT COALESCE(max(sequence),0)+1 FROM events").Scan(&next); err != nil || next < 1 {
		return info, domain.Fail("BACKUP_INVALID")
	}
	// ALTER identity RESTART is transactional; setval would escape a rollback.
	if _, err = tx.Exec(ctx, fmt.Sprintf("ALTER TABLE events ALTER COLUMN sequence RESTART WITH %d", next)); err != nil {
		return info, err
	}
	if validateOnly {
		return info, nil
	}
	return info, tx.Commit(ctx)
}

package store

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"image"
	"io"
	"io/fs"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/media"
	"os"
	"sort"
	"strings"
	"time"
)

const MaxArchiveBytes int64 = 50 << 30

type archiveLimitWriter struct {
	writer io.Writer
	size   int64
}

func (w *archiveLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > MaxArchiveBytes-w.size {
		return 0, domain.Fail("BACKUP_TOO_LARGE")
	}
	n, err := w.writer.Write(p)
	w.size += int64(n)
	return n, err
}

type backupPhoto struct {
	Photo      domain.Photo `json:"photo"`
	Workspace  string       `json:"workspace_id"`
	File       string       `json:"file"`
	EditedFile string       `json:"edited_file,omitempty"`
}
type photoManifest struct {
	Format     string        `json:"format"`
	DataSHA256 string        `json:"data_sha256"`
	Photos     []backupPhoto `json:"photos"`
}

// Originals and business data come from a single repeatable database snapshot.
func (s *Store) WriteBackupZIP(ctx context.Context, dest io.Writer, progress func(int, int)) error {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	data, err := backupTx(ctx, tx)
	if err != nil {
		return err
	}
	manifest := photoManifest{Format: "meterya-backup-v2", DataSHA256: Hash(string(data)), Photos: []backupPhoto{}}
	rows, err := tx.Query(ctx, "SELECT f.document,p.workspace_id FROM photos f JOIN points p ON p.id=f.point_id ORDER BY f.id")
	if err != nil {
		return err
	}
	for rows.Next() {
		var body []byte
		var p backupPhoto
		if err = rows.Scan(&body, &p.Workspace); err != nil {
			break
		}
		if err = json.Unmarshal(body, &p.Photo); err != nil {
			break
		}
		p.File = "photos/" + p.Photo.ID + "." + photoExtension(p.Photo.MIME)
		if p.Photo.EditedSHA256 != "" {
			p.EditedFile = "photos/" + p.Photo.ID + "-edited.jpg"
		}
		manifest.Photos = append(manifest.Photos, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	archive := zip.NewWriter(dest)
	write := func(name string, body []byte) error {
		f, e := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if e != nil {
			return e
		}
		_, e = f.Write(body)
		return e
	}
	if err = write("data.json", data); err != nil {
		return err
	}
	progress(0, len(manifest.Photos))
	for n, p := range manifest.Photos {
		var raw []byte
		if err = tx.QueryRow(ctx, "SELECT original FROM photos WHERE id=$1", p.Photo.ID).Scan(&raw); err != nil {
			return err
		}
		if err = checkPhoto(raw, p.Photo); err != nil {
			return err
		}
		if err = write(p.File, raw); err != nil {
			return err
		}
		if p.EditedFile != "" {
			var edited []byte
			if err = tx.QueryRow(ctx, "SELECT edited FROM photos WHERE id=$1", p.Photo.ID).Scan(&edited); err != nil {
				return err
			}
			if err = checkEdited(edited, p.Photo); err != nil {
				return err
			}
			if err = write(p.EditedFile, edited); err != nil {
				return err
			}
		}
		progress(n+1, len(manifest.Photos))
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err = write("manifest.json", body); err != nil {
		return err
	}
	if err = archive.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func photoExtension(mime string) string {
	return map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp"}[mime]
}
func checkPhoto(raw []byte, p domain.Photo) error {
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != p.Size || len(raw) > media.MaxBytes || hex.EncodeToString(sum[:]) != p.SHA256 {
		return domain.Fail("BACKUP_PHOTO_INVALID")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != p.Width || cfg.Height != p.Height || int64(cfg.Width)*int64(cfg.Height) > media.MaxPixels {
		return domain.Fail("BACKUP_PHOTO_INVALID")
	}
	if photoExtension(p.MIME) != map[string]string{"jpeg": "jpg", "png": "png", "webp": "webp"}[format] {
		return domain.Fail("BACKUP_PHOTO_INVALID")
	}
	if _, err = media.Decode(raw); err != nil {
		return domain.Fail("BACKUP_PHOTO_INVALID")
	}
	return nil
}
func checkEdited(raw []byte, p domain.Photo) error {
	if len(p.Edits) == 0 || len(p.Edits) > 32 {
		return domain.Fail("BACKUP_PHOTO_INVALID")
	}
	return checkPhoto(raw, domain.Photo{MIME: "image/jpeg", Size: p.EditedSize, SHA256: p.EditedSHA256, Width: p.PreviewWidth, Height: p.PreviewHeight})
}
func readZIP(f *zip.File, limit uint64) ([]byte, error) {
	if f.UncompressedSize64 > limit || f.Flags&1 != 0 || f.FileInfo().IsDir() {
		return nil, domain.Fail("BACKUP_INVALID")
	}
	r, err := f.Open()
	if err != nil {
		return nil, domain.Fail("BACKUP_INVALID")
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil || uint64(len(raw)) > limit {
		return nil, domain.Fail("BACKUP_INVALID")
	}
	return raw, nil
}

// Every file and relationship is validated before replacing existing data.
func (s *Store) RestoreBackupFile(ctx context.Context, path string, validateOnly bool) (BackupInfo, error) {
	var info BackupInfo
	if validateOnly {
		// Validate large imports away from live tables so status polling and reads remain available.
		schema := "meterya_validation_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		identifier := pgx.Identifier{schema}.Sanitize()
		if _, err := s.Pool.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
			return info, err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = s.Pool.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE")
		}()
		config := s.Pool.Config()
		config.ConnConfig.RuntimeParams["search_path"] = schema
		db := stdlib.OpenDB(*config.ConnConfig)
		defer db.Close()
		migrationFS, err := fs.Sub(migrations, "migrations")
		if err != nil {
			return info, err
		}
		provider, err := goose.NewProvider(goose.DialectPostgres, db, migrationFS)
		if err != nil {
			return info, err
		}
		if _, err = provider.Up(ctx); err != nil {
			return info, err
		}
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			return info, err
		}
		isolated := &Store{Pool: pool}
		defer pool.Close()
		return isolated.RestoreBackupFile(ctx, path, false)
	}
	f, err := os.Open(path)
	if err != nil {
		return info, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() > MaxArchiveBytes {
		return info, domain.Fail("BACKUP_TOO_LARGE")
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return info, err
	}
	archiveHash := hex.EncodeToString(h.Sum(nil))
	f.Seek(0, io.SeekStart)
	magic := make([]byte, 4)
	if _, err = io.ReadFull(f, magic); err != nil {
		return info, domain.Fail("BACKUP_INVALID")
	}
	f.Seek(0, io.SeekStart)
	if string(magic) != "PK\x03\x04" {
		body, err := io.ReadAll(io.LimitReader(f, MaxBackupBytes+1))
		if err != nil {
			return info, err
		}
		info, err = s.RestoreBackup(ctx, body, validateOnly)
		info.SHA256 = archiveHash
		return info, err
	}
	archive, err := zip.NewReader(f, stat.Size())
	if err != nil {
		return info, domain.Fail("BACKUP_INVALID")
	}
	entries := map[string]*zip.File{}
	for _, entry := range archive.File {
		if entries[entry.Name] != nil || strings.Contains(entry.Name, "\\") || strings.Contains(entry.Name, "..") || len(entries) > 100000 {
			return info, domain.Fail("BACKUP_INVALID")
		}
		entries[entry.Name] = entry
	}
	if entries["data.json"] == nil || entries["manifest.json"] == nil {
		return info, domain.Fail("BACKUP_INVALID")
	}
	data, err := readZIP(entries["data.json"], MaxBackupBytes)
	if err != nil {
		return info, err
	}
	rawManifest, err := readZIP(entries["manifest.json"], MaxBackupBytes)
	if err != nil {
		return info, err
	}
	var manifest photoManifest
	if strictJSON(rawManifest, &manifest) != nil || manifest.Format != "meterya-backup-v2" || manifest.DataSHA256 != Hash(string(data)) {
		return info, domain.Fail("BACKUP_INVALID")
	}
	expectedFiles := len(manifest.Photos) + 2
	for _, p := range manifest.Photos {
		if p.EditedFile != "" {
			expectedFiles++
		}
	}
	if len(entries) != expectedFiles {
		return info, domain.Fail("BACKUP_INVALID")
	}
	payload, _, err := ParseBackup(data)
	if err != nil {
		return info, err
	}
	// Committed photo documents must agree with their immutable attachment events.
	var events []struct {
		Sequence  int64           `json:"sequence"`
		Type      string          `json:"event_type"`
		Aggregate string          `json:"aggregate_id"`
		Payload   json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(payload.Tables["events"], &events) != nil {
		return info, domain.Fail("BACKUP_INVALID")
	}
	documents := map[string]domain.Photo{}
	sort.Slice(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })
	readings := map[string]domain.Reading{}
	for _, event := range events {
		switch event.Type {
		case "PointCreated":
			var p domain.Point
			if json.Unmarshal(event.Payload, &p) != nil {
				return info, domain.Fail("BACKUP_INVALID")
			}
			for _, r := range p.Readings {
				readings[event.Aggregate+":"+r.ID] = r
			}
		case "ReadingRecorded":
			var r domain.Reading
			if json.Unmarshal(event.Payload, &r) != nil {
				return info, domain.Fail("BACKUP_INVALID")
			}
			readings[event.Aggregate+":"+r.ID] = r
		case "ReadingCorrected", "ReadingRetracted":
			var change domain.ReadingChangeApplied
			if json.Unmarshal(event.Payload, &change) != nil {
				return info, domain.Fail("BACKUP_INVALID")
			}
			key := event.Aggregate + ":" + change.ID
			if change.Reading == nil {
				delete(readings, key)
			} else {
				readings[key] = *change.Reading
			}
		}
		if event.Type == "ReadingPhotoAttached" {
			var photo domain.Photo
			if json.Unmarshal(event.Payload, &photo) != nil || photo.PointID != event.Aggregate || documents[photo.ID].ID != "" {
				return info, domain.Fail("BACKUP_INVALID")
			}
			r, ok := readings[event.Aggregate+":"+photo.ReadingID]
			if !ok || r.Date != photo.ConfirmedDate || r.Value != photo.ConfirmedValue || r.RegisterID != photo.RegisterID {
				return info, domain.Fail("BACKUP_PHOTO_INVALID")
			}
			documents[photo.ID] = photo
		}
	}
	seen := map[string]bool{}
	for _, p := range manifest.Photos {
		if _, err = uuid.Parse(p.Photo.ID); err != nil || p.File != "photos/"+p.Photo.ID+"."+photoExtension(p.Photo.MIME) || photoExtension(p.Photo.MIME) == "" || seen[p.Photo.ID] || entries[p.File] == nil {
			return info, domain.Fail("BACKUP_INVALID")
		}
		seen[p.Photo.ID] = true
		expected, ok := documents[p.Photo.ID]
		a, _ := json.Marshal(expected)
		b, _ := json.Marshal(p.Photo)
		if !ok || !bytes.Equal(a, b) {
			return info, domain.Fail("BACKUP_PHOTO_INVALID")
		}
		raw, err := readZIP(entries[p.File], media.MaxBytes)
		if err != nil {
			return info, err
		}
		if err = checkPhoto(raw, p.Photo); err != nil {
			return info, err
		}
		if p.Photo.EditedSHA256 != "" {
			if p.EditedFile != "photos/"+p.Photo.ID+"-edited.jpg" || entries[p.EditedFile] == nil {
				return info, domain.Fail("PHOTO_MISSING")
			}
			edited, e := readZIP(entries[p.EditedFile], media.MaxBytes)
			if e != nil {
				return info, e
			}
			if e = checkEdited(edited, p.Photo); e != nil {
				return info, e
			}
			img, e := media.Decode(raw)
			if e != nil {
				return info, e
			}
			if _, e = media.Edit(img, p.Photo.Edits); e != nil {
				return info, domain.Fail("BACKUP_PHOTO_INVALID")
			}
		} else if p.EditedFile != "" || len(p.Photo.Edits) > 0 {
			return info, domain.Fail("BACKUP_PHOTO_INVALID")
		}
	}
	if len(documents) != len(manifest.Photos) {
		return info, domain.Fail("PHOTO_MISSING")
	}
	info, err = s.restoreBackup(ctx, data, validateOnly, func(tx pgx.Tx) error {
		for _, p := range manifest.Photos {
			raw, e := readZIP(entries[p.File], media.MaxBytes)
			if e != nil {
				return e
			}
			var ws string
			if e = tx.QueryRow(ctx, "SELECT workspace_id FROM points WHERE id=$1", p.Photo.PointID).Scan(&ws); e != nil || ws != p.Workspace {
				return domain.Fail("BACKUP_PHOTO_INVALID")
			}
			body, _ := json.Marshal(p.Photo)
			var edited []byte
			if p.EditedFile != "" {
				edited, e = readZIP(entries[p.EditedFile], media.MaxBytes)
				if e != nil {
					return e
				}
			}
			_, e = tx.Exec(ctx, "INSERT INTO photos(id,workspace_id,point_id,reading_id,owner,original,media_type,size,sha256,width,height,captured_date,created_at,document,edited,preview) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)", p.Photo.ID, ws, p.Photo.PointID, p.Photo.ReadingID, p.Photo.Actor, raw, p.Photo.MIME, p.Photo.Size, p.Photo.SHA256, p.Photo.Width, p.Photo.Height, p.Photo.CapturedDate, p.Photo.UploadedAt, body, edited)
			if e != nil {
				return domain.Fail("BACKUP_PHOTO_INVALID")
			}
		}
		return nil
	})
	info.SHA256 = archiveHash
	info.Photos = len(manifest.Photos)
	info.ContainsPhotos = true
	return info, err
}

func (s *Store) BackupFile(ctx context.Context, path string, progress func(int, int)) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = s.WriteBackupZIP(ctx, &archiveLimitWriter{writer: f}, progress)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return fmt.Errorf("close backup: %w", closeErr)
	}
	return nil
}

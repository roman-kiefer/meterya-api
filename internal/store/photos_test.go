package store

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"image"
	"image/color"
	"image/png"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/media"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPhotoEvidenceReplayMoveAndArchive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "House", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.Bootstrap(ctx, "Other", "other", "other@example.test")
	if err != nil {
		t.Fatal(err)
	}
	p := domain.Point{ID: uuid.NewString(), WorkspaceID: ws, Name: "Meter", Kind: "PHYSICAL", Meter: domain.Meter{ID: uuid.NewString(), Serial: "photo-meter", Type: "ELECTRICITY", Registers: []domain.Register{{ID: uuid.NewString(), Direction: "IMPORT", Unit: "kWh"}}}, Readings: []domain.Reading{}}
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "create", NewPoint: &p})
	if err != nil {
		t.Fatal(err)
	}
	bitmap := image.NewRGBA(image.Rect(0, 0, 40, 30))
	bitmap.Set(0, 0, color.White)
	var b bytes.Buffer
	if err = png.Encode(&b, bitmap); err != nil {
		t.Fatal(err)
	}
	original := b.Bytes()
	img, err := media.Decode(original)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(original)
	draft := domain.Photo{Name: "photo", MIME: img.MIME, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(original)), Width: img.Width, Height: img.Height, PreviewWidth: 40, PreviewHeight: 30}
	upload := Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "upload"}
	draft, err = s.SavePhotoDraft(ctx, upload, draft, original, img.Preview)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.SavePhotoDraft(ctx, upload, draft, original, img.Preview)
	if err != nil || repeated.ID != draft.ID {
		t.Fatal("upload retry", repeated, err)
	}
	if _, _, err = s.PhotoFile(ctx, ws, draft.ID, "other", false); err == nil {
		t.Fatal("private upload exposed")
	}
	r := domain.Reading{ID: uuid.NewString(), RegisterID: p.Meter.Registers[0].ID, Date: "2026-01-01", Value: "10.000000", Actor: "admin", RecordedAt: time.Now().UTC()}
	c := Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "confirm", PointID: p.ID, Expected: p.Version, Reading: &r, PhotoID: draft.ID, Today: "2026-10-12"}
	c.PhotoEdits = []domain.PhotoEdit{{Rotation: 90}, {Crop: &domain.PhotoCrop{Left: 0, Top: 0, Right: 20, Bottom: 30}}}
	edited, err := media.Edit(img, c.PhotoEdits)
	if err != nil {
		t.Fatal(err)
	}
	c.EditedPhoto = edited.Preview
	stale := c
	stale.Expected++
	if _, err = s.Execute(ctx, stale); domain.Code(err) != "AGGREGATE_VERSION_CONFLICT" {
		t.Fatal("version check", err)
	}
	p, err = s.Execute(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(ctx, c); err != nil {
		t.Fatal("confirm retry", err)
	}
	if p.Version != 3 || len(p.Readings) != 1 {
		t.Fatal("non-atomic photo/reading", p)
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE photos SET original=$1 WHERE id=$2", []byte("modified"), draft.ID); err == nil {
		t.Fatal("original mutation allowed")
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE photos SET edited=$1 WHERE id=$2", []byte("modified"), draft.ID); err == nil {
		t.Fatal("edited evidence mutation allowed")
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	evidence, raw, err := s.PhotoFile(ctx, ws, draft.ID, "other", false)
	if err != nil || !bytes.Equal(raw, original) || evidence.ConfirmedValue != r.Value {
		t.Fatal("replay lost original", evidence, err)
	}
	_, savedEdited, err := s.PhotoBlob(ctx, ws, draft.ID, "other", false, true)
	if err != nil || !bytes.Equal(savedEdited, edited.Preview) {
		t.Fatal("edited evidence lost", err)
	}
	archivePath := filepath.Join(t.TempDir(), "backup.zip")
	change := domain.ReadingChange{ID: r.ID, Date: r.Date, Value: "12.000000", Reason: "Correction"}
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "correct", PointID: p.ID, Expected: p.Version, ReadingChange: &change, Today: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	change = domain.ReadingChange{ID: r.ID, Retract: true, Reason: "Withdrawal"}
	p, err = s.Execute(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "retract", PointID: p.ID, Expected: p.Version, ReadingChange: &change, Today: "2026-10-12"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	evidence, raw, err = s.PhotoFile(ctx, ws, draft.ID, "admin", false)
	if err != nil || evidence.ConfirmedValue != "10.000000" || !bytes.Equal(raw, original) {
		t.Fatal("correction or withdrawal changed evidence", evidence, err)
	}
	if err = s.BackupFile(ctx, archivePath, func(int, int) {}); err != nil {
		t.Fatal(err)
	}
	info, err := s.RestoreBackupFile(ctx, archivePath, true)
	if err != nil || info.Photos != 1 || !info.ContainsPhotos {
		t.Fatal("ZIP preview", info, err)
	}
	if _, err = s.Backup(ctx); domain.Code(err) != "PHOTO_ARCHIVE_REQUIRED" {
		t.Fatal("incomplete JSON backup offered", err)
	}
	// Removing a required original must be rejected without changing existing data.
	input, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	badPath := filepath.Join(t.TempDir(), "missing.zip")
	file, err := os.Create(badPath)
	if err != nil {
		t.Fatal(err)
	}
	bad := zip.NewWriter(file)
	for _, entry := range input.File {
		if entry.Name != "data.json" && entry.Name != "manifest.json" {
			continue
		}
		raw, err := readZIP(entry, MaxBackupBytes)
		if err != nil {
			t.Fatal(err)
		}
		out, err := bad.Create(entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(raw)
	}
	bad.Close()
	file.Close()
	if _, err = s.RestoreBackupFile(ctx, badPath, false); err == nil {
		t.Fatal("missing photo accepted")
	}
	if _, raw, err = s.PhotoFile(ctx, ws, draft.ID, "admin", false); err != nil || !bytes.Equal(raw, original) {
		t.Fatal("failed restore damaged original", err)
	}
	if _, err = s.RestoreBackupFile(ctx, archivePath, false); err != nil {
		t.Fatal("ZIP restore", err)
	}
	restored, restoredImage, err := s.PhotoBlob(ctx, ws, draft.ID, "admin", false, true)
	if err != nil || !bytes.Equal(restoredImage, edited.Preview) || len(restored.Edits) != 2 {
		t.Fatal("ZIP restore lost edited evidence", err)
	}
	p, err = s.Point(ctx, ws, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.MovePoint(ctx, Command{Workspace: ws, Subject: "admin", Key: uuid.NewString(), Fingerprint: "move", PointID: p.ID, Expected: p.Version}, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.PhotoFile(ctx, ws, draft.ID, "admin", false); err == nil {
		t.Fatal("old workspace can read photo")
	}
	if _, raw, err = s.PhotoFile(ctx, target, draft.ID, "admin", false); err != nil || !bytes.Equal(raw, original) {
		t.Fatal("move lost photo", err)
	}
	if err = s.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.PurgePoint(ctx, Command{Workspace: target, Subject: "admin", Key: uuid.NewString(), Fingerprint: "purge", PointID: p.ID, Expected: p.Version}, p.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM photos WHERE id=$1", draft.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("purge retained photo", count, err)
	}
}

func TestPhotoOwnershipGuestsExpiry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ws, err := s.Bootstrap(ctx, "House", "admin", "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"USER", "GUEST"} {
		if _, err = s.Pool.Exec(ctx, "INSERT INTO members(workspace_id,subject,email,role) VALUES($1,$2,$3,$4)", ws, role, role+"@example.test", role); err != nil {
			t.Fatal(err)
		}
	}
	p := domain.Photo{Name: "photo", MIME: "image/png", Size: 3, SHA256: Hash("raw"), Width: 1, Height: 1}
	if _, err = s.SavePhotoDraft(ctx, Command{Workspace: ws, Subject: "GUEST", Key: uuid.NewString(), Fingerprint: "upload"}, p, []byte("raw"), nil); domain.Code(err) != "PERMISSION_DENIED" {
		t.Fatal("guest upload", err)
	}
	p, err = s.SavePhotoDraft(ctx, Command{Workspace: ws, Subject: "USER", Key: uuid.NewString(), Fingerprint: "upload"}, p, []byte("raw"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.PhotoFile(ctx, ws, p.ID, "admin", false); err == nil {
		t.Fatal("admin read another user's private draft")
	}
	if _, err = s.Pool.Exec(ctx, "UPDATE photos SET expires_at=now()-interval '1 second' WHERE id=$1", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.PhotoFile(ctx, ws, p.ID, "USER", false); err == nil {
		t.Fatal("expired draft readable")
	}
	if err = s.SweepPhotoDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM photos WHERE id=$1", p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired draft retained", count, err)
	}
}

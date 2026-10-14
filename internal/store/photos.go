package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"image"
	"meterya.local/api/internal/domain"
	"time"
)

func (s *Store) SavePhotoDraft(ctx context.Context, c Command, p domain.Photo, original, preview []byte) (domain.Photo, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	var role string
	if err = tx.QueryRow(ctx, roleQuery+" FOR SHARE", c.Workspace, c.Subject).Scan(&role); err != nil || role == "GUEST" {
		return p, domain.Fail("PERMISSION_DENIED")
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", c.Subject+":"+c.Key); err != nil {
		return p, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "photo-owner:"+c.Subject); err != nil {
		return p, err
	}
	if found, e := receipt(ctx, tx, c, &p); found || e != nil {
		if found {
			var present bool
			err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM photos WHERE id=$1 AND owner=$2 AND (expires_at IS NULL OR expires_at>now()))", p.ID, c.Subject).Scan(&present)
			if err != nil {
				return p, err
			}
			if !present {
				return p, domain.Fail("PHOTO_UNAVAILABLE")
			}
		}
		return p, e
	}
	if _, err = tx.Exec(ctx, "DELETE FROM photos WHERE point_id IS NULL AND expires_at<=now()"); err != nil {
		return p, err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM photos WHERE owner=$1 AND point_id IS NULL", c.Subject).Scan(&count); err != nil {
		return p, err
	}
	if count >= 10 {
		return p, domain.Fail("PHOTO_DRAFT_LIMIT")
	}
	p.ID, p.Actor, p.UploadedAt = uuid.NewString(), c.Subject, time.Now().UTC()
	body, _ := json.Marshal(p)
	if _, err = tx.Exec(ctx, "INSERT INTO photos(id,workspace_id,owner,original,preview,media_type,size,sha256,width,height,captured_date,created_at,expires_at,document) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12::timestamptz+interval '23 hours 45 minutes',$13)", p.ID, c.Workspace, c.Subject, original, preview, p.MIME, p.Size, p.SHA256, p.Width, p.Height, p.CapturedDate, p.UploadedAt, body); err != nil {
		return p, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO idempotency(subject,key,fingerprint,response) VALUES($1,$2,$3,$4)", c.Subject, c.Key, c.Fingerprint, body); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}
func (s *Store) PhotoFile(ctx context.Context, ws, id, subject string, preview bool) (domain.Photo, []byte, error) {
	return s.PhotoBlob(ctx, ws, id, subject, preview, false)
}
func (s *Store) PhotoBlob(ctx context.Context, ws, id, subject string, preview, edited bool) (domain.Photo, []byte, error) {
	var p domain.Photo
	var body, blob []byte
	column := "original"
	if preview {
		column = "preview"
	}
	if edited {
		column = "COALESCE(f.edited,f.original)"
	} else {
		column = "f." + column
	}
	err := s.Pool.QueryRow(ctx, "SELECT f.document,"+column+" FROM photos f LEFT JOIN points p ON p.id=f.point_id WHERE f.id=$1 AND ((f.point_id IS NULL AND f.workspace_id=$2 AND f.owner=$3 AND f.expires_at>now()) OR (p.workspace_id=$2))", id, ws, subject).Scan(&body, &blob)
	if err != nil {
		return p, nil, mapError(err)
	}
	err = json.Unmarshal(body, &p)
	return p, blob, err
}
func (s *Store) DiscardPhoto(ctx context.Context, ws, id, subject string) error {
	if role, err := s.Role(ctx, ws, subject); err != nil || role == "GUEST" {
		return domain.Fail("PERMISSION_DENIED")
	}
	_, err := s.Pool.Exec(ctx, "DELETE FROM photos WHERE id=$1 AND workspace_id=$2 AND owner=$3 AND point_id IS NULL", id, ws, subject)
	return err
}
func bindPhoto(ctx context.Context, tx pgx.Tx, c Command, reading domain.Reading) (domain.Photo, error) {
	var p domain.Photo
	var body []byte
	if err := tx.QueryRow(ctx, "SELECT document FROM photos WHERE id=$1 AND workspace_id=$2 AND owner=$3 AND point_id IS NULL AND expires_at>now() FOR UPDATE", c.PhotoID, c.Workspace, c.Subject).Scan(&body); err != nil {
		return p, domain.Fail("PHOTO_UNAVAILABLE")
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, err
	}
	p.PointID, p.ReadingID, p.RegisterID, p.ConfirmedDate, p.ConfirmedValue = c.PointID, reading.ID, reading.RegisterID, reading.Date, reading.Value

	if len(c.PhotoEdits) > 0 {
		if len(c.EditedPhoto) == 0 {
			return p, domain.Fail("PHOTO_INVALID")
		}
		sum := sha256.Sum256(c.EditedPhoto)
		p.Edits = c.PhotoEdits
		p.EditedSHA256 = hex.EncodeToString(sum[:])
		p.EditedSize = int64(len(c.EditedPhoto))
		cfg, _, err := image.DecodeConfig(bytes.NewReader(c.EditedPhoto))
		if err != nil {
			return p, domain.Fail("PHOTO_INVALID")
		}
		p.PreviewWidth, p.PreviewHeight = cfg.Width, cfg.Height
	}
	body, _ = json.Marshal(p)
	_, err := tx.Exec(ctx, "UPDATE photos SET point_id=$1,reading_id=$2,expires_at=NULL,document=$3,edited=$5,preview=CASE WHEN $5::bytea IS NULL THEN preview ELSE $5 END WHERE id=$4", p.PointID, p.ReadingID, body, p.ID, c.EditedPhoto)
	return p, err
}
func (s *Store) Photos(ctx context.Context, ws string) (map[string][]domain.Photo, error) {
	return photos(ctx, s.Pool, ws)
}
func photos(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, ws string) (map[string][]domain.Photo, error) {
	out := map[string][]domain.Photo{}
	rows, err := q.Query(ctx, "SELECT f.document,p.id FROM photos f JOIN points p ON p.id=f.point_id WHERE p.workspace_id=$1 ORDER BY f.created_at DESC", ws)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var body []byte
		var id string
		var p domain.Photo
		if err = rows.Scan(&body, &id); err != nil {
			return out, err
		}
		if err = json.Unmarshal(body, &p); err != nil {
			return out, err
		}
		out[id] = append(out[id], p)
	}
	return out, rows.Err()
}
func (s *Store) SweepPhotoDrafts(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, "DELETE FROM photos WHERE point_id IS NULL AND expires_at<=now()")
	return err
}

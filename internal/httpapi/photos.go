package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"io"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/media"
	"meterya.local/api/internal/store"
	"net/http"
	"strings"
	"time"
)

func photoURLs(p domain.Photo, ws string) domain.Photo {
	base := "/api/v1/workspaces/" + ws + "/photos/" + p.ID
	p.PreviewURL, p.OriginalURL = base+"/preview", base+"/original"
	p.EditedURL = base + "/edited"
	return p
}

func (s *Server) photoWork(h handler) handler {
	return func(w http.ResponseWriter, r *http.Request, v store.Session) error {
		select {
		case s.photoProcessing <- struct{}{}:
			defer func() { <-s.photoProcessing }()
		default:
			return domain.Fail("PHOTO_OCR_BUSY")
		}
		return h(w, r, v)
	}
}
func photoViews(photos []domain.Photo, p domain.Point, ws string) []domain.Photo {
	result := make([]domain.Photo, 0, len(photos))
	for _, f := range photos {
		f.Status = "RETRACTED"
		for _, r := range p.Readings {
			if r.ID == f.ReadingID {
				f.Status = "CURRENT"
				if r.Date != f.ConfirmedDate || r.Value != f.ConfirmedValue {
					f.Status = "CORRECTED"
				}
				break
			}
		}
		result = append(result, photoURLs(f, ws))
	}
	return result
}
func (s *Server) photoPermission(r *http.Request, v store.Session) error {
	role, err := s.Store.Role(r.Context(), r.PathValue("ws"), v.Subject)
	if err != nil {
		return err
	}
	if role == "GUEST" {
		return domain.Fail("PERMISSION_DENIED")
	}
	if store.CanManage(role) && v.ACR != "2" {
		return domain.Fail("MFA_REQUIRED")
	}
	return nil
}
func (s *Server) uploadPhoto(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.photoPermission(r, v); err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	// Raw binary avoids multipart temporary files and preserves the original exactly.
	raw, err := io.ReadAll(io.LimitReader(r.Body, media.MaxBytes+1))
	if err != nil {
		return domain.Fail("PHOTO_INVALID")
	}
	img, err := media.Decode(raw)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	f := domain.Photo{Name: "photo", MIME: img.MIME, SHA256: sha, Size: int64(len(raw)), Width: img.Width, Height: img.Height, PreviewWidth: img.Working.Bounds().Dx(), PreviewHeight: img.Working.Bounds().Dy(), CapturedDate: img.CapturedDate}
	f, err = s.Store.SavePhotoDraft(r.Context(), store.Command{Workspace: r.PathValue("ws"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + sha)}, f, raw, img.Preview)
	if err != nil {
		return err
	}
	proposal, err := s.proposePhoto(r, v, f, img, nil, photoSelection{SkipRecognition: true})
	if err == nil {
		write(w, 201, proposal)
	}
	return err
}

type photoSelection struct {
	SkipRecognition bool               `json:"-"`
	Edits           []domain.PhotoEdit `json:"edits,omitempty"`
	Target          string             `json:"target,omitempty"`
	Crop            *media.Crop        `json:"crop,omitempty"`
	PointID         string             `json:"point_id,omitempty"`
	RegisterID      string             `json:"register_id,omitempty"`
	Date            string             `json:"date,omitempty"`
	Value           string             `json:"value,omitempty"`
}

func (s *Server) analyzePhoto(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.photoPermission(r, v); err != nil {
		return err
	}
	var selection photoSelection
	if _, err := decode(r, &selection); err != nil {
		return err
	}
	f, raw, err := s.Store.PhotoFile(r.Context(), r.PathValue("ws"), r.PathValue("photo"), v.Subject, false)
	if err != nil {
		return err
	}
	if f.PointID != "" || f.Actor != v.Subject {
		return domain.Fail("PHOTO_UNAVAILABLE")
	}
	img, err := media.Decode(raw)
	if err != nil {
		return err
	}
	img, err = media.Edit(img, selection.Edits)
	if err != nil {
		return err
	}
	if selection.Target != "" && selection.Target != "serial" && selection.Target != "register" && selection.Target != "value" {
		return domain.Fail("VALIDATION_FAILED")
	}
	proposal, err := s.proposePhoto(r, v, f, img, selection.Crop, selection)
	if err == nil {
		write(w, 200, proposal)
	}
	return err
}
func (s *Server) proposePhoto(r *http.Request, v store.Session, f domain.Photo, img media.Image, crop *media.Crop, selection photoSelection) (domain.PhotoProposal, error) {
	snap, err := s.Store.Snapshot(r.Context(), r.PathValue("ws"))
	if err != nil {
		return domain.PhotoProposal{}, err
	}
	rec := media.Recognition{Flags: []string{"PHOTO_CHECK_REQUIRED"}}
	if !selection.SkipRecognition && (selection.PointID == "" || crop != nil) {
		if selection.Target != "" {
			rec, err = s.PhotoRecognizer.RecognizeField(r.Context(), img, crop, snap.Points, selection.Target)
		} else {
			rec, err = s.PhotoRecognizer.Recognize(r.Context(), img, crop, snap.Points)
		}
		if err != nil {
			return domain.PhotoProposal{}, err
		}
	}
	var zone string
	if err = s.Store.Pool.QueryRow(r.Context(), "SELECT timezone FROM workspaces WHERE id=$1", r.PathValue("ws")).Scan(&zone); err != nil {
		return domain.PhotoProposal{}, err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return domain.PhotoProposal{}, err
	}
	out := domain.PhotoProposal{Photo: photoURLs(f, r.PathValue("ws")), PointID: rec.PointID, Serial: rec.Serial, Value: rec.Value, Date: f.CapturedDate, Flags: rec.Flags}
	if selection.Target != "" {
		out.PointID = selection.PointID
		out.Value = selection.Value
		if selection.Target == "serial" {
			out.PointID = rec.PointID
		}
		if selection.Target == "value" {
			out.Value = rec.Value
		}
	}
	if out.Date == "" {
		out.Date = time.Now().In(loc).Format("2006-01-02")
	}
	if selection.PointID != "" && selection.Target != "serial" {
		out.PointID = selection.PointID
		if crop == nil || selection.Target != "" && selection.Target != "value" {
			out.Value = selection.Value
		}
	}
	if selection.Date != "" {
		out.Date = selection.Date
	}
	if value, e := domain.Decimal(strings.ReplaceAll(out.Value, ",", ".")); e == nil {
		out.Value = value
	}
	role, err := s.Store.Role(r.Context(), r.PathValue("ws"), v.Subject)
	if err != nil {
		return out, err
	}
	for _, p := range snap.Points {
		if p.ID != out.PointID {
			continue
		}
		out.ExpectedVersion = p.Version
		out.Serial = p.Meter.Serial
		for _, reg := range p.Meter.Registers {
			if selection.Target != "register" && selection.Target != "serial" && selection.RegisterID == reg.ID || (selection.RegisterID == "" || selection.Target == "register" || selection.Target == "serial") && (reg.Direction == rec.Direction || len(p.Meter.Registers) == 1) {
				out.RegisterID = reg.ID
			}
		}
		out.CanCreate = !p.Archived && out.RegisterID != ""
		for _, reading := range p.Readings {
			if reading.RegisterID == out.RegisterID && reading.Date == out.Date {
				out.ExistingReadingID = reading.ID
				out.ExistingValue = reading.Value
				out.CanCreate = false
				out.CanAttach = (store.CanManage(role) || reading.Actor == v.Subject) && reading.Value == out.Value
				if !store.CanManage(role) && reading.Actor != v.Subject {
					out.Flags = append(out.Flags, "PHOTO_ATTACH_DENIED")
				}
				if reading.Value != out.Value {
					out.Flags = append(out.Flags, "PHOTO_READING_MISMATCH")
				}
			}
		}
		if out.ExistingReadingID == "" && out.RegisterID != "" && out.Value != "" {
			if err = p.ValidateReading(domain.Reading{RegisterID: out.RegisterID, Date: out.Date, Value: out.Value}, time.Now().In(loc).Format("2006-01-02")); err != nil {
				if selection.PointID != "" && crop == nil {
					return out, err
				}
				out.CanCreate = false
				out.Flags = append(out.Flags, domain.Code(err))
			}
		}
	}
	return out, nil
}
func (s *Server) confirmPhoto(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.photoPermission(r, v); err != nil {
		return err
	}
	var input struct {
		Edits      []domain.PhotoEdit `json:"edits,omitempty"`
		PointID    string             `json:"point_id"`
		RegisterID string             `json:"register_id"`
		Date       string             `json:"date"`
		Value      string             `json:"value"`
		Expected   int64              `json:"expected_version"`
		Existing   string             `json:"existing_reading_id,omitempty"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	if _, err = uuid.Parse(input.PointID); err != nil {
		return domain.Fail("VALIDATION_FAILED")
	}
	if _, err = uuid.Parse(input.RegisterID); err != nil {
		return domain.Fail("VALIDATION_FAILED")
	}
	if input.Existing != "" {
		if _, err = uuid.Parse(input.Existing); err != nil {
			return domain.Fail("VALIDATION_FAILED")
		}
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	value, err := domain.Decimal(strings.ReplaceAll(input.Value, ",", "."))
	if err != nil {
		return err
	}
	var zone string
	if err = s.Store.Pool.QueryRow(r.Context(), "SELECT timezone FROM workspaces WHERE id=$1", r.PathValue("ws")).Scan(&zone); err != nil {
		return err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return err
	}
	reading := domain.Reading{ID: uuid.NewString(), RegisterID: input.RegisterID, Date: input.Date, Value: value, Actor: v.Subject, RecordedAt: time.Now().UTC()}
	c := store.Command{Workspace: r.PathValue("ws"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), PointID: input.PointID, Expected: input.Expected, PhotoID: r.PathValue("photo"), Today: time.Now().In(loc).Format("2006-01-02"), Reading: &reading, AttachReadingID: input.Existing}
	if len(input.Edits) > 0 {
		_, raw, e := s.Store.PhotoFile(r.Context(), c.Workspace, c.PhotoID, v.Subject, false)
		if e != nil {
			return e
		}
		img, e := media.Decode(raw)
		if e != nil {
			return e
		}
		img, e = media.Edit(img, input.Edits)
		if e != nil {
			return e
		}
		c.PhotoEdits, c.EditedPhoto = input.Edits, img.Preview
	}
	// The transaction verifies the existing reading's immutable identity and confirmed values.
	p, err := s.Store.Execute(r.Context(), c)
	if err == nil {
		write(w, 201, p)
	}
	return err
}
func (s *Server) discardPhoto(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.photoPermission(r, v); err != nil {
		return err
	}
	if err := s.Store.DiscardPhoto(r.Context(), r.PathValue("ws"), r.PathValue("photo"), v.Subject); err != nil {
		return err
	}
	write(w, 200, map[string]bool{"discarded": true})
	return nil
}
func (s *Server) photoFile(w http.ResponseWriter, r *http.Request, v store.Session) error {
	preview := strings.HasSuffix(r.URL.Path, "/preview")
	edited := strings.HasSuffix(r.URL.Path, "/edited")
	f, raw, err := s.Store.PhotoBlob(r.Context(), r.PathValue("ws"), r.PathValue("photo"), v.Subject, preview, edited)
	if err != nil {
		return err
	}
	mime := f.MIME
	if edited && f.EditedSHA256 != "" {
		mime = "image/jpeg"
	}
	if preview {
		mime = "image/jpeg"
		if len(raw) == 0 {
			select {
			case s.photoProcessing <- struct{}{}:
				defer func() { <-s.photoProcessing }()
			default:
				return domain.Fail("PHOTO_OCR_BUSY")
			}
			_, original, e := s.Store.PhotoFile(r.Context(), r.PathValue("ws"), f.ID, v.Subject, false)
			if e != nil {
				return e
			}
			img, e := media.Decode(original)
			if e != nil {
				return e
			}
			img, e = media.Edit(img, f.Edits)
			if e != nil {
				return e
			}
			raw = img.Preview
			if _, e = s.Store.Pool.Exec(r.Context(), "UPDATE photos SET preview=$1 WHERE id=$2 AND preview IS NULL", raw, f.ID); e != nil {
				return e
			}
		}
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !preview {
		extension := map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp"}[mime]
		w.Header().Set("Content-Disposition", "attachment; filename=\""+f.ID+"."+extension+"\"")
	}
	w.WriteHeader(200)
	_, err = w.Write(raw)
	return err
}

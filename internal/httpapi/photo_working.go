package httpapi

import (
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/media"
	"meterya.local/api/internal/store"
	"net/http"
)

// Working previews are private, ephemeral and derived from the immutable upload.
func (s *Server) workingPhoto(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.photoPermission(r, v); err != nil {
		return err
	}
	var input struct {
		Edits []domain.PhotoEdit `json:"edits"`
	}
	if _, err := decode(r, &input); err != nil {
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
	img, err = media.Edit(img, input.Edits)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, err = w.Write(img.Preview)
	return err
}

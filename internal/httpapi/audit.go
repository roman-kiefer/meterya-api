package httpapi

import (
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
)

func (s *Server) audit(w http.ResponseWriter, r *http.Request, v store.Session) error {
	ws := r.PathValue("ws")
	if ws != "" {
		if err := s.admin(r, v); err != nil {
			return err
		}
	} else {
		admin, err := s.Store.IsAdmin(r.Context(), v.Subject)
		if err != nil {
			return err
		}
		if !admin {
			return domain.Fail("PERMISSION_DENIED")
		}
		if v.ACR != "2" {
			return domain.Fail("MFA_REQUIRED")
		}
	}
	page, err := s.Store.Audit(r.Context(), v.Subject, ws, r.URL.Query().Get("event_type"), r.URL.Query().Get("cursor"))
	if err == nil {
		write(w, 200, page)
	}
	return err
}

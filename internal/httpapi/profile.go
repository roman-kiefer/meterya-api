package httpapi

import (
	"meterya.local/api/internal/store"
	"net/http"
)

func (s *Server) profile(w http.ResponseWriter, r *http.Request, session store.Session) error {
	w.Header().Set("Cache-Control", "no-store")
	profile, err := s.Identity.UserProfile(r.Context(), session.Subject)
	if err != nil {
		return err
	}
	write(w, http.StatusOK, profile)
	return nil
}
func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request, session store.Session) error {
	var input struct {
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
	}
	if _, err := decode(r, &input); err != nil {
		return err
	}
	profile, err := s.Identity.SaveUserNames(r.Context(), session.Subject, input.FirstName, input.LastName)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, profile)
	return nil
}

package httpapi

import (
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
	"time"
)

func (s *Server) recordTransition(w http.ResponseWriter, r *http.Request, v store.Session) error {
	var input struct {
		Continuation string           `json:"continuation_point"`
		Date         string           `json:"date"`
		OldPoint     string           `json:"old_point"`
		NewPoints    []string         `json:"new_points"`
		Expected     int64            `json:"expected_version"`
		Points       map[string]int64 `json:"expected_point_versions"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	if v.ACR != "2" {
		return domain.Fail("MFA_REQUIRED")
	}
	var zone string
	if err = s.Store.Pool.QueryRow(r.Context(), "SELECT timezone FROM workspaces WHERE id=$1", r.PathValue("ws")).Scan(&zone); err != nil {
		return err
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return err
	}
	result, err := s.Store.RecordMeterTransition(r.Context(), store.Command{Workspace: r.PathValue("ws"), Subject: v.Subject, Expected: input.Expected, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Today: time.Now().In(location).Format(time.DateOnly)}, domain.MeterTransition{Continuation: input.Continuation, Date: input.Date, OldPoint: input.OldPoint, NewPoints: input.NewPoints}, input.Points)
	if err == nil {
		write(w, 201, result)
	}
	return err
}

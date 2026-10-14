package httpapi

import (
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
	"time"
)

func (s *Server) configureHeat(w http.ResponseWriter, r *http.Request, session store.Session) error {
	var input struct {
		Name    string           `json:"name"`
		From    string           `json:"valid_from"`
		Net     string           `json:"net_point"`
		House   string           `json:"house_point"`
		Version int64            `json:"expected_version"`
		Points  map[string]int64 `json:"expected_point_versions"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	if session.ACR != "2" {
		return domain.Fail("MFA_REQUIRED")
	}
	var zone string
	if err = s.Store.Pool.QueryRow(r.Context(), "SELECT timezone FROM workspaces WHERE id=$1", r.PathValue("ws")).Scan(&zone); err != nil {
		return err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return err
	}
	result, err := s.Store.ConfigureHeat(r.Context(), store.Command{Workspace: r.PathValue("ws"), Subject: session.Subject, Expected: input.Version, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Today: time.Now().In(loc).Format(time.DateOnly)}, domain.PowerToHeat{Name: input.Name, From: input.From, NetPoint: input.Net, HousePoint: input.House}, input.Points)
	if err == nil {
		write(w, 201, result)
	}
	return err
}

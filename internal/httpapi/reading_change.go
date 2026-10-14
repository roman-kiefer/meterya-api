package httpapi

import (
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
	"strings"
	"time"
)

func (s *Server) changeReading(w http.ResponseWriter, r *http.Request, v store.Session) error {
	var input struct {
		Date     string `json:"date"`
		Value    string `json:"value"`
		Reason   string `json:"reason"`
		Expected int64  `json:"expected_version"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	retract := strings.HasSuffix(r.URL.Path, "/retract")
	if retract && (input.Date != "" || input.Value != "") {
		return domain.Fail("VALIDATION_FAILED")
	}
	var zone string
	if err = s.Store.Pool.QueryRow(r.Context(), "SELECT timezone FROM workspaces WHERE id=$1", r.PathValue("ws")).Scan(&zone); err != nil {
		return err
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return err
	}
	change := domain.ReadingChange{ID: r.PathValue("reading"), Date: input.Date, Value: input.Value, Reason: input.Reason, Retract: retract}
	p, err := s.Store.Execute(r.Context(), store.Command{Workspace: r.PathValue("ws"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Expected: input.Expected, PointID: r.PathValue("point"), ReadingChange: &change, Today: time.Now().In(location).Format("2006-01-02")})
	if err == nil {
		write(w, 200, p)
	}
	return err
}

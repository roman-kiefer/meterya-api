package httpapi

import (
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
)

func (s *Server) saveTariffs(w http.ResponseWriter, r *http.Request, v store.Session) error {
	var input struct {
		Tariffs  domain.Tariffs `json:"tariffs"`
		Expected int64          `json:"expected_version"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	if input.Tariffs.BasePrices == nil {
		input.Tariffs.BasePrices = []domain.BasePrice{}
	}
	if input.Tariffs.UnitPrices == nil {
		input.Tariffs.UnitPrices = []domain.UnitPrice{}
	}
	p, err := s.Store.Execute(r.Context(), store.Command{Workspace: r.PathValue("ws"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Expected: input.Expected, PointID: r.PathValue("point"), Tariffs: &input.Tariffs})
	if err == nil {
		write(w, 200, p)
	}
	return err
}

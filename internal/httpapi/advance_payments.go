package httpapi

import (
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
)

// saveAdvancePayments replaces the complete monthly advance payment schedule of
// an electricity meter.
func (s *Server) saveAdvancePayments(w http.ResponseWriter, r *http.Request, v store.Session) error {
	var input struct {
		AdvancePayments []domain.AdvancePayment `json:"advance_payments"`
		Expected        int64                   `json:"expected_version"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	if input.AdvancePayments == nil {
		input.AdvancePayments = []domain.AdvancePayment{}
	}
	p, err := s.Store.Execute(r.Context(), store.Command{Workspace: r.PathValue("ws"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Expected: input.Expected, PointID: r.PathValue("point"), AdvancePayments: &input.AdvancePayments})
	if err == nil {
		write(w, 200, p)
	}
	return err
}

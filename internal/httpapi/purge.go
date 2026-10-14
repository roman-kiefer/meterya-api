package httpapi

import (
	"meterya.local/api/internal/downloads"
	"meterya.local/api/internal/store"
	"net/http"
)

func (s *Server) purgePoint(cache *downloads.Cache) handler {
	return func(w http.ResponseWriter, r *http.Request, v store.Session) error {
		var input struct {
			Expected       int64  `json:"expected_version"`
			ConfirmPointID string `json:"confirm_point_id"`
		}
		body, err := decode(r, &input)
		if err != nil {
			return err
		}
		k, err := key(r)
		if err != nil {
			return err
		}
		err = s.Store.PurgePoint(r.Context(), store.Command{Workspace: r.PathValue("ws"), PointID: r.PathValue("point"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Expected: input.Expected}, input.ConfirmPointID)
		if err != nil {
			return err
		}
		cache.RemovePoint(r.PathValue("ws"), r.PathValue("point"))
		write(w, http.StatusOK, map[string]bool{"deleted": true})
		return nil
	}
}

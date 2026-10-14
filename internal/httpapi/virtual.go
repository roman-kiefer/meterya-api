package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
)

func (s *Server) virtualCandidates(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.admin(r, v); err != nil {
		return err
	}
	result, err := s.Store.VirtualCandidates(r.Context(), r.PathValue("ws"))
	if err == nil {
		write(w, 200, result)
	}
	return err
}
func (s *Server) saveVirtual(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.admin(r, v); err != nil {
		return err
	}
	var input struct {
		Definition domain.VirtualPoint `json:"definition"`
		Expected   int64               `json:"expected_version"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	result, err := s.Store.SaveVirtual(r.Context(), store.Command{Workspace: r.PathValue("ws"), PointID: r.PathValue("point"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Expected: input.Expected}, input.Definition, false)
	if err == nil {
		write(w, 200, result)
	}
	return err
}
func (s *Server) removeVirtual(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.admin(r, v); err != nil {
		return err
	}
	var input struct {
		Expected int64 `json:"expected_version"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	var raw []byte
	if err = s.Store.Pool.QueryRow(r.Context(), "SELECT state FROM virtual_points WHERE id=$1 AND workspace_id=$2", r.PathValue("point"), r.PathValue("ws")).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Fail("RESOURCE_NOT_FOUND")
		}
		return err
	}
	var definition domain.VirtualPoint
	if err = json.Unmarshal(raw, &definition); err != nil {
		return err
	}
	result, e := s.Store.SaveVirtual(r.Context(), store.Command{Workspace: r.PathValue("ws"), PointID: definition.ID, Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Expected: input.Expected}, definition, true)
	if e == nil {
		write(w, 200, result)
	}
	return e
}
func (s *Server) reportPointExists(r *http.Request) error {
	if _, err := s.Store.Point(r.Context(), r.PathValue("ws"), r.PathValue("point")); err == nil {
		return nil
	} else if domain.Code(err) != "RESOURCE_NOT_FOUND" {
		return err
	}
	snap, err := s.Store.Snapshot(r.Context(), r.PathValue("ws"))
	if err != nil {
		return err
	}
	for _, v := range snap.Definitions {
		if v.ID == r.PathValue("point") {
			return nil
		}
	}
	return domain.Fail("RESOURCE_NOT_FOUND")
}

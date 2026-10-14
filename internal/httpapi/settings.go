package httpapi

import (
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
)

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if v.ACR != "2" {
		return domain.Fail("MFA_REQUIRED")
	}
	var input struct {
		Name string `json:"name"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	result, err := s.Store.CreateWorkspace(r.Context(), store.Command{Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body))}, input.Name, v.Email)
	if err == nil {
		write(w, http.StatusCreated, result)
	}
	return err
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if v.ACR != "2" {
		return domain.Fail("MFA_REQUIRED")
	}
	result, err := s.Store.WorkspaceMembers(r.Context(), r.PathValue("ws"), v.Subject)
	if err == nil {
		write(w, http.StatusOK, result)
	}
	return err
}

func (s *Server) manageMember(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if v.ACR != "2" {
		return domain.Fail("MFA_REQUIRED")
	}
	var input struct {
		Subject  string `json:"subject"`
		Role     string `json:"role"`
		Expected int64  `json:"expected_version"`
		Active   *bool  `json:"active"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	remove := r.PathValue("action") == "remove"
	activity := r.PathValue("action") == "activity"
	if (activity && (input.Active == nil || input.Role != "")) || (!activity && input.Active != nil) || (remove && input.Role != "") {
		return domain.Fail("VALIDATION_FAILED")
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	command := store.Command{Workspace: r.PathValue("ws"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Expected: input.Expected}
	if activity {
		err = s.Store.SetMemberActive(r.Context(), command, input.Subject, *input.Active)
	} else {
		err = s.Store.ManageMember(r.Context(), command, input.Subject, input.Role, remove)
	}
	if err == nil {
		write(w, http.StatusOK, map[string]bool{"updated": true})
	}
	return err
}

func (s *Server) renameWorkspace(w http.ResponseWriter, r *http.Request, v store.Session) error {
	var input struct {
		Name     string `json:"name"`
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
	result, err := s.Store.RenameWorkspace(r.Context(), store.Command{Workspace: r.PathValue("ws"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Expected: input.Expected}, input.Name)
	if err == nil {
		write(w, http.StatusOK, result)
	}
	return err
}

func (s *Server) managePoint(w http.ResponseWriter, r *http.Request, v store.Session) error {
	var input struct {
		Name     *string `json:"name"`
		Serial   *string `json:"serial"`
		Expected int64   `json:"expected_version"`
	}
	body, err := decode(r, &input)
	if err != nil {
		return err
	}
	k, err := key(r)
	if err != nil {
		return err
	}
	c := store.Command{Workspace: r.PathValue("ws"), PointID: r.PathValue("point"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Expected: input.Expected, Name: input.Name}
	if r.PathValue("action") == "details" && (input.Name == nil || input.Serial == nil) {
		return domain.Fail("VALIDATION_FAILED")
	}
	if r.PathValue("action") != "details" && input.Serial != nil {
		return domain.Fail("VALIDATION_FAILED")
	}
	if (r.PathValue("action") == "archive" || r.PathValue("action") == "restore") && input.Name != nil {
		return domain.Fail("VALIDATION_FAILED")
	}
	if r.PathValue("action") == "details" {
		c.Serial = input.Serial
	}
	if r.PathValue("action") == "archive" || r.PathValue("action") == "restore" {
		value := r.PathValue("action") == "archive"
		c.Archived = &value
	}
	result, err := s.Store.Execute(r.Context(), c)
	if err == nil {
		write(w, http.StatusOK, result)
	}
	return err
}

func (s *Server) movePoint(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if v.ACR != "2" {
		return domain.Fail("MFA_REQUIRED")
	}
	var input struct {
		Target   string `json:"target_workspace_id"`
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
	result, err := s.Store.MovePoint(r.Context(), store.Command{Workspace: r.PathValue("ws"), PointID: r.PathValue("point"), Subject: v.Subject, Key: k, Fingerprint: store.Hash(r.URL.Path + string(body)), Expected: input.Expected}, input.Target)
	if err == nil {
		write(w, http.StatusOK, result)
	}
	return err
}

package httpapi

import (
	"io"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/downloads"
	"meterya.local/api/internal/store"
	"net/http"
	"time"
)

func (s *Server) operator(v store.Session) error {
	if s.OperatorSubject == "" || v.Subject != s.OperatorSubject {
		return domain.Fail("PERMISSION_DENIED")
	}
	if v.ACR != "2" {
		return domain.Fail("MFA_REQUIRED")
	}
	return nil
}
func (s *Server) backupDownload(w http.ResponseWriter, r *http.Request, v store.Session) error {
	if err := s.operator(v); err != nil {
		return err
	}
	body, err := s.Store.Backup(r.Context())
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="meterya-backup-`+time.Now().UTC().Format("20060102-150405")+`.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return nil
}
func (s *Server) restoreBackup(files *downloads.Cache, validateOnly bool) handler {
	return func(w http.ResponseWriter, r *http.Request, v store.Session) error {
		if err := s.operator(v); err != nil {
			return err
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, store.MaxBackupBytes+1))
		if err != nil {
			return domain.Fail("BACKUP_INVALID")
		}
		if len(body) > store.MaxBackupBytes {
			return domain.Fail("BACKUP_TOO_LARGE")
		}
		if !validateOnly && (r.Header.Get("X-Backup-Confirm") != "RESTORE" || r.Header.Get("X-Backup-SHA256") != store.Hash(string(body))) {
			return domain.Fail("BACKUP_CONFIRMATION_REQUIRED")
		}
		info, err := s.Store.RestoreBackup(r.Context(), body, validateOnly)
		if err != nil {
			return err
		}
		if !validateOnly {
			files.Close()
			http.SetCookie(w, &http.Cookie{Name: "meterya_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Auth.Secure, SameSite: http.SameSiteLaxMode})
		}
		write(w, 200, info)
		return nil
	}
}

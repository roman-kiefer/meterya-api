package httpapi

import (
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBackupRequiresExplicitOperatorAndMFA(t *testing.T) {
	s := Server{OperatorSubject: "operator"}
	for _, v := range []store.Session{{Subject: "workspace-admin", ACR: "2"}, {Subject: "operator", ACR: "1"}, {Subject: "", ACR: "2"}} {
		if s.operator(v) == nil {
			t.Fatal("unexpected operator access", v)
		}
	}
	v := store.Session{Subject: "operator", ACR: "2"}
	if err := s.operator(v); err != nil {
		t.Fatal(err)
	}
	s.OperatorSubject = ""
	if s.operator(v) == nil {
		t.Fatal("disabled operator accepted")
	}
}
func TestRestoreRequiresFileDigestAndConfirmationBeforeDatabaseAccess(t *testing.T) {
	s := Server{OperatorSubject: "operator"}
	r := httptest.NewRequest("POST", "/api/v1/operator/backup/restore", strings.NewReader(`{}`))
	err := s.restoreBackup(nil, false)(httptest.NewRecorder(), r, store.Session{Subject: "operator", ACR: "2"})
	if domain.Code(err) != "BACKUP_CONFIRMATION_REQUIRED" {
		t.Fatal(err)
	}
}

package httpapi

import (
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/downloads"
	"meterya.local/api/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReportDownloadUsesAttachmentAndPrivateScope(t *testing.T) {
	cache := downloads.New()
	t.Cleanup(cache.Close)
	scope := downloads.Scope{Workspace: "ws", Point: "p", Year: "2026", Subject: "owner"}
	body := "<!DOCTYPE html><h1>Meterya – 24 kWh</h1>"
	file := downloads.File{Name: reportFileName("p", 2026, 9, "html"), MediaType: "text/html; charset=utf-8", Content: body}
	id, err := cache.Put(scope, file)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("ws", "ws")
	r.SetPathValue("point", "p")
	r.SetPathValue("year", "2026")
	r.SetPathValue("export", id)
	h := (&Server{}).downloadReport(cache)
	w := httptest.NewRecorder()
	if err = h(w, r, store.Session{Subject: "owner"}); err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != body || !strings.Contains(w.Header().Get("Content-Disposition"), `attachment; filename="meterya-2026-p-v9.html"`) || w.Header().Get("Content-Type") != file.MediaType || w.Header().Get("Content-Length") == "" {
		t.Fatal(w.Header(), w.Body.String())
	}
	if err = h(httptest.NewRecorder(), r, store.Session{Subject: "other"}); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
	r.SetPathValue("year", "2025")
	if err = h(httptest.NewRecorder(), r, store.Session{Subject: "owner"}); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
}

func TestReportRejectsNonCanonicalYear(t *testing.T) {
	for _, year := range []string{"2026/../2025", "0002026", "0", "1899", "9999", "2026.5"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetPathValue("year", year)
		if _, err := reportYear(r); domain.Code(err) != "RESOURCE_NOT_FOUND" {
			t.Fatal(year, err)
		}
	}
}

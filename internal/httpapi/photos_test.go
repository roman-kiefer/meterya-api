package httpapi

import (
	"meterya.local/api/internal/domain"
	"testing"
)

func TestPhotoDocumentationKeepsConfirmedValueAndStatus(t *testing.T) {
	p := domain.Point{Readings: []domain.Reading{{ID: "current", Date: "2026-01-01", Value: "10.000000"}, {ID: "corrected", Date: "2026-01-02", Value: "12.000000"}}}
	photos := []domain.Photo{{ID: "one", ReadingID: "current", ConfirmedDate: "2026-01-01", ConfirmedValue: "10.000000"}, {ID: "two", ReadingID: "corrected", ConfirmedDate: "2026-01-02", ConfirmedValue: "11.000000"}, {ID: "three", ReadingID: "retracted", ConfirmedDate: "2026-01-03", ConfirmedValue: "14.000000"}}
	got := photoViews(photos, p, "workspace")
	if got[0].Status != "CURRENT" || got[1].Status != "CORRECTED" || got[2].Status != "RETRACTED" || got[1].ConfirmedValue != "11.000000" || got[0].OriginalURL != "/api/v1/workspaces/workspace/photos/one/original" {
		t.Fatal(got)
	}
}

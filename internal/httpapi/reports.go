package httpapi

import (
	"encoding/json"
	"fmt"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/downloads"
	"meterya.local/api/internal/store"
	"net/http"
	"strconv"
)

func reportYear(r *http.Request) (int, error) {
	year, err := strconv.Atoi(r.PathValue("year"))
	if err != nil || year < 1900 || year > 9998 || strconv.Itoa(year) != r.PathValue("year") {
		return 0, domain.Fail("RESOURCE_NOT_FOUND")
	}
	return year, nil
}
func reportFileName(point string, year int, version int64, format string) string {
	return fmt.Sprintf("meterya-%d-%s-v%d.%s", year, point, version, format)
}
func downloadScope(r *http.Request, v store.Session) downloads.Scope {
	return downloads.Scope{Workspace: r.PathValue("ws"), Point: r.PathValue("point"), Year: r.PathValue("year"), Subject: v.Subject}
}
func (s *Server) prepareReport(cache *downloads.Cache) handler {
	return func(w http.ResponseWriter, r *http.Request, v store.Session) error {
		year, err := reportYear(r)
		if err != nil {
			return err
		}
		var input struct {
			DataVersion string `json:"data_version"`
			Expected    int64  `json:"expected_version"`
			Calculation int    `json:"calculation_version"`
			Format      string `json:"format"`
			Content     string `json:"content"`
		}
		if _, err = decode(r, &input); err != nil {
			return err
		}
		if input.Content == "" || (input.Format != "html" && input.Format != "json") {
			return domain.Fail("VALIDATION_FAILED")
		}
		if input.Format == "json" && !json.Valid([]byte(input.Content)) {
			return domain.Fail("VALIDATION_FAILED")
		}
		snap, err := s.Store.Snapshot(r.Context(), r.PathValue("ws"))
		if err != nil {
			return err
		}
		var p domain.Point
		summaries := []domain.AnnualSummary{}
		for _, point := range snap.Points {
			if point.ID == r.PathValue("point") {
				p = point
				summaries = snap.Annual[p.ID]
				break
			}
		}
		if p.ID == "" {
			reports, e := snap.VirtualReports()
			if e != nil {
				return e
			}
			for _, report := range reports {
				if report.Point.ID == r.PathValue("point") {
					p = report.Point
					summaries = report.Annual
					if input.DataVersion == "" || input.DataVersion != report.Info.DataVersion {
						return domain.Fail("AGGREGATE_VERSION_CONFLICT")
					}
					break
				}
			}
		}
		if p.ID == "" {
			return domain.Fail("RESOURCE_NOT_FOUND")
		}
		if p.Version != input.Expected {
			return domain.Fail("AGGREGATE_VERSION_CONFLICT")
		}
		found := false
		for _, summary := range summaries {
			if summary.Year == year {
				found = true
				if summary.PointVersion != p.Version || summary.Version != input.Calculation {
					return domain.Fail("AGGREGATE_VERSION_CONFLICT")
				}
			}
		}
		if !found {
			return domain.Fail("RESOURCE_NOT_FOUND")
		}
		mediaType := "text/html; charset=utf-8"
		if input.Format == "json" {
			mediaType = "application/json; charset=utf-8"
		}
		id, err := cache.Put(downloadScope(r, v), downloads.File{Name: reportFileName(p.ID, year, p.Version, input.Format), MediaType: mediaType, Content: input.Content, DataVersion: input.DataVersion})
		if err != nil {
			return err
		}
		write(w, 201, map[string]string{"download_url": r.URL.Path + "/" + id})
		return nil
	}
}
func (s *Server) downloadReport(cache *downloads.Cache) handler {
	return func(w http.ResponseWriter, r *http.Request, v store.Session) error {
		if _, err := reportYear(r); err != nil {
			return err
		}
		file, err := cache.Get(downloadScope(r, v), r.PathValue("export"))
		if err != nil {
			return err
		}
		if file.DataVersion != "" {
			snap, e := s.Store.Snapshot(r.Context(), r.PathValue("ws"))
			if e != nil {
				return e
			}
			reports, e := snap.VirtualReports()
			if e != nil {
				return e
			}
			matched := false
			for _, report := range reports {
				if report.Point.ID == r.PathValue("point") && report.Info.DataVersion == file.DataVersion {
					matched = true
				}
			}
			if !matched {
				return domain.Fail("AGGREGATE_VERSION_CONFLICT")
			}
		}
		w.Header().Set("Content-Type", file.MediaType)
		w.Header().Set("Content-Disposition", `attachment; filename="`+file.Name+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(file.Content)))
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.WriteHeader(http.StatusOK)
		// Nach Beginn der Dateiantwort darf kein JSON-Fehler angehängt werden.
		_, _ = w.Write([]byte(file.Content))
		return nil
	}
}

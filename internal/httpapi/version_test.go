package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVersionEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		want    string
	}{
		{"release", "v1.2.3", "v1.2.3"},
		{"development", "dev", "dev"},
		{"empty", "", "dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No authentication or database services are configured.
			server := Server{Version: tc.version}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			for name, want := range map[string]string{
				"Content-Type":  "application/json",
				"Cache-Control": "no-store",
			} {
				if got := response.Header().Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body["version"] != tc.want {
				t.Fatalf("body = %v, want only version=%q", body, tc.want)
			}
		})
	}
}

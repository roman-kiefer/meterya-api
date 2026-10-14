package httpapi

import (
	"context"
	"encoding/json"
	"meterya.local/api/internal/auth"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProfileUpdatesOnlySessionSubjectAndNames(t *testing.T) {
	writes := 0
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "test"})
			return
		}
		if r.URL.Path == "/admin/realms/meterya/users/own-subject/credentials" {
			json.NewEncoder(w).Encode([]any{})
			return
		}
		if r.URL.Path != "/admin/realms/meterya/users/own-subject" {
			t.Errorf("wrong target: %s", r.URL.Path)
		}
		if r.Method == "PUT" {
			writes++
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			if len(payload) != 2 || payload["firstName"] != "Anna" || payload["lastName"] != "Müller" {
				t.Errorf("unexpected update: %v", payload)
			}
			w.WriteHeader(204)
			return
		}
		json.NewEncoder(w).Encode(auth.UserProfile{FirstName: "Anna", LastName: "Müller", Email: "verified@example.test", EmailVerified: true})
	}))
	defer identity.Close()
	server := Server{Identity: auth.Keycloak{Base: identity.URL, Realm: "meterya", Client: identity.Client()}}
	session := store.Session{Subject: "own-subject"}
	for _, body := range []string{
		`{"firstName":"Anna","lastName":"Müller","subject":"other"}`,
		`{"firstName":"Anna","lastName":"Müller","email":"other@example.test"}`,
		`{"firstName":"Anna","lastName":"Müller","emailVerified":true}`,
		`{"firstName":"Anna","lastName":"Müller","role":"ADMIN"}`,
	} {
		r := httptest.NewRequest("POST", "/api/v1/profile", strings.NewReader(body))
		if err := server.saveProfile(httptest.NewRecorder(), r, session); domain.Code(err) != "VALIDATION_FAILED" {
			t.Fatalf("accepted protected field: %v", err)
		}
	}
	if writes != 0 {
		t.Fatal("invalid requests changed identity")
	}
	r := httptest.NewRequest("POST", "/api/v1/profile?subject=other", strings.NewReader(`{"firstName":" Anna ","lastName":" Müller "}`))
	w := httptest.NewRecorder()
	if err := server.saveProfile(w, r, session); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing update or cache protection")
	}
	if _, err := server.Identity.SaveUserNames(context.Background(), "own-subject", strings.Repeat("a", 101), "last"); domain.Code(err) != "VALIDATION_FAILED" {
		t.Fatal("long name accepted")
	}
}

func TestAccountActionsRejectArbitraryActionsBeforeLogin(t *testing.T) {
	a := auth.Auth{}
	if err := a.AccountAction(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), "delete_account", true); domain.Code(err) != "RESOURCE_NOT_FOUND" {
		t.Fatal(err)
	}
}

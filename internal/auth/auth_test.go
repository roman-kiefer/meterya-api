package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCallbackRejectsMissingOrMismatchedStateBeforeDatabase(t *testing.T) {
	a := &Auth{}
	for _, state := range []string{"", "wrong"} {
		req := httptest.NewRequest("GET", "/auth/callback?state="+state+"&code=code", nil)
		req.AddCookie(&http.Cookie{Name: "meterya_login", Value: "expected"})
		w := httptest.NewRecorder()
		a.Callback(w, req)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
}
func TestSessionCookieFlags(t *testing.T) {
	a := &Auth{Secure: true}
	w := httptest.NewRecorder()
	a.cookie(w, "meterya_session", "secret", 3600)
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatal(cookie)
	}
}
func TestExistingAdminInvitationPreservesConfiguredAuthentication(t *testing.T) {
	for _, credentialType := range []string{"otp", "webauthn", "webauthn-passwordless", "recovery-authn-codes", "password"} {
		var actions []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/token"):
				json.NewEncoder(w).Encode(map[string]string{"access_token": "test"})
			case strings.HasSuffix(r.URL.Path, "/users"):
				if r.Method != "GET" {
					t.Error("existing account recreated")
				}
				json.NewEncoder(w).Encode([]map[string]string{{"id": "subject", "email": "admin@example.test"}})
			case strings.HasSuffix(r.URL.Path, "/credentials"):
				json.NewEncoder(w).Encode([]map[string]string{{"type": credentialType}})
			case strings.HasSuffix(r.URL.Path, "/execute-actions-email"):
				if r.Method != "PUT" {
					t.Error(r.Method)
				}
				if err := json.NewDecoder(r.Body).Decode(&actions); err != nil {
					t.Error(err)
				}
				if r.URL.Query().Get("redirect_uri") != "http://localhost/invitations/accept?token=test" {
					t.Error("wrong redirect")
				}
				w.WriteHeader(204)
			default:
				t.Error(r.URL.Path)
				w.WriteHeader(404)
			}
		}))
		defer server.Close()
		k := Keycloak{Base: server.URL, Realm: "meterya", ClientID: "service", Secret: "test", Client: server.Client()}
		sub, err := k.SendInvite(context.Background(), "admin@example.test", "ADMIN", "http://localhost/invitations/accept?token=test", time.Now().Add(time.Hour))
		expected := []string{"VERIFY_EMAIL"}
		if credentialType == "password" {
			expected = append(expected, "meterya-configure-factor")
		}
		if err != nil || sub != "subject" || !reflect.DeepEqual(actions, expected) {
			t.Fatal(credentialType, sub, actions, err)
		}
	}
}

func TestNewInvitationOffersFactorChoiceForAdmin(t *testing.T) {
	for _, role := range []string{"ADMIN", "MODERATOR", "USER", "GUEST"} {
		t.Run(role, func(t *testing.T) {
			created := false
			var actions []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/token"):
					json.NewEncoder(w).Encode(map[string]string{"access_token": "test"})
				case strings.HasSuffix(r.URL.Path, "/users"):
					if r.Method == "POST" {
						created = true
						w.WriteHeader(201)
						return
					}
					if !created {
						w.Write([]byte("[]"))
						return
					}
					json.NewEncoder(w).Encode([]map[string]string{{"id": "new-subject"}})
				case strings.HasSuffix(r.URL.Path, "/credentials"):
					w.Write([]byte("[]"))
				case strings.HasSuffix(r.URL.Path, "/execute-actions-email"):
					json.NewDecoder(r.Body).Decode(&actions)
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			k := Keycloak{Base: server.URL, Realm: "meterya", Client: server.Client()}
			_, err := k.SendInvite(context.Background(), "new@example.test", role, "http://localhost/invitations/accept?token=test", time.Now().Add(time.Hour))
			expected := []string{"VERIFY_EMAIL", "UPDATE_PASSWORD"}
			if role == "ADMIN" || role == "MODERATOR" {
				expected = append(expected, "meterya-configure-factor")
			}
			if err != nil || !created || !reflect.DeepEqual(actions, expected) {
				t.Fatal(actions, err)
			}
		})
	}
}

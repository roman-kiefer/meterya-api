package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestProfileAuthenticationMethods(t *testing.T) {
	for _, kind := range []string{"password", "otp", "webauthn", "webauthn-passwordless", "recovery-authn-codes", "unknown", ""} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/token"):
					json.NewEncoder(w).Encode(map[string]string{"access_token": "test"})
				case r.URL.Path == "/admin/realms/meterya/users/own/credentials":
					if kind == "" {
						w.Write([]byte("[]"))
						return
					}
					json.NewEncoder(w).Encode([]map[string]string{{"type": kind, "id": "private-id", "secretData": "private-secret"}})
				case r.URL.Path == "/admin/realms/meterya/users/own":
					w.Write([]byte(`{"firstName":"Anna"}`))
				default:
					t.Errorf("unexpected identity target: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			k := Keycloak{Base: server.URL, Realm: "meterya", Client: server.Client()}
			profile, err := k.UserProfile(context.Background(), "own")
			if err != nil {
				t.Fatal(err)
			}
			expected := AuthenticationMethods{Password: kind == "password", OTP: kind == "otp", Passkey: kind == "webauthn" || kind == "webauthn-passwordless", Recovery: kind == "recovery-authn-codes"}
			if profile.AuthenticationMethods != expected {
				t.Fatalf("wrong status: %+v", profile.AuthenticationMethods)
			}
			data, _ := json.Marshal(profile)
			if strings.Contains(string(data), "private-") {
				t.Fatal("credential material leaked")
			}
		})
	}
}

func TestProfileCredentialFailureIsNotUnconfigured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			w.Write([]byte(`{"access_token":"test"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/credentials") {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"firstName":"Anna"}`))
	}))
	defer server.Close()
	k := Keycloak{Base: server.URL, Realm: "meterya", Client: server.Client()}
	if _, err := k.UserProfile(context.Background(), "own"); err == nil {
		t.Fatal("credential failure hidden")
	}
}

func TestProfileUsesConfiguredRealm(t *testing.T) {
	a := &Auth{OAuth: oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: "https://example.test/identity/realms/meterya/protocol/openid-connect/auth"}}}
	w := httptest.NewRecorder()
	if err := a.Profile(w, httptest.NewRequest("GET", "/auth/profile?redirect_uri=https://untrusted.test", nil)); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "https://example.test/identity/realms/meterya/account/" {
		t.Fatalf("unexpected profile redirect: %d %s", w.Code, w.Header().Get("Location"))
	}
}

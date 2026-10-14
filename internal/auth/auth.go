package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"log/slog"
	"meterya.local/api/internal/domain"
	"meterya.local/api/internal/store"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func Random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

type transport struct{ public, internal *url.URL }

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	u := *r.URL
	copy.URL = &u
	if u.Host == t.public.Host && strings.HasPrefix(u.Path, t.public.Path) {
		copy.URL.Scheme = t.internal.Scheme
		copy.URL.Host = t.internal.Host
		copy.Host = t.internal.Host
	}
	return http.DefaultTransport.RoundTrip(copy)
}

type Auth struct {
	Store    *store.Store
	Provider *oidc.Provider
	OAuth    oauth2.Config
	Client   *http.Client
	Origin   string
	Secure   bool
}

func New(ctx context.Context, s *store.Store, origin, issuer, internal, clientID, secret string) (*Auth, error) {
	publicURL, err := url.Parse(issuer)
	if err != nil {
		return nil, err
	}
	internalURL, err := url.Parse(internal)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: transport{publicURL, internalURL}, Timeout: 20 * time.Second}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), issuer)
	if err != nil {
		return nil, err
	}
	return &Auth{s, provider, oauth2.Config{ClientID: clientID, ClientSecret: secret, Endpoint: provider.Endpoint(), RedirectURL: origin + "/auth/callback", Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}, client, origin, strings.HasPrefix(origin, "https://")}, nil
}
func (a *Auth) cookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: a.Secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	a.login(w, r, "")
}

// Application initiated actions retain normal state, nonce and PKCE checks.
func (a *Auth) AccountAction(w http.ResponseWriter, r *http.Request, action string, mfa bool) error {
	actions := map[string]string{"email": "UPDATE_EMAIL", "password": "UPDATE_PASSWORD", "passkey": "webauthn-register", "recovery": "CONFIGURE_RECOVERY_AUTHN_CODES", "otp": "CONFIGURE_TOTP"}
	requiredAction, ok := actions[action]
	if !ok {
		return domain.Fail("RESOURCE_NOT_FOUND")
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	copy.URL = &u
	q := url.Values{"return_to": {"/profile"}}
	if mfa {
		q.Set("mfa", "1")
	}
	copy.URL.RawQuery = q.Encode()
	a.login(w, copy, requiredAction)
	return nil
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request, action string) {
	state, nonce, verifier := Random(), Random(), oauth2.GenerateVerifier()
	returnTo := r.URL.Query().Get("return_to")
	if returnTo != "/profile" && !strings.HasPrefix(returnTo, "/invitations/accept?") {
		returnTo = "/"
	}
	_, err := a.Store.Pool.Exec(r.Context(), "INSERT INTO oidc_states(state_hash,verifier,nonce,return_to,expires_at) VALUES($1,$2,$3,$4,$5)", store.Hash(state), verifier, nonce, returnTo, time.Now().Add(10*time.Minute))
	if err != nil {
		http.Error(w, "SERVICE_UNAVAILABLE", 503)
		return
	}
	a.cookie(w, "meterya_login", state, 600)
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)}
	if action != "" {
		opts = append(opts, oauth2.SetAuthURLParam("kc_action", action))
	}
	if r.URL.Query().Get("mfa") == "1" {
		opts = append(opts, oauth2.SetAuthURLParam("acr_values", "2"), oauth2.SetAuthURLParam("claims", `{"id_token":{"acr":{"essential":true,"values":["2"]}}}`))
	}
	http.Redirect(w, r, a.OAuth.AuthCodeURL(state, opts...), http.StatusFound)
}
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie("meterya_login")
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(cookie.Value)) != 1 {
		authFailure(w, "LOGIN_STATE")
		return
	}
	var verifier, nonce, returnTo string
	err = a.Store.Pool.QueryRow(r.Context(), "DELETE FROM oidc_states WHERE state_hash=$1 AND expires_at>now() RETURNING verifier,nonce,return_to", store.Hash(state)).Scan(&verifier, &nonce, &returnTo)
	if err != nil {
		authFailure(w, "LOGIN_STATE_EXPIRED_OR_USED")
		return
	}
	a.cookie(w, "meterya_login", "", -1)
	ctx := oidc.ClientContext(context.WithValue(r.Context(), oauth2.HTTPClient, a.Client), a.Client)
	token, err := a.OAuth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		authFailure(w, "TOKEN_EXCHANGE")
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		authFailure(w, "ID_TOKEN_MISSING")
		return
	}
	id, err := a.Provider.Verifier(&oidc.Config{ClientID: a.OAuth.ClientID}).Verify(ctx, raw)
	if err != nil {
		authFailure(w, "ID_TOKEN_VERIFICATION")
		return
	}
	if id.Nonce != nonce {
		authFailure(w, "NONCE_VERIFICATION")
		return
	}
	var claims struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
		ACR      string `json:"acr"`
	}
	if err = id.Claims(&claims); err != nil || !claims.Verified {
		http.Error(w, "EMAIL_NOT_VERIFIED", 401)
		return
	}
	admin, err := a.Store.IsAdmin(ctx, id.Subject)
	if err != nil {
		http.Error(w, "SERVICE_UNAVAILABLE", 503)
		return
	}
	if admin && claims.ACR != "2" {
		http.Redirect(w, r, "/auth/login?mfa=1&return_to="+url.QueryEscape(returnTo), 302)
		return
	}
	value := Random()
	v := store.Session{Subject: id.Subject, Email: strings.ToLower(claims.Email), ACR: claims.ACR, CSRF: Random(), Expires: time.Now().Add(8 * time.Hour)}
	if err = a.Store.SaveSession(ctx, value, v); err != nil {
		http.Error(w, "SERVICE_UNAVAILABLE", 503)
		return
	}
	a.cookie(w, "meterya_session", value, 8*60*60)
	http.Redirect(w, r, returnTo, 302)
}

func authFailure(w http.ResponseWriter, stage string) {
	// Never log callbacks, authorization codes, token bodies or provider error descriptions.
	slog.Warn("authentication failed", "stage", stage)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Error(w, "AUTHENTICATION_FAILED ("+stage+")", http.StatusUnauthorized)
}
func (a *Auth) Session(r *http.Request) (store.Session, error) {
	c, err := r.Cookie("meterya_session")
	if err != nil {
		return store.Session{}, err
	}
	return a.Store.Session(r.Context(), c.Value)
}

// Profile uses the configured public Keycloak realm, never a client-supplied URL.
func (a *Auth) Profile(w http.ResponseWriter, r *http.Request) error {
	u, err := url.Parse(a.OAuth.Endpoint.AuthURL)
	if err != nil || !strings.HasSuffix(u.Path, "/protocol/openid-connect/auth") {
		return errors.New("unsupported profile endpoint")
	}
	u.Path = strings.TrimSuffix(u.Path, "/protocol/openid-connect/auth") + "/account/"
	u.RawQuery = ""
	u.Fragment = ""
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
	return nil
}

func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie("meterya_session"); err == nil {
		if _, err = a.Store.Pool.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", store.Hash(c.Value)); err != nil {
			return err
		}
	}
	a.cookie(w, "meterya_session", "", -1)
	// End the SSO session as well, so the next login can select another account.
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"logout_url": a.OAuth.Endpoint.AuthURL[:strings.LastIndex(a.OAuth.Endpoint.AuthURL, "/auth")] + "/logout?client_id=" + url.QueryEscape(a.OAuth.ClientID) + "&post_logout_redirect_uri=" + url.QueryEscape(a.Origin+"/")})
	return nil
}

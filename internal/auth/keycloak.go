package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Keycloak struct {
	Base, Realm, ClientID, Secret string
	Client                        *http.Client
}

func (k Keycloak) request(ctx context.Context, method, path, token string, body any) ([]byte, error) {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, k.Base+"/admin/realms/"+url.PathEscape(k.Realm)+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := k.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 300 {
		return nil, errors.New("identity service request failed")
	}
	return data, err
}
func (k Keycloak) Token(ctx context.Context) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {k.ClientID}, "client_secret": {k.Secret}}
	req, err := http.NewRequestWithContext(ctx, "POST", k.Base+"/realms/"+url.PathEscape(k.Realm)+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r, err := k.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return "", errors.New("identity service authentication failed")
	}
	var body struct {
		Token string `json:"access_token"`
	}
	err = json.NewDecoder(r.Body).Decode(&body)
	return body.Token, err
}
func (k Keycloak) SendInvite(ctx context.Context, email, role, redirect string, expiry time.Time) (string, error) {
	token, err := k.Token(ctx)
	if err != nil {
		return "", err
	}
	find := func() ([]struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}, error) {
		data, e := k.request(ctx, "GET", "/users?exact=true&email="+url.QueryEscape(email), token, nil)
		var users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		}
		if e == nil {
			e = json.Unmarshal(data, &users)
		}
		return users, e
	}
	users, err := find()
	if err != nil {
		return "", err
	}
	newAccount := len(users) == 0
	if newAccount {
		_, err = k.request(ctx, "POST", "/users", token, map[string]any{"username": email, "email": email, "enabled": true, "emailVerified": false})
		if err != nil {
			return "", err
		}
		users, err = find()
		if err != nil {
			return "", err
		}
	}
	if len(users) != 1 {
		return "", errors.New("ambiguous identity")
	}
	id := users[0].ID
	actions := []string{"VERIFY_EMAIL"}
	if newAccount {
		actions = append(actions, "UPDATE_PASSWORD")
	}
	if role == "ADMIN" || role == "MODERATOR" {
		data, e := k.request(ctx, "GET", "/users/"+url.PathEscape(id)+"/credentials", token, nil)
		if e != nil {
			return "", e
		}
		var credentials []struct {
			Type string `json:"type"`
		}
		if e = json.Unmarshal(data, &credentials); e != nil {
			return "", e
		}
		configured := false
		for _, credential := range credentials {
			switch credential.Type {
			case "otp", "webauthn", "webauthn-passwordless", "recovery-authn-codes":
				configured = true
			}
		}
		if !configured {
			actions = append(actions, "meterya-configure-factor")
		}
	}
	lifetime := int(time.Until(expiry).Seconds())
	if lifetime <= 0 {
		return "", errors.New("expired invitation")
	}
	q := url.Values{"client_id": {"meterya-web"}, "redirect_uri": {redirect}, "lifespan": {jsonNumber(lifetime)}}
	path := "/users/" + url.PathEscape(id) + "/execute-actions-email?" + q.Encode()
	_, err = k.request(ctx, "PUT", path, token, actions)
	return id, err
}
func jsonNumber(n int) string { b, _ := json.Marshal(n); return string(b) }

package auth

import (
	"context"
	"encoding/json"
	"meterya.local/api/internal/domain"
	"net/url"
	"strings"
	"unicode/utf8"
)

// AuthenticationMethods exposes presence only, never credential material.
type AuthenticationMethods struct {
	Password bool `json:"password"`
	OTP      bool `json:"otp"`
	Passkey  bool `json:"passkey"`
	Recovery bool `json:"recovery"`
}

type UserProfile struct {
	Username              string                `json:"username"`
	FirstName             string                `json:"firstName"`
	LastName              string                `json:"lastName"`
	Email                 string                `json:"email"`
	EmailVerified         bool                  `json:"emailVerified"`
	AuthenticationMethods AuthenticationMethods `json:"authenticationMethods"`
}

func (k Keycloak) UserProfile(ctx context.Context, subject string) (UserProfile, error) {
	token, err := k.Token(ctx)
	if err != nil {
		return UserProfile{}, err
	}
	data, err := k.request(ctx, "GET", "/users/"+url.PathEscape(subject), token, nil)
	if err != nil {
		return UserProfile{}, err
	}
	var profile UserProfile
	if err = json.Unmarshal(data, &profile); err != nil {
		return UserProfile{}, err
	}
	data, err = k.request(ctx, "GET", "/users/"+url.PathEscape(subject)+"/credentials", token, nil)
	if err != nil {
		return UserProfile{}, err
	}
	var credentials []struct {
		Type string `json:"type"`
	}
	if err = json.Unmarshal(data, &credentials); err != nil {
		return UserProfile{}, err
	}
	for _, credential := range credentials {
		switch credential.Type {
		case "password":
			profile.AuthenticationMethods.Password = true
		case "otp":
			profile.AuthenticationMethods.OTP = true
		case "webauthn", "webauthn-passwordless":
			profile.AuthenticationMethods.Passkey = true
		case "recovery-authn-codes":
			profile.AuthenticationMethods.Recovery = true
		}
	}
	return profile, nil
}

// Never accept an email, role, credential or target subject in this command.
func (k Keycloak) SaveUserNames(ctx context.Context, subject, first, last string) (UserProfile, error) {
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	if utf8.RuneCountInString(first) > 100 || utf8.RuneCountInString(last) > 100 || strings.ContainsAny(first+last, "\r\n\x00") {
		return UserProfile{}, domain.Fail("VALIDATION_FAILED")
	}
	token, err := k.Token(ctx)
	if err != nil {
		return UserProfile{}, err
	}
	_, err = k.request(ctx, "PUT", "/users/"+url.PathEscape(subject), token, map[string]string{"firstName": first, "lastName": last})
	if err != nil {
		return UserProfile{}, err
	}
	return k.UserProfile(ctx, subject)
}

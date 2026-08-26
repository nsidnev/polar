package main

import (
	"encoding/base64"
	"testing"
)

func makeJWT(t *testing.T, payload string) string {
	t.Helper()
	encode := func(s string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	return encode(`{"alg":"RS256"}`) + "." + encode(payload) + ".signature"
}

func TestParseClaimsStringAudience(t *testing.T) {
	token := makeJWT(t, `{"iss":"https://oidc.vercel.com/team","aud":"https://vercel.com/team","sub":"owner:team:project:polar:environment:production"}`)

	got, err := parseClaims(token)
	if err != nil {
		t.Fatalf("parseClaims: %v", err)
	}

	if got.Issuer != "https://oidc.vercel.com/team" {
		t.Errorf("iss = %q", got.Issuer)
	}
	if len(got.Audience) != 1 || got.Audience[0] != "https://vercel.com/team" {
		t.Errorf("aud = %v", got.Audience)
	}
	if got.Subject != "owner:team:project:polar:environment:production" {
		t.Errorf("sub = %q", got.Subject)
	}
}

// RFC 7519 allows aud to be an array, and Tailscale's expected audience may sit
// alongside the default one.
func TestParseClaimsArrayAudience(t *testing.T) {
	token := makeJWT(t, `{"iss":"i","aud":["https://vercel.com/team","api.tailscale.com/abc"],"sub":"s"}`)

	got, err := parseClaims(token)
	if err != nil {
		t.Fatalf("parseClaims: %v", err)
	}

	if len(got.Audience) != 2 || got.Audience[1] != "api.tailscale.com/abc" {
		t.Errorf("aud = %v", got.Audience)
	}
}

func TestParseClaimsMissingAudience(t *testing.T) {
	got, err := parseClaims(makeJWT(t, `{"iss":"i","sub":"s"}`))
	if err != nil {
		t.Fatalf("parseClaims: %v", err)
	}
	if len(got.Audience) != 0 {
		t.Errorf("aud = %v, want none", got.Audience)
	}
}

func TestParseClaimsRejectsNonJWT(t *testing.T) {
	for _, token := range []string{"", "not-a-jwt", "a.b", "a.b.c.d"} {
		if _, err := parseClaims(token); err == nil {
			t.Errorf("parseClaims(%q) = nil error, want failure", token)
		}
	}
}

func TestParseClaimsRejectsBadPayload(t *testing.T) {
	if _, err := parseClaims(makeJWT(t, `not json`)); err == nil {
		t.Error("expected failure on non-JSON payload")
	}
}

// The token must never end up in a log line.
func TestClaimsStringOmitsToken(t *testing.T) {
	rendered := claims{Issuer: "i", Audience: []string{"a"}, Subject: "s"}.String()

	if want := "iss=i aud=a sub=s"; rendered != want {
		t.Errorf("String() = %q, want %q", rendered, want)
	}
}

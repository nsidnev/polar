package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// claims are the parts of the OIDC token worth reporting at startup. None of
// them are secret, and the API has to be configured with matching values, so
// having the box state what it actually carries turns a mismatch into a log line
// rather than a guess.
type claims struct {
	Issuer   string
	Audience []string
	Subject  string
}

func (c claims) String() string {
	return fmt.Sprintf("iss=%s aud=%s sub=%s",
		c.Issuer, strings.Join(c.Audience, ","), c.Subject)
}

// parseClaims reads the payload of a JWT without verifying it. Verification is
// the recipient's job; this is only to report what is being sent.
func parseClaims(token string) (claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}, fmt.Errorf("not a JWT: %d segments", len(parts))
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, fmt.Errorf("decode payload: %w", err)
	}

	// aud is a string or an array of strings, per RFC 7519.
	var raw struct {
		Issuer   string          `json:"iss"`
		Audience json.RawMessage `json:"aud"`
		Subject  string          `json:"sub"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return claims{}, fmt.Errorf("parse payload: %w", err)
	}

	parsed := claims{Issuer: raw.Issuer, Subject: raw.Subject}

	if len(raw.Audience) > 0 {
		var single string
		if err := json.Unmarshal(raw.Audience, &single); err == nil {
			parsed.Audience = []string{single}
		} else if err := json.Unmarshal(raw.Audience, &parsed.Audience); err != nil {
			return claims{}, fmt.Errorf("parse aud: %w", err)
		}
	}

	return parsed, nil
}

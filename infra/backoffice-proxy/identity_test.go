package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const clientTag = "tag:polar-backoffice-client"

func TestUntaggedPeerUsesItsOwnLogin(t *testing.T) {
	cfg := &config{}

	// A person's own machine: the tailnet already knows who they are.
	if got := cfg.operatorFor(nil, testOperator); got != testOperator {
		t.Errorf("operator = %q, want %q", got, testOperator)
	}
}

// A personal tailnet logs in with an address that is not a Polar account, so
// attribution has to be configured rather than inferred.
func TestOverrideReplacesUntaggedLogin(t *testing.T) {
	cfg := &config{operatorOverride: testOperator}

	got := cfg.operatorFor(nil, "someone@personal.example")

	if got != testOperator {
		t.Errorf("operator = %q, want %q", got, testOperator)
	}
}

func TestTaggedPeerIsRefusedWithoutTrust(t *testing.T) {
	cfg := &config{operatorOverride: testOperator}

	if got := cfg.operatorFor([]string{clientTag}, "tagged-devices"); got != "" {
		t.Errorf("operator = %q, want empty", got)
	}
}

// Without an override there is no identity to forward for a tagged peer, so
// trusting the tag alone must not be enough.
func TestTrustedTagIsRefusedWithoutOverride(t *testing.T) {
	cfg := &config{trustedClientTags: []string{clientTag}}

	if got := cfg.operatorFor([]string{clientTag}, "tagged-devices"); got != "" {
		t.Errorf("operator = %q, want empty", got)
	}
}

func TestTrustedTaggedPeerUsesOverride(t *testing.T) {
	cfg := &config{
		trustedClientTags: []string{clientTag},
		operatorOverride:  testOperator,
	}

	got := cfg.operatorFor([]string{clientTag}, "tagged-devices")

	if got != testOperator {
		t.Errorf("operator = %q, want %q", got, testOperator)
	}
}

func TestUntrustedTagIsRefused(t *testing.T) {
	cfg := &config{
		trustedClientTags: []string{clientTag},
		operatorOverride:  testOperator,
	}

	// Being on the tailnet is not enough: the tag has to be the nominated one.
	if got := cfg.operatorFor([]string{"tag:ci"}, "tagged-devices"); got != "" {
		t.Errorf("operator = %q, want empty", got)
	}
}

func TestTrustedTagAmongSeveral(t *testing.T) {
	cfg := &config{
		trustedClientTags: []string{clientTag},
		operatorOverride:  testOperator,
	}

	got := cfg.operatorFor([]string{"tag:ci", clientTag}, "tagged-devices")

	if got != testOperator {
		t.Errorf("operator = %q, want %q", got, testOperator)
	}
}

// The proxy itself is tagged. Its own tag must not grant access.
func TestProxyOwnTagIsRefused(t *testing.T) {
	cfg := &config{
		tags:              []string{defaultTag},
		trustedClientTags: []string{clientTag},
		operatorOverride:  testOperator,
	}

	if got := cfg.operatorFor([]string{defaultTag}, "tagged-devices"); got != "" {
		t.Errorf("operator = %q, want empty", got)
	}
}

func TestPeerIdentifierUsesConnectingPeer(t *testing.T) {
	cfg := &config{trustedClientTags: []string{clientTag}, operatorOverride: testOperator}
	whoIs := func(context.Context, string) ([]string, string, error) {
		return []string{clientTag}, "tagged-devices", nil
	}

	got, err := peerIdentifier(cfg, whoIs)(
		context.Background(), httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if got != testOperator {
		t.Errorf("operator = %q, want %q", got, testOperator)
	}
}

func TestPeerIdentifierPropagatesFailure(t *testing.T) {
	cfg := &config{}
	whoIs := func(context.Context, string) ([]string, string, error) {
		return nil, "", errors.New("peer not found")
	}

	if _, err := peerIdentifier(cfg, whoIs)(
		context.Background(), httptest.NewRequest(http.MethodGet, "/", nil),
	); err == nil {
		t.Error("expected the whois failure to propagate")
	}
}

// A service endpoint terminates the connection in tailscaled, so the peer's
// address is loopback and only headers survive.
func TestServiceIdentifierUsesUserHeader(t *testing.T) {
	cfg := &config{}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set(tailscaleUserLoginHeader, "person@example.com")

	got, err := serviceIdentifier(cfg)(context.Background(), request)
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if got != "person@example.com" {
		t.Errorf("operator = %q, want the header value", got)
	}
}

// Tagged callers carry no user header, so the override is the only identity.
func TestServiceIdentifierFallsBackToOverride(t *testing.T) {
	cfg := &config{operatorOverride: testOperator}

	got, err := serviceIdentifier(cfg)(
		context.Background(), httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if got != testOperator {
		t.Errorf("operator = %q, want %q", got, testOperator)
	}
}

func TestServiceIdentifierRefusesWithoutIdentity(t *testing.T) {
	cfg := &config{}

	got, _ := serviceIdentifier(cfg)(
		context.Background(), httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if got != "" {
		t.Errorf("operator = %q, want empty", got)
	}
}

func TestLocalIdentifierUsesOverride(t *testing.T) {
	cfg := &config{operatorOverride: testOperator}

	got, _ := localIdentifier(cfg)(
		context.Background(), httptest.NewRequest(http.MethodGet, "/", nil),
	)
	if got != testOperator {
		t.Errorf("operator = %q, want %q", got, testOperator)
	}
}

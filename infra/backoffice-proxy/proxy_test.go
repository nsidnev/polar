package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

const (
	testFQDN     = "polar-backoffice.example.ts.net"
	testOperator = "admin@example.com"
	testToken    = "test.jwt.token"
	testBypass   = "bypass-secret"
)

// upstreamRecorder stands in for the Vercel deployment and records what the
// proxy actually sent.
type upstreamRecorder struct {
	server  *httptest.Server
	request *http.Request
}

func newUpstream(t *testing.T) *upstreamRecorder {
	t.Helper()
	rec := &upstreamRecorder{}
	rec.server = httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec.request = r.Clone(context.Background())
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("upstream"))
		}),
	)
	t.Cleanup(rec.server.Close)
	return rec
}

func newTestProxy(t *testing.T, upstream string, identify identifyFunc) *proxy {
	t.Helper()
	parsed, err := url.Parse(upstream)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}

	tokens := newTokenSource("/nonexistent")
	t.Setenv("VERCEL_OIDC_TOKEN", testToken)

	cfg := &config{
		upstream:     parsed,
		pathPrefix:   defaultPathPrefix,
		bypassSecret: testBypass,
	}
	return newProxy(cfg, identify, tokens, testFQDN)
}

func identifyAs(email string) identifyFunc {
	return func(context.Context, *http.Request) (string, error) { return email, nil }
}

func do(p *proxy, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "http://ignored"+path, nil)
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, request)
	return recorder
}

func TestForwardsBackofficeRequest(t *testing.T) {
	upstream := newUpstream(t)
	p := newTestProxy(t, upstream.server.URL, identifyAs(testOperator))

	response := do(p, "/api/backoffice/customers")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if upstream.request == nil {
		t.Fatal("upstream was not called")
	}
	if got := upstream.request.URL.Path; got != "/api/backoffice/customers" {
		t.Errorf("path = %q, want it forwarded verbatim", got)
	}
}

func TestSetsIdentityHeaders(t *testing.T) {
	upstream := newUpstream(t)
	p := newTestProxy(t, upstream.server.URL, identifyAs(testOperator))

	do(p, "/api/backoffice/")

	for header, want := range map[string]string{
		tokenHeader:          testToken,
		operatorHeader:       testOperator,
		forwardedHostHeader:  testFQDN,
		forwardedProtoHeader: "http",
		bypassHeader:         testBypass,
	} {
		if got := upstream.request.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestSendsUpstreamHostHeader(t *testing.T) {
	upstream := newUpstream(t)
	p := newTestProxy(t, upstream.server.URL, identifyAs(testOperator))

	do(p, "/api/backoffice/")

	// Vercel routes on Host, so it has to stay the deployment's.
	upstreamHost := upstream.server.Listener.Addr().String()
	if upstream.request.Host != upstreamHost {
		t.Errorf("Host = %q, want %q", upstream.request.Host, upstreamHost)
	}
}

func TestOmitsBypassHeaderWhenUnset(t *testing.T) {
	upstream := newUpstream(t)
	p := newTestProxy(t, upstream.server.URL, identifyAs(testOperator))
	p.cfg.bypassSecret = ""

	do(p, "/api/backoffice/")

	if got := upstream.request.Header.Get(bypassHeader); got != "" {
		t.Errorf("%s = %q, want empty", bypassHeader, got)
	}
}

// The box reaches the upstream over the public internet, so anything outside
// the backoffice prefix would turn it into an open relay past Vercel's
// deployment protection.
func TestRejectsPathsOutsidePrefix(t *testing.T) {
	paths := []string{
		"/",
		"/api/v1/orders",
		"/api/backoffice-public/leak",
		"/dashboard",
		"/api",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			upstream := newUpstream(t)
			p := newTestProxy(t, upstream.server.URL, identifyAs(testOperator))

			response := do(p, path)

			if response.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", response.Code)
			}
			if upstream.request != nil {
				t.Error("upstream was called for an out-of-scope path")
			}
		})
	}
}

func TestAllowsBarePrefix(t *testing.T) {
	upstream := newUpstream(t)
	p := newTestProxy(t, upstream.server.URL, identifyAs(testOperator))

	response := do(p, "/api/backoffice")

	if response.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", response.Code)
	}
}

func TestRejectsNonUserPeer(t *testing.T) {
	upstream := newUpstream(t)
	// A tagged node resolves to no user.
	p := newTestProxy(t, upstream.server.URL, identifyAs(""))

	response := do(p, "/api/backoffice/")

	if response.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", response.Code)
	}
	if upstream.request != nil {
		t.Error("upstream was called for a non-user peer")
	}
}

func TestRejectsUnidentifiablePeer(t *testing.T) {
	upstream := newUpstream(t)
	failing := func(context.Context, *http.Request) (string, error) {
		return "", errors.New("whois failed")
	}
	p := newTestProxy(t, upstream.server.URL, failing)

	response := do(p, "/api/backoffice/")

	if response.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", response.Code)
	}
	if upstream.request != nil {
		t.Error("upstream was called for an unidentifiable peer")
	}
}

func TestFailsClosedWithoutToken(t *testing.T) {
	upstream := newUpstream(t)
	parsed, _ := url.Parse(upstream.server.URL)
	cfg := &config{upstream: parsed, pathPrefix: defaultPathPrefix}
	// No token file and no env var: the request must not reach the upstream
	// unauthenticated, where it would be served the 404 gate anyway.
	t.Setenv("VERCEL_OIDC_TOKEN", "")
	p := newProxy(cfg, identifyAs(testOperator), newTokenSource("/nonexistent"), testFQDN)

	response := do(p, "/api/backoffice/")

	if response.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", response.Code)
	}
	if upstream.request != nil {
		t.Error("upstream was called without a token")
	}
}

func TestClientSuppliedHeadersAreOverwritten(t *testing.T) {
	upstream := newUpstream(t)
	p := newTestProxy(t, upstream.server.URL, identifyAs(testOperator))

	request := httptest.NewRequest(
		http.MethodGet, "http://ignored/api/backoffice/", nil,
	)
	// A tailnet user must not be able to claim to be someone else.
	request.Header.Set(operatorHeader, "attacker@example.com")
	request.Header.Set(tokenHeader, "forged")
	request.Header.Set(forwardedHostHeader, "attacker.example.com")
	p.ServeHTTP(httptest.NewRecorder(), request)

	if got := upstream.request.Header.Get(operatorHeader); got != testOperator {
		t.Errorf("%s = %q, want %q", operatorHeader, got, testOperator)
	}
	if got := upstream.request.Header.Get(tokenHeader); got != testToken {
		t.Errorf("%s = %q, want the real token", tokenHeader, got)
	}
	if got := upstream.request.Header.Get(forwardedHostHeader); got != testFQDN {
		t.Errorf("%s = %q, want %q", forwardedHostHeader, got, testFQDN)
	}
}

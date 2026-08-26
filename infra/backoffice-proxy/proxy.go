package main

import (
	"context"
	"log"
	"net/http"
	"net/http/httputil"
	"strings"
)

// Headers the Polar API expects. The token proves the request came from this
// box; the operator is who the tailnet says is on the other end of it; the
// forwarded host makes the API generate URLs that point back here.
const (
	tokenHeader          = "X-Polar-Backoffice-Token"
	operatorHeader       = "X-Polar-Backoffice-User"
	forwardedHostHeader  = "X-Polar-Forwarded-Host"
	forwardedProtoHeader = "X-Polar-Forwarded-Proto"
	bypassHeader         = "x-vercel-protection-bypass"
)

// identifyFunc resolves a request to the email of the operator behind it. An
// empty string means the caller must not be served.
//
// It takes the whole request because identity arrives differently depending on
// how the request got here: a direct tailnet connection carries the peer's
// address, while a Tailscale service endpoint terminates the connection in
// tailscaled and forwards it over loopback, leaving only headers.
type identifyFunc func(ctx context.Context, r *http.Request) (string, error)

type proxy struct {
	cfg      *config
	identify identifyFunc
	tokens   *tokenSource
	fqdn     string
	reverse  *httputil.ReverseProxy
}

func newProxy(cfg *config, identify identifyFunc, tokens *tokenSource, fqdn string) *proxy {
	p := &proxy{cfg: cfg, identify: identify, tokens: tokens, fqdn: fqdn}
	p.reverse = &httputil.ReverseProxy{
		Rewrite:      p.rewrite,
		ErrorHandler: p.handleUpstreamError,
	}
	return p
}

// rewrite points the request at the upstream while leaving the path untouched.
// The API mounts the backoffice at the same prefix the proxy serves, so paths
// map one to one and no URL rewriting is needed in either direction.
func (p *proxy) rewrite(r *httputil.ProxyRequest) {
	r.SetURL(p.cfg.upstream)
	// SetURL leaves Out.Host empty so the Host header follows the upstream URL,
	// which is what Vercel routes on.
	r.Out.Header.Set(forwardedHostHeader, p.fqdn)
	r.Out.Header.Set(forwardedProtoHeader, p.forwardedProto())
	if p.cfg.bypassSecret != "" {
		r.Out.Header.Set(bypassHeader, p.cfg.bypassSecret)
	}
}

func (p *proxy) forwardedProto() string {
	// The Tailscale service terminates plain HTTP for now; see the README.
	return "http"
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.inScope(r.URL.Path) {
		// The box reaches the upstream over the public internet, so anything
		// outside the backoffice would make this an open relay past Vercel's
		// deployment protection.
		http.NotFound(w, r)
		return
	}

	operator, err := p.identify(r.Context(), r)
	if err != nil {
		log.Printf("could not identify caller %s: %v", r.RemoteAddr, err)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if operator == "" {
		log.Printf("rejected non-user peer %s", r.RemoteAddr)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	token, err := p.tokens.get()
	if err != nil {
		log.Printf("no OIDC token available: %v", err)
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}

	// Set on the inbound request so Rewrite can't be bypassed for these two.
	r.Header.Set(tokenHeader, token)
	r.Header.Set(operatorHeader, operator)

	p.reverse.ServeHTTP(w, r)
}

// inScope matches the prefix on a path-segment boundary, so a sibling path
// like /api/backoffice-public can't slip through.
func (p *proxy) inScope(path string) bool {
	return path == p.cfg.pathPrefix || strings.HasPrefix(path, p.cfg.pathPrefix+"/")
}

func (p *proxy) handleUpstreamError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("upstream %s %s failed: %v", r.Method, r.URL.Path, err)
	http.Error(w, "Bad Gateway", http.StatusBadGateway)
}

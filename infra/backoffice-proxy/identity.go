package main

import (
	"context"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// Tailscale sets these on requests it forwards through a layer 7 service
// endpoint. A tagged source has no user, so they may be absent.
const (
	tailscaleUserLoginHeader = "Tailscale-User-Login"
	tailscaleHeaderPrefix    = "Tailscale-"
)

// operatorFor decides which operator, if any, a tailnet peer represents.
// An empty result means the peer must not be served.
//
// Authorization and attribution are separate questions. Whether a peer may
// connect at all is settled by the Tailscale grants plus, for tagged nodes, the
// trusted-tag list. Which Polar admin the request is attributed to is a second
// step, because a tailnet identity and a Polar account are different things:
//
//   - An untagged node belongs to a person, so its login name is used, which
//     works when tailnet logins and Polar accounts are the same addresses.
//   - A tagged node belongs to nobody and carries no identity to forward.
//   - operatorOverride, when set, attributes every authorized peer to one
//     admin. Required for tagged peers, and for tailnets whose logins don't
//     match Polar accounts.
func (c *config) operatorFor(tags []string, loginName string) string {
	if len(tags) > 0 {
		if !c.isTrustedClient(tags) {
			return ""
		}
		// No identity of its own, so an override is the only option.
		return c.operatorOverride
	}

	if c.operatorOverride != "" {
		return c.operatorOverride
	}
	return loginName
}

func (c *config) isTrustedClient(tags []string) bool {
	for _, tag := range tags {
		if slices.Contains(c.trustedClientTags, tag) {
			return true
		}
	}
	return false
}

// peerIdentifier resolves identity from the connecting peer, for listeners that
// receive tailnet connections directly. This is the stronger of the two paths:
// the identity comes from the tailnet rather than from a header.
func peerIdentifier(
	cfg *config,
	whoIs func(ctx context.Context, remoteAddr string) (tags []string, loginName string, err error),
) identifyFunc {
	return func(ctx context.Context, r *http.Request) (string, error) {
		tags, loginName, err := whoIs(ctx, r.RemoteAddr)
		if err != nil {
			return "", err
		}
		return cfg.operatorFor(tags, loginName), nil
	}
}

// localIdentifier attributes every request to the configured operator. Local
// mode has no tailnet to ask, and validateLocal already requires an override.
func localIdentifier(cfg *config) identifyFunc {
	return func(context.Context, *http.Request) (string, error) {
		return cfg.operatorOverride, nil
	}
}

// serviceIdentifier resolves identity for a Tailscale service endpoint. The
// connection is terminated by tailscaled and forwarded over loopback, so the
// peer's address is gone and only headers remain.
//
// Tailscale has already applied the grants for the service by the time a request
// arrives, so reaching here means the caller was authorized. What's missing is
// who they are: a tagged source carries no user header, which is why an operator
// override is required to use a service endpoint at all.
func serviceIdentifier(cfg *config) identifyFunc {
	var once sync.Once
	return func(_ context.Context, r *http.Request) (string, error) {
		// Log the headers once so the identity Tailscale actually provides is
		// discoverable rather than guessed at.
		once.Do(func() {
			var forwarded []string
			for name := range r.Header {
				if strings.HasPrefix(name, tailscaleHeaderPrefix) {
					forwarded = append(forwarded, name)
				}
			}
			slices.Sort(forwarded)
			log.Printf("service endpoint headers: %v", forwarded)
		})

		if login := r.Header.Get(tailscaleUserLoginHeader); login != "" {
			return cfg.operatorFor(nil, login), nil
		}
		return cfg.operatorOverride, nil
	}
}

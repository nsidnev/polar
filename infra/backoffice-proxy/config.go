package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// config is resolved entirely from the environment. Vercel injects the system
// variables (VERCEL_PROJECT_PRODUCTION_URL, VERCEL_AUTOMATION_BYPASS_SECRET)
// into the box, so only the Tailscale identity has to be set by hand.
type config struct {
	// Tailscale
	serviceName string
	hostname    string
	tags        []string
	clientID    string
	authKey     string
	stateDir    string
	port        uint16

	// Tagged tailnet peers allowed to connect despite having no identity of
	// their own, and the admin every authorized peer is attributed to.
	trustedClientTags []string
	operatorOverride  string

	// Serve on a plain local address instead of joining the tailnet. For
	// developing the proxy-to-API contract without any Tailscale involvement.
	localAddr string

	// Upstream
	upstream     *url.URL
	bypassSecret string
	pathPrefix   string

	// Vercel OIDC
	tokenPath string
}

const (
	defaultServiceName = "svc:polar-backoffice"
	defaultTag         = "tag:polar-backoffice"
	defaultPathPrefix  = "/api/backoffice"
	defaultStateDir    = "/tmp/tsnet"
	defaultTokenPath   = "/var/run/secrets/vercel.com/token"
)

func loadConfig() (*config, error) {
	c := &config{
		serviceName: env("TS_SERVICE_NAME", defaultServiceName),
		hostname:    env("TS_HOSTNAME", ""),
		tags:        splitAndTrim(env("TS_TAGS", defaultTag)),
		clientID:    env("TS_CLIENT_ID", ""),
		authKey:     env("TS_AUTHKEY", ""),
		stateDir:    env("TS_STATE_DIR", defaultStateDir),
		//nolint:mnd // the Tailscale service is defined on tcp:80
		port:              80,
		trustedClientTags: splitAndTrim(env("TS_TRUSTED_CLIENT_TAGS", "")),
		operatorOverride:  env("TS_OPERATOR", ""),
		localAddr:         env("LOCAL_ADDR", ""),
		bypassSecret:      env("VERCEL_AUTOMATION_BYPASS_SECRET", ""),
		pathPrefix:        strings.TrimSuffix(env("BACKOFFICE_PATH_PREFIX", defaultPathPrefix), "/"),
		tokenPath:         env("VERCEL_OIDC_TOKEN_PATH", defaultTokenPath),
	}

	if !strings.HasPrefix(c.serviceName, "svc:") {
		c.serviceName = "svc:" + c.serviceName
	}

	if raw := env("TS_PORT", ""); raw != "" {
		port, err := strconv.ParseUint(raw, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("TS_PORT: %w", err)
		}
		c.port = uint16(port)
	}

	if c.hostname == "" {
		// Replica ordinal keeps the hostname stable across restarts, so the
		// tailnet doesn't fill up with abandoned node names.
		c.hostname = "polar-backoffice-proxy"
		if ordinal := env("VERCEL_BOX_REPLICA_ORDINAL", ""); ordinal != "" {
			c.hostname += "-" + ordinal
		}
	}

	upstream, err := resolveUpstream()
	if err != nil {
		return nil, err
	}
	c.upstream = upstream

	return c, nil
}

// resolveUpstream prefers an explicit override, then the project's stable
// production hostname. VERCEL_URL is deliberately not used: it points at a
// single deployment, while a box outlives every one of them.
func resolveUpstream() (*url.URL, error) {
	if raw := env("POLAR_UPSTREAM_URL", ""); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("POLAR_UPSTREAM_URL: %w", err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("POLAR_UPSTREAM_URL must be absolute, got %q", raw)
		}
		return parsed, nil
	}

	if host := env("VERCEL_PROJECT_PRODUCTION_URL", ""); host != "" {
		return &url.URL{Scheme: "https", Host: host}, nil
	}

	return nil, fmt.Errorf(
		"no upstream: set POLAR_UPSTREAM_URL or VERCEL_PROJECT_PRODUCTION_URL",
	)
}

// authDescription reports which Tailscale credential will be used, so the
// startup log makes the active path obvious.
func (c *config) authDescription() (string, error) {
	switch {
	case c.clientID != "":
		return "workload identity federation (Vercel OIDC)", nil
	case c.authKey != "":
		return "auth key", nil
	default:
		return "", fmt.Errorf("no Tailscale credential: set TS_CLIENT_ID or TS_AUTHKEY")
	}
}

// validateLocal rejects a local-mode setup that can't attribute requests. There
// is no tailnet to ask, so an operator has to be named outright.
func (c *config) validateLocal() error {
	if c.operatorOverride == "" {
		return fmt.Errorf("LOCAL_ADDR requires TS_OPERATOR: no tailnet to identify the caller")
	}
	return nil
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func splitAndTrim(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// tokenSource reads the Vercel OIDC token the blackbox reconciler drops on
// disk. The token is rotated in place, so it is re-read rather than captured
// once at startup; a short TTL keeps that off the hot path without holding a
// stale token long enough for it to expire mid-request.
type tokenSource struct {
	path string
	ttl  time.Duration
	now  func() time.Time

	mu       sync.Mutex
	cached   string
	cachedAt time.Time
}

const defaultTokenTTL = 15 * time.Second

func newTokenSource(path string) *tokenSource {
	return &tokenSource{path: path, ttl: defaultTokenTTL, now: time.Now}
}

func (t *tokenSource) get() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cached != "" && t.now().Sub(t.cachedAt) < t.ttl {
		return t.cached, nil
	}

	token, err := t.read()
	if err != nil {
		// Serve the cached token rather than failing outright: a transient read
		// error shouldn't take the proxy down mid-rotation.
		if t.cached != "" {
			return t.cached, nil
		}
		return "", err
	}

	t.cached = token
	t.cachedAt = t.now()
	return token, nil
}

// read prefers the file the box is given, and falls back to the environment so
// the same binary runs locally against `vercel env pull`.
func (t *tokenSource) read() (string, error) {
	contents, fileErr := os.ReadFile(t.path)
	if fileErr == nil {
		if token := strings.TrimSpace(string(contents)); token != "" {
			return token, nil
		}
		fileErr = fmt.Errorf("%s is empty", t.path)
	}

	if token := strings.TrimSpace(os.Getenv("VERCEL_OIDC_TOKEN")); token != "" {
		return token, nil
	}

	return "", errors.Join(fileErr, errors.New("VERCEL_OIDC_TOKEN is unset"))
}

// source describes where the token came from, for the startup log.
func (t *tokenSource) source() string {
	if _, err := os.Stat(t.path); err == nil {
		return "file " + t.path
	}
	return "env VERCEL_OIDC_TOKEN"
}

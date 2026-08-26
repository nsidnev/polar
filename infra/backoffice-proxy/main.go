// Command backoffice-proxy publishes the Polar backoffice as a Tailscale
// service.
//
// It runs as a long-running container (a Vercel blackbox) that joins the tailnet
// with the box's own Vercel OIDC identity, advertises a Tailscale service, and
// reverse-proxies the backoffice from the project's production deployment. Every
// proxied request carries a fresh OIDC token, so the API can tell a request that
// came through here from one that arrived off the public internet, and the
// identity of the operator behind the connection.
//
// Blackboxes have no public ingress, which is the point: the tailnet is the only
// way in.
//
// Setting LOCAL_ADDR serves on a plain local address instead, for developing the
// proxy-to-API contract without any Tailscale involvement.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "tailscale.com/feature/identityfederation"
	"tailscale.com/tsnet"
)

const (
	shutdownGrace     = 10 * time.Second
	readHeaderTimeout = 30 * time.Second
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("backoffice-proxy: ")

	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	defer stop()

	tokens := newTokenSource(cfg.tokenPath)

	log.Printf("upstream=%s prefix=%s", cfg.upstream, cfg.pathPrefix)
	logTokenClaims(tokens)
	if cfg.bypassSecret == "" {
		log.Print(
			"warning: VERCEL_AUTOMATION_BYPASS_SECRET unset; " +
				"upstream requests will be blocked if deployment protection is on",
		)
	}

	if cfg.localAddr != "" {
		return runLocal(ctx, cfg, tokens)
	}
	return runTailnet(ctx, cfg, tokens)
}

// logTokenClaims reports what the OIDC token actually carries. Both Tailscale
// and the Polar API have to be configured with matching values, and neither
// tells you what it expected when it rejects one, so this is the cheapest way to
// find a mismatch. A missing token is not fatal here: the mode-specific setup
// reports that with better context.
func logTokenClaims(tokens *tokenSource) {
	token, err := tokens.get()
	if err != nil {
		log.Printf("no OIDC token yet: %v", err)
		return
	}
	parsed, err := parseClaims(token)
	if err != nil {
		log.Printf("could not read OIDC token claims: %v", err)
		return
	}
	log.Printf("OIDC token from %s: %s", tokens.source(), parsed)
}

// runLocal serves on a plain local address, with no tailnet. Everything past the
// proxy behaves identically, so this exercises the OIDC token, the headers and
// the API's gate; only the private-network hop is missing.
func runLocal(ctx context.Context, cfg *config, tokens *tokenSource) error {
	if err := cfg.validateLocal(); err != nil {
		return err
	}

	listener, err := net.Listen("tcp", cfg.localAddr)
	if err != nil {
		return err
	}

	log.Printf("local mode: no tailnet, every request acts as %s", cfg.operatorOverride)
	log.Printf("serving http://%s%s/", cfg.localAddr, cfg.pathPrefix)

	handler := newProxy(cfg, localIdentifier(cfg), tokens, cfg.localAddr)
	return serve(ctx, listenerHandler{listener, handler})
}

func runTailnet(ctx context.Context, cfg *config, tokens *tokenSource) error {
	authDescription, err := cfg.authDescription()
	if err != nil {
		return err
	}

	log.Printf("hostname=%s service=%s", cfg.hostname, cfg.serviceName)
	log.Printf("tailscale auth: %s", authDescription)
	switch {
	case cfg.operatorOverride != "":
		log.Printf("attributing every authorized peer to %s (tagged peers allowed: %v)",
			cfg.operatorOverride, cfg.trustedClientTags)
	case len(cfg.trustedClientTags) > 0:
		log.Print(
			"warning: TS_TRUSTED_CLIENT_TAGS is set without TS_OPERATOR, " +
				"so tagged peers have no identity and will be refused",
		)
	default:
		log.Print(
			"attributing requests to each peer's own tailnet login; " +
				"set TS_OPERATOR if tailnet logins differ from Polar accounts",
		)
	}

	server, err := newTSNetServer(cfg, tokens)
	if err != nil {
		return err
	}
	defer server.Close()

	serviceListener, err := server.ListenService(cfg.serviceName, tsnet.ServiceModeHTTP{
		Port: cfg.port,
		// Plain HTTP avoids per-restart certificate issuance. The node is
		// ephemeral with throwaway state, so HTTPS would re-provision a
		// certificate on every boot and run into rate limits. Nothing here
		// relies on browser cookies, so this costs no security on the tailnet.
		HTTPS: false,
	})
	if err != nil {
		return err
	}
	defer serviceListener.Close()

	// Also serve on the node's own address. A Tailscale service needs to be
	// defined in the admin console before it gets a VIP, and until then its name
	// doesn't resolve, while the node's name always does. Both paths are equally
	// private, so this just removes a setup step from the critical path.
	nodeListener, err := server.Listen("tcp", fmt.Sprintf(":%d", cfg.port))
	if err != nil {
		return err
	}
	defer nodeListener.Close()

	localClient, err := server.LocalClient()
	if err != nil {
		return err
	}

	// Closure rather than a stored client so the concrete Tailscale client type
	// stays an implementation detail of this function.
	whoIs := func(ctx context.Context, remoteAddr string) ([]string, string, error) {
		who, err := localClient.WhoIs(ctx, remoteAddr)
		if err != nil {
			return nil, "", err
		}
		var tags []string
		if who.Node != nil {
			tags = who.Node.Tags
		}
		var loginName string
		if who.UserProfile != nil {
			loginName = who.UserProfile.LoginName
		}
		return tags, loginName, nil
	}

	status, err := localClient.StatusWithoutPeers(ctx)
	if err != nil {
		return err
	}
	nodeFQDN := strings.TrimSuffix(status.Self.DNSName, ".")
	log.Printf("serving http://%s%s/ (service)", serviceListener.FQDN, cfg.pathPrefix)
	log.Printf("serving http://%s%s/ (node)", nodeFQDN, cfg.pathPrefix)

	if cfg.operatorOverride == "" {
		log.Print(
			"warning: the service endpoint cannot identify tagged callers " +
				"without TS_OPERATOR; use the node address instead",
		)
	}

	// Each listener reports the host it is reached at, so the API generates URLs
	// that keep the browser on whichever name was used to get here, and resolves
	// identity the way that listener can.
	return serve(ctx,
		listenerHandler{
			serviceListener,
			newProxy(cfg, serviceIdentifier(cfg), tokens, serviceListener.FQDN),
		},
		listenerHandler{
			nodeListener,
			newProxy(cfg, peerIdentifier(cfg, whoIs), tokens, nodeFQDN),
		},
	)
}

type listenerHandler struct {
	listener net.Listener
	handler  http.Handler
}

func serve(ctx context.Context, targets ...listenerHandler) error {
	servers := make([]*http.Server, 0, len(targets))
	serveErr := make(chan error, len(targets))

	for _, target := range targets {
		httpServer := &http.Server{
			Handler:           target.handler,
			ReadHeaderTimeout: readHeaderTimeout,
		}
		servers = append(servers, httpServer)

		go func() {
			if err := httpServer.Serve(target.listener); err != nil &&
				!errors.Is(err, http.ErrServerClosed) {
				serveErr <- err
			}
		}()
	}

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Print("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	var shutdownErr error
	for _, httpServer := range servers {
		if err := httpServer.Shutdown(shutdownCtx); err != nil && shutdownErr == nil {
			shutdownErr = err
		}
	}
	return shutdownErr
}

// newTSNetServer prefers workload identity federation, where the box proves who
// it is with a Vercel-issued OIDC token and holds no long-lived secret. An auth
// key is accepted as a fallback for tailnets that don't trust the Vercel issuer.
func newTSNetServer(cfg *config, tokens *tokenSource) (*tsnet.Server, error) {
	server := &tsnet.Server{
		Hostname:      cfg.hostname,
		Dir:           cfg.stateDir,
		AdvertiseTags: cfg.tags,
		Logf:          log.Printf,
		UserLogf:      log.Printf,
		// The node is disposable: state lives in the container's writable layer
		// and is gone on restart, so leaving stale nodes behind is worse than
		// re-registering.
		Ephemeral: true,
	}

	if cfg.clientID != "" {
		token, err := tokens.get()
		if err != nil {
			return nil, err
		}
		log.Printf("registering with OIDC token from %s", tokens.source())
		server.ClientID = cfg.clientID
		server.IDToken = token
		return server, nil
	}

	server.AuthKey = cfg.authKey
	return server, nil
}

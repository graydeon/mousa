package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Each process binds one OAuth subject to its startup caller, sources and store.
// An HTTPS reverse proxy and an OAuth 2.1 authorization server are external prerequisites.
type mcpHTTPConfig struct {
	Listen           string `json:"listen"`
	Resource         string `json:"resource"`
	Issuer           string `json:"issuer"`
	Subject          string `json:"subject"`
	IntrospectionURL string `json:"introspection_url"`
	ClientID         string `json:"client_id"`
	ClientSecretEnv  string `json:"client_secret_env"`
	secret           string
}

func loadMCPHTTPConfig(path string) (*mcpHTTPConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open HTTP configuration")
	}
	defer file.Close()
	bounded := &io.LimitedReader{R: file, N: 16385}
	decoder := json.NewDecoder(bounded)
	decoder.DisallowUnknownFields()
	var config mcpHTTPConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, errors.New("invalid HTTP configuration")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || bounded.N == 0 {
		return nil, errors.New("HTTP configuration has trailing data")
	}
	host, port, err := net.SplitHostPort(config.Listen)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || portErr != nil || portNumber == 0 || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("HTTP listen must be a numeric loopback address and nonzero port")
	}
	for _, endpoint := range []string{config.Resource, config.Issuer, config.IntrospectionURL} {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("OAuth endpoints must be absolute HTTPS URLs without credentials, query or fragment")
		}
	}
	resource, _ := url.Parse(config.Resource)
	if resource.Path != "/mcp" || resource.RawPath != "" {
		return nil, errors.New("OAuth resource must use /mcp")
	}
	config.secret = os.Getenv(config.ClientSecretEnv)
	if config.Subject == "" || config.ClientID == "" || config.ClientSecretEnv == "" || config.secret == "" {
		return nil, errors.New("OAuth subject, client ID and populated client-secret environment variable are required")
	}
	return &config, nil
}

func (config *mcpHTTPConfig) verifier(client *http.Client) auth.TokenVerifier {
	return func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		if len(token) > 8192 {
			return nil, auth.ErrInvalidToken
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.IntrospectionURL, strings.NewReader(url.Values{"token": {token}, "token_type_hint": {"access_token"}}.Encode()))
		if err != nil {
			return nil, errors.New("authorization service unavailable")
		}
		request.SetBasicAuth(config.ClientID, config.secret)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := client.Do(request)
		if err != nil {
			return nil, errors.New("authorization service unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, errors.New("authorization service unavailable")
		}
		var claims struct {
			Active    bool            `json:"active"`
			Issuer    string          `json:"iss"`
			Subject   string          `json:"sub"`
			Audience  json.RawMessage `json:"aud"`
			Expiry    int64           `json:"exp"`
			NotBefore int64           `json:"nbf"`
			Scope     string          `json:"scope"`
		}
		bounded := &io.LimitedReader{R: response.Body, N: 16385}
		decoder := json.NewDecoder(bounded)
		if err := decoder.Decode(&claims); err != nil {
			return nil, auth.ErrInvalidToken
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || bounded.N == 0 {
			return nil, auth.ErrInvalidToken
		}
		var audience []string
		var single string
		if json.Unmarshal(claims.Audience, &single) == nil {
			audience = []string{single}
		} else if json.Unmarshal(claims.Audience, &audience) != nil {
			return nil, auth.ErrInvalidToken
		}
		if !claims.Active || claims.Issuer != config.Issuer || claims.Subject != config.Subject || !slices.Contains(audience, config.Resource) || claims.Expiry <= time.Now().Unix() || claims.NotBefore > time.Now().Unix() {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: claims.Subject, Expiration: time.Unix(claims.Expiry, 0), Scopes: strings.Fields(claims.Scope)}, nil
	}
}

func mcpHTTPHandler(server *mcp.Server, config *mcpHTTPConfig, client *http.Client) http.Handler {
	resource, _ := url.Parse(config.Resource)
	metadataURL := resource.Scheme + "://" + resource.Host + "/.well-known/oauth-protected-resource/mcp"
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: mcpMaxFrameBytes, DisableLocalhostProtection: true})
	protected := auth.RequireBearerToken(config.verifier(client), &auth.RequireBearerTokenOptions{ResourceMetadataURL: metadataURL, Scopes: []string{"mousa:read"}})(transport)
	mux := http.NewServeMux()
	mux.Handle("/mcp", protected)
	metadataPath := "/.well-known/oauth-protected-resource/mcp"
	mux.Handle(metadataPath, auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource: config.Resource, AuthorizationServers: []string{config.Issuer},
		ScopesSupported: []string{"mousa:read", "mousa:write"}, BearerMethodsSupported: []string{"header"},
	}))
	admission := make(chan struct{}, 8)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Do not trust forwarded host/identity headers. The proxy preserves the
		// configured public Host; direct native clients can use the loopback Host.
		if r.Host != config.Listen && r.Host != resource.Host {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); r.URL.Path != metadataPath && origin != "" && origin != resource.Scheme+"://"+resource.Host {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		select {
		case admission <- struct{}{}:
			defer func() { <-admission }()
		default:
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func runMCPHTTP(ctx context.Context, server *mcp.Server, config *mcpHTTPConfig) error {
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return errors.New("cannot bind HTTP loopback listener")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	httpServer := &http.Server{Handler: mcpHTTPHandler(server, config, client), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	finished := make(chan error, 1)
	go func() { finished <- httpServer.Serve(listener) }()
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		// Drain handlers before the caller closes the canonical store. Request
		// read deadlines and application cancellation bound this shutdown.
		if err := httpServer.Shutdown(context.Background()); err != nil {
			return err
		}
		err := <-finished
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

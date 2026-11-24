package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

// The native client is the CLI's transport to a served store. It accepts only
// a loopback HTTP origin and takes its credential from the environment or
// the client configuration.

const (
	nativeClientTimeout    = 60 * time.Second
	nativeMaxResponseBytes = 8 << 20
)

type nativeClient struct {
	base     string
	token    string
	metadata mousa.ClientMetadata
	http     *http.Client
}

// newNativeClient validates the loopback endpoint and resolves the token. The
// token is never a flag, argument, or part of the URL.
func newNativeClient(endpoint string, config clientConfig) (*nativeClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("endpoint must be an http://127.0.0.1:port URL without credentials, path, query or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("endpoint must not carry a path")
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || port == "" {
		return nil, errors.New("endpoint must name host and port")
	}
	address, err := netip.ParseAddr(host)
	if err != nil || !address.IsLoopback() {
		return nil, errors.New("endpoint host must be a loopback IP literal")
	}
	token, err := config.token()
	if err != nil {
		return nil, err
	}
	// Reported-only harness identity; the service marks it never-verified.
	metadata := mousa.ClientMetadata{
		Harness: os.Getenv("MOUSA_HARNESS"),
		Agent:   os.Getenv("MOUSA_AGENT"),
		Model:   os.Getenv("MOUSA_MODEL"),
		RunID:   os.Getenv("MOUSA_RUN_ID"),
	}
	if err := metadata.Validate(); err != nil {
		return nil, errors.New("MOUSA_* client metadata must be printable ASCII within 96 bytes each")
	}
	return &nativeClient{
		base:     "http://" + net.JoinHostPort(host, port),
		token:    token,
		metadata: metadata,
		http: &http.Client{
			Timeout:       nativeClientTimeout,
			Transport:     &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// nativeRequestError is a stable endpoint failure: the service's code and the
// request ID it assigned, never raw transport or credential detail.
type nativeRequestError struct {
	code      string
	requestID string
}

func (e *nativeRequestError) Error() string {
	if e.requestID == "" {
		return e.code
	}
	return fmt.Sprintf("%s (request %s)", e.code, e.requestID)
}

// nativeReplyCodes is the closed set of service codes the client echoes.
// Anything else is scrubbed to endpoint_error.
var nativeReplyCodes = map[string]bool{
	"unauthorized": true, "source_denied": true, "forbidden": true, "not_found": true,
	"invalid_request": true, "too_large": true, "conflict": true, "busy": true,
	"canceled": true, "coverage_unavailable": true, "internal": true,
}

// post sends one JSON request and returns the envelope's raw result. A
// transport failure leaves the write outcome unknown; the error says so.
func (c *nativeClient) post(ctx context.Context, route string, request any) (json.RawMessage, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+route, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(httpRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("endpoint request did not complete; outcome unknown: %w", ctxErr)
		}
		return nil, errors.New("endpoint request did not complete; outcome unknown")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, nativeMaxResponseBytes+1))
	if err != nil || len(data) > nativeMaxResponseBytes {
		return nil, errors.New("endpoint response could not be read; outcome unknown")
	}
	var reply nativeReply
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, errors.New("endpoint returned an unreadable reply; outcome unknown")
	}
	if !mousa.ValidRequestID(reply.RequestID) {
		reply.RequestID = ""
	}
	if reply.Error != nil || response.StatusCode != http.StatusOK {
		code := "endpoint_error"
		if reply.Error != nil && nativeReplyCodes[reply.Error.Code] {
			code = reply.Error.Code
		}
		return nil, &nativeRequestError{code: code, requestID: reply.RequestID}
	}
	return reply.Result, nil
}

// emitRawResult prints a raw endpoint result in the same indented shape the
// offline commands emit.
func emitRawResult(result json.RawMessage) error {
	var indented bytes.Buffer
	if err := json.Indent(&indented, result, "", "  "); err != nil {
		return err
	}
	indented.WriteByte('\n')
	_, err := os.Stdout.Write(indented.Bytes())
	return err
}

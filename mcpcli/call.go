package mcpcli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const clientName = "mcp-cli-gen"

// clientVersion is overridden via -ldflags at build time; defaults to "dev".
var clientVersion = "dev"

// headerRoundTripper injects static headers onto every outbound request.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}

// expandHeaders applies os.Expand to every header value.
func expandHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = os.Expand(v, os.Getenv)
	}
	return out
}

// oauthRuntime collects OAuth-related runtime knobs that the CLI populates
// from persistent flags. Zero value yields the default: file-based token
// cache at $XDG_CACHE_HOME/mcp-cli-gen/tokens, ephemeral callback port,
// no preset client_id.
type oauthRuntime struct {
	storeOpts    oauthStoreOptions
	callbackPort int
	clientID     string
	stderr       io.Writer
}

// Connect dials the given server with Streamable HTTP transport using
// default OAuth runtime settings. The caller must Close the returned
// session.
func Connect(ctx context.Context, srv ServerSpec) (*mcp.ClientSession, error) {
	return connectWith(ctx, srv, oauthRuntime{stderr: os.Stderr})
}

// connectWith is the internal entry that the cobra runtime uses so that CLI
// flags can supply token store and callback configuration.
func connectWith(ctx context.Context, srv ServerSpec, ort oauthRuntime) (*mcp.ClientSession, error) {
	store, err := newTokenStore(ort.storeOpts)
	if err != nil {
		return nil, fmt.Errorf("oauth store: %w", err)
	}
	stderr := ort.stderr
	if stderr == nil {
		stderr = io.Discard
	}
	oauthRT := &oauthRoundTripper{
		base:      http.DefaultTransport,
		store:     store,
		server:    srv.Name,
		serverURL: srv.URL,
		clientID:  ort.clientID,
		cbPort:    ort.callbackPort,
		stderr:    stderr,
	}
	httpClient := &http.Client{
		Transport: &headerRoundTripper{
			base:    oauthRT,
			headers: expandHeaders(srv.Headers),
		},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: clientVersion}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL,
		HTTPClient: httpClient,
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s (%s): %w", srv.Name, srv.URL, err)
	}
	return session, nil
}

package mcpcli

import (
	"context"
	"fmt"
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

// Connect dials the given server with Streamable HTTP transport. The caller
// must Close the returned session.
func Connect(ctx context.Context, srv ServerSpec) (*mcp.ClientSession, error) {
	httpClient := &http.Client{
		Transport: &headerRoundTripper{
			base:    http.DefaultTransport,
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

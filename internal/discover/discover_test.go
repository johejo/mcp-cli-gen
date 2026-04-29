package discover

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/johejo/mcp-cli-gen/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiscover_PartialFailure(t *testing.T) {
	good := startMCP(t, "echo")
	defer good.Close()

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()

	cfg := &config.Config{Servers: []config.Server{
		{Name: "good", URL: good.URL},
		{Name: "bad", URL: bad.URL},
	}}

	var stderr bytes.Buffer
	snap := Discover(context.Background(), cfg, &stderr)

	if len(snap.Servers) != 1 || snap.Servers[0].Name != "good" {
		t.Errorf("expected only 'good' in snapshot, got %+v", snap.Servers)
	}
	if len(snap.Tools) != 1 || snap.Tools[0].Name != "echo" {
		t.Errorf("expected only echo tool, got %+v", snap.Tools)
	}
	if !strings.Contains(stderr.String(), "bad") {
		t.Errorf("expected warning about 'bad' server, got: %q", stderr.String())
	}
}

func TestDiscover_AllFailNoError(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()

	cfg := &config.Config{Servers: []config.Server{{Name: "bad", URL: bad.URL}}}

	var stderr bytes.Buffer
	snap := Discover(context.Background(), cfg, &stderr)

	// Empty snapshot, no panic, no Go error: caller decides.
	if len(snap.Tools) != 0 {
		t.Errorf("expected empty snapshot, got %+v", snap.Tools)
	}
}

func startMCP(t *testing.T, toolName string) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "v0"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        toolName,
		Description: "echo",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"m":{"type":"string"}}}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	return httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
}

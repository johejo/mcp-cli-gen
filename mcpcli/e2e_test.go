package mcpcli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johejo/mcp-cli-gen/mcpcli"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// startTestServer brings up an HTTP test server hosting one MCP server
// with a single "echo" tool that returns its message argument.
func startTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0.0.1"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "Echo a message",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`),
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		// Surface the inbound Authorization header so the env-expansion test can verify it.
		auth := req.Extra.Header.Get("Authorization")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Message + "|" + auth}}}, nil
	})
	srv.AddTool(&mcp.Tool{
		Name:        "noargs",
		Description: "No-args ping",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
	})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	return httptest.NewServer(handler)
}

func snapshotFor(t *testing.T, ts *httptest.Server) mcpcli.Snapshot {
	t.Helper()
	// We can't list tools directly without exposing internals; just build the
	// snapshot statically the way generated code would, so this test mirrors
	// the codegen path.
	return mcpcli.Snapshot{
		Servers: []mcpcli.ServerSpec{
			{Name: "test", URL: ts.URL, Headers: map[string]string{"Authorization": "Bearer ${TEST_PAT}"}},
		},
		Tools: []mcpcli.ToolSpec{
			{Server: "test", Name: "echo", Description: []string{"Echo a message"}, SchemaJSON: `{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`},
			{Server: "test", Name: "noargs", Description: []string{"No-args ping"}, SchemaJSON: `{"type":"object"}`},
		},
	}
}

func TestE2E_PerFlag(t *testing.T) {
	ts := startTestServer(t)
	defer ts.Close()
	t.Setenv("TEST_PAT", "secret123")

	snap := snapshotFor(t, ts)
	var stdout, stderr bytes.Buffer
	err := mcpcli.Execute(context.Background(), snap, []string{"test", "echo", "--message", "hello"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello|Bearer secret123") {
		t.Errorf("output missing echo+auth: %s", stdout.String())
	}
}

func TestE2E_ParametersInline(t *testing.T) {
	ts := startTestServer(t)
	defer ts.Close()

	snap := snapshotFor(t, ts)
	var stdout, stderr bytes.Buffer
	err := mcpcli.Execute(context.Background(), snap,
		[]string{"test", "echo", "--parameters", `{"message":"world"}`}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `world`) {
		t.Errorf("expected world in output: %s", stdout.String())
	}
}

func TestE2E_RequiredFlagMissing(t *testing.T) {
	ts := startTestServer(t)
	defer ts.Close()

	snap := snapshotFor(t, ts)
	var stdout, stderr bytes.Buffer
	err := mcpcli.Execute(context.Background(), snap, []string{"test", "echo"}, &stdout, &stderr)
	if err == nil {
		t.Fatalf("expected error for missing --message")
	}
	if !strings.Contains(err.Error(), "--message") {
		t.Errorf("error should mention --message: %v", err)
	}
}

func TestE2E_NoArgsTool(t *testing.T) {
	ts := startTestServer(t)
	defer ts.Close()

	snap := snapshotFor(t, ts)
	var stdout, stderr bytes.Buffer
	err := mcpcli.Execute(context.Background(), snap, []string{"test", "noargs"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(stdout.String(), "pong") {
		t.Errorf("expected pong: %s", stdout.String())
	}
}

func TestE2E_ParametersFromFile(t *testing.T) {
	ts := startTestServer(t)
	defer ts.Close()

	tmp := t.TempDir()
	path := filepath.Join(tmp, "p.json")
	if err := os.WriteFile(path, []byte(`{"message":"from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	snap := snapshotFor(t, ts)
	var stdout, stderr bytes.Buffer
	err := mcpcli.Execute(context.Background(), snap,
		[]string{"test", "echo", "--parameters", path}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Execute: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "from-file") {
		t.Errorf("expected from-file in output: %s", stdout.String())
	}
}

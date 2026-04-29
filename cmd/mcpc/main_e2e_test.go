package main_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMcpc_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a binary; skipping under -short")
	}
	ts := startServer(t)
	defer ts.Close()

	tmp := t.TempDir()
	mcpJSON := filepath.Join(tmp, "mcp.json")
	if err := os.WriteFile(mcpJSON, []byte(`{"mcpServers":{"test":{"url":"`+ts.URL+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	repoRoot := repoRoot(t)
	bin := filepath.Join(tmp, "mcpc")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/mcpc")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build mcpc: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "--config", mcpJSON, "--config-flavor", "claude", "test", "echo", "--message", "yo").CombinedOutput()
	if err != nil {
		t.Fatalf("run mcpc: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "yo") {
		t.Errorf("expected 'yo' in output: %s", out)
	}
}

func startServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "e2e", Version: "v0.0.1"}, nil)
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
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Message}}}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	return httptest.NewServer(h)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

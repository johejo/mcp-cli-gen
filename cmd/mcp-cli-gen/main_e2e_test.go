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

// TestE2E_FullPipeline:
//
//  1. Boots an MCP server in-process via httptest.
//  2. Builds and runs the mcp-cli-gen binary against an mcp.json pointing at it.
//  3. Compiles the generated Go file.
//  4. Runs the generated binary and checks it can call the tool.
func TestE2E_FullPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles two binaries; skipping under -short")
	}
	ts := startMCPServer(t)
	defer ts.Close()

	tmp := t.TempDir()
	mcpJSON := filepath.Join(tmp, "mcp.json")
	configBody := `{"mcpServers":{"test":{"url":"` + ts.URL + `"}}}`
	if err := os.WriteFile(mcpJSON, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Build mcp-cli-gen from the repo root.
	moduleRoot := repoRoot(t)
	genBin := filepath.Join(tmp, "mcp-cli-gen")
	mustRun(t, moduleRoot, "go", "build", "-o", genBin, "./cmd/mcp-cli-gen")

	// 2. Run the generator.
	genSrc := filepath.Join(tmp, "generated.go")
	srcOut := mustRun(t, "", genBin, "--config", mcpJSON, "--config-flavor", "claude", "--package", "main")
	if err := os.WriteFile(genSrc, []byte(srcOut), 0o600); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(srcOut, "echo") {
		t.Fatalf("generated source missing echo tool:\n%s", srcOut)
	}

	// 3. Compile the generated source. We need a module context so the import
	//    of github.com/johejo/mcp-cli-gen/mcpcli resolves to this checkout.
	mod := `module gentest

go 1.26

require github.com/johejo/mcp-cli-gen v0.0.0

replace github.com/johejo/mcp-cli-gen => ` + moduleRoot + `
`
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte(mod), 0o600); err != nil {
		t.Fatal(err)
	}
	mustRun(t, tmp, "go", "mod", "tidy")
	cliBin := filepath.Join(tmp, "mycli")
	mustRun(t, tmp, "go", "build", "-o", cliBin, ".")

	// 4. Run the generated binary.
	out := mustRun(t, tmp, cliBin, "test", "echo", "--message", "from-gen")
	if !strings.Contains(out, "from-gen") {
		t.Fatalf("generated CLI didn't echo: %s", out)
	}
}

func startMCPServer(t *testing.T) *httptest.Server {
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

func mustRun(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

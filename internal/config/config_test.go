package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ClaudeFlavor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{
		"mcpServers": {
			"github": {
				"url": "https://api.githubcopilot.com/mcp/",
				"headers": {"Authorization": "Bearer ${TEST_PAT}"}
			},
			"fs": {"url": "https://fs.example.com/mcp"}
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path, FlavorClaude)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 2 {
		t.Fatalf("want 2 servers, got %d", len(cfg.Servers))
	}
	// Sorted by name: fs then github.
	if cfg.Servers[0].Name != "fs" || cfg.Servers[1].Name != "github" {
		t.Fatalf("servers not sorted: %+v", cfg.Servers)
	}
	if got := cfg.Servers[1].Headers["Authorization"]; got != "Bearer ${TEST_PAT}" {
		t.Fatalf("raw header should not be expanded at load: %q", got)
	}
}

func TestLoad_RejectsUnsupportedFlavor(t *testing.T) {
	if _, err := Load("nonexistent", "openai"); err == nil {
		t.Fatal("expected error for non-claude flavor")
	}
}

func TestLoad_RejectsMissingURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"x":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, FlavorClaude); err == nil {
		t.Fatal("expected error for missing url")
	}
}

func TestExpandHeaders(t *testing.T) {
	t.Setenv("FOO", "bar")
	got := ExpandHeaders(map[string]string{
		"X": "Bearer ${FOO}",
		"Y": "$FOO/path",
		"Z": "${MISSING}",
	})
	if got["X"] != "Bearer bar" {
		t.Errorf("X = %q", got["X"])
	}
	if got["Y"] != "bar/path" {
		t.Errorf("Y = %q", got["Y"])
	}
	if got["Z"] != "" {
		t.Errorf("Z = %q (expected empty)", got["Z"])
	}
}

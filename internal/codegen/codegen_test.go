package codegen

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/johejo/mcp-cli-gen/mcpcli"
)

func TestRender_ProducesValidGo(t *testing.T) {
	snap := mcpcli.Snapshot{
		Servers: []mcpcli.ServerSpec{
			{Name: "github", URL: "https://api.githubcopilot.com/mcp/", Headers: map[string]string{"Authorization": "Bearer ${GITHUB_PAT}"}},
		},
		Tools: []mcpcli.ToolSpec{
			{Server: "github", Name: "create_issue", Description: "Create an issue.", SchemaJSON: `{"type":"object","properties":{"title":{"type":"string"}}}`},
		},
	}
	out, err := Render(snap, "main")
	if err != nil {
		t.Fatal(err)
	}
	src := string(out)

	// Must parse as Go.
	if _, err := parser.ParseFile(token.NewFileSet(), "generated.go", src, 0); err != nil {
		t.Fatalf("generated source does not parse: %v\n%s", err, src)
	}

	// Must reference the runtime library.
	if !strings.Contains(src, `"github.com/johejo/mcp-cli-gen/mcpcli"`) {
		t.Errorf("missing import:\n%s", src)
	}
	// Must include the snapshot literal contents.
	for _, sub := range []string{"create_issue", "Bearer ${GITHUB_PAT}", "Create an issue."} {
		if !strings.Contains(src, sub) {
			t.Errorf("missing %q in:\n%s", sub, src)
		}
	}
}

func TestRender_RequiresPackageName(t *testing.T) {
	if _, err := Render(mcpcli.Snapshot{}, ""); err == nil {
		t.Fatal("expected error for empty package")
	}
}

func TestRender_TricksyContent(t *testing.T) {
	// Backticks and quotes in description and schema must round-trip safely.
	snap := mcpcli.Snapshot{
		Servers: []mcpcli.ServerSpec{{Name: "s", URL: "http://x"}},
		Tools: []mcpcli.ToolSpec{
			{Server: "s", Name: "t", Description: "weird `backticks` and \"quotes\"", SchemaJSON: `{"type":"object"}`},
		},
	}
	out, err := Render(snap, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "g.go", out, 0); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
}

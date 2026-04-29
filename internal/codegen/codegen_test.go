package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
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
			{Server: "github", Name: "create_issue", Description: []string{"Create an issue."}, SchemaJSON: `{"type":"object","properties":{"title":{"type":"string"}}}`},
		},
	}
	out, err := Render(snap, "main")
	if err != nil {
		t.Fatal(err)
	}
	src := string(out)

	if _, err := parser.ParseFile(token.NewFileSet(), "generated.go", src, 0); err != nil {
		t.Fatalf("generated source does not parse: %v\n%s", err, src)
	}
	if !strings.Contains(src, `"github.com/johejo/mcp-cli-gen/mcpcli"`) {
		t.Errorf("missing import:\n%s", src)
	}
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
	// With Description as []string, every entry goes through strconv.Quote,
	// so backticks need no special handling.
	snap := mcpcli.Snapshot{
		Servers: []mcpcli.ServerSpec{{Name: "s", URL: "http://x"}},
		Tools: []mcpcli.ToolSpec{
			{Server: "s", Name: "t", Description: []string{"weird `backticks` and \"quotes\""}, SchemaJSON: `{"type":"object"}`},
		},
	}
	out, err := Render(snap, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "g.go", out, 0); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
	got := extractDescriptionSlice(t, out)
	want := []string{"weird `backticks` and \"quotes\""}
	if !slices.Equal(got, want) {
		t.Errorf("Description round-trip mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestRender_MultilineDescription_SliceLiteral(t *testing.T) {
	desc := []string{
		"Line one.",
		"",
		"## Heading",
		"- bullet a",
		"- bullet b",
	}
	snap := mcpcli.Snapshot{
		Servers: []mcpcli.ServerSpec{{Name: "s", URL: "http://x"}},
		Tools: []mcpcli.ToolSpec{
			{Server: "s", Name: "t", Description: desc, SchemaJSON: `{"type":"object"}`},
		},
	}
	out, err := Render(snap, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "g.go", string(out), 0); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
	// Must emit a multi-line []string literal, not a join expression or single line.
	if !strings.Contains(string(out), "Description: []string{\n") {
		t.Errorf("expected multi-line []string literal:\n%s", out)
	}
	for _, line := range desc {
		if !strings.Contains(string(out), strconv.Quote(line)+",") {
			t.Errorf("expected quoted line %q in output:\n%s", line, out)
		}
	}
	got := extractDescriptionSlice(t, out)
	if !slices.Equal(got, desc) {
		t.Errorf("Description round-trip mismatch:\n got: %#v\nwant: %#v", got, desc)
	}
}

func TestRender_EmptyDescription_Nil(t *testing.T) {
	snap := mcpcli.Snapshot{
		Servers: []mcpcli.ServerSpec{{Name: "s", URL: "http://x"}},
		Tools: []mcpcli.ToolSpec{
			{Server: "s", Name: "t", Description: nil, SchemaJSON: `{"type":"object"}`},
		},
	}
	out, err := Render(snap, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "g.go", out, 0); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Description: nil,") {
		t.Errorf("expected nil Description:\n%s", out)
	}
}

func TestRender_SchemaJSON_PrettyPrinted(t *testing.T) {
	schema := `{"type":"object","properties":{"a":{"type":"string"}}}`
	snap := mcpcli.Snapshot{
		Servers: []mcpcli.ServerSpec{{Name: "s", URL: "http://x"}},
		Tools: []mcpcli.ToolSpec{
			{Server: "s", Name: "t", Description: []string{"x"}, SchemaJSON: schema},
		},
	}
	out, err := Render(snap, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "g.go", string(out), 0); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "\n  \"properties\": {") {
		t.Errorf("expected pretty-printed SchemaJSON with 2-space indent:\n%s", out)
	}
	if !strings.Contains(string(out), "SchemaJSON: `{\n") {
		t.Errorf("expected SchemaJSON to be a raw literal:\n%s", out)
	}
}

// extractDescriptionSlice parses src, finds the Description field literal,
// and returns the decoded []string. Fails the test on any malformed element
// rather than silently skipping it, so partial corruption can't masquerade
// as a passing round-trip.
func extractDescriptionSlice(t *testing.T, src []byte) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "g.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out []string
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		id, ok := kv.Key.(*ast.Ident)
		if !ok || id.Name != "Description" {
			return true
		}
		comp, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			t.Fatalf("Description value is %T, want *ast.CompositeLit", kv.Value)
		}
		for i, el := range comp.Elts {
			lit, ok := el.(*ast.BasicLit)
			if !ok {
				t.Fatalf("Description[%d] is %T, want *ast.BasicLit", i, el)
			}
			if lit.Kind != token.STRING {
				t.Fatalf("Description[%d] kind is %v, want STRING", i, lit.Kind)
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("Description[%d] unquote: %v (raw: %s)", i, err, lit.Value)
			}
			out = append(out, s)
		}
		found = true
		return false
	})
	if !found {
		t.Fatalf("Description []string literal not found in:\n%s", src)
	}
	return out
}

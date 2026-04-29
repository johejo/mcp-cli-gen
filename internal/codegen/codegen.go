// Package codegen renders a mcpcli.Snapshot into a Go source file that
// embeds the snapshot literal and delegates execution to mcpcli.Run.
package codegen

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"go/format"
	"strconv"
	"strings"
	"text/template"

	"github.com/johejo/mcp-cli-gen/mcpcli"
)

//go:embed template.go.tmpl
var rawTemplate string

var tmpl = template.Must(template.New("generated").Funcs(template.FuncMap{
	"quote":         strconv.Quote,
	"multilineJSON": multilineJSON,
	"stringSlice":   stringSliceLiteral,
}).Parse(rawTemplate))

// Render returns formatted Go source for the snapshot under the given
// package name (e.g. "main").
func Render(snap mcpcli.Snapshot, packageName string) ([]byte, error) {
	if packageName == "" {
		return nil, fmt.Errorf("package name is required")
	}
	tools := make([]mcpcli.ToolSpec, len(snap.Tools))
	for i, t := range snap.Tools {
		tools[i] = t
		tools[i].SchemaJSON = prettySchema(t.SchemaJSON)
	}
	data := struct {
		Package string
		Servers []mcpcli.ServerSpec
		Tools   []mcpcli.ToolSpec
	}{
		Package: packageName,
		Servers: snap.Servers,
		Tools:   tools,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated source: %w\n--- raw ---\n%s", err, buf.String())
	}
	return formatted, nil
}

// prettySchema returns s reformatted with 2-space indentation if it is
// non-empty valid JSON. Invalid or empty input is returned unchanged.
// Runtime parsing uses json.Unmarshal, which is whitespace-insensitive.
func prettySchema(s string) string {
	if s == "" {
		return s
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return s
	}
	return buf.String()
}

// multilineJSON renders s (already pretty-printed JSON) as a Go string
// expression. Pretty-printed JSON never contains backticks, so we can
// always use a raw string literal for multi-line forms; an empty schema
// or a single-line value falls back to strconv.Quote.
func multilineJSON(s string) string {
	if !strings.Contains(s, "\n") || strings.ContainsAny(s, "`\r") {
		return strconv.Quote(s)
	}
	return "`" + s + "`"
}

// stringSliceLiteral renders ss as a Go []string composite literal. The
// generated source places each element on its own line so that diffs are
// scoped to the lines that actually changed.
func stringSliceLiteral(ss []string) string {
	if len(ss) == 0 {
		return "nil"
	}
	var b strings.Builder
	b.WriteString("[]string{\n")
	for _, line := range ss {
		b.WriteString(strconv.Quote(line))
		b.WriteString(",\n")
	}
	b.WriteString("}")
	return b.String()
}

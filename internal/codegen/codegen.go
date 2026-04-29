// Package codegen renders a mcpcli.Snapshot into a Go source file that
// embeds the snapshot literal and delegates execution to mcpcli.Run.
package codegen

import (
	"bytes"
	_ "embed"
	"fmt"
	"go/format"
	"strconv"
	"text/template"

	"github.com/johejo/mcp-cli-gen/mcpcli"
)

//go:embed template.go.tmpl
var rawTemplate string

var tmpl = template.Must(template.New("generated").Funcs(template.FuncMap{
	"quote": strconv.Quote,
}).Parse(rawTemplate))

// Render returns formatted Go source for the snapshot under the given
// package name (e.g. "main").
func Render(snap mcpcli.Snapshot, packageName string) ([]byte, error) {
	if packageName == "" {
		return nil, fmt.Errorf("package name is required")
	}
	data := struct {
		Package string
		Servers []mcpcli.ServerSpec
		Tools   []mcpcli.ToolSpec
	}{
		Package: packageName,
		Servers: snap.Servers,
		Tools:   snap.Tools,
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

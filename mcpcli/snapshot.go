// Package mcpcli is the runtime library shared by both the generator-emitted
// CLI and the generic mcpc client. Generated Go code imports this package and
// calls Run with a static Snapshot.
package mcpcli

// Snapshot holds the full set of MCP servers and their tools that a CLI
// should expose. For the generator path, this is materialized at codegen
// time and embedded as Go literals. For mcpc, it is built at startup by
// connecting to each server and calling tools/list.
type Snapshot struct {
	Servers []ServerSpec
	Tools   []ToolSpec
}

// ServerSpec is one MCP server endpoint.
type ServerSpec struct {
	Name    string
	URL     string
	Headers map[string]string // values may contain ${VAR} / $VAR
}

// ToolSpec is a single MCP tool exposed under a given server.
type ToolSpec struct {
	Server      string   // matches ServerSpec.Name
	Name        string   // verbatim MCP tool name
	Description []string // one entry per source line; joined with "\n" at runtime
	SchemaJSON  string   // JSON-encoded inputSchema; unmarshalled into *jsonschema.Schema at runtime
}

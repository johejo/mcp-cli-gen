// Package config parses Claude-flavored mcp.json files.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

const FlavorClaude = "claude"

// Server is one entry under "mcpServers".
type Server struct {
	Name    string
	URL     string
	Headers map[string]string
}

// Config is the parsed mcp.json.
type Config struct {
	Servers []Server // sorted by Name for stable output
}

// rawClaude is the on-disk shape for the Claude flavor.
type rawClaude struct {
	McpServers map[string]rawClaudeServer `json:"mcpServers"`
}

type rawClaudeServer struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Load reads and parses an mcp.json. flavor must be FlavorClaude.
func Load(path, flavor string) (*Config, error) {
	if flavor != FlavorClaude {
		return nil, fmt.Errorf("unsupported config-flavor %q (only %q is supported)", flavor, FlavorClaude)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var raw rawClaude
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(raw.McpServers) == 0 {
		return nil, fmt.Errorf("%s: mcpServers is empty", path)
	}
	servers := make([]Server, 0, len(raw.McpServers))
	for name, s := range raw.McpServers {
		if s.URL == "" {
			return nil, fmt.Errorf("server %q: url is required (stdio servers are not supported)", name)
		}
		servers = append(servers, Server{Name: name, URL: s.URL, Headers: s.Headers})
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return &Config{Servers: servers}, nil
}

// ExpandHeaders returns headers with ${VAR} / $VAR replaced via os.Getenv.
// Missing variables expand to empty string (matching os.Expand semantics).
func ExpandHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = os.Expand(v, os.Getenv)
	}
	return out
}

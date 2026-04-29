// Package discover connects to every server in a config and produces a
// Snapshot by calling tools/list on each. Failed servers are reported on
// stderr but do not abort discovery.
package discover

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/johejo/mcp-cli-gen/internal/config"
	"github.com/johejo/mcp-cli-gen/mcpcli"
)

// Discover lists tools from every server in cfg in parallel. Servers that
// fail to connect or list tools are reported on stderr and excluded from
// the snapshot. The returned snapshot may be empty (e.g. all servers
// unreachable); callers decide whether that warrants an error.
func Discover(ctx context.Context, cfg *config.Config, stderr io.Writer) mcpcli.Snapshot {
	type result struct {
		srv   mcpcli.ServerSpec
		tools []mcpcli.ToolSpec
		err   error
	}

	results := make([]result, len(cfg.Servers))
	var wg sync.WaitGroup
	for i, s := range cfg.Servers {
		wg.Add(1)
		go func(i int, s config.Server) {
			defer wg.Done()
			spec := mcpcli.ServerSpec{Name: s.Name, URL: s.URL, Headers: s.Headers}
			tools, err := listTools(ctx, spec)
			results[i] = result{srv: spec, tools: tools, err: err}
		}(i, s)
	}
	wg.Wait()

	snap := mcpcli.Snapshot{}
	for _, r := range results {
		if r.err != nil {
			fmt.Fprintf(stderr, "warning: server %s: %v\n", r.srv.Name, r.err)
			continue
		}
		snap.Servers = append(snap.Servers, r.srv)
		snap.Tools = append(snap.Tools, r.tools...)
	}

	sort.SliceStable(snap.Servers, func(i, j int) bool { return snap.Servers[i].Name < snap.Servers[j].Name })
	sort.SliceStable(snap.Tools, func(i, j int) bool {
		if snap.Tools[i].Server != snap.Tools[j].Server {
			return snap.Tools[i].Server < snap.Tools[j].Server
		}
		return snap.Tools[i].Name < snap.Tools[j].Name
	})

	return snap
}

func listTools(ctx context.Context, spec mcpcli.ServerSpec) ([]mcpcli.ToolSpec, error) {
	session, err := mcpcli.Connect(ctx, spec)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	out := make([]mcpcli.ToolSpec, 0, len(res.Tools))
	for _, t := range res.Tools {
		schemaJSON := ""
		if t.InputSchema != nil {
			b, err := json.Marshal(t.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("marshal inputSchema for tool %s: %w", t.Name, err)
			}
			schemaJSON = string(b)
		}
		out = append(out, mcpcli.ToolSpec{
			Server:      spec.Name,
			Name:        t.Name,
			Description: splitLines(t.Description),
			SchemaJSON:  schemaJSON,
		})
	}
	return out, nil
}

// splitLines breaks s on "\n" so each entry maps to one source line in the
// generated code. Empty input yields nil (consistent with how the runtime
// treats an absent description). The mapping is invertible via
// strings.Join(_, "\n").
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

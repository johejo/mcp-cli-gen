package mcpcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// Run builds the cobra tree from snap and dispatches based on os.Args.
// The process exits with a non-zero status on error. The variadic opts
// argument keeps backward compatibility with generated code that calls
// Run(snap) with no options; only the first element is consulted.
func Run(snap Snapshot, opts ...Options) {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	if err := Execute(context.Background(), snap, o, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Execute is the testable entry point: it runs cobra against the supplied
// args and writes output to stdout/stderr writers. It returns nil on success
// and a non-nil error if the user-facing process should exit non-zero.
func Execute(ctx context.Context, snap Snapshot, opts Options, args []string, stdout, stderr io.Writer) error {
	if opts.Flatten && len(snap.Servers) > 1 {
		names := make([]string, len(snap.Servers))
		for i, s := range snap.Servers {
			names[i] = s.Name
		}
		return fmt.Errorf("--flatten requires a single server, but %d are configured: %s", len(snap.Servers), strings.Join(names, ", "))
	}
	root := buildRoot(ctx, snap, opts, stdout, stderr)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return root.Execute()
}

func buildRoot(ctx context.Context, snap Snapshot, opts Options, stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           filepath.Base(os.Args[0]),
		Short:         "MCP CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	servers := map[string]ServerSpec{}
	for _, s := range snap.Servers {
		servers[s.Name] = s
	}

	if opts.Flatten {
		for _, t := range snap.Tools {
			srv, ok := servers[t.Server]
			if !ok {
				fmt.Fprintf(stderr, "warning: tool %s/%s skipped: server %q not in snapshot\n", t.Server, t.Name, t.Server)
				continue
			}
			root.AddCommand(buildToolCmd(ctx, srv, t, stdout, stderr))
		}
		return root
	}

	groups := map[string]*cobra.Command{}
	for _, s := range snap.Servers {
		if _, ok := groups[s.Name]; ok {
			continue
		}
		c := &cobra.Command{
			Use:   s.Name,
			Short: fmt.Sprintf("Tools served by %s (%s)", s.Name, s.URL),
		}
		groups[s.Name] = c
		root.AddCommand(c)
	}

	for _, t := range snap.Tools {
		group, ok := groups[t.Server]
		if !ok {
			// Snapshot referenced a tool whose server wasn't declared.
			// Surface as a warning rather than panic; nothing to attach to.
			fmt.Fprintf(stderr, "warning: tool %s/%s skipped: server %q not in snapshot\n", t.Server, t.Name, t.Server)
			continue
		}
		group.AddCommand(buildToolCmd(ctx, servers[t.Server], t, stdout, stderr))
	}
	return root
}

func buildToolCmd(ctx context.Context, srv ServerSpec, tool ToolSpec, stdout, stderr io.Writer) *cobra.Command {
	schema := parseSchema(tool.SchemaJSON, stderr, tool.Server, tool.Name)

	cmd := &cobra.Command{
		Use:   tool.Name,
		Short: shortDescription(tool.Description),
		Long:  strings.Join(tool.Description, "\n"),
	}
	flags := bindFlags(cmd.Flags(), schema, stderr)
	cmd.Flags().String(flagParameters, "", "Send full payload as JSON: inline JSON object, or path to a .json file. Overrides per-flag values.")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		args, err := buildArgs(cmd, flags)
		if err != nil {
			return err
		}
		return invoke(ctx, srv, tool.Name, args, stdout)
	}
	return cmd
}

func parseSchema(schemaJSON string, stderr io.Writer, server, tool string) *jsonschema.Schema {
	if schemaJSON == "" {
		return nil
	}
	var s jsonschema.Schema
	if err := json.Unmarshal([]byte(schemaJSON), &s); err != nil {
		fmt.Fprintf(stderr, "warning: tool %s/%s: cannot parse inputSchema: %v\n", server, tool, err)
		return nil
	}
	return &s
}

func shortDescription(d []string) string {
	if len(d) == 0 {
		return ""
	}
	first := d[0]
	if i := strings.IndexAny(first, "\r\n"); i >= 0 {
		return first[:i]
	}
	return first
}

// buildArgs returns the arguments map to send to tools/call.
// If --parameters is present it wins; otherwise per-flag values are collected
// and required-property validation runs.
func buildArgs(cmd *cobra.Command, flags *boundFlags) (map[string]any, error) {
	params, _ := cmd.Flags().GetString(flagParameters)
	if params != "" {
		return loadParameters(params)
	}
	if missing := flags.missingRequired(); len(missing) > 0 {
		return nil, fmt.Errorf("required flag(s) not set: --%s", strings.Join(missing, ", --"))
	}
	return flags.collect()
}

// loadParameters resolves the --parameters value: inline JSON or path to a
// .json file. We treat a non-JSON-looking value as a path; a value starting
// with '{' or '[' as inline JSON.
func loadParameters(v string) (map[string]any, error) {
	trim := strings.TrimSpace(v)
	var data []byte
	if strings.HasPrefix(trim, "{") || strings.HasPrefix(trim, "[") {
		data = []byte(v)
	} else {
		b, err := os.ReadFile(v)
		if err != nil {
			return nil, fmt.Errorf("--parameters: %w", err)
		}
		data = b
	}
	var args map[string]any
	if err := json.Unmarshal(data, &args); err != nil {
		return nil, fmt.Errorf("--parameters: invalid JSON: %w", err)
	}
	return args, nil
}

func invoke(ctx context.Context, srv ServerSpec, toolName string, args map[string]any, stdout io.Writer) error {
	session, err := Connect(ctx, srv)
	if err != nil {
		return err
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return fmt.Errorf("call %s/%s: %w", srv.Name, toolName, err)
	}

	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	if res.IsError {
		return fmt.Errorf("tool %s/%s returned an error", srv.Name, toolName)
	}
	return nil
}

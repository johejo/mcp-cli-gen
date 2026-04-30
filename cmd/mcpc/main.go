// Command mcpc is the generic MCP CLI: it reads mcp.json at startup,
// connects to every server, and exposes each discovered tool as a typed
// cobra subcommand.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/johejo/mcp-cli-gen/internal/config"
	"github.com/johejo/mcp-cli-gen/internal/discover"
	"github.com/johejo/mcp-cli-gen/mcpcli"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mcpc:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("mcpc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "Path to mcp.json")
	flavor := fs.String("config-flavor", "", "Config flavor (only \"claude\" is supported)")
	flatten := fs.Bool("flatten", false, "Drop the server-name subcommand tier (single-server configs only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *configPath == "" {
		return fmt.Errorf("--config is required")
	}
	if *flavor == "" {
		return fmt.Errorf("--config-flavor is required")
	}

	cfg, err := config.Load(*configPath, *flavor)
	if err != nil {
		return err
	}

	ctx := context.Background()
	snap := discover.Discover(ctx, cfg, os.Stderr)
	// Empty snapshot is allowed: lets `mcpc --help` still print the root tree.

	rest := fs.Args()
	return mcpcli.Execute(ctx, snap, mcpcli.Options{Flatten: *flatten}, rest, os.Stdout, os.Stderr)
}

// Command mcp-cli-gen reads an mcp.json and emits a Go source file that
// exposes each MCP tool as a typed cobra subcommand. The emitted file is a
// thin bootstrap that delegates to github.com/johejo/mcp-cli-gen/mcpcli at
// runtime.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/johejo/mcp-cli-gen/internal/codegen"
	"github.com/johejo/mcp-cli-gen/internal/config"
	"github.com/johejo/mcp-cli-gen/internal/discover"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mcp-cli-gen:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("mcp-cli-gen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "Path to mcp.json")
	flavor := fs.String("config-flavor", "", "Config flavor (only \"claude\" is supported)")
	pkg := fs.String("package", "main", "Go package name for the generated file")
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

	snap := discover.Discover(context.Background(), cfg, os.Stderr)
	if len(snap.Tools) == 0 {
		return fmt.Errorf("no tools discovered from any server; nothing to generate")
	}

	out, err := codegen.Render(snap, *pkg)
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(out); err != nil {
		return err
	}
	return nil
}

# mcp-cli-gen

MCP CLI Generator.

`mcp-cli-gen` reads an `mcp.json` and exposes each MCP tool as a typed CLI subcommand. Two delivery modes are supported:

- **A. Generated Go binary** — generate Go source from `mcp.json` and `go build` a dedicated CLI.
- **B. Generic client `mcpc`** — reads `mcp.json` directly at runtime, no codegen step.

## Usage

### A. Generate a Go CLI

```
mcp-cli-gen --config </path/to/mcp.json> --config-flavor claude --package main > generated.go
go build -o <CLI_NAME> generated.go
<CLI_NAME> <SERVER> <TOOL_NAME> [flags...]
```

### B. Generic client

```
mcpc --config </path/to/mcp.json> --config-flavor claude <SERVER> <TOOL_NAME> [flags...]
```

## Subcommand layout

`mcp.json` typically declares multiple servers, each with multiple tools. Both binaries route via a two-tier `<server> <tool>` structure:

```
mycli github create_issue --title "fix bug" --labels bug,urgent
mycli filesystem read_file --path ./README.md
```

Tool names are not normalized — the original MCP tool name is used verbatim as the cobra subcommand name.

## Parameter UX

Tool parameters are exposed as typed flags derived from each tool's JSON Schema. Scalars, simple arrays, and enums become `--flag` style:

```
mycli search query --query foo --limit 10
```

Object-typed fields (or `oneOf` / `anyOf` / `$ref`) are accepted as a JSON string for that single field — graceful degradation:

```
mycli search query --query foo --filter '{"a":{"b":1}}'
```

You can also bypass per-flag mapping entirely by passing the full payload via `--parameters`, either inline JSON or a path to a JSON file:

```
mycli search query --parameters '{"query":"foo","limit":10}'
mycli search query --parameters ./params.json
```

## Output

`tools/call` results are serialized to JSON on stdout by default.

## Example `mcp.json`

Remote MCP only — URL plus headers. (stdio servers are out of scope.)

```json
{
  "mcpServers": {
    "github": {
      "url": "https://api.githubcopilot.com/mcp/",
      "headers": {
        "Authorization": "Bearer ${GITHUB_PAT}"
      }
    }
  }
}
```

## Design Notes

- Transport: remote MCP only (SSE / streamable-http). stdio is not supported.
- `--config-flavor` accepts `claude` only (other flavors error out; can be added later).
- Only `tools` are supported. `resources` and `prompts` are out of scope.
- Default output format is JSON.
- Generated Go code depends on this repository's runtime library; `generated.go` is a thin bootstrap that registers tool metadata and delegates to the runtime.
- Tool names are passed through unchanged.
- Use ONLY `github.com/modelcontextprotocol/go-sdk/...` for MCP interaction.
- Use `github.com/spf13/cobra` for CLI.
- Generator CLI: `./cmd/mcp-cli-gen`
- Generic client CLI: `./cmd/mcpc`
- Shared structures: `./internal`

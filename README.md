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

### Flattened layout (`--flatten`)

When `mcp.json` has a single server, the `<cli_name>` and `<server>` segments tend to overlap (e.g. `aws-knowledge-mcp aws-knowledge ...`). Pass `--flatten` to drop the server tier so tools attach directly to the root:

```
# default
aws-knowledge-mcp aws-knowledge aws___list_regions

# with --flatten
aws-knowledge-mcp aws___list_regions
```

`--flatten` is opt-in on both `mcp-cli-gen` and `mcpc`, and requires exactly one server in `mcp.json`; a multi-server config returns an error rather than silently merging tools.

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

## Examples

Two example CLIs live under `cmd/`, each generated from a checked-in `mcp.json`:

- `cmd/aws-knowledge-mcp` — [AWS Knowledge MCP](https://awslabs.github.io/mcp/servers/aws-knowledge-mcp-server) (no auth, generated with `--flatten`).
- `cmd/gh-mcp` — [GitHub remote MCP](https://github.com/github/github-mcp-server) (`GITHUB_PAT` required at runtime).

Run them via `go run`:

```
go run ./cmd/aws-knowledge-mcp aws___list_regions
GITHUB_PAT=<token> go run ./cmd/gh-mcp github get_me
```

Regenerate `generated.go` after the upstream tool list changes:

```
go run ./cmd/mcp-cli-gen --config ./cmd/aws-knowledge-mcp/mcp.json --config-flavor claude --package main --flatten > ./cmd/aws-knowledge-mcp/generated.go
GITHUB_PAT=<token> go run ./cmd/mcp-cli-gen --config ./cmd/gh-mcp/mcp.json --config-flavor claude --package main > ./cmd/gh-mcp/generated.go
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

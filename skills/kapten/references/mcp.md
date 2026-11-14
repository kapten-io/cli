# MCP server

Read this when connecting an agent to Kapten over MCP instead of shell commands.

`kapten mcp serve` exposes the CLI's resource operations, reads and writes, as MCP tools over local stdio. It uses the same authentication as the CLI: a saved context or server mode. Log in before starting it.

```json
{
  "mcpServers": {
    "kapten": {
      "command": "/absolute/path/to/kapten",
      "args": ["--context", "work", "mcp", "serve"]
    }
  }
}
```

For Claude Code: `claude mcp add kapten -- "$(command -v kapten)" --context work mcp serve`.

## Choosing the tools

- The context is fixed when the server starts. `kapten auth use` does not change a running server; restart it with another `--context`.
- `--operations id1,id2` exposes only the listed operations (their OpenAPI operation IDs, such as `listProjects` or `queryApplicationRunLogs`). Prefer it for agents that only need to read or to act on a few resources.
- `--request-timeout SECONDS` (default 30) and `--max-result-bytes BYTES` bound each call.
- Delete tools are marked destructive and reads read-only. These hints are advisory: keep the client's approval prompts on for writes.
- The server has no network listener. Do not expose it through a proxy.

## CLI or MCP

Use the CLI when a shell is available: its help shows schemas, and `-o json` with `jq` composes well. Use MCP for clients without a shell, or to give an agent a reduced set of operations with `--operations`.

Details: https://docs.kapten.io/cli/mcp.md

The docs have their own MCP server, for search only: `https://docs.kapten.io/mcp`.

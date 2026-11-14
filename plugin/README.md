# Kapten for coding agents

Skills and MCP servers that let coding agents such as Claude Code, Codex and
Cursor operate [Kapten](https://kapten.io):

- the `kapten` skill: Kapten's resource model, and how to work through the
  `kapten` CLI and its MCP server;
- the `kapten-docs` skill: how to search and read the docs at
  [docs.kapten.io](https://docs.kapten.io);
- the `kapten` MCP server, `kapten mcp serve`, run with
  `npx -y @kapten/cli@latest`: it needs Node.js and a login
  (`npx -y @kapten/cli@latest login`);
- the `kapten-docs` MCP server, `https://docs.kapten.io/mcp`.

## Install

```bash
# Claude Code
/plugin marketplace add kapten-io/skills
/plugin install kapten@kapten

# Skills only, for any agent
npx skills add kapten-io/skills
```

Codex, Cursor and other clients of the [Agent Plugins](https://agent-plugins.org)
standard install the plugin from this repository: `plugin.json` and
`mcp.json` follow it, and `.agents/plugins/marketplace.json` lists it for
Codex.

The CLI installs the same skill and servers, for the CLI version you run:
`kapten skills install --mcp`. See
[Coding agents](https://docs.kapten.io/cli/mcp).

## Versions

Each [CLI release](https://github.com/kapten-io/cli/releases) publishes this
repository's content, tagged with the same version. Its source is in
[kapten-io/cli](https://github.com/kapten-io/cli) (`plugin/` and
`skills/kapten`): open issues and pull requests there.

## License

[Apache-2.0](LICENSE)

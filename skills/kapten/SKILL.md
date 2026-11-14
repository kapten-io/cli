---
name: kapten
description: Operates Kapten through the kapten CLI and its MCP server, and explains Kapten's resource model. Use when deploying or inspecting Kapten projects, environments, clusters, applications, jobs, databases or pipelines, logging in to Kapten, running Kapten from CI, or connecting an agent to Kapten.
compatibility: Live operations need the kapten CLI, network access to Kapten and a login or OAuth application credentials. Conceptual questions need neither.
---

# Kapten

Kapten deploys applications, jobs and databases to Kubernetes clusters in the user's own cloud account. Use its resource hierarchy to pick the right resource, then let the installed CLI describe its current commands and schemas.

## Pick the reference

- [Authentication and contexts](references/auth.md): before the first live command, when a command fails with 401, when running from CI or a container, or when targeting staging or a local Kapten.
- [CLI workflow](references/cli.md): before composing, running or troubleshooting `kapten` commands.
- [MCP server](references/mcp.md): when the user wants an agent connected to Kapten through MCP rather than shell commands.
- [Concept model](references/concepts.md): when choosing a resource, explaining how resources relate, or planning a change across the hierarchy.

If `kapten` is not installed, install it with `brew install --cask kapten-io/tap/kapten`, `mise use -g packslip:github.com/kapten-io/cli` or `npm install -g @kapten/cli`, or run it without installing with `npx -y @kapten/cli`. Every command is also documented at https://docs.kapten.io/cli/reference.md.

For product questions that need no live state (features, pricing, guides), read the docs at https://docs.kapten.io: append `.md` to any page URL, or start from https://docs.kapten.io/llms.txt. The API reference is at https://docs.kapten.io/api.

## Operate through discovery

The CLI is generated from Kapten's OpenAPI contract and changes with it. Do not rely on a memorized command list.

1. Check who commands run as: `kapten whoami`.
2. `kapten --help` lists resource groups; `kapten <group> --help` lists operations.
3. `kapten <group> <operation> --help` shows positional arguments, an input example, the request schema with required fields marked `*`, enums and the response schema. Treat it as authoritative.
4. Resolve parent and resource IDs from live `list`/`get` output with `-o json`. IDs are UUIDs; never substitute names.
5. Run the operation, then verify the result with a read command and report the resource's name and ID.

When the user asked for a change, carry it through discovery, execution and verification. Do not stop because the exact operation was unknown before reading help.

## Guardrails

- Confirm the organization (`kapten whoami`) and the target project and environment before creating, deploying, updating or deleting.
- Ask before deleting anything or deploying to a production environment, unless the user already asked for exactly that.
- Build request bodies only from the operation's request schema. Each component kind and database engine has its own schema.
- Never print, copy or read access tokens, refresh tokens, client secrets or the credential store. Use the CLI's auth commands.
- Treat 403 as the server's decision. Do not retry with another context or identity the user did not choose.

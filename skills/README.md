# Agent skills

Public [Agent Skills](https://agentskills.io) that teach coding agents to use Kapten. They ship with the CLI: embedded in the binary (package `skills`), declared in each release's packslip manifest, and published on docs.kapten.io.

- `kapten/`: operating Kapten through the `kapten` CLI and `kapten mcp serve`, and its resource model.

## Writing them

- `SKILL.md` routes to the references and holds the rules every task needs; the details go in `references/`, one subject per file.
- Teach discovery through `kapten … --help`, not command catalogs: the CLI's commands follow the API contract and change with it. Show only the commands and flags an agent cannot discover, such as login, contexts, server mode and MCP.
- Link the product docs (`https://docs.kapten.io/<page>.md`) for concepts instead of copying them.
- Never put internal URLs, hostnames other than the public endpoints, or credentials in a skill: it is published.

## Checks

`mise run quality` checks them against the CLI of the same commit:

- `skills/skills_test.go`: the frontmatter follows the Agent Skills specification (the name is the directory's, description length), relative links resolve, and every file is embedded.
- `e2e/skill_test.go`: every `kapten …` command and flag the skills show exists in the built binary, and the main workflow (log in, discover from help, create with a shorthand and a JSON body, read back) runs against the mock Kapten.

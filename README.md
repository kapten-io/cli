# Kapten CLI

`kapten` manages [Kapten](https://kapten.io) from a terminal, CI jobs and coding
agents. Documentation: [docs.kapten.io/cli](https://docs.kapten.io/cli).

## Install

With Homebrew, on macOS and Linux:

```bash
brew install --cask kapten-io/tap/kapten
```

With [mise](https://mise.jdx.dev), which checks the release's signed
[packslip](https://packslip.dev) manifest before installing it, and installs
the shell completions of the version in use:

```bash
mise use -g packslip:github.com/kapten-io/cli
```

With npm, or without installing it, for MCP clients and CI jobs that have
Node.js:

```bash
npm install -g @kapten/cli
npx -y @kapten/cli@<version> mcp serve
```

Or download the archive for your platform from
[Releases](https://github.com/kapten-io/cli/releases), check it against
`checksums.txt` and its provenance
(`gh attestation verify <archive> --repo kapten-io/cli`), and put both
executables, `kapten` and `restish-mcp`, in the same directory on your `PATH`.

## How it works

The Kapten CLI is a branded Go binary backed by the MIT-licensed embedded
[Restish](https://github.com/rest-sh/restish) OpenAPI runtime. Resource commands
are selected and grouped directly in the Engine OpenAPI operations using
`x-cli-ignore`, `x-cli-name`, and tags.

## Development

```bash
mise run test
mise run build
./kapten --help
```

### Engine API contract

The commands come from the Engine OpenAPI document embedded in
`internal/spec/openapi.yml`. When the Engine's contract changes, copy the
bundled document from a checkout of the Engine's repository:

```bash
mise run spec:update -- ../kapten
```

### Endpoints

By default, the CLI logs in to production: the auth issuer
`https://auth.kapten.io` and the Engine API `https://api.kapten.io`. A login
saves the endpoints it used in its context, so several Kaptens can be used side
by side with `--context`. `kapten login --issuer-url URL --api-url URL`, or
`KAPTEN_ISSUER_URL` and `KAPTEN_API_URL`, point a new login (and server mode)
elsewhere; the flags win over the variables.

```bash
# Staging
kapten login --name staging --issuer-url https://auth-staging.kapten.io --api-url https://api-staging.kapten.io
kapten --context staging projects list

# The local stack (mise run dev)
kapten login --name local --issuer-url http://localhost:9200 --api-url http://localhost:9000
```

### Mock Kapten

`kapten-mock` serves a stand-in for auth and the Engine on one local port, to
try the CLI without an account or a running stack:

```bash
mise run mock            # or: go run ./cmd/kapten-mock
export KAPTEN_ISSUER_URL=http://127.0.0.1:8787/auth KAPTEN_API_URL=http://127.0.0.1:8787/api
kapten login --name mock       # approve the code on the page it prints
kapten projects list
```

Logins are approved on the mock's `/auth/device` page (`--auto-approve` skips
it). It also accepts the OAuth application it prints, for server mode. Engine
responses are generated from the embedded OpenAPI document: they have the right
shape, not real or consistent data. The mock is a development tool, not part of
a release.

The CLI reads the endpoints from the issuer's
`/.well-known/openid-configuration`. Its OAuth client is
`cli_9811a064-0835-43f4-a478-9ff95d92bc28`, which may
use the device authorization grant; `KAPTEN_OAUTH_CLIENT_ID` overrides it and
`KAPTEN_OAUTH_SCOPE` sets the requested scope.

## Commands

```bash
kapten login [--name work] [--no-browser]
kapten whoami [--json]
kapten logout [work|--all]
kapten auth list [--json]
kapten auth use work
kapten projects list
kapten environments list <project-id>
```

`login`, `whoami` and `logout` are shortcuts for `kapten auth login` and so on.

### Logging in

`kapten login` uses the device authorization grant (RFC 8628): it prints a page
and a code such as `WDJB-MJHT`, opens the page in a browser when one can open
(never over SSH, nor on Linux without a graphical session), and waits while
the user approves the code and picks an organization, on this machine or any
other. The code expires after ten minutes.

Every token is bound to an ES256 DPoP key (RFC 9449) the CLI generates at
login: each request to the token endpoint carries a proof signed by it. The
key, the access token and the refresh token are saved together in the OS
keychain, or in the owner-only file `credentials.json` under the Kapten config
directory (`~/.config/kapten` on Linux) when the keychain is unavailable, with a
warning at login; `auth list` reports the effective backend.
`KAPTEN_CREDENTIALS_STORE=file` chooses the file without trying the keychain,
for containers and CI machines where none runs. Access tokens are renewed
before they expire. auth rotates refresh tokens on every use and revokes the
grant when one is replayed, so concurrent kapten processes refresh one at a
time and reuse the token the first one got.

API requests carry the access token as `Authorization: Bearer`, like the UI's:
the Engine does not verify DPoP proofs yet. `apiAuthScheme` in
`internal/app/auth.go` switches them to the DPoP scheme with a proof per request.

`kapten logout` removes the saved credentials. auth has no revocation endpoint:
the refresh tokens also end with the browser session that approved them.
`kapten whoami` checks the session with auth: when it ended, it says to log in
again instead of showing a stale identity.

While waiting for approval, `kapten login` keeps polling through network errors
and auth restarts, backing off up to a minute between polls, until the code
expires or the user presses Ctrl-C.

### Output and errors

Lists print a table by default; `-o json` prints the API's response, pagination
included. API errors print one line to stderr and exit with
status 1, so stdout only ever holds results; with `-o json` the error document
goes to stdout for scripts.

### Server mode

With `KAPTEN_CLIENT_ID` set to an OAuth application's client id, commands and
`mcp serve` authenticate as that application through the client credentials
grant, without a login, using either:

- `KAPTEN_CLIENT_SECRET`, a client secret, or
- `KAPTEN_CLIENT_PRIVATE_KEY` (or `KAPTEN_CLIENT_PRIVATE_KEY_FILE`, a path), a
  private key as a JWK or PEM whose public key is registered on the
  application, signing a `private_key_jwt` client assertion.

The token stays in memory and is never saved. An explicit `--context` wins over
the environment, which wins over the active saved login; `kapten login` refuses
to run while `KAPTEN_CLIENT_ID` is set, and `kapten whoami` names the source.

```bash
KAPTEN_CLIENT_ID=cli_… KAPTEN_CLIENT_SECRET=kapten_sk_… kapten projects list
KAPTEN_CLIENT_ID=cli_… KAPTEN_CLIENT_PRIVATE_KEY_FILE=deploy.pem kapten projects list
```

The CLI materializes its embedded OpenAPI document under a content-addressed
cache path, so different installed versions cannot overwrite each other's
command contract. Cache failures are reported instead of silently using stale
specification data.

## Local MCP server

Kapten exposes its resource operations as MCP tools over local stdio, including
create, update, and delete operations:

```bash
kapten login --name work
kapten --context work mcp serve
```

Authenticate before starting the server, with `kapten login` or the server-mode
variables; it reuses the CLI's saved context, authentication, and token refresh. The selected context is pinned at startup.
Changing the active context with `auth use` does not change a running MCP
session: restart it to select another context.

For an MCP client that accepts `mcpServers` configuration, use an absolute path
to the installed Kapten executable:

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

CLI-exposed resource operations are enabled by default, including writes;
operations marked `x-cli-ignore` or `x-mcp-ignore` remain excluded. There is no
separate write opt-in. DELETE tools carry `readOnlyHint: false` and
`destructiveHint: true`. GET/HEAD and the semantic-read POST log queries carry
`readOnlyHint: true` and `destructiveHint: false`; other writes retain MCP's
conservative destructive-hint default. These annotations are advisory: client
confirmation policies and backend authorization remain responsible for consent
and access control.

Use `--operations operationId,...` to restrict the tool inventory (and the host's
request guard), `--request-timeout SECONDS` to bound API request time, and
`--max-result-bytes BYTES` to bound tool result size. These controls do not grant
access to otherwise excluded operations. For example, append
`"--operations", "queryApplicationRunLogs", "--request-timeout", "30",
"--max-result-bytes", "65536"` to the JSON `args` array above.

The upstream Restish MCP server is a **separate executable**, not an importable
server embedded in Kapten. The development build and every release archive
include `restish-mcp` (`restish-mcp.exe` on Windows), built from the Restish
module version pinned in `go.mod`. Keep it beside the real `kapten`
executable. Kapten resolves executable symlinks before finding the helper,
including Homebrew installations; it does not search `PATH` or automatically
install a missing helper. Start MCP through `kapten ... mcp serve`, not by
launching the helper directly.

Kapten uses standard newline-delimited JSON-RPC on stdio. A bounded framing
adapter translates to the pinned helper's legacy Content-Length framing and
adds tool annotations from the guarded contract; MCP tool generation and server
logic remain upstream. The helper reports protocol
version `2025-03-26`; clients must support its legacy `initialize` handshake.

MCP protocol output goes to stdout and diagnostics to stderr. This server has
no remote HTTP listener: keep the client connection local and do not expose
it through a network proxy. Tools run with the selected context's access to API
data, including logs, and can modify or delete resources. Configure client
approval policies appropriately or use `--operations` to expose only the tools
needed for a session.

## Coding agents

`kapten skills install` sets up coding agents to use Kapten: it writes the
agent skill of this CLI version where each agent reads skills, and with
`--mcp` adds two MCP servers to each agent's configuration: `kapten`
(`kapten mcp serve`, below) and `kapten-docs` (`https://docs.kapten.io/mcp`,
search and read the docs). Without `--agent`, it installs for every agent that
has run on this machine; `--global` installs for the user instead of the
current project, `--dry-run` only prints the files.

| Agent (`--agent`) | Skill, project / `--global` | MCP configuration, project / `--global` |
| --- | --- | --- |
| `claude-code` | `.claude/skills` / `~/.claude/skills` | `.mcp.json` / `~/.claude.json` |
| `codex` | `.agents/skills` / `~/.codex/skills` | `.codex/config.toml` / `~/.codex/config.toml` |
| `cursor` | `.agents/skills` / `~/.cursor/skills` | `.cursor/mcp.json` / `~/.cursor/mcp.json` |
| `opencode` | `.agents/skills` / `~/.config/opencode/skills` | `opencode.json` / `~/.config/opencode/opencode.json` |
| `copilot` | `.agents/skills` / `~/.copilot/skills` | `.vscode/mcp.json` / `~/.copilot/mcp-config.json` |

It replaces only the `kapten` skill and the `kapten` and `kapten-docs` server
entries, and refuses to touch a configuration file that is not valid JSON.
Running it again updates them, and says when the copy it replaces came from
another CLI version. The `kapten` server runs this executable by its absolute
path (or `npx -y @kapten/cli@<version>` when it runs from npx), with
`--context` when one is given: `kapten --context work skills install --mcp`.

## Agent plugin

This repository is also a plugin for coding agents, with the `kapten` and
`kapten-docs` skills and two MCP servers: `kapten` (run with
`npx -y @kapten/cli@latest mcp serve`, so it needs Node.js and a login made
with `kapten login` or the server-mode variables) and `kapten-docs`
(`https://docs.kapten.io/mcp`).

```bash
# Claude Code
/plugin marketplace add kapten-io/cli
/plugin install kapten@kapten

# Codex, Cursor and other Agent Plugins clients: install from this
# repository; the Codex marketplace is .agents/plugins/marketplace.json

# Skills only, for any agent (npx skills)
npx skills add kapten-io/cli
```

The root `plugin.json` and `mcp.json` follow
[Agent Plugins 1.0](https://agent-plugins.org); `.claude-plugin/` holds
Claude Code's manifest and marketplace. `skills/plugin_test.go` checks them
against the Agent Plugins schemas and against each other.
`skills/kapten-docs` is a copy of the skill docs.kapten.io generates:
`mise run skills:docs` updates it, and a weekly workflow fails when it is out
of date.

## Agent skill

`skills/kapten/` is an [Agent Skill](https://agentskills.io) that teaches
coding agents to operate Kapten through this CLI and `kapten mcp serve`. It
ships with the CLI, at the CLI's version: embedded in the binary, declared in
each release's packslip manifest (mise links it with
`mise skills sync --dir .agents/skills`), and published on docs.kapten.io.
`mise run quality` checks it against the binary: see
[skills/README.md](skills/README.md).

## Documentation

The command reference on [docs.kapten.io](https://docs.kapten.io/cli/reference)
is generated from the command tree and the help the CLI prints, by
`cmd/kapten-docs` (a development tool, not part of a release). The docs site
runs it at the CLI version it documents:

```bash
go run github.com/kapten-io/cli/cmd/kapten-docs@<version> <directory>
mise run docs -- <directory>   # from this checkout
```

`kapten-docs -skills <directory>` (`mise run docs:skills -- <directory>`)
writes the agent skills, for the docs site to publish.

## Tests

Run `mise run test` for the Go suite and offline release integration tests, or
`mise run quality` for everything CI runs.

### End to end

`e2e/` builds the real `kapten` binary and runs it as a separate process, with
an empty HOME and `KAPTEN_CREDENTIALS_STORE=file`, against `internal/mockserver`
(the stand-in `kapten-mock` serves), whose Engine only accepts access tokens its
auth issued. Logins are approved by submitting the device page the CLI prints.
The scenarios: help and version; login, whoami (text and JSON), auth list, a
second login refreshing the same context, logout; a denied or expired code;
a second Kapten named with `--issuer-url`/`--api-url`, `--context` and
`auth use`; every exposed resource command; table and JSON output; an access
token the Engine refuses, refreshed once and retried; an ended session asking
to log in again; server mode with a secret, a wrong or missing secret, a
private key, and precedence; API errors on stderr; unreachable services.

```bash
cd cli && go test ./e2e/
```

### Integration
Go unit tests are limited to argument parsing/output-option handling; other
behavior is exercised through command, HTTP-provider, and filesystem integration
scenarios.

The in-process mock in `internal/mockengine` reads the embedded OpenAPI contract,
generates fixtures with libopenapi, and validates requests/responses with
kin-openapi. It has no per-operation handlers or handwritten resource bodies.
UUID/date/time fixtures are normalized generically for reproducibility, and UUID
format validation is explicitly registered.

The suite executes every exposed resource operation with both explicit JSON and
default output, checks every resource group/subcommand help page, and compares
the actual help inventory with covered commands. Operations explicitly marked
`x-cli-ignore` are not CLI commands. Auth tests run against a fake auth service
(`internal/mockauth`, served by `internal/oauth/oauthtest`) that verifies every DPoP proof, binds device codes
and refresh tokens to the proof key and revokes a replayed refresh token, with
mocked keyrings; executable tests build the real CLI and check
metadata, help, completion scripts, and exit handling. Shell-completion queries
also cover both resource and auth command trees without requiring login.

MCP integration tests compile and launch the real pinned helper, exercise every
eligible resource tool (including creates, updates, and deletes) against the
schema-generated loopback mock, verify destructive DELETE annotations, operation
subset enforcement and context selection, and check 401-triggered OAuth refresh. Framing,
request guards, helper cancellation/reaping, and stdio isolation also have
coverage. They use fake credentials and temporary configuration/cache
directories, never a logged-in developer profile. Packaging regression tests use
synthetic Go outputs but inspect real tar/ZIP archives for both executables;
they do not publish releases.

All Kapten/Restish test configuration/cache directories are temporary, including
macOS HOME isolation. No real browser, keychain, credentials, or deployed API is used.
Resource mock calls deliberately disable retries/cache/auto-pagination to assert
one contract request. This is command/contract coverage, not proof of stateful
backend behavior, multi-page aggregation, or production OAuth interoperability.

## Releases

Releases are SemVer versions, tagged `vX.Y.Z` on `main`. To release, run the
[Release](https://github.com/kapten-io/cli/actions/workflows/release.yml)
workflow on `main`: "Run workflow" in the Actions tab. It releases `main`'s
head as the next version, picked from the commit titles since the latest
release (`scripts/release-next`): a breaking change (`feat!:`, or a
`BREAKING CHANGE:` footer) bumps the major version, so `feat!:` takes 0.x
to 1.0.0, `feat:` the minor version, and any other commit but `ci:`, `docs:`,
`test:` and `style:` the patch version. With only those, it fails: there is
nothing to release. The workflow runs on GitHub's runners:

1. **Build**: runs the quality gate again, cross-compiles `kapten` and the
   Restish MCP helper pinned in `go.mod` for darwin, linux and windows on amd64
   and arm64 (`scripts/release-build`), writes an SPDX SBOM per archive with
   Syft and `checksums.txt`, and the notes: the commits since the latest
   release (`scripts/release-notes`).
2. **Sign**: [packslip](https://packslip.dev) describes the archives (digests,
   platforms, the `kapten` executable, shell completions) in
   `packslip.sigstore.json`, signed keyless by this workflow's identity, and
   attests each archive's build provenance. It runs without write access to
   the release.
3. **Publish**: creates the GitHub release as a draft, uploads the files and
   the signed manifest and publishes it, which creates the tag on the released
   commit. A rerun replaces a draft's files.
4. **npm**: publishes `@kapten/cli-<os>-<cpu>` for each platform, holding both
   executables from the release's archives (checked against `checksums.txt`),
   then `@kapten/cli`, a launcher (`npm/bin/kapten.js`, no install script)
   that depends on them optionally, so npm installs only the matching one
   (`scripts/release-npm`). It runs in the `npm` environment, which only
   `main` can use, with npm trusted publishing: no token, and npm records the
   provenance.
5. **Verify**: checks the published archive against `checksums.txt` and its
   provenance, then runs the release installed with mise, which verifies the
   manifest, and with npx (`mise run release:verify -- vX.Y.Z`).

mise accepts a release only when a workflow of this repository signed it, and
remembers the workflow file: sign every release from `release.yml`.

Each archive holds both executables and this README; Windows archives are ZIPs
with `.exe` files.

Build the archives locally without publishing:

```bash
mise run release:snapshot
./dist/bin/linux_amd64/kapten --version
```

`python3 scripts/release_test.py` tests the release scripts offline, with
synthetic builds and local Git repositories; `mise run test` and
`mise run quality` include it.

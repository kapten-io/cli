# Authentication and contexts

Read this before the first live command, when a command fails with 401, or when Kapten runs unattended.

## Who commands run as

```bash
kapten whoami          # identity, organization, context, issuer, API URL
kapten whoami --json
kapten auth list       # saved contexts; * marks the active one
```

A **context** is a saved login: one identity, one organization, one auth issuer and one API URL. It is local to the machine, not a Kapten resource, and it is not a project or environment.

Commands use, in order of precedence:

1. `--context <name>` on the command;
2. OAuth application credentials from the environment (server mode, below);
3. the active saved context (`kapten auth use <name>` changes it).

## Logging in (people)

```bash
kapten login                    # prints a page and a code, opens a browser when it can
kapten login --no-browser       # over SSH or without a desktop: open the page on any device
kapten login --name work        # name the context (default: the organization's slug)
kapten logout [name|--all]
```

Login is interactive: the user approves the code in a browser and picks the organization. An agent can start it and relay the page and code, but cannot approve it. The code expires after ten minutes.

Credentials go to the OS keychain, or to an owner-only `credentials.json` in the Kapten config directory when no keychain is available. Set `KAPTEN_CREDENTIALS_STORE=file` in containers to skip the keychain. Access tokens refresh automatically.

When `whoami` says the session ended, or a command fails with 401 after a refresh, ask the user to run `kapten login` again.

## Server mode (CI, containers, unattended agents)

With an OAuth application's client ID in the environment, every command and `mcp serve` authenticate as that application, with no login and nothing saved:

```bash
export KAPTEN_CLIENT_ID=cli_…
export KAPTEN_CLIENT_SECRET=kapten_sk_…              # a client secret, or
export KAPTEN_CLIENT_PRIVATE_KEY_FILE=deploy.pem     # a private key (JWK or PEM), or KAPTEN_CLIENT_PRIVATE_KEY
kapten whoami
```

The application's roles decide what it can do. `kapten login` refuses to run while `KAPTEN_CLIENT_ID` is set. Read secrets from the CI's secret store; never write them into files, commands shown to the user, or logs.

## Other Kapten installations

Production (`https://auth.kapten.io`, `https://api.kapten.io`) is the default. A login to another installation saves its URLs in its context:

```bash
kapten login --name staging --issuer-url https://auth-staging.kapten.io --api-url https://api-staging.kapten.io
kapten --context staging projects list
```

`KAPTEN_ISSUER_URL` and `KAPTEN_API_URL` set the same URLs for a new login or for server mode; the flags win over them.

Details: https://docs.kapten.io/cli/authentication.md

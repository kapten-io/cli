# CLI workflow

Read this before composing, running or diagnosing `kapten` commands.

## Discover before composing

The binary embeds the API contract of its version. Its help is the source of truth:

```bash
kapten --help                       # resource groups and global flags
kapten <group> --help               # operations of a group
kapten <group> <operation> --help   # arguments, input example, request and response schemas
kapten <group> --help-all           # also the embedded runtime's inherited flags
```

If an expected command is absent, say the installed CLI does not expose it; do not build HTTP requests by hand from API conventions. `kapten --version` tells which CLI is installed.

Some groups have one operation per kind rather than a generic one. Databases have one create, get and update per engine (`kapten databases create-postgresql`, `get-mysql`, …): read the group's help for the engines and the database operations (deploy, runs, logs, and others the API exposes) of the installed version.

## Resolve the hierarchy

Start from the nearest known parent and list downward. Usage lines give the argument order; most commands under an environment need the project ID and the environment ID before the resource ID:

```bash
kapten projects list -o json
kapten environments list <project-id> -o json
kapten components list <project-id> <environment-id> -o json
```

## Build request bodies from help

Pass the fields as one quoted shorthand argument, or as JSON on stdin for larger bodies:

```bash
kapten projects create 'name: storefront, description: "Web store"'
jq -n '{name: "storefront", description: "Web store"}' | kapten projects create
```

In shorthand, nested fields use dots (`parent.child: value`), `list[]: value` appends, and unquoted numbers and booleans are typed: quote a value to keep it a string (`port: "8080"`). Shorthand after a piped document overrides its fields.

- `--rsh-generate-body` prints an example body with every field and sends nothing.
- `--rsh-validate` checks the body against the schema before sending it.

Supply every required field (`*`), keep enum spellings and units, and replace every placeholder value. Details: https://docs.kapten.io/cli/request-bodies.md

## Output

- The default output is for people: tables for lists, no HTTP headers.
- `-o json` prints the API's response, pagination included. Use it to capture IDs, with `jq`, and whenever exact fields matter.
- `-f '<jq expression>'` keeps part of the response, which sits under `.body`: `kapten projects list -f '.body.records[].id'`.
- Lists return one page: `--page-limit` and `--page-number` move through them.
- Errors print one line on stderr with the HTTP status and exit with status 1; with `-o json` the error document goes to stdout.
- `-v` adds request and response headers, `-vv` TLS details. Use them once to diagnose, and never quote authorization headers.

## Mutate and verify

1. Confirm the context (`kapten whoami`) and the parent hierarchy.
2. Read the operation's help and build the body from its schema.
3. Run the mutation and capture the returned ID.
4. Read the resource back (`get`, or `list` for deletions) and check the state. A deployment is an asynchronous run: follow it with the group's `get-run`, `list-runs` and `query-run-logs` operations until it succeeds or fails.

A zero exit status alone is not verification.

## Diagnose failures

- Discovery or connection error naming the issuer: the auth URL of the context is wrong or unreachable.
- Connection refused or DNS error naming the API URL: the API URL of the context is wrong or unreachable.
- `400`: the arguments or body do not match the schema; reread the operation's help (IDs must be UUIDs).
- `401`: the login ended; see [authentication](auth.md).
- `403`: the identity's role does not allow this in this organization, project or environment.
- `404`: recheck argument order, parent IDs and the target ID, and the context's organization.
- `409`: read the conflict in the response (for example, a project that still has environments) and agree on a correction with the user.
- `5xx`: retry once if the operation is safe to repeat, then report.

Details: https://docs.kapten.io/cli/output.md

Report failures with the command shape (no secrets), the context name, the HTTP status or transport error, and the next action.

package app

import (
	"fmt"
	"regexp"
	"strings"
)

// groupDescriptions replaces the generic "Operations tagged …" summaries the
// embedded runtime gives resource groups. Every generated group needs one
// (TestEveryResourceGroupHasADescription).
var groupDescriptions = map[string]string{
	"applications":      "Deploy and run long-running applications",
	"caches":            "Create and manage caches",
	"cloud-credentials": "Manage cloud provider credentials",
	"clusters":          "Connect and manage Kubernetes clusters",
	"components":        "List the components of an environment",
	"databases":         "Create and manage databases",
	"environments":      "Create and manage the environments of a project",
	"jobs":              "Run and schedule jobs",
	"pipelines":         "Build and run deployment pipelines",
	"projects":          "Create and manage projects",
	"usage":             "Show environment usage for billing",
}

var taggedSummary = regexp.MustCompile(`Operations tagged ([A-Za-z0-9 _-]+)`)

// rootHelp is the root page: Kapten's own commands, then the resource groups
// the embedded runtime generated from the Engine API.
const rootHelp = `Manage Kapten from your terminal, CI jobs and coding agents.

Usage:
  kapten <command> [flags]

Get started:
  login        Log in with a code approved in a browser
  whoami       Show who commands run as
  logout       Remove saved credentials
  auth         List, select and remove saved logins (contexts)

Resources:
%s
Coding agents:
  mcp          Serve Kapten operations over local stdio MCP
  skills       Install the agent skill and MCP servers for coding agents

Other:
  completion   Generate the autocompletion script for a shell
  help         Help about any command

Flags:
      --context string   Saved login to use for this command (see kapten auth list)
  -h, --help             Show help
      --help-all         Show every flag of the embedded runtime
  -v, --rsh-verbose      Show request and response headers (-vv adds TLS details)
      --version          Show the version

Examples:
  kapten login
  kapten projects list
  kapten environments list <project-id>
  kapten --context staging projects list
  KAPTEN_CLIENT_ID=… KAPTEN_CLIENT_SECRET=… kapten projects list

Use "kapten <command> --help" for more information about a command.
`

// rewriteHelp gives generated help pages Kapten's wording: real group
// descriptions and, on the root page, the login and MCP commands the
// embedded runtime does not know about.
func rewriteHelp(help string, root bool) string {
	help = taggedSummary.ReplaceAllStringFunc(help, func(match string) string {
		tag := taggedSummary.FindStringSubmatch(match)[1]
		if description, ok := groupDescriptions[tag]; ok {
			return description
		}
		return match
	})
	if !root {
		return help
	}
	_, resources, ok := strings.Cut(help, "Additional Commands:\n")
	if !ok {
		return help
	}
	resources, _, _ = strings.Cut(resources, "\n\n")
	return fmt.Sprintf(rootHelp, resources+"\n")
}

// isRootHelp reports whether args ask for the root help page.
func isRootHelp(args []string) bool {
	for _, arg := range beforeDelimiter(args) {
		if arg == "help" || strings.HasPrefix(arg, "-") {
			continue
		}
		return false
	}
	return true
}

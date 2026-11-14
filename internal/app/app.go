package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kapten-io/cli/internal/contextstore"
)

// clientID is the kapten CLI's public client in auth,
// which may use the device authorization grant.
const clientID = "cli_9811a064-0835-43f4-a478-9ff95d92bc28"

// Production endpoints. A login saves the endpoints it used in its context;
// --issuer-url and --api-url, or KAPTEN_ISSUER_URL and KAPTEN_API_URL, point
// a new login or server mode at staging or a local stack.
const defaultIssuerURL = "https://auth.kapten.io"
const defaultAPIURL = "https://api.kapten.io"
const paginatedRecordsFilter = `.body | if type == "object" and has("records") then .records else . end`

type App struct {
	In       io.Reader
	Out, Err io.Writer
	// MCPExecutable overrides the adjacent pinned helper for embedding/tests.
	MCPExecutable   string
	Version, Commit string
	BuildDate       string
	warnedFile      bool
	// contextName is the --context option, which Kapten consumes anywhere.
	contextName string
	// OpenBrowser overrides the browser launcher for embedding/headless flows.
	// A nil value uses the operating-system launcher when a browser can open.
	OpenBrowser func(context.Context, string) error
	// Sleep overrides the wait between device login polls in tests.
	Sleep func(context.Context, time.Duration) error
}

func New(out, err io.Writer) *App {
	return &App{In: os.Stdin, Out: out, Err: err, Version: "dev", Commit: "none", BuildDate: "unknown"}
}
func (a *App) Run(argv []string) error {
	return a.RunContext(context.Background(), argv)
}

func (a *App) RunContext(runCtx context.Context, argv []string) error {
	a.warnedFile = false
	args := []string{}
	if len(argv) > 1 {
		args = argv[1:]
	}
	if isCompletionRequest(args) {
		return a.complete(runCtx, args)
	}
	opts, args, err := parseKaptenArgs(args)
	if err != nil {
		return err
	}
	a.contextName = opts.Context
	if opts.Version && !opts.Help {
		fmt.Fprintf(a.Out, "kapten %s (commit %s, built %s)\n", a.Version, a.Commit, a.BuildDate)
		return nil
	}
	if len(args) >= 2 && args[0] == "help" && args[1] == "mcp" {
		return a.mcpContext(runCtx, append(append([]string{}, args[2:]...), "--help"), opts.Context)
	}
	for i, arg := range beforeDelimiter(args) {
		if arg == "mcp" {
			return a.mcpContext(runCtx, append(append([]string{}, args[:i]...), args[i+1:]...), opts.Context)
		}
		if arg != "--help" && !strings.HasPrefix(arg, "--help=") && arg != "-h" && !strings.HasPrefix(arg, "-h=") {
			break
		}
	}
	// login, logout and whoami are shortcuts for the auth commands.
	if len(args) > 0 && (args[0] == "login" || args[0] == "logout" || args[0] == "whoami") {
		return a.authContext(runCtx, args)
	}
	if len(args) >= 2 && args[0] == "help" && args[1] == "auth" {
		return a.authContext(runCtx, append(append([]string{}, args[2:]...), "--help"))
	}
	// Only leading Kapten help flags may precede the owned auth tree.
	for i, arg := range beforeDelimiter(args) {
		if arg == "auth" {
			return a.authContext(runCtx, append(append([]string{}, args[:i]...), args[i+1:]...))
		}
		if arg != "--help" && !strings.HasPrefix(arg, "--help=") && arg != "-h" && !strings.HasPrefix(arg, "-h=") {
			break
		}
	}
	return a.resourceWithContext(runCtx, args, opts.Context, opts.Help)
}

func (a *App) resourceWithContext(runCtx context.Context, args []string, name string, showHelp bool) error {
	runCtx, cancel := context.WithCancel(runCtx)
	defer cancel()
	showHelp = showHelp || len(args) == 0 || args[0] == "help" || args[0] == "completion" || optionSet(args, "--help-all")
	ctx := contextstore.Context{APIURL: env("KAPTEN_API_URL", defaultAPIURL)}
	if !showHelp {
		var err error
		ctx, err = resourceContext(name)
		if err != nil {
			return err
		}
	}

	cli, err := a.newResourceRuntime(runCtx, ctx)
	if err != nil {
		return err
	}
	rootHelp := showHelp && isRootHelp(args) && !optionSet(args, "--help-all")
	if !isCompletionRequest(args) {
		args = withPaginatedOutputDefaults(args)
		args = withHumanOutputDefault(args)
	}
	call := append([]string{"kapten", "--rsh-profile", "default"}, args...)
	var help *bytes.Buffer
	if showHelp && !isCompletionRequest(args) && (len(args) == 0 || args[0] != "completion") {
		help = &bytes.Buffer{}
		cli.Stdout = help
		defer func() { fmt.Fprint(a.Out, rewriteHelp(help.String(), rootHelp)) }()
	}
	if err := cli.Run(call); err != nil {
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		if description := restishExitDescription(err); description != "" {
			if usesHumanOutput(args) {
				return renderedAPIError{cause: err}
			}
			return fmt.Errorf("%s: %w", description, err)
		}
		return err
	}
	return nil
}

func restishExitDescription(err error) string {
	switch err.Error() {
	case "exit code 3":
		return "API request returned a redirect response"
	case "exit code 4":
		return "API request returned a client, authentication, or validation error"
	case "exit code 5":
		return "API request returned a server error"
	default:
		return ""
	}
}

// Retain the empty config: Restish emits first-run shell-setup hints using its
// own command name when this file is missing, which is misleading for Kapten.
func ensureRestishConfig() (string, error) {
	path := filepath.Join(contextstore.Dir(), "restish.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create Kapten config directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("create embedded CLI config: %w", err)
	}
	if _, err = file.WriteString("{}\n"); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("initialize embedded CLI config: %w", err)
	}
	if err = file.Close(); err != nil {
		return "", fmt.Errorf("close embedded CLI config: %w", err)
	}
	return path, nil
}

func withPaginatedOutputDefaults(args []string) []string {
	if len(args) < 2 || (args[1] != "list" && !strings.HasPrefix(args[1], "list-")) {
		return args
	}
	if outputOptionsSet(args) || os.Getenv("RSH_OUTPUT_FORMAT") != "" || os.Getenv("RSH_FILTER") != "" {
		return args
	}
	args = addDefaults(args,
		"--rsh-filter", paginatedRecordsFilter,
		"--rsh-output-format", "table",
	)
	if !optionSet(args, "--rsh-columns") && os.Getenv("RSH_COLUMNS") == "" {
		if columns := defaultTableColumns(args[0], args[1]); columns != "" {
			args = addDefaults(args, "--rsh-columns", columns)
		}
	}
	return args
}

func defaultTableColumns(group, command string) string {
	if command == "list-runs" {
		if group == "pipelines" {
			return "id,status,started_at,finished_at,message"
		}
		return "component_name,id,status,action,trigger,started_at,finished_at,message"
	}
	switch group {
	case "projects":
		return "name,id,description"
	case "environments":
		return "name,id,type,cluster_id"
	case "components":
		return "name,id,s_id,kind,subtype,resource_state,last_run_status"
	case "clusters":
		return "name,id,cloud_provider,state"
	case "pipelines":
		return "name,id,is_active,last_run_status,description"
	default:
		return ""
	}
}

func outputOptionsSet(args []string) bool {
	if optionSet(args, "--rsh-headers") || optionSet(args, "--rsh-status") || optionSet(args, "--rsh-silent") || optionSet(args, "-S") {
		return true
	}
	for _, arg := range beforeDelimiter(args) {
		switch {
		case strings.HasPrefix(arg, "-o") && !strings.HasPrefix(arg, "--"), arg == "--rsh-output-format", strings.HasPrefix(arg, "--rsh-output-format="),
			strings.HasPrefix(arg, "-f") && !strings.HasPrefix(arg, "--"), arg == "--rsh-filter", strings.HasPrefix(arg, "--rsh-filter="):
			return true
		}
	}
	return false
}

func optionSet(args []string, name string) bool {
	for _, arg := range beforeDelimiter(args) {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

func (a *App) list(jsonOutput bool) error {
	c, e := contextstore.Load()
	if e != nil {
		return e
	}
	if jsonOutput {
		if c.Contexts == nil {
			c.Contexts = []contextstore.Context{}
		}
		return json.NewEncoder(a.Out).Encode(struct {
			Active   string                 `json:"active"`
			Contexts []contextstore.Context `json:"contexts"`
		}{c.Active, c.Contexts})
	}
	if len(c.Contexts) == 0 {
		fmt.Fprintln(a.Out, "No Kapten contexts. Run `kapten login`.")
		return nil
	}
	for _, x := range c.Contexts {
		mark := " "
		if x.Name == c.Active {
			mark = "*"
		}
		fmt.Fprintf(a.Out, "%s %-20s org=%s user=%s storage=%s\n", mark, x.Name, x.OrganizationID, emailOr(x.Subject, x.Email), x.Storage)
	}
	return nil
}
func (a *App) use(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: kapten auth use <context>")
	}
	c, e := contextstore.Load()
	if e != nil {
		return e
	}
	if _, ok := c.Find(args[0]); !ok {
		return fmt.Errorf("context %q was not found", args[0])
	}
	c.Active = args[0]
	return contextstore.Save(c)
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func emailOr(s, e string) string {
	if e != "" {
		return e
	}
	return s
}

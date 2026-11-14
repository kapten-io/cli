package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type kaptenOptions struct {
	Context       string
	Help, Version bool
}

// Restish has no public AddCommand hook. Consume Kapten's context/version
// options, validate shared help flags, and preserve generated argv (including --).
func parseKaptenArgs(args []string) (kaptenOptions, []string, error) {
	var opts kaptenOptions
	flags := pflag.NewFlagSet("kapten", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.Context, "context", "", "Kapten context")
	flags.BoolVarP(&opts.Help, "help", "h", false, "Show help")
	flags.BoolVar(&opts.Version, "version", false, "Show version")
	owned, rest := []string{}, []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if arg == "--context" {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return opts, nil, fmt.Errorf("flag needs an argument: --context")
			}
			owned = append(owned, arg, args[i+1])
			i++
		} else if strings.HasPrefix(arg, "--context=") {
			owned = append(owned, arg)
		} else if arg == "--version" || strings.HasPrefix(arg, "--version=") {
			owned = append(owned, arg)
		} else if arg == "--help" || strings.HasPrefix(arg, "--help=") || arg == "-h" || strings.HasPrefix(arg, "-h=") {
			owned = append(owned, arg)
			rest = append(rest, arg)
		} else {
			rest = append(rest, arg)
		}
	}
	if err := flags.Parse(owned); err != nil {
		return opts, nil, err
	}
	return opts, rest, nil
}

func beforeDelimiter(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[:i]
		}
	}
	return args
}

func addDefaults(args []string, defaults ...string) []string {
	i := len(beforeDelimiter(args))
	out := append([]string{}, args[:i]...)
	out = append(out, defaults...)
	return append(out, args[i:]...)
}

func isCompletionRequest(args []string) bool {
	return len(args) > 0 && (args[0] == cobra.ShellCompRequestCmd || args[0] == cobra.ShellCompNoDescRequestCmd)
}

// Both generated shell scripts query the same executable. Route owned auth
// completion to Cobra and retain Restish's generated resource completion.
func (a *App) complete(ctx context.Context, args []string) error {
	if len(args) >= 2 && args[1] == "auth" {
		return a.authContext(ctx, append([]string{args[0]}, args[2:]...))
	}
	if len(args) >= 2 && args[1] == "mcp" {
		return a.mcpContext(ctx, append([]string{args[0]}, args[2:]...), "")
	}
	if len(args) >= 2 && args[1] == "skills" {
		return a.skillsContext(ctx, append([]string{args[0]}, args[2:]...))
	}
	if len(args) != 2 || (!strings.HasPrefix("auth", args[1]) && !strings.HasPrefix("mcp", args[1]) && !strings.HasPrefix("skills", args[1])) {
		return a.resourceWithContext(ctx, args, "", true)
	}
	var output bytes.Buffer
	copy := *a
	copy.Out = &output
	if err := copy.resourceWithContext(ctx, args, "", true); err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], ":") {
		return fmt.Errorf("unexpected shell completion response")
	}
	for _, line := range lines[:len(lines)-1] {
		if line != "" {
			fmt.Fprintln(a.Out, line)
		}
	}
	for _, owned := range []struct{ name, description string }{
		{"auth", "Manage Kapten authentication contexts"},
		{"mcp", "Serve Kapten resource operations over local stdio MCP"},
		{"skills", "Install the Kapten agent skill and MCP servers for coding agents"},
	} {
		if !strings.HasPrefix(owned.name, args[1]) {
			continue
		}
		suggestion := owned.name
		if args[0] == cobra.ShellCompRequestCmd {
			suggestion += "\t" + owned.description
		}
		fmt.Fprintln(a.Out, suggestion)
	}
	fmt.Fprintln(a.Out, lines[len(lines)-1])
	return nil
}

func (a *App) authContext(ctx context.Context, args []string) error {
	root := a.authCommand()
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// authCommand is the tree of Kapten's own auth commands, which the embedded
// runtime does not know about. The CLI reference is generated from it too.
func (a *App) authCommand() *cobra.Command {
	root := &cobra.Command{Use: "kapten", Annotations: map[string]string{cobra.CommandDisplayNameAnnotation: "kapten auth"}, Short: "Manage Kapten authentication contexts", SilenceErrors: true, SilenceUsage: true}
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	var output string
	var jsonOutput, all bool
	var loginOpts loginOptions
	login := &cobra.Command{Use: "login", Short: "Log in with a code approved in a browser", Args: cobra.NoArgs,
		Long:    "Log in with the device authorization grant: open the page shown, on this machine or any other,\nand approve the code. Works over SSH. Credentials are saved in the OS keychain, or in an\nowner-only file when no keychain is available.\n\nWith KAPTEN_CLIENT_ID and KAPTEN_CLIENT_SECRET set, commands authenticate as that OAuth\napplication instead and no login is needed.",
		Example: "  kapten login\n  kapten login --no-browser\n  kapten login --name staging --issuer-url https://auth-staging.kapten.io --api-url https://api-staging.kapten.io",
		RunE:    func(cmd *cobra.Command, _ []string) error { return a.login(cmd.Context(), loginOpts) }}
	login.Flags().StringVar(&loginOpts.name, "name", "", "Name for a new context (default: the organization's slug)")
	login.Flags().BoolVar(&loginOpts.noBrowser, "no-browser", false, "Do not open a browser")
	login.Flags().StringVar(&loginOpts.issuerURL, "issuer-url", "", "Auth service URL (default $KAPTEN_ISSUER_URL, else "+defaultIssuerURL+")")
	login.Flags().StringVar(&loginOpts.apiURL, "api-url", "", "Engine API URL (default $KAPTEN_API_URL, else "+defaultAPIURL+")")
	whoami := &cobra.Command{Use: "whoami", Short: "Show who commands run as", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.whoami(cmd.Context(), a.contextName, jsonOutput) }}
	whoami.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON")
	list := &cobra.Command{Use: "list", Short: "List saved contexts", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if output != "" && output != "json" && output != "table" {
				return fmt.Errorf("unsupported auth list output %q", output)
			}
			if jsonOutput || output == "json" {
				return a.list(true)
			}
			return a.list(false)
		}}
	list.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON")
	list.Flags().StringVarP(&output, "output", "o", "", "Output format: table or json")
	use := &cobra.Command{Use: "use <context>", Short: "Select the active context", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return a.use(args) }}
	logout := &cobra.Command{Use: "logout [context]", Short: "Remove saved credentials", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("--all cannot be combined with a context name")
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			return a.logoutContext(name, all)
		}}
	logout.Flags().BoolVar(&all, "all", false, "Log out of every context")
	root.AddCommand(login, whoami, list, use, logout)
	return root
}

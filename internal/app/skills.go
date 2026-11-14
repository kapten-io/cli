package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kapten-io/cli/skills"
)

// docsMCPURL is the docs site's MCP server: search and read the docs.
const docsMCPURL = "https://docs.kapten.io/mcp"

// versionFile, written beside an installed skill, records the CLI version
// that wrote it.
const versionFile = ".kapten-cli-version"

// mcpFormat is the shape of an agent's MCP configuration file.
type mcpFormat int

const (
	mcpServersJSON mcpFormat = iota // {"mcpServers": {...}}: Claude Code, Cursor
	copilotJSON                     // {"mcpServers": {...}} with type and tools: Copilot CLI
	vscodeJSON                      // {"servers": {...}}: VS Code
	opencodeJSON                    // {"mcp": {...}}: OpenCode
	codexTOML                       // [mcp_servers.<name>]: Codex
)

type agentLocation struct {
	skills string // directory of skill directories
	mcp    string // MCP configuration file
	format mcpFormat
}

// codingAgent is where an agent reads skills and MCP servers, in a project
// (relative to its root) and for the user (relative to HOME).
type codingAgent struct {
	name, title string
	detect      string // a directory under HOME that exists once the agent ran
	project     agentLocation
	user        agentLocation
}

// codingAgents are the agents `skills install` knows. Several read the shared
// .agents/skills directory of a project.
var codingAgents = []codingAgent{
	{"claude-code", "Claude Code", ".claude",
		agentLocation{".claude/skills", ".mcp.json", mcpServersJSON},
		agentLocation{".claude/skills", ".claude.json", mcpServersJSON}},
	{"codex", "Codex", ".codex",
		agentLocation{".agents/skills", ".codex/config.toml", codexTOML},
		agentLocation{".codex/skills", ".codex/config.toml", codexTOML}},
	{"cursor", "Cursor", ".cursor",
		agentLocation{".agents/skills", ".cursor/mcp.json", mcpServersJSON},
		agentLocation{".cursor/skills", ".cursor/mcp.json", mcpServersJSON}},
	{"opencode", "OpenCode", ".config/opencode",
		agentLocation{".agents/skills", "opencode.json", opencodeJSON},
		agentLocation{".config/opencode/skills", ".config/opencode/opencode.json", opencodeJSON}},
	{"copilot", "GitHub Copilot", ".copilot",
		agentLocation{".agents/skills", ".vscode/mcp.json", vscodeJSON},
		agentLocation{".copilot/skills", ".copilot/mcp-config.json", copilotJSON}},
}

func agentNames() []string {
	names := make([]string, len(codingAgents))
	for i, agent := range codingAgents {
		names[i] = agent.name
	}
	return names
}

type skillsInstallOptions struct {
	agents []string
	global bool
	mcp    bool
	dryRun bool
	dir    string
}

func (a *App) skillsContext(ctx context.Context, args []string) error {
	root := a.skillsCommand()
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// skillsCommand is the tree of the skills commands.
func (a *App) skillsCommand() *cobra.Command {
	root := &cobra.Command{Use: "kapten", Annotations: map[string]string{cobra.CommandDisplayNameAnnotation: "kapten skills"},
		Short: "Install the Kapten agent skill and MCP servers for coding agents", SilenceErrors: true, SilenceUsage: true}
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	var opts skillsInstallOptions
	install := &cobra.Command{Use: "install", Short: "Install the agent skill, and the MCP servers with --mcp", Args: cobra.NoArgs,
		Long: "Write the kapten agent skill of this CLI version where coding agents read skills: in the\n" +
			"current project, or for the user with --global. Without --agent, it installs for every agent\n" +
			"that has run on this machine. Agents: " + strings.Join(agentNames(), ", ") + ".\n\n" +
			"With --mcp, it also adds two MCP servers to each agent's configuration, leaving other\n" +
			"entries untouched: kapten (`kapten mcp serve`, with --context when given) and kapten-docs\n" +
			"(" + docsMCPURL + "). Running it again updates what it wrote.",
		Example: "  kapten skills install\n  kapten skills install --agent claude-code --mcp\n  kapten skills install --global --mcp --dry-run\n  kapten --context work skills install --agent codex --mcp",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.installSkills(opts)
		}}
	install.Flags().StringSliceVar(&opts.agents, "agent", nil, "Agents to install for, comma-separated: "+strings.Join(agentNames(), ", "))
	install.Flags().BoolVar(&opts.global, "global", false, "Install for the user instead of the current project")
	install.Flags().BoolVar(&opts.mcp, "mcp", false, "Also add the kapten and kapten-docs MCP servers")
	install.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Print what would be written, and write nothing")
	install.Flags().StringVar(&opts.dir, "dir", "", "Project root (default: the current directory)")
	_ = install.RegisterFlagCompletionFunc("agent", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return agentNames(), cobra.ShellCompDirectiveNoFileComp
	})
	root.AddCommand(install)
	return root
}

func (a *App) installSkills(opts skillsInstallOptions) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	base := opts.dir
	if opts.global {
		if base != "" {
			return errors.New("--dir and --global cannot be used together")
		}
		base = home
	} else if base == "" {
		if base, err = os.Getwd(); err != nil {
			return err
		}
	}

	var agents []codingAgent
	if len(opts.agents) > 0 {
		for _, name := range opts.agents {
			i := slices.IndexFunc(codingAgents, func(agent codingAgent) bool { return agent.name == name })
			if i < 0 {
				return fmt.Errorf("unknown agent %q: use %s", name, strings.Join(agentNames(), ", "))
			}
			if !slices.ContainsFunc(agents, func(agent codingAgent) bool { return agent.name == name }) {
				agents = append(agents, codingAgents[i])
			}
		}
	} else {
		for _, agent := range codingAgents {
			if info, err := os.Stat(filepath.Join(home, agent.detect)); err == nil && info.IsDir() {
				agents = append(agents, agent)
			}
		}
		if len(agents) == 0 {
			return fmt.Errorf("found no coding agent on this machine; name one with --agent (%s)", strings.Join(agentNames(), ", "))
		}
	}

	verb := "Wrote"
	if opts.dryRun {
		verb = "Would write"
	}
	written := map[string]bool{}
	for _, agent := range agents {
		location := agent.project
		if opts.global {
			location = agent.user
		}
		dir := filepath.Join(base, filepath.FromSlash(location.skills))
		if !written[dir] {
			written[dir] = true
			for _, name := range skills.Names() {
				target := filepath.Join(dir, name)
				note := a.installedNote(target)
				if !opts.dryRun {
					if err := skills.Write(name, dir); err != nil {
						return err
					}
					if err := os.WriteFile(filepath.Join(target, versionFile), []byte(a.Version+"\n"), 0o644); err != nil {
						return err
					}
				}
				fmt.Fprintf(a.Out, "%s the %s skill to %s (%s)%s\n", verb, name, a.display(base, target, opts.global), agentTitles(agents, location.skills, opts.global), note)
			}
		}
		if opts.mcp {
			file := filepath.Join(base, filepath.FromSlash(location.mcp))
			server, err := a.mcpServer()
			if err != nil {
				return err
			}
			if !opts.dryRun {
				if err := writeMCPConfig(file, location.format, server); err != nil {
					return fmt.Errorf("%s: %w", file, err)
				}
			}
			fmt.Fprintf(a.Out, "%s the kapten and kapten-docs MCP servers to %s (%s)\n", verb, a.display(base, file, opts.global), agent.title)
		}
	}
	if opts.mcp && !opts.dryRun {
		fmt.Fprintln(a.Out, "Restart the agents to load the MCP servers. The kapten server uses the CLI's login: run `kapten login` first.")
	}
	return nil
}

// installedNote says when a skill already there came from another version.
func (a *App) installedNote(target string) string {
	data, err := os.ReadFile(filepath.Join(target, versionFile))
	if err != nil {
		return ""
	}
	previous := strings.TrimSpace(string(data))
	if previous == a.Version {
		return ""
	}
	return fmt.Sprintf(", replacing the copy from CLI %s", previous)
}

func agentTitles(agents []codingAgent, skillsDir string, global bool) string {
	var titles []string
	for _, agent := range agents {
		location := agent.project
		if global {
			location = agent.user
		}
		if location.skills == skillsDir {
			titles = append(titles, agent.title)
		}
	}
	return strings.Join(titles, ", ")
}

func (a *App) display(base, path string, global bool) string {
	relative, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	if global {
		return filepath.Join("~", relative)
	}
	return relative
}

// stdioServer is how an agent starts `kapten mcp serve`.
type stdioServer struct {
	command string
	args    []string
}

// mcpServer is this executable's MCP server command: its absolute path, or
// npx when it runs from npx's cache, which npm may clear.
func (a *App) mcpServer() (stdioServer, error) {
	var global []string
	if a.contextName != "" {
		global = []string{"--context", a.contextName}
	}
	executable, err := os.Executable()
	if err != nil {
		return stdioServer{}, fmt.Errorf("find the kapten executable: %w", err)
	}
	if strings.Contains(filepath.ToSlash(executable), "/_npx/") {
		pkg := "@kapten/cli"
		if a.Version != "dev" {
			pkg += "@" + a.Version
		}
		return stdioServer{"npx", append(append([]string{"-y", pkg}, global...), "mcp", "serve")}, nil
	}
	return stdioServer{executable, append(global, "mcp", "serve")}, nil
}

// writeMCPConfig adds or replaces the kapten and kapten-docs servers in an
// agent's MCP configuration and leaves the rest of the file as it was.
func writeMCPConfig(file string, format mcpFormat, server stdioServer) error {
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var out []byte
	if format == codexTOML {
		out = codexConfig(data, server)
	} else {
		out, err = jsonConfig(data, format, server)
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(file); err == nil {
		mode = info.Mode().Perm()
	}
	tmp := file + ".kapten-tmp"
	if err := os.WriteFile(tmp, out, mode); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func jsonConfig(data []byte, format mcpFormat, server stdioServer) ([]byte, error) {
	config := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("not valid JSON, left unchanged: %w", err)
		}
	}
	key := map[mcpFormat]string{mcpServersJSON: "mcpServers", copilotJSON: "mcpServers", vscodeJSON: "servers", opencodeJSON: "mcp"}[format]
	servers := map[string]json.RawMessage{}
	if raw, ok := config[key]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, fmt.Errorf("%s is not an object, left unchanged: %w", key, err)
		}
	}
	var kapten, docs any
	switch format {
	case mcpServersJSON:
		kapten = map[string]any{"command": server.command, "args": server.args}
		docs = map[string]any{"type": "http", "url": docsMCPURL}
	case copilotJSON:
		kapten = map[string]any{"type": "local", "command": server.command, "args": server.args, "tools": []string{"*"}}
		docs = map[string]any{"type": "http", "url": docsMCPURL, "tools": []string{"*"}}
	case vscodeJSON:
		kapten = map[string]any{"type": "stdio", "command": server.command, "args": server.args}
		docs = map[string]any{"type": "http", "url": docsMCPURL}
	case opencodeJSON:
		kapten = map[string]any{"type": "local", "command": append([]string{server.command}, server.args...), "enabled": true}
		docs = map[string]any{"type": "remote", "url": docsMCPURL, "enabled": true}
	}
	for name, value := range map[string]any{"kapten": kapten, "kapten-docs": docs} {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		servers[name] = raw
	}
	raw, err := json.Marshal(servers)
	if err != nil {
		return nil, err
	}
	config[key] = raw
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

var tomlTable = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)

// codexConfig replaces the [mcp_servers.kapten] and [mcp_servers.kapten-docs]
// tables of a Codex config.toml, keeping every other line as it was.
func codexConfig(data []byte, server stdioServer) []byte {
	owned := func(table string) bool {
		table = strings.ReplaceAll(strings.ReplaceAll(table, `"`, ""), " ", "")
		for _, name := range []string{"kapten", "kapten-docs"} {
			if table == "mcp_servers."+name || strings.HasPrefix(table, "mcp_servers."+name+".") {
				return true
			}
		}
		return false
	}
	var kept []string
	skipping := false
	for _, line := range strings.Split(string(data), "\n") {
		if m := tomlTable.FindStringSubmatch(line); m != nil {
			skipping = owned(m[1])
		}
		if !skipping {
			kept = append(kept, line)
		}
	}
	text := strings.TrimRight(strings.Join(kept, "\n"), "\n")
	quoted := make([]string, len(server.args))
	for i, arg := range server.args {
		quoted[i] = strconv.Quote(arg)
	}
	var b strings.Builder
	if text != "" {
		b.WriteString(text + "\n\n")
	}
	fmt.Fprintf(&b, "[mcp_servers.kapten]\ncommand = %s\nargs = [%s]\n\n", strconv.Quote(server.command), strings.Join(quoted, ", "))
	fmt.Fprintf(&b, "[mcp_servers.kapten-docs]\nurl = %s\n", strconv.Quote(docsMCPURL))
	return []byte(b.String())
}

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// referenceRoute is where the generated pages are served on the docs site.
const referenceRoute = "/cli/reference"

// WriteReference writes the CLI reference of the docs site into dir: one MDX
// page per command group, from the same command trees and help the CLI
// shows, so the pages cannot drift from the binary. Files in dir that it
// did not write are removed.
func (a *App) WriteReference(ctx context.Context, dir string) error {
	pages, err := a.referencePages(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, page := range pages {
		keep[page.name] = true
		if err := os.WriteFile(filepath.Join(dir, page.name), []byte(page.body), 0o644); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && !keep[entry.Name()] {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

type referencePage struct{ name, body string }

type referenceGroup struct{ name, description string }

func (a *App) referencePages(ctx context.Context) ([]referencePage, error) {
	root, err := a.helpText(ctx, nil)
	if err != nil {
		return nil, err
	}
	groups := helpEntries(root, "Resources:")
	if len(groups) == 0 {
		return nil, fmt.Errorf("the root help lists no resource groups")
	}

	var pages []referencePage
	order := []string{"index", "auth"}
	pages = append(pages, referencePage{"auth.mdx", cobraPage("auth", "Log in, check who commands run as, and switch between saved logins (contexts).", authIntro, a.authCommand())})
	for _, group := range groups {
		body, err := a.groupPage(ctx, group)
		if err != nil {
			return nil, err
		}
		pages = append(pages, referencePage{group.name + ".mdx", body})
		order = append(order, group.name)
	}
	pages = append(pages, referencePage{"mcp.mdx", cobraPage("mcp", "Serve Kapten operations as MCP tools to a coding agent, over stdio.", mcpIntro, a.mcpCommand(""))})
	order = append(order, "mcp")
	pages = append(pages, referencePage{"skills.mdx", cobraPage("skills", "Install the Kapten agent skill and MCP servers for coding agents.", skillsIntro, a.skillsCommand())})
	order = append(order, "skills")
	pages = append(pages, referencePage{"index.mdx", indexPage(root, groups)})
	pages = append(pages, referencePage{"meta.ts", metaFile(order)})
	sort.Slice(pages, func(i, j int) bool { return pages[i].name < pages[j].name })
	return pages, nil
}

// helpText is the help page the CLI prints for args, as a user sees it.
func (a *App) helpText(ctx context.Context, args []string) (string, error) {
	var out, errOut bytes.Buffer
	copy := *a
	copy.Out, copy.Err = &out, &errOut
	if err := copy.resourceWithContext(ctx, append(append([]string{}, args...), "--help"), "", true); err != nil {
		return "", fmt.Errorf("help for %q: %w: %s", strings.Join(args, " "), err, errOut.String())
	}
	return out.String(), nil
}

const authIntro = "`kapten login`, `kapten whoami` and `kapten logout` are shortcuts for the `kapten auth` commands of the same name. See [Authentication](/cli/authentication) for how logins, contexts and server mode work."

const skillsIntro = "See [Coding agents](/cli/mcp) for what the skill and the MCP servers give an agent."

const mcpIntro = "See [Coding agents](/cli/mcp) to configure an MCP client."

func (a *App) groupPage(ctx context.Context, group referenceGroup) (string, error) {
	help, err := a.helpText(ctx, []string{group.name})
	if err != nil {
		return "", err
	}
	commands := helpEntries(help, "Available Commands:")
	if len(commands) == 0 {
		return "", fmt.Errorf("group %s has no commands", group.name)
	}
	var b strings.Builder
	writeFrontmatter(&b, "kapten "+group.name, sentence(group.description)+".", group.name)
	fmt.Fprintf(&b, "Every command takes the [global flags](%s#global-flags).\n", referenceRoute)
	for _, command := range commands {
		help, err := a.helpText(ctx, []string{group.name, command.name})
		if err != nil {
			return "", err
		}
		writeResourceCommand(&b, "kapten "+group.name+" "+command.name, parseResourceHelp(help))
	}
	return b.String(), nil
}

// resourceHelp is a generated command's help page, split into its sections.
type resourceHelp struct {
	description string
	arguments   [][2]string
	body        string
	usage       string
	examples    []string
	options     [][2]string
}

var sectionHeader = regexp.MustCompile(`^(Arguments|Argument Schema|Option Schema|Input Example|Request Schema \([^)]*\)|Response [^ ]+ \([^)]*\)|Usage|Examples|Flags|Aliases):$`)

func parseResourceHelp(help string) resourceHelp {
	sections := map[string][]string{}
	var description []string
	current := ""
	for _, line := range strings.Split(help, "\n") {
		if m := sectionHeader.FindStringSubmatch(line); m != nil {
			current = m[1]
			if strings.HasPrefix(current, "Request Schema") {
				current = "Request Schema"
			}
			continue
		}
		if current == "" {
			description = append(description, line)
			continue
		}
		sections[current] = append(sections[current], line)
	}
	h := resourceHelp{description: strings.TrimSpace(strings.Join(description, "\n"))}
	for _, line := range sections["Arguments"] {
		if fields := strings.Fields(line); len(fields) > 0 {
			h.arguments = append(h.arguments, [2]string{fields[0], strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))})
		}
	}
	h.body = fencedBlock(sections["Request Schema"])
	for _, line := range sections["Usage"] {
		if strings.TrimSpace(line) != "" {
			h.usage = strings.TrimSpace(line)
			break
		}
	}
	for _, line := range sections["Examples"] {
		if strings.TrimSpace(line) != "" {
			h.examples = append(h.examples, strings.TrimSpace(line))
		}
	}
	inOptions := false
	for _, line := range sections["Flags"] {
		switch strings.TrimSpace(line) {
		case "Options":
			inOptions = true
		case "General Options":
			inOptions = false
		default:
			if flag, ok := parseFlagLine(line); ok && inOptions {
				h.options = append(h.options, flag)
			}
		}
	}
	return h
}

// fencedBlock returns the content of the first ``` block in lines.
func fencedBlock(lines []string) string {
	var out []string
	in := false
	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if in {
				break
			}
			in = true
			continue
		}
		if in {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

var flagLine = regexp.MustCompile(`^\s+(-[A-Za-z], )?(--[A-Za-z0-9-]+)( [A-Za-z0-9.]+)?\s{2,}(.+)$`)

func parseFlagLine(line string) ([2]string, bool) {
	m := flagLine.FindStringSubmatch(line)
	if m == nil {
		return [2]string{}, false
	}
	name := m[2] + m[3]
	if m[1] != "" {
		name = strings.TrimSuffix(m[1], ", ") + ", " + name
	}
	return [2]string{name, strings.TrimSpace(m[4])}, true
}

func writeResourceCommand(b *strings.Builder, name string, h resourceHelp) {
	fmt.Fprintf(b, "\n## %s\n\n", name)
	if h.description != "" {
		fmt.Fprintf(b, "%s\n\n", prose(h.description))
	}
	fmt.Fprintf(b, "```bash\n%s\n```\n", h.usage)
	arguments := h.arguments
	if h.body != "" {
		arguments = append(arguments, [2]string{"body...", "The request body, as shorthand or JSON. See [Request bodies](/cli/request-bodies)."})
	}
	if len(arguments) > 0 {
		b.WriteString("\n| Argument | Description |\n| --- | --- |\n")
		for _, argument := range arguments {
			fmt.Fprintf(b, "| `%s` | %s |\n", argument[0], cell(argument[1]))
		}
	}
	if len(h.options) > 0 {
		b.WriteString("\n| Flag | Description |\n| --- | --- |\n")
		for _, option := range h.options {
			fmt.Fprintf(b, "| `%s` | %s |\n", option[0], cell(option[1]))
		}
	}
	if h.body != "" {
		fmt.Fprintf(b, "\nRequest body fields, `*` for required:\n\n```text\n%s\n```\n", h.body)
	}
	if len(h.examples) > 0 {
		fmt.Fprintf(b, "\nExample:\n\n```bash\n%s\n```\n", strings.Join(h.examples, "\n"))
	}
}

// cobraPage documents a tree of Kapten's own commands.
func cobraPage(group, description, intro string, root *cobra.Command) string {
	var b strings.Builder
	writeFrontmatter(&b, "kapten "+group, description, group)
	fmt.Fprintf(&b, "%s Every command takes the [global flags](%s#global-flags).\n", intro, referenceRoute)
	for _, cmd := range root.Commands() {
		if cmd.Hidden || cmd.Name() == "help" || cmd.Name() == "completion" {
			continue
		}
		name := "kapten " + group + " " + cmd.Name()
		fmt.Fprintf(&b, "\n## %s\n\n", name)
		text := cmd.Long
		if text == "" {
			text = cmd.Short + "."
		}
		paragraphs := strings.Split(text, "\n\n")
		for i, paragraph := range paragraphs {
			paragraphs[i] = prose(strings.ReplaceAll(paragraph, "\n", " "))
		}
		fmt.Fprintf(&b, "%s\n\n", strings.Join(paragraphs, "\n\n"))
		usage := "kapten " + group + " " + cmd.Use
		if cmd.HasAvailableFlags() {
			usage += " [flags]"
		}
		fmt.Fprintf(&b, "```bash\n%s\n```\n", usage)
		var flags [][2]string
		cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
			if flag.Hidden || flag.Name == "help" {
				return
			}
			name := "--" + flag.Name
			if flag.Shorthand != "" {
				name = "-" + flag.Shorthand + ", " + name
			}
			kind, usage := pflag.UnquoteUsage(flag)
			if kind != "" {
				name += " " + kind
			}
			if flag.DefValue != "" && flag.DefValue != "false" && flag.DefValue != "0" && flag.DefValue != "[]" {
				usage += fmt.Sprintf(" (default: %s)", flag.DefValue)
			}
			flags = append(flags, [2]string{name, usage})
		})
		if len(flags) > 0 {
			b.WriteString("\n| Flag | Description |\n| --- | --- |\n")
			for _, flag := range flags {
				fmt.Fprintf(&b, "| `%s` | %s |\n", flag[0], cell(flag[1]))
			}
		}
		if cmd.Example != "" {
			lines := strings.Split(cmd.Example, "\n")
			for i := range lines {
				lines[i] = strings.TrimSpace(lines[i])
			}
			fmt.Fprintf(&b, "\nExamples:\n\n```bash\n%s\n```\n", strings.Join(lines, "\n"))
		}
	}
	return b.String()
}

func indexPage(root string, groups []referenceGroup) string {
	var b strings.Builder
	writeFrontmatter(&b, "Command reference", "Every kapten command, its arguments and its flags.", "Overview")
	b.WriteString("Every command the CLI has, generated from the CLI itself. `kapten <command> --help` prints the same information in a terminal.\n\n")
	b.WriteString("| Command | What it does |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| [`kapten auth`](%s/auth) | Log in, check who commands run as, and switch between saved logins |\n", referenceRoute)
	for _, group := range groups {
		fmt.Fprintf(&b, "| [`kapten %s`](%s/%s) | %s |\n", group.name, referenceRoute, group.name, cell(group.description))
	}
	fmt.Fprintf(&b, "| [`kapten mcp`](%s/mcp) | Serve Kapten operations as MCP tools to a coding agent |\n", referenceRoute)
	b.WriteString("\n## Global flags\n\nThese flags work with every command.\n\n| Flag | Description |\n| --- | --- |\n")
	for _, line := range sectionLines(root, "Flags:") {
		if flag, ok := parseFlagLine(line); ok && !strings.Contains(flag[0], "--help-all") {
			fmt.Fprintf(&b, "| `%s` | %s |\n", flag[0], cell(flag[1]))
		}
	}
	return b.String()
}

func metaFile(order []string) string {
	pages, _ := json.Marshal(order)
	return fmt.Sprintf(`// Generated by kapten-docs from the CLI's command tree. DO NOT EDIT.
import { defineMeta } from "blume";

export default defineMeta({
  title: "Command reference",
  order: 10,
  pages: %s,
});
`, strings.ReplaceAll(string(pages), ",", ", "))
}

func writeFrontmatter(b *strings.Builder, title, description, label string) {
	q := func(s string) string { out, _ := json.Marshal(s); return string(out) }
	fmt.Fprintf(b, "---\n# Generated by kapten-docs from the CLI's command tree. DO NOT EDIT.\ntitle: %s\ndescription: %s\nsidebar:\n  label: %s\n---\n\n", q(title), q(description), q(label))
}

// helpEntries lists the "name  description" lines of a help section.
func helpEntries(help, header string) []referenceGroup {
	var entries []referenceGroup
	for _, line := range sectionLines(help, header) {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == "help" || fields[0] == "completion" {
			continue
		}
		entries = append(entries, referenceGroup{fields[0], strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))})
	}
	return entries
}

// sectionLines returns the lines under header, up to the next blank line.
func sectionLines(help, header string) []string {
	_, rest, ok := strings.Cut(help, "\n"+header+"\n")
	if !ok {
		return nil
	}
	rest, _, _ = strings.Cut(rest, "\n\n")
	return strings.Split(rest, "\n")
}

func sentence(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// prose escapes text for MDX outside code spans, where < and { start JSX.
func prose(s string) string {
	parts := strings.Split(s, "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = strings.NewReplacer("<", "&lt;", ">", "&gt;", "{", "&#123;", "}", "&#125;").Replace(parts[i])
	}
	return strings.Join(parts, "`")
}

// cell is prose that fits in one table cell.
func cell(s string) string {
	return strings.ReplaceAll(prose(strings.Join(strings.Fields(s), " ")), "|", "\\|")
}

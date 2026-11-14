package e2e

import (
	"bufio"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The agent skill in ../skills shows kapten commands and flags. Each one must
// exist in the built binary, so a renamed command or flag fails the change
// that renames it.
func TestSkillCommandsExist(t *testing.T) {
	commands := skillCommands(t)
	if len(commands) < 20 {
		t.Fatalf("found only %d kapten commands in the skill", len(commands))
	}
	k := newKapten(t)
	// --help names the subcommands; --help-all also shows the flags the
	// embedded runtime adds, where a group or operation has them.
	helps := map[string]string{}
	help := func(path []string, all bool) string {
		flag := "--help"
		if all {
			flag = "--help-all"
		}
		key := strings.Join(append(append([]string{}, path...), flag), " ")
		if text, ok := helps[key]; ok {
			return text
		}
		r := k.run(append(append([]string{}, path...), flag)...)
		text := ""
		if r.code == 0 {
			text = r.stdout + r.stderr
		}
		helps[key] = text
		return text
	}
	for _, command := range commands {
		path, flags := []string{}, []string{}
		positional := false
		for i := 0; i < len(command.args); i++ {
			arg := command.args[i]
			switch {
			case arg == "--context":
				i++ // a global flag and its value, valid anywhere
			case strings.HasPrefix(arg, "-"):
				flags = append(flags, strings.SplitN(arg, "=", 2)[0])
			case !positional && isSubcommand(help(path, false), arg):
				path = append(path, arg)
			case !positional && isGroup(help(path, false)) && !strings.HasPrefix(arg, "<"):
				t.Errorf("%s: kapten %s has no command %s", command.where, strings.Join(path, " "), arg)
				positional = true
			default:
				positional = true
			}
		}
		text := help(path, false) + help(path, true)
		if text == "" {
			t.Errorf("%s: kapten %s has no help", command.where, strings.Join(path, " "))
			continue
		}
		for _, flag := range flags {
			if flag == "--help" || flag == "-h" {
				continue
			}
			if !regexp.MustCompile(`(^|[\s,])` + regexp.QuoteMeta(flag) + `([\s,=]|$)`).MatchString(text) {
				t.Errorf("%s: kapten %s has no flag %s", command.where, strings.Join(path, " "), flag)
			}
		}
	}
}

// isGroup reports whether help is a command group's, whose arguments are
// subcommands.
func isGroup(help string) bool {
	return strings.Contains(help, " [command]") || strings.Contains(help, " <command>")
}

// isSubcommand reports whether help lists name as a command: an indented line
// that starts with it.
func isSubcommand(help, name string) bool {
	if strings.HasPrefix(name, "<") || strings.HasPrefix(name, "[") {
		return false
	}
	pattern := regexp.MustCompile(`(?m)^ {2,4}` + regexp.QuoteMeta(name) + `(\s|$)`)
	return pattern.MatchString(help)
}

type skillCommand struct {
	where string
	args  []string
}

var inlineCode = regexp.MustCompile("`([^`]+)`")

// skillCommands extracts the kapten invocations of the skill: lines of code
// blocks and inline code that start with kapten, or pipe into it.
func skillCommands(t *testing.T) []skillCommand {
	t.Helper()
	var commands []skillCommand
	add := func(where, line string) {
		line = strings.TrimSpace(strings.SplitN(line, " #", 2)[0])
		if i := strings.LastIndex(line, "| kapten "); i >= 0 {
			line = line[i+2:]
		}
		if !strings.HasPrefix(line, "kapten ") {
			return
		}
		args, err := splitShell(strings.TrimPrefix(line, "kapten "))
		if err != nil {
			t.Fatalf("%s: %v", where, err)
		}
		commands = append(commands, skillCommand{where, args})
	}
	err := filepath.WalkDir("../skills", func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(file) != ".md" {
			return err
		}
		if filepath.Dir(file) == "../skills" {
			return nil // the directory's own README is not part of a skill
		}
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		inBlock := false
		for n := 1; scanner.Scan(); n++ {
			line := scanner.Text()
			where := file + ":" + strconv.Itoa(n)
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				add(where, line)
				continue
			}
			for _, match := range inlineCode.FindAllStringSubmatch(line, -1) {
				add(where, match[1])
			}
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return commands
}

// splitShell splits a command line into words, honoring single and double
// quotes, as a POSIX shell would for these simple commands.
func splitShell(line string) ([]string, error) {
	var words []string
	var word strings.Builder
	inWord := false
	var quote rune
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			word.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, &unclosedQuote{line}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words, nil
}

type unclosedQuote struct{ line string }

func (e *unclosedQuote) Error() string { return "unclosed quote in " + e.line }

// The skill's main workflow, as an agent following SKILL.md and
// references/cli.md runs it: check the identity, discover the operation from
// help, create a resource with a shorthand body and with JSON on stdin, then
// read it back by ID.
func TestSkillWorkflow(t *testing.T) {
	k := newKapten(t)
	if r := k.login(); r.code != 0 {
		t.Fatalf("login:\n%s", r)
	}
	if r := k.ok("whoami"); !strings.Contains(r.stdout, "Organization") && !strings.Contains(r.stdout, "organization") {
		t.Fatalf("whoami names no organization:\n%s", r)
	}
	if r := k.ok("--help"); !strings.Contains(r.stdout, "projects") {
		t.Fatalf("--help lists no projects group:\n%s", r)
	}
	if r := k.ok("projects", "--help"); !strings.Contains(r.stdout, "create") {
		t.Fatalf("projects --help lists no create:\n%s", r)
	}
	// The request schema, required fields marked, is in the operation's help
	if r := k.ok("projects", "create", "--help"); !strings.Contains(r.stdout, "name") {
		t.Fatalf("projects create --help shows no request schema:\n%s", r)
	}

	from := len(k.requests(0))
	created := decode[map[string]any](t, k.ok("projects", "create", `name: storefront, description: "Web store"`, "-o", "json").stdout)
	got := k.requests(from)
	if len(got) != 1 || got[0].OperationID != "createProject" || got[0].Rejected {
		t.Fatalf("shorthand create sent %+v", got)
	}
	body := decode[map[string]any](t, string(got[0].Body))
	if body["name"] != "storefront" || body["description"] != "Web store" {
		t.Fatalf("shorthand body was %s", got[0].Body)
	}

	from = len(k.requests(0))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := k.command(ctx, "projects", "create", "-o", "json")
	cmd.Stdin = strings.NewReader(`{"name": "storefront", "description": "Web store"}`)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create from stdin: %v\n%s", err, output)
	}
	got = k.requests(from)
	if len(got) != 1 || got[0].OperationID != "createProject" || string(got[0].Body) == "" {
		t.Fatalf("stdin create sent %+v", got)
	}

	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create printed no id: %v", created)
	}
	from = len(k.requests(0))
	k.ok("projects", "get", id, "-o", "json")
	got = k.requests(from)
	if len(got) != 1 || got[0].OperationID != "getProject" || !strings.HasSuffix(got[0].Path, "/"+id) {
		t.Fatalf("get sent %+v", got)
	}
	if errs := k.mock.Engine.Errors(); len(errs) != 0 {
		t.Errorf("requests outside the contract: %v", errs)
	}
}

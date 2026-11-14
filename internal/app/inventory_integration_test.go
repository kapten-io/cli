package app

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kapten-io/cli/internal/spec"
	"github.com/kapten-io/cli/internal/mockengine"
)

// The actual help surface is compared to the inventory, so a new owned/support
// command cannot quietly appear without an integration scenario being added.
func TestPublicCommandInventoryIntegration(t *testing.T) {
	isolateCommandFilesystem(t)
	server := mockengine.New(t, spec.OpenAPI)
	// Kapten's own commands are listed on the root page with the groups.
	expected := map[string]bool{"help": true, "completion": true, "login": true, "whoami": true, "logout": true, "auth": true, "mcp": true, "skills": true}
	for _, operation := range server.Operations {
		if !operation.Ignored {
			expected[operation.Groups[0]] = true
		}
	}
	root := []string{}
	for name := range expected {
		root = append(root, name)
	}
	sort.Strings(root)
	scenarios := []struct {
		args     []string
		expected []string
	}{
		{[]string{"--help"}, root},
		{[]string{"auth", "--help"}, []string{"completion", "help", "list", "login", "logout", "use", "whoami"}},
		{[]string{"completion", "--help"}, []string{"bash", "fish", "powershell", "zsh"}},
		{[]string{"skills", "--help"}, []string{"completion", "help", "install"}},
		{[]string{"auth", "completion", "--help"}, []string{"bash", "fish", "powershell", "zsh"}},
	}
	for _, scenario := range scenarios {
		t.Run(strings.Join(scenario.args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			if err := New(&out, &diagnostics).Run(append([]string{"kapten"}, scenario.args...)); err != nil {
				t.Fatalf("inventory help: %v; %s", err, &diagnostics)
			}
			got := listedHelpCommands(out.String())
			if !reflect.DeepEqual(got, scenario.expected) {
				t.Fatalf("public command inventory changed: got %v want %v; add integration coverage for new commands\n%s", got, scenario.expected, &out)
			}
		})
	}
	if len(server.Requests()) != 0 {
		t.Fatal("inventory help made network requests")
	}
}

// Every resource group gets a real description, not the generic one.
func TestEveryResourceGroupHasADescription(t *testing.T) {
	isolateCommandFilesystem(t)
	server := mockengine.New(t, spec.OpenAPI)
	for _, operation := range server.Operations {
		if operation.Ignored {
			continue
		}
		if _, ok := groupDescriptions[operation.Groups[0]]; !ok {
			t.Errorf("resource group %q has no entry in groupDescriptions", operation.Groups[0])
		}
	}
	var out, diagnostics bytes.Buffer
	if err := New(&out, &diagnostics).Run([]string{"kapten", "--help"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Operations tagged") || !strings.Contains(out.String(), "Log in with a code approved in a browser") {
		t.Fatalf("root help:\n%s", &out)
	}
}

func listedHelpCommands(text string) []string {
	commands := []string{}
	section := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "Help", "Available Commands:", "Additional Commands:", "Get started:", "Resources:", "Coding agents:", "Other:":
			section = true
			continue
		case "":
			section = false
			continue
		}
		if !section {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.HasPrefix(line, "  ") {
			commands = append(commands, fields[0])
		}
	}
	sort.Strings(commands)
	return commands
}

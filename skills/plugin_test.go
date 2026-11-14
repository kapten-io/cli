package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The repository root is a plugin: Agent Plugins 1.0 (plugin.json, mcp.json,
// skills/) for Codex, Cursor and other clients of the standard, and
// .claude-plugin/ for Claude Code. These tests keep the manifests valid and
// in step with each other.

func readJSON(t *testing.T, path string) any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	v, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func TestAgentPluginManifestsMatchTheirSchemas(t *testing.T) {
	// The Agent Plugins 1.0.0 schemas, copied into testdata: clients must not
	// fetch them, and neither does this test.
	for manifest, schema := range map[string]string{
		"../plugin.json": "testdata/plugin.schema.json",
		"../mcp.json":    "testdata/mcp.schema.json",
	} {
		compiler := jsonschema.NewCompiler()
		doc := readJSON(t, schema)
		id := doc.(map[string]any)["$id"].(string)
		// Go's regexp has no lookahead: the name pattern's (?!.*(?:--|\.\.))
		// is checked below instead.
		if name, ok := doc.(map[string]any)["properties"].(map[string]any)["name"].(map[string]any); ok {
			name["pattern"] = strings.Replace(name["pattern"].(string), `(?!.*(?:--|\.\.))`, "", 1)
		}
		if err := compiler.AddResource(id, doc); err != nil {
			t.Fatal(err)
		}
		compiled, err := compiler.Compile(id)
		if err != nil {
			t.Fatal(err)
		}
		instance := readJSON(t, manifest)
		if err := compiled.Validate(instance); err != nil {
			t.Errorf("%s: %v", manifest, err)
		}
		if name, ok := instance.(map[string]any)["name"].(string); ok && (strings.Contains(name, "--") || strings.Contains(name, "..")) {
			t.Errorf("%s: name %q holds -- or ..", manifest, name)
		}
	}
}

type claudePlugin struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	MCPServers  map[string]map[string]any `json:"mcpServers"`
}

func decodeFile[T any](t *testing.T, path string) T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func TestEveryClientGetsTheSamePlugin(t *testing.T) {
	type portable struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	type mcp struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	agentPlugin := decodeFile[portable](t, "../plugin.json")
	agentMCP := decodeFile[mcp](t, "../mcp.json")
	claude := decodeFile[claudePlugin](t, "../.claude-plugin/plugin.json")

	if claude.Name != agentPlugin.Name || claude.Description != agentPlugin.Description {
		t.Errorf("Claude Code's manifest names %q (%q), plugin.json %q (%q)", claude.Name, claude.Description, agentPlugin.Name, agentPlugin.Description)
	}
	if len(agentMCP.MCPServers) != 2 || len(claude.MCPServers) != 2 {
		t.Fatalf("want the kapten and kapten-docs servers, got %v and %v", agentMCP.MCPServers, claude.MCPServers)
	}
	// Same command for the stdio server; same URL for the remote one, whose
	// type each format spells its own way.
	for name, server := range agentMCP.MCPServers {
		other, ok := claude.MCPServers[name]
		if !ok {
			t.Errorf("Claude Code's manifest has no %s server", name)
			continue
		}
		switch server["type"] {
		case "stdio":
			if other["command"] != server["command"] || !reflect.DeepEqual(other["args"], server["args"]) {
				t.Errorf("%s: %v in mcp.json, %v for Claude Code", name, server, other)
			}
		case "streamable-http":
			if other["type"] != "http" || other["url"] != server["url"] {
				t.Errorf("%s: %v in mcp.json, %v for Claude Code", name, server, other)
			}
		}
	}
	if docs := agentMCP.MCPServers["kapten-docs"]; docs["url"] != "https://docs.kapten.io/mcp" {
		t.Errorf("kapten-docs server: %v", docs)
	}

	// Each skill in skills/ is one of the plugin's skills.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var skillDirs []string
	for _, entry := range entries {
		if _, err := os.Stat(filepath.Join(entry.Name(), "SKILL.md")); entry.IsDir() && err == nil {
			skillDirs = append(skillDirs, entry.Name())
		}
	}
	if !reflect.DeepEqual(skillDirs, []string{"kapten", "kapten-docs"}) {
		t.Errorf("skills: %v", skillDirs)
	}
}

func TestMarketplacesListThePlugin(t *testing.T) {
	type claudeMarketplace struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	claude := decodeFile[claudeMarketplace](t, "../.claude-plugin/marketplace.json")
	if claude.Name != "kapten" || len(claude.Plugins) != 1 || claude.Plugins[0].Name != "kapten" || claude.Plugins[0].Source != "./" {
		t.Errorf("Claude Code marketplace: %+v", claude)
	}
	type codexMarketplace struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			} `json:"source"`
			Policy struct {
				Installation string `json:"installation"`
			} `json:"policy"`
		} `json:"plugins"`
	}
	codex := decodeFile[codexMarketplace](t, "../.agents/plugins/marketplace.json")
	if codex.Name != "kapten" || len(codex.Plugins) != 1 {
		t.Fatalf("Codex marketplace: %+v", codex)
	}
	if p := codex.Plugins[0]; p.Name != "kapten" || p.Source.Source != "local" || p.Source.Path != "./" || p.Policy.Installation != "AVAILABLE" {
		t.Errorf("Codex marketplace entry: %+v", p)
	}
}

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsInstallForDetectedAgents(t *testing.T) {
	k := newKapten(t)
	if r := k.run("skills", "install"); r.code != 1 || !strings.Contains(r.stderr, "found no coding agent") {
		t.Fatalf("with no agent on the machine:\n%s", r)
	}
	for _, dir := range []string{".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(k.home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The project already configures another MCP server.
	writeFile(t, filepath.Join(k.home, ".mcp.json"), `{"mcpServers": {"other": {"command": "other"}}, "keep": true}`)
	writeFile(t, filepath.Join(k.home, ".codex/config.toml"), "model = \"x\"\n\n[mcp_servers.other]\ncommand = \"other\"\n\n[mcp_servers.kapten]\ncommand = \"stale\"\nargs = []\n\n[profiles.p]\nmodel = \"y\"\n")

	dry := k.ok("skills", "install", "--mcp", "--dry-run")
	if !strings.Contains(dry.stdout, "Would write the kapten skill to .claude/skills/kapten") {
		t.Fatalf("dry run:\n%s", dry)
	}
	if _, err := os.Stat(filepath.Join(k.home, ".claude/skills")); !os.IsNotExist(err) {
		t.Fatalf("a dry run wrote the skill: %v", err)
	}

	r := k.ok("--context", "work", "skills", "install", "--mcp")
	for _, want := range []string{".claude/skills/kapten (Claude Code)", ".agents/skills/kapten (Codex)", ".mcp.json (Claude Code)", ".codex/config.toml (Codex)"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("output does not mention %s:\n%s", want, r)
		}
	}
	for _, dir := range []string{".claude/skills/kapten", ".agents/skills/kapten"} {
		skill, err := os.ReadFile(filepath.Join(k.home, dir, "SKILL.md"))
		if err != nil || !strings.Contains(string(skill), "name: kapten") {
			t.Fatalf("%s/SKILL.md: %v", dir, err)
		}
		if _, err := os.Stat(filepath.Join(k.home, dir, "references", "cli.md")); err != nil {
			t.Fatal(err)
		}
	}

	config := readJSON(t, filepath.Join(k.home, ".mcp.json"))
	servers := config["mcpServers"].(map[string]any)
	if config["keep"] != true || servers["other"] == nil {
		t.Fatalf("other entries were not kept: %v", config)
	}
	kapten := servers["kapten"].(map[string]any)
	if kapten["command"] != binary || strings.Join(anyStrings(kapten["args"]), " ") != "--context work mcp serve" {
		t.Fatalf("kapten server: %v", kapten)
	}
	if docs := servers["kapten-docs"].(map[string]any); docs["url"] != "https://docs.kapten.io/mcp" {
		t.Fatalf("kapten-docs server: %v", docs)
	}

	toml, err := os.ReadFile(filepath.Join(k.home, ".codex/config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(toml)
	for _, want := range []string{"model = \"x\"", "[mcp_servers.other]", "[profiles.p]\nmodel = \"y\"", "[mcp_servers.kapten-docs]\nurl = \"https://docs.kapten.io/mcp\""} {
		if !strings.Contains(text, want) {
			t.Errorf("config.toml lost %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "stale") || strings.Count(text, "[mcp_servers.kapten]") != 1 {
		t.Errorf("config.toml kept the old kapten server:\n%s", text)
	}

	// Running it again, from another CLI version, says it replaces that copy.
	writeFile(t, filepath.Join(k.home, ".claude/skills/kapten/.kapten-cli-version"), "0.0.1\n")
	writeFile(t, filepath.Join(k.home, ".claude/skills/kapten/stale.md"), "")
	again := k.ok("skills", "install", "--agent", "claude-code")
	if !strings.Contains(again.stdout, "replacing the copy from CLI 0.0.1") {
		t.Fatalf("reinstall:\n%s", again)
	}
	if _, err := os.Stat(filepath.Join(k.home, ".claude/skills/kapten/stale.md")); !os.IsNotExist(err) {
		t.Fatalf("a file of the old copy was kept: %v", err)
	}
	if version, _ := os.ReadFile(filepath.Join(k.home, ".claude/skills/kapten/.kapten-cli-version")); strings.TrimSpace(string(version)) != "1.2.3-e2e" {
		t.Fatalf("version file: %q", version)
	}
}

func TestSkillsInstallGlobalForEachAgent(t *testing.T) {
	k := newKapten(t)
	k.ok("skills", "install", "--global", "--mcp", "--agent", "claude-code,codex,cursor,opencode,copilot")
	for _, dir := range []string{".claude/skills", ".codex/skills", ".cursor/skills", ".config/opencode/skills", ".copilot/skills"} {
		if _, err := os.Stat(filepath.Join(k.home, dir, "kapten", "SKILL.md")); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
	checks := map[string][2]string{
		".claude.json":                   {"mcpServers", "command"},
		".cursor/mcp.json":               {"mcpServers", "command"},
		".copilot/mcp-config.json":       {"mcpServers", "command"},
		".config/opencode/opencode.json": {"mcp", "command"},
	}
	for file, keys := range checks {
		config := readJSON(t, filepath.Join(k.home, file))
		servers, ok := config[keys[0]].(map[string]any)
		if !ok || servers["kapten"] == nil || servers["kapten-docs"] == nil {
			t.Errorf("%s: %v", file, config)
			continue
		}
		if servers["kapten"].(map[string]any)[keys[1]] == nil {
			t.Errorf("%s: kapten server has no %s", file, keys[1])
		}
	}
	if _, err := os.Stat(filepath.Join(k.home, ".codex/config.toml")); err != nil {
		t.Error(err)
	}
	if r := k.run("skills", "install", "--agent", "vim"); r.code != 1 || !strings.Contains(r.stderr, `unknown agent "vim"`) {
		t.Fatalf("unknown agent:\n%s", r)
	}
	// A configuration that is not JSON is left as it was.
	writeFile(t, filepath.Join(k.home, ".cursor/mcp.json"), "{not json")
	if r := k.run("skills", "install", "--global", "--mcp", "--agent", "cursor"); r.code != 1 || !strings.Contains(r.stderr, "not valid JSON") {
		t.Fatalf("invalid JSON:\n%s", r)
	}
	if data, _ := os.ReadFile(filepath.Join(k.home, ".cursor/mcp.json")); string(data) != "{not json" {
		t.Fatalf("invalid JSON was changed: %q", data)
	}
}

func anyStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, len(items))
	for i, item := range items {
		out[i], _ = item.(string)
	}
	return out
}

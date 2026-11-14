package skills

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The Agent Skills specification's frontmatter rules
// (https://agentskills.io/specification).
var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

type frontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  string            `yaml:"allowed-tools"`
}

func TestFrontmatterFollowsTheSpecification(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("no skills embedded")
	}
	for _, name := range names {
		data, err := FS.ReadFile(name + "/SKILL.md")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		parts := strings.SplitN(string(data), "---\n", 3)
		if len(parts) != 3 || parts[0] != "" {
			t.Fatalf("%s/SKILL.md does not start with YAML frontmatter", name)
		}
		var meta frontmatter
		decoder := yaml.NewDecoder(strings.NewReader(parts[1]))
		decoder.KnownFields(true)
		if err := decoder.Decode(&meta); err != nil {
			t.Fatalf("%s/SKILL.md frontmatter: %v", name, err)
		}
		if meta.Name != name || len(name) > 64 || !skillName.MatchString(name) {
			t.Errorf("%s: name %q must equal the directory, in lowercase words joined by hyphens, at most 64 characters", name, meta.Name)
		}
		if n := len([]rune(meta.Description)); n == 0 || n > 1024 {
			t.Errorf("%s: description has %d characters, want 1 to 1024", name, n)
		}
		if n := len([]rune(meta.Compatibility)); n > 500 {
			t.Errorf("%s: compatibility has %d characters, want at most 500", name, n)
		}
		if lines := strings.Count(parts[2], "\n"); lines > 500 {
			t.Errorf("%s: SKILL.md body has %d lines; move details to references/", name, lines)
		}
	}
}

var markdownLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

func TestRelativeLinksResolve(t *testing.T) {
	err := fs.WalkDir(FS, ".", func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || path.Ext(file) != ".md" {
			return err
		}
		data, err := FS.ReadFile(file)
		if err != nil {
			return err
		}
		for _, match := range markdownLink.FindAllStringSubmatch(string(data), -1) {
			target := strings.SplitN(match[1], "#", 2)[0]
			if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			resolved := path.Join(path.Dir(file), target)
			if _, err := fs.Stat(FS, resolved); err != nil {
				t.Errorf("%s links %s, which does not exist", file, match[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Every file of the directory is embedded: a file added to a skill and not
// to the embed pattern would ship in the repository but not in the binary.
func TestEveryFileIsEmbedded(t *testing.T) {
	for _, name := range Names() {
		err := filepath.WalkDir(name, func(file string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			if _, err := fs.Stat(FS, filepath.ToSlash(file)); err != nil {
				t.Errorf("%s is not embedded", file)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriteCopiesTheSkill(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "kapten", "stale.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write("kapten", dir); err != nil {
		t.Fatal(err)
	}
	want, _ := FS.ReadFile("kapten/references/cli.md")
	got, err := os.ReadFile(filepath.Join(dir, "kapten", "references", "cli.md"))
	if err != nil || string(got) != string(want) {
		t.Fatalf("references/cli.md not copied: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a file the skill does not hold was kept: %v", err)
	}
}

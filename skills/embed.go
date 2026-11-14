// Package skills holds the Agent Skills (https://agentskills.io) that ship
// with the CLI: each directory holds a SKILL.md and the files it links.
package skills

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// FS holds every skill, one directory each.
//
//go:embed kapten
var FS embed.FS

// Names lists the skills.
func Names() []string {
	entries, _ := FS.ReadDir(".")
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

// Write copies the skill name into dir/name, replacing what is there.
func Write(name, dir string) error {
	target := filepath.Join(dir, name)
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return fs.WalkDir(FS, name, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out := filepath.Join(dir, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := FS.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
}

// Command kapten-docs writes the CLI reference of the docs site
// (docs/product/content/cli/reference) from the CLI's own command tree, or,
// with -skills, the agent skills the CLI ships, for the docs site to publish.
// It is a development tool, not part of a release.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/kapten-io/cli/internal/app"
	"github.com/kapten-io/cli/skills"
)

// writeSkills writes each skill into dir/<name>, replacing what is there.
func writeSkills(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range skills.Names() {
		if err := skills.Write(name, dir); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	switch {
	case len(os.Args) == 3 && os.Args[1] == "-skills":
		if err := writeSkills(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "kapten-docs:", err)
			os.Exit(1)
		}
		return
	case len(os.Args) != 2:
		fmt.Fprintln(os.Stderr, "usage: kapten-docs <directory>\n       kapten-docs -skills <directory>")
		os.Exit(2)
	}
	// The help pages need no login, but the embedded runtime writes its
	// config: keep it away from the developer's own.
	dir, err := os.MkdirTemp("", "kapten-docs")
	if err == nil {
		defer os.RemoveAll(dir)
		err = os.Setenv("KAPTEN_CONFIG_DIR", dir)
	}
	if err == nil {
		err = app.New(os.Stdout, os.Stderr).WriteReference(context.Background(), os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kapten-docs:", err)
		os.Exit(1)
	}
}

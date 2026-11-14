// Command kapten-docs writes the CLI reference of the docs site
// (docs/product/content/cli/reference) from the CLI's own command tree. It
// is a development tool, not part of a release.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/kapten-io/cli/internal/app"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: kapten-docs <directory>")
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

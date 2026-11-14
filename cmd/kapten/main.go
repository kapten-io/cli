package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kapten-io/cli/internal/app"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli := app.New(os.Stdout, os.Stderr)
	cli.Version = version
	cli.Commit = commit
	cli.BuildDate = date
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.RunContext(ctx, os.Args); err != nil {
		if !app.IsRenderedAPIError(err) {
			fmt.Fprintf(os.Stderr, "kapten: %v\n", err)
		}
		os.Exit(1)
	}
}

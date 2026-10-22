// Command bl is a terminal backlog for people and coding agents.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/brettinternet/backlog/internal/cli"
)

// Set by builds through -ldflags.
var (
	buildVersion = "dev"
	buildCommit  = "unknown"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cli.Run(ctx, os.Args, cli.BuildInfo{
		Version: buildVersion, Commit: buildCommit,
	}, os.Stdin, os.Stdout, os.Stderr)
}

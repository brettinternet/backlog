// Command bl is a terminal backlog for people and coding agents.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/brettinternet/backlog/internal/cli"
)

// Set by builds through -ldflags.
var (
	buildVersion = "dev"
	buildCommit  = "unknown"
	buildTime    = "unknown"
)

func main() {
	// Leave headroom for SQLite and process overhead within the batch CLI's
	// 400 MiB RSS budget. Interactive startup restores normal Go heap growth
	// to avoid GC thrashing when its live snapshot exceeds this soft target.
	// An explicit Go runtime setting always takes precedence.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(300 << 20)
	}
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cli.Run(ctx, os.Args, cli.BuildInfo{
		Version: buildVersion, Commit: buildCommit, BuildTime: buildTime,
	}, os.Stdin, os.Stdout, os.Stderr)
}

// Command bl is a fast terminal backlog for people and coding agents.
//
// This is a placeholder until BL-001 establishes the CLI; see docs/design.md.
package main

import (
	"fmt"
	"os"
)

// Set by release builds through -ldflags.
var (
	buildVersion = "dev"
	buildCommit  = "unknown"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("bl %s (%s)\n", buildVersion, buildCommit)
		return
	}
	fmt.Fprintln(os.Stderr, "bl: not implemented yet; see docs/design.md")
	os.Exit(2)
}

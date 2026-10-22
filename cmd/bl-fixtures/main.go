// bl-fixtures generates a deterministic native project for local measurements.
package main

import (
	"flag"
	"fmt"
	"github.com/brettinternet/backlog/internal/fixtures"
	"os"
)

func main() {
	count := flag.Int("count", 10000, "Number of items")
	seed := flag.Uint64("seed", 1, "Deterministic seed")
	out := flag.String("out", "", "New output directory (must not exist)")
	flag.Parse()
	if *out == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: bl-fixtures -out NEW_DIRECTORY [-count 10000] [-seed 1]")
		os.Exit(2)
	}
	if err := fixtures.Generate(*out, *count, *seed); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

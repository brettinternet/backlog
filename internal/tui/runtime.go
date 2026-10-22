package tui

import (
	"os"
	"runtime/debug"
)

// ConfigureRuntime bounds interactive heap growth without a fixed target below
// the live backlog. Call before opening the store and defer the returned restore
// function. Explicit standard runtime environment settings always take precedence.
func ConfigureRuntime() func() {
	setMemory := os.Getenv("GOMEMLIMIT") == ""
	setGC := os.Getenv("GOGC") == ""
	var memory int64
	var gc int
	if setMemory {
		memory = debug.SetMemoryLimit(1<<63 - 1)
	}
	if setGC {
		gc = debug.SetGCPercent(20)
	}
	return func() {
		if setMemory {
			debug.SetMemoryLimit(memory)
		}
		if setGC {
			debug.SetGCPercent(gc)
		}
	}
}

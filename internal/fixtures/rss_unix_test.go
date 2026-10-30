//go:build unix

package fixtures_test

import (
	"os"
	"runtime"
	"syscall"
)

// peakRSS reports a finished process's peak resident set in MiB.
func peakRSS(state *os.ProcessState) float64 {
	rss := float64(state.SysUsage().(*syscall.Rusage).Maxrss) / (1024 * 1024)
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	return rss
}

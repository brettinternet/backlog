package fixtures_test

import "os"

// peakRSS is unavailable once a Windows process has exited; report zero.
func peakRSS(*os.ProcessState) float64 { return 0 }

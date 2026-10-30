package native_test

import "golang.org/x/sys/windows"

func cpuSeconds() float64 {
	var creation, exit, kernel, user windows.Filetime
	_ = windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user)
	ticks := func(f windows.Filetime) float64 { return float64(uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime)) }
	return (ticks(kernel) + ticks(user)) / 1e7 // 100 ns units
}

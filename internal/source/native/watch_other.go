//go:build !darwin && !linux && !windows

package native

func openDirectoryWatch(string) (DirectoryWatch, error) { return nil, errPollingOnly }

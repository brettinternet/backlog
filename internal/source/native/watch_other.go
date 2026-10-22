//go:build !darwin && !linux

package native

func openDirectoryWatch(string) (DirectoryWatch, error) { return nil, errPollingOnly }

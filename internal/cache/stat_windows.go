package cache

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileBasicInfo is FILE_BASIC_INFO. ChangeTime is NTFS's ctime: SetFileTime
// cannot restore it, so it detects edits that restore size and mtime.
type fileBasicInfo struct {
	CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
	FileAttributes                                          uint32
	_                                                       uint32
}

// identity reads volume serial, file index, size, last write and change time
// through an attributes-only handle that shares delete, so it never blocks a
// writer's rename. Without follow, reparse points are not followed, as with
// Lstat.
func identity(path string, follow bool) (string, uint32, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", 0, err
	}
	flags := uint32(windows.FILE_FLAG_BACKUP_SEMANTICS)
	if !follow {
		flags |= windows.FILE_FLAG_OPEN_REPARSE_POINT
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return "", 0, &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer windows.CloseHandle(h) //nolint:errcheck
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &info); err != nil {
		return "", 0, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	var basic fileBasicInfo
	if err = windows.GetFileInformationByHandleEx(h, windows.FileBasicInfo, (*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil {
		return "", 0, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	size := uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow)
	index := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	write := int64(info.LastWriteTime.HighDateTime)<<32 | int64(info.LastWriteTime.LowDateTime)
	return fmt.Sprintf("%d:%d:%d:%d:%d", size, write, info.VolumeSerialNumber, index, basic.ChangeTime), info.FileAttributes, nil
}

// FileIdentity detects replacements and edits even when size and mtime are
// restored. Non-regular files are rejected.
func FileIdentity(path string) (string, error) {
	id, attributes, err := identity(path, false)
	if err != nil {
		return "", err
	}
	if attributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		return "", fmt.Errorf("item is not a regular file")
	}
	return id, nil
}

func directoryIdentity(path string) (string, error) {
	id, attributes, err := identity(path, true)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", fmt.Errorf("item path is not a directory")
	}
	return id, nil
}

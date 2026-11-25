package service

import (
	"golang.org/x/sys/windows"
)

func worktreeDirectoryIdentity(path string) (worktreeDirectoryID, error) {
	utf16Path, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return worktreeDirectoryID{}, err
	}
	directory, err := windows.CreateFile(utf16Path, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return worktreeDirectoryID{}, err
	}
	defer func() { _ = windows.CloseHandle(directory) }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(directory, &info); err != nil {
		return worktreeDirectoryID{}, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return worktreeDirectoryID{}, errSelectedWorktreeNotDirectory
	}
	id := worktreeDirectoryID{
		Volume:    uint64(info.VolumeSerialNumber),
		FileIndex: uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
	}
	if info.CreationTime.HighDateTime != 0 || info.CreationTime.LowDateTime != 0 {
		id.CreatedAtUnixNanos = info.CreationTime.Nanoseconds()
	}
	return id, nil
}

package service

import (
	"syscall"
	"time"
)

func worktreeDirectoryIdentity(path string) (worktreeDirectoryID, error) {
	info, err := verifyWorktreeDirectory(path)
	if err != nil {
		return worktreeDirectoryID{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return worktreeDirectoryID{}, nil
	}
	id := worktreeDirectoryID{Volume: uint64(stat.Dev), FileIndex: stat.Ino}
	if stat.Birthtimespec.Sec > 0 || (stat.Birthtimespec.Sec == 0 && stat.Birthtimespec.Nsec > 0) {
		id.CreatedAtUnixNanos = time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec).UnixNano()
	}
	return id, nil
}

package service

import (
	"errors"
	"os"
	"syscall"
	"time"
)

func worktreeDirectoryIdentity(path string) (worktreeDirectoryID, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return worktreeDirectoryID{}, err
	}
	if !info.IsDir() {
		return worktreeDirectoryID{}, errors.New("the selected Git path is no longer a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Birthtimespec.Sec == 0 && stat.Birthtimespec.Nsec == 0 {
		return worktreeDirectoryID{}, nil
	}
	return worktreeDirectoryID{
		Volume: uint64(stat.Dev), FileIndex: stat.Ino,
		CreatedAtUnixNanos: time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec).UnixNano(),
	}, nil
}

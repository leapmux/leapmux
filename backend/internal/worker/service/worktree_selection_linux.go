package service

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

func worktreeDirectoryIdentity(path string) (worktreeDirectoryID, error) {
	const required = unix.STATX_INO | unix.STATX_BTIME
	var stat unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_TYPE|required, &stat)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		return worktreeDirectoryID{}, nil
	}
	if err != nil {
		return worktreeDirectoryID{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return worktreeDirectoryID{}, errors.New("the selected Git path is no longer a directory")
	}
	if stat.Mask&required != required {
		return worktreeDirectoryID{}, nil
	}
	return worktreeDirectoryID{
		Volume: uint64(stat.Dev_major)<<32 | uint64(stat.Dev_minor), FileIndex: stat.Ino,
		CreatedAtUnixNanos: time.Unix(stat.Btime.Sec, int64(stat.Btime.Nsec)).UnixNano(),
	}, nil
}

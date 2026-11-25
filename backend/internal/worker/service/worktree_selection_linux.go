package service

import (
	"errors"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func worktreeDirectoryIdentity(path string) (worktreeDirectoryID, error) {
	var stat unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_TYPE|unix.STATX_INO|unix.STATX_BTIME, &stat)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
		info, lstatErr := verifyWorktreeDirectory(path)
		if lstatErr != nil {
			return worktreeDirectoryID{}, lstatErr
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			return worktreeDirectoryID{Volume: uint64(st.Dev), FileIndex: st.Ino}, nil
		}
		return worktreeDirectoryID{}, nil
	}
	if err != nil {
		return worktreeDirectoryID{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return worktreeDirectoryID{}, errSelectedWorktreeNotDirectory
	}
	if stat.Mask&unix.STATX_INO == 0 {
		return worktreeDirectoryID{}, nil
	}
	id := worktreeDirectoryID{
		Volume:    uint64(stat.Dev_major)<<32 | uint64(stat.Dev_minor),
		FileIndex: stat.Ino,
	}
	if stat.Mask&unix.STATX_BTIME != 0 && (stat.Btime.Sec > 0 || (stat.Btime.Sec == 0 && stat.Btime.Nsec > 0)) {
		id.CreatedAtUnixNanos = time.Unix(stat.Btime.Sec, int64(stat.Btime.Nsec)).UnixNano()
	}
	return id, nil
}

//go:build unix

package useronly

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func verifyUnixOwner(info os.FileInfo, path string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uint32(os.Geteuid()) {
		return fmt.Errorf("%s belongs to another account", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is wider than this account (mode %s)", path, info.Mode().Perm())
	}
	return nil
}

// OpenFile verifies the opened object before reading any private bytes.
func OpenFile(path string) (*os.File, error) {
	return openPrivateFile(path, os.O_RDONLY)
}

// OpenAppend validates the opened file before appending any private bytes.
func OpenAppend(path string) (*os.File, error) {
	return openPrivateFile(path, os.O_WRONLY|os.O_APPEND)
}

func openPrivateFile(path string, flags int) (*os.File, error) {
	file, err := os.OpenFile(path, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := regularFile(file, path)
	if err == nil {
		err = verifyUnixOwner(info, path)
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// VerifyDirectory checks the directory itself rather than following a link.
func VerifyDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a private directory", path)
	}
	return verifyUnixOwner(info, path)
}

// Mkdir creates one private directory. The parent must exist.
func Mkdir(path string) error { return os.Mkdir(path, 0o700) }

// MkdirTemp creates a private staging directory.
func MkdirTemp(parent, pattern string) (string, error) { return os.MkdirTemp(parent, pattern) }

// CreateTemp creates an account-private temporary file.
func CreateTemp(parent, pattern string) (*os.File, error) { return os.CreateTemp(parent, pattern) }

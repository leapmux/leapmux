// Package execfile writes a program file that a process starts later.
package execfile

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// CreateExclusive creates a new program file at path with perm, has fill write
// its content, and closes it. It refuses an existing path (os.ErrExist), and
// removes the created file on any error or panic. Every fork of this process
// waits while the file is open for writing; see write for details and the
// ForkLock constraint on fill.
func CreateExclusive(path string, perm os.FileMode, fill func(io.Writer) error) error {
	return write(path, perm, true, fill)
}

// Overwrite creates or truncates the program file at path, updates its mode to
// perm, has fill write its content, and closes it. An existing file is kept on
// error. Every fork of this process waits while the file is open for writing;
// see write for details and the ForkLock constraint on fill.
func Overwrite(path string, perm os.FileMode, fill func(io.Writer) error) error {
	return write(path, perm, false, fill)
}

// write opens path, runs fill under syscall.ForkLock.RLock, and closes it.
// Because fill runs while syscall.ForkLock is read-locked, fill must not spawn
// subprocesses or block on a goroutine that acquires ForkLock.
//
// A plain write can make the next start of the file fail with ETXTBSY (`text
// file busy`). A fork in another goroutine copies the descriptor that the
// write holds open, and the child keeps that copy until its own exec. Linux
// refuses to run a file that any process holds open for writing, so the start
// fails even after the writer closed its own descriptor. A rename does not
// help, because the copy still refers to the same file.
//
// syscall.forkExec holds the write side of syscall.ForkLock across each fork,
// so the read side that write holds keeps every fork of this process out of
// the time that the descriptor is open, and no child can inherit it.
func write(path string, perm os.FileMode, exclusive bool, fill func(io.Writer) error) (err error) {
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if exclusive {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	removeOnFailure := false
	succeeded := false
	defer func() {
		if removeOnFailure && !succeeded {
			_ = os.Remove(path)
		}
	}()
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	file, err := os.OpenFile(path, flags, perm)
	if err != nil {
		return err
	}
	removeOnFailure = exclusive
	err = func() (writeErr error) {
		defer func() {
			writeErr = errors.Join(writeErr, file.Close())
		}()
		if !exclusive {
			if chmodErr := file.Chmod(perm); chmodErr != nil {
				return chmodErr
			}
		}
		return fill(file)
	}()
	if err != nil {
		return err
	}
	succeeded = true
	return nil
}

// Package execfile writes a program file that a process starts later.
package execfile

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// Write creates the program file path with perm, has fill write its content,
// and closes it. Every fork of this process waits while the file is open for
// writing. When exclusive is true, a path that exists is refused, and a
// failure removes the file that Write created; otherwise an existing file is
// truncated.
//
// A plain write can make the next start of the file fail with ETXTBSY (`text
// file busy`). A fork in another goroutine copies the descriptor that the
// write holds open, and the child keeps that copy until its own exec. Linux
// refuses to run a file that any process holds open for writing, so the start
// fails even after the writer closed its own descriptor. A rename does not
// help, because the copy still refers to the same file.
//
// syscall.forkExec holds the write side of syscall.ForkLock across each fork,
// so the read side that Write holds keeps every fork of this process out of
// the time that the descriptor is open, and no child can inherit it.
func Write(path string, perm os.FileMode, exclusive bool, fill func(io.Writer) error) (err error) {
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if exclusive {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	file, err := os.OpenFile(path, flags, perm)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, file.Close())
		if err != nil && exclusive {
			_ = os.Remove(path)
		}
	}()
	return fill(file)
}

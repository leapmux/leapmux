//go:build !windows

package terminal

import (
	pty "github.com/aymanbagabas/go-pty"
)

// closePTYChildSide closes the pty end the child process wrote through: the
// slave (tty). The kernel then hands the master reader every byte still
// buffered and ends the next read with EIO, so readOutput drains and returns.
//
// go-pty keeps its own slave descriptor open in THIS process for the pty's
// whole life (see unixPty), which is why the close has to happen here. Linux
// ends a master read only when the LAST descriptor for the slave closes, so the
// child exiting is not enough: without this close readOutput blocks for ever
// and nothing that waits on the reader ever runs. Darwin hides the defect,
// because it revokes the tty when the session leader exits.
//
// Closing the slave does not disturb a child that is still alive. Stop calls
// this before it closes the master, and the child holds descriptors of its own
// for the same tty.
//
// Terminal.closeChildSide is the only caller, and its once-guard is required by
// the Windows implementation rather than by this one.
func closePTYChildSide(p pty.Pty) {
	u, ok := p.(pty.UnixPty)
	if !ok {
		// Unreachable: pty.New returns a unixPty on every non-Windows target.
		// Close the whole pty rather than leave the reader blocked for ever.
		// That costs the last unread chunk, which is better than a reader
		// that never finishes.
		_ = p.Close()
		return
	}
	_ = u.Slave().Close()
}

// closePTYWorkerSide closes the end this process reads and writes through: the
// master (ptmx). unixPty.Close closes both ends, and the already-closed slave
// only makes it report an error that no caller acts on.
func closePTYWorkerSide(p pty.Pty) {
	_ = p.Close()
}

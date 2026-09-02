//go:build windows

package terminal

import (
	pty "github.com/aymanbagabas/go-pty"
	"golang.org/x/sys/windows"
)

// closePTYChildSide closes the pty end the child process wrote through: the
// pseudoconsole. The console host flushes what it still holds into the output
// pipe and exits, which ends readOutput's next read with a broken pipe, so the
// reader drains and returns.
//
// go-pty keeps the pseudoconsole open for the pty's whole life, and the console
// host owns the only remaining write handle for the output pipe (see newPty in
// that package). The child exiting therefore closes nothing: without this call
// readOutput blocks for ever and nothing that waits on the reader ever runs.
//
// It closes the pseudoconsole ALONE, not the pipes that conPty.Close would take
// with it. A pipe that closes here is a pipe the reader can no longer read, and
// the bytes the console host just flushed into it are exactly the ones this
// path exists to keep. closePTYWorkerSide closes them afterwards.
//
// Terminal.closeChildSide guards this with a sync.Once, which this
// implementation requires: ClosePseudoConsole takes a raw handle that go-pty
// does not clear, so a second call closes a handle this process no longer owns.
func closePTYChildSide(p pty.Pty) {
	c, ok := p.(pty.ConPty)
	if !ok {
		// Unreachable: pty.New returns a conPty on Windows. Close the whole pty
		// rather than leave the reader blocked for ever. That costs the last
		// unread chunk, which is better than a reader that never finishes.
		_ = p.Close()
		return
	}
	windows.ClosePseudoConsole(windows.Handle(c.Fd()))
}

// closePTYWorkerSide closes the ends this process reads and writes through: the
// ConPTY input and output pipes. It does NOT route through conPty.Close, which
// would close the pseudoconsole a second time.
func closePTYWorkerSide(p pty.Pty) {
	c, ok := p.(pty.ConPty)
	if !ok {
		_ = p.Close()
		return
	}
	_ = c.InputPipe().Close()
	_ = c.OutputPipe().Close()
}

package procutil

import (
	"os"
	"os/signal"
	"syscall"
)

// KeepRunningOnBrokenPipe installs a Go handler for SIGPIPE, so a write to a
// lost stdout or stderr returns EPIPE instead of killing the process with the
// runtime's fatal SIGPIPE.
//
// A Go program dies on the first EPIPE write to file descriptor 1 or 2 unless
// it handles SIGPIPE: the runtime raises a fatal signal for exactly those two
// descriptors (writes to other descriptors merely return EPIPE). A long-lived
// entry point must survive its stdio peer -- a desktop shell that exits, a
// `tee` that died, a closed terminal -- so the drain and the lease release can
// still finish.
//
// signal.Ignore is impossible here: SIG_IGN survives exec, so every child --
// agent CLI and shell alike -- would inherit it and never learn that its
// reader went away (`yes | head` would spin forever). A Go-installed handler
// resets to the default disposition in each child, so children keep the
// behaviour their own code expects.
//
// Only a long-lived entry point calls this. A short-lived command such as
// `leapmux control ... | head` must keep dying by SIGPIPE, which is what stops
// it when its reader goes away.
func KeepRunningOnBrokenPipe() {
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
}

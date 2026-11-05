package procutil

import (
	"os"
	"os/signal"
	"syscall"
)

// ShutdownSignals lists the signals whose arrival starts the orderly
// shutdown: SIGINT and SIGTERM always, and SIGHUP unless the process
// inherited it as ignored.
//
// SIGHUP is in the list so that closing the terminal which runs the command
// -- or the loss of its controlling process -- drains the Hub, the Worker and
// the solo instance instead of killing the process with the lease still held
// and agent CLIs orphaned. signal.Notify would override an inherited SIG_IGN,
// so the signal.Ignored check keeps a deliberate nohup working: `nohup
// leapmux solo` must survive the hang-up of its terminal. syscall.SIGHUP
// compiles on Windows, where no signal delivery reaches it.
//
// Every long-lived entry point registers the result and never calls
// signal.Stop, so a further signal during the drain is dropped rather than
// force-terminating a process that is releasing the lease. kill -9 is the
// only force exit.
func ShutdownSignals() []os.Signal {
	signals := []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	if !signal.Ignored(syscall.SIGHUP) {
		signals = append(signals, syscall.SIGHUP)
	}
	return signals
}

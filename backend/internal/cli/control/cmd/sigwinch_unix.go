//go:build !windows

package cmd

import (
	"os"
	"os/signal"
	"syscall"
)

func notifyResizeSignals() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch, func() { signal.Stop(ch) }
}

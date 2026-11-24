package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strconv"
	"syscall"
)

// ExitInterrupted is the exit status of a command that the user stopped
// with an interrupt, as a shell reports a process that SIGINT ended.
const ExitInterrupted = 130

// ExitStatus is an error that already reported itself and carries only the
// exit status of the process. Execute prints nothing for it. A command whose
// exit status is part of its result returns one after it prints the result,
// as `leapmux daemon <mode> status` exits 8 for a stopped instance.
type ExitStatus struct {
	Code int
}

func (e *ExitStatus) Error() string { return "exit status " + strconv.Itoa(e.Code) }

// ErrInterrupted is the cause of a context that an interrupt ended.
var ErrInterrupted = errors.New("interrupted")

// WithInterrupt returns a context that SIGINT or SIGTERM ends with the
// cause ErrInterrupted, and the function that stops listening. A command
// that waits on the network observes the context, so an interrupt ends it
// cleanly instead of leaving its connections to the process exit.
func WithInterrupt(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-signals:
			cancel(ErrInterrupted)
		case <-done:
		}
	}()
	return ctx, func() {
		signal.Stop(signals)
		close(done)
		cancel(nil)
	}
}

// Interrupted reports whether an interrupt ended ctx.
func Interrupted(ctx context.Context) bool {
	return ctx != nil && errors.Is(context.Cause(ctx), ErrInterrupted)
}

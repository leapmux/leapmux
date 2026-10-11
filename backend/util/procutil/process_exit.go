package procutil

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

const processExitWaitQuantum = 50 * time.Millisecond
const processTreeExitTimeout = 2 * time.Second

// ProcessExitObserver receives the result of native exit observation for an exact creation identity.
// A nil native error proves exit. A nonnil error states that native observation failed.
// Construction fixes the observer. The owner calls it outside its mutex.
// The observer must not call Close synchronously because that call waits for the observer itself.
type ProcessExitObserver interface {
	ObserveExit(context.Context, ProcessIdentity, error) error
}

// processExitWatch observes one OS process object. It never follows a replacement PID.
type processExitWatch struct {
	identity  ProcessIdentity
	completed bool
	poll      func(time.Duration) (bool, error)
	release   func() error
	closed    bool
	closeErr  error
}

type processExitSystem interface {
	open(int) (*processExitWatch, error)
	created(context.Context, int) (int64, error)
}

type nativeProcessExitSystem struct{}

func (nativeProcessExitSystem) open(pid int) (*processExitWatch, error) {
	return openNativeProcessExitWatch(pid)
}

func (nativeProcessExitSystem) created(ctx context.Context, pid int) (int64, error) {
	return (&process.Process{Pid: int32(pid)}).CreateTimeWithContext(ctx)
}

func openProcessExitWatch(ctx context.Context, identity ProcessIdentity) (*processExitWatch, error) {
	return openProcessExitWatchWith(ctx, identity, nativeProcessExitSystem{})
}

// Open the OS watch before reading creation identity. A reused PID cannot redirect that watch.
// A native exit event proves completion. A failed identity lookup proves nothing.
func openProcessExitWatchWith(ctx context.Context, identity ProcessIdentity, system processExitSystem) (*processExitWatch, error) {
	if ctx == nil {
		return nil, errors.New("the process exit context is absent")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if identity.PID <= 0 || identity.PID > math.MaxInt32 || identity.StartTime <= 0 || identity.PID == os.Getpid() {
		return nil, errors.New("the process exit watch requires a valid foreign process identity")
	}
	watch, err := system.open(identity.PID)
	if errors.Is(err, os.ErrProcessDone) {
		return &processExitWatch{identity: identity, completed: true}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open the process exit watch: %w", err)
	}
	watch.identity = identity
	completed, err := watch.poll(0)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("read the process exit watch: %w", err), watch.close())
	}
	if completed {
		watch.completed = true
		return watch, nil
	}
	created, creationErr := system.created(ctx, identity.PID)
	if creationErr == nil && created == identity.StartTime {
		return watch, nil
	}
	if creationErr != nil {
		creationErr = fmt.Errorf("read the watched process creation identity: %w", creationErr)
	} else if created <= 0 {
		creationErr = errors.New("the watched process supplied no creation identity")
	} else {
		// Linux wall-clock changes can alter this value for a live process.
		// A mismatch grants no signaling ownership and does not prove exit.
		creationErr = errors.New("the watched process creation identity changed before exit")
	}
	// Exit can occur during the lookup. Only this already-open native watch proves completion.
	completed, exitErr := watch.poll(0)
	if exitErr == nil && completed {
		watch.completed = true
		return watch, nil
	}
	return nil, errors.Join(creationErr, exitErr, watch.close())
}

func (w *processExitWatch) wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("the process exit context is absent")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if w.completed {
			return nil
		}
		quantum := processExitWaitQuantum
		if deadline, present := ctx.Deadline(); present {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return context.DeadlineExceeded
			}
			quantum = min(quantum, remaining)
		}
		completed, err := w.poll(quantum)
		if contextErr := ctx.Err(); contextErr != nil {
			return errors.Join(contextErr, err)
		}
		if err != nil {
			return err
		}
		if completed {
			w.completed = true
			return nil
		}
	}
}

func (w *processExitWatch) close() error {
	if w.closed {
		return w.closeErr
	}
	w.closed = true
	if w.release != nil {
		w.closeErr = w.release()
	}
	return w.closeErr
}

// observeProcessExit keeps native evidence and observer errors separate until it joins the result.
func observeProcessExit(ctx context.Context, watch *processExitWatch, observer ProcessExitObserver) error {
	if watch == nil {
		return errors.New("the process exit watch is absent")
	}
	nativeErr := watch.wait(ctx)
	var result error
	if nativeErr != nil {
		result = fmt.Errorf("wait for owned process %d: %w", watch.identity.PID, nativeErr)
	}
	if observer != nil {
		if err := observer.ObserveExit(ctx, watch.identity, nativeErr); err != nil {
			result = errors.Join(result, fmt.Errorf("observe exit of owned process %d: %w", watch.identity.PID, err))
		}
	}
	return errors.Join(result, watch.close())
}

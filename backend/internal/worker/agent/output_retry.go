package agent

import (
	"context"
	"errors"
	"time"

	"github.com/coder/quartz"
)

// OutputRetryTimerTag identifies the timer that retries retained transcript writes.
const OutputRetryTimerTag = "output-retry"
const outputRetryDelay = time.Second

type outputRetry struct {
	ctx     context.Context
	cancel  context.CancelFunc
	clock   quartz.Clock
	onError func(error)
	done    chan struct{}
}

// ConfigureRetry installs the owning Worker's clock and lifetime before output starts.
// A failed final write must recover without requiring another native frame.
// The error observer must not call StopRetries, because it runs on the retry goroutine.
func (queue *OutputQueue) ConfigureRetry(ctx context.Context, clock quartz.Clock, onError func(error)) {
	if ctx == nil || clock == nil {
		panic("output retries require a context and a clock")
	}
	queue.mu.Lock()
	if queue.retry != nil {
		queue.mu.Unlock()
		panic("output retries are already configured")
	}
	retryCtx, cancel := context.WithCancel(ctx)
	queue.retry = &outputRetry{ctx: retryCtx, cancel: cancel, clock: clock, onError: onError}
	queue.mu.Unlock()
	queue.requestRetry()
}

func (queue *OutputQueue) requestRetry() {
	queue.mu.Lock()
	retry := queue.retry
	if retry == nil || retry.done != nil || len(queue.pending) == 0 || retry.ctx.Err() != nil {
		queue.mu.Unlock()
		return
	}
	done := make(chan struct{})
	retry.done = done
	queue.mu.Unlock()
	go queue.retryPending(retry, done)
}

func (queue *OutputQueue) retryPending(retry *outputRetry, done chan struct{}) {
	finished := false
	defer func() {
		if finished {
			return
		}
		queue.mu.Lock()
		if retry.done == done {
			retry.done = nil
		}
		close(done)
		queue.mu.Unlock()
	}()
	reported := false
	for {
		if retry.ctx.Err() != nil {
			return
		}
		timer := retry.clock.NewTimer(outputRetryDelay, OutputRetryTimerTag)
		select {
		case <-retry.ctx.Done():
			timer.Stop(OutputRetryTimerTag)
			return
		case <-timer.C:
			timer.Stop(OutputRetryTimerTag)
		}
		err := queue.Flush()
		if err != nil && !errors.Is(err, ErrOutputInFlight) && !reported && retry.onError != nil {
			reported = true
			retry.onError(err)
		}
		queue.mu.Lock()
		if len(queue.pending) == 0 {
			// Clear the running retry under the same lock as the empty check.
			// A later failed submission can then start a new retry loop.
			close(done)
			retry.done = nil
			finished = true
			queue.mu.Unlock()
			return
		}
		queue.mu.Unlock()
	}
}

// StopRetries cancels and joins the retry loop. Pending writes remain available for a final flush.
func (queue *OutputQueue) StopRetries() {
	queue.mu.Lock()
	retry := queue.retry
	if retry == nil {
		queue.mu.Unlock()
		return
	}
	retry.cancel()
	done := retry.done
	queue.mu.Unlock()
	if done != nil {
		<-done
	}
}

package cli

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithInterrupt_StopIsIdempotent(t *testing.T) {
	ctx, stop := WithInterrupt(t.Context())
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NotPanics(t, stop)
		}()
	}
	wg.Wait()
	assert.Error(t, ctx.Err())
	assert.False(t, Interrupted(ctx))
}

func TestWithInterrupt_StopsSignalImmediatelyOnFirstInterrupt(t *testing.T) {
	var sigCh chan<- os.Signal
	stoppedCh := make(chan struct{}, 2)

	notify := func(c chan<- os.Signal, _ ...os.Signal) {
		sigCh = c
	}
	stopSignal := func(c chan<- os.Signal) {
		if c == sigCh {
			stoppedCh <- struct{}{}
		}
	}

	ctx, stop := withInterrupt(t.Context(), notify, stopSignal)
	require.NotNil(t, sigCh)

	// Deliver the first interrupt signal.
	sigCh <- os.Interrupt

	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for context cancellation on interrupt")
	}
	assert.True(t, Interrupted(ctx))

	// signal.Stop must have been called before stop() is invoked so a second
	// Ctrl+C falls back to the default OS handler.
	select {
	case <-stoppedCh:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for immediate signal.Stop after first interrupt")
	}

	// Calling stop() repeatedly after an interrupt must not panic and must
	// preserve the Interrupted cause.
	assert.NotPanics(t, func() {
		stop()
		stop()
	})
	assert.True(t, Interrupted(ctx))
}

func TestWithInterrupt_SIGTERMAndParentCancel(t *testing.T) {
	t.Run("SIGTERM cancels with ErrInterrupted", func(t *testing.T) {
		var sigCh chan<- os.Signal
		ctx, stop := withInterrupt(t.Context(),
			func(c chan<- os.Signal, _ ...os.Signal) { sigCh = c },
			func(chan<- os.Signal) {},
		)
		defer stop()
		require.NotNil(t, sigCh)

		sigCh <- syscall.SIGTERM
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for SIGTERM cancellation")
		}
		assert.True(t, Interrupted(ctx))
		assert.ErrorIs(t, context.Cause(ctx), ErrInterrupted)
	})

	t.Run("parent cancel preserves non-interrupt cause", func(t *testing.T) {
		parentCause := errors.New("parent canceled")
		parentCtx, parentCancel := context.WithCancelCause(t.Context())
		ctx, stop := withInterrupt(parentCtx,
			func(chan<- os.Signal, ...os.Signal) {},
			func(chan<- os.Signal) {},
		)
		defer stop()

		parentCancel(parentCause)
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for parent cancellation")
		}
		assert.False(t, Interrupted(ctx))
		assert.ErrorIs(t, context.Cause(ctx), parentCause)
	})
}

func TestExitStatusAndInterruptedEdgeCases(t *testing.T) {
	assert.Equal(t, "exit status 8", (&ExitStatus{Code: ExitStopped}).Error())
	var nilCtx context.Context
	assert.False(t, Interrupted(nilCtx))
	assert.False(t, Interrupted(context.Background()))
}

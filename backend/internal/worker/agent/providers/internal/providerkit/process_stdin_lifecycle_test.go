package providerkit

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

type stdinCloseCounter struct {
	io.WriteCloser
	closeCount atomic.Int32
	closeErr   error
}

func (counter *stdinCloseCounter) Close() error {
	counter.closeCount.Add(1)
	return errors.Join(counter.WriteCloser.Close(), counter.closeErr)
}

func TestCloseStdinWithoutInputClosesAdmission(t *testing.T) {
	t.Parallel()
	process := NewProcessFrom(ProcessConfig{})
	process.closeStdin()
	require.ErrorIs(t, process.WriteStdin(nil), errStdinClosed)
	process.closeStdin()
	require.ErrorIs(t, process.WriteStdin([]byte("after close")), errStdinClosed)
}

func TestCloseStdinSerializesConcurrentCleanup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		closeErr error
	}{
		{name: "successful close"},
		{name: "close reports an error", closeErr: assert.AnError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = reader.Close() })
			counter := &stdinCloseCounter{WriteCloser: writer, closeErr: tc.closeErr}
			process := NewProcessFrom(ProcessConfig{Stdin: counter})
			t.Cleanup(process.closeStdin)
			var workers sync.WaitGroup
			for range 8 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					process.closeStdin()
				}()
			}
			done := make(chan struct{})
			go func() { workers.Wait(); close(done) }()
			ctx := testutil.DeadlineContext(t)
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("concurrent stdin cleanup did not finish")
			}
			assert.Equal(t, int32(1), counter.closeCount.Load())
			require.ErrorIs(t, process.WriteStdin([]byte("after close")), io.ErrClosedPipe)
			process.closeStdin()
			assert.Equal(t, int32(1), counter.closeCount.Load(), "later cleanup must not close stdin again")
		})
	}
}

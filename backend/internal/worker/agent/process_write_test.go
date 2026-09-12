package agent

import (
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type partialStdinWriter struct {
	written int
	err     error
}

type inspectingInputWriter struct {
	inspect func()
}

func (writer inspectingInputWriter) Write(data []byte) (int, error) {
	writer.inspect()
	return len(data), nil
}

func (inspectingInputWriter) Close() error { return nil }

func TestClaudeInputWriteKeepsStateAndStopAvailable(t *testing.T) {
	provider := &ClaudeCodeAgent{sink: &testSink{}}
	unlocked := false
	provider.stdin = inspectingInputWriter{inspect: func() {
		unlocked = provider.mu.TryLock()
		if unlocked {
			provider.mu.Unlock()
		}
	}}
	require.NoError(t, provider.SendInput("input", nil))
	require.True(t, unlocked, "a blocked stdin write must not prevent Stop from acquiring the process mutex")
}

func TestClaudeInputDoesNotReopenATurnThatEndsDuringTheWrite(t *testing.T) {
	provider := &ClaudeCodeAgent{sink: &testSink{}}
	provider.stdin = inspectingInputWriter{inspect: func() {
		if provider.mu.TryLock() {
			provider.mu.Unlock()
			provider.disarmTurn()
		}
	}}
	require.NoError(t, provider.SendInput("input", nil))
	require.False(t, provider.PublishTurnActive().Active)
	provider.mu.Lock()
	awaiting := provider.awaitingResult
	provider.mu.Unlock()
	require.False(t, awaiting)
}

func (w partialStdinWriter) Write([]byte) (int, error) { return w.written, w.err }
func (partialStdinWriter) Close() error                { return nil }

func TestRawInputReportsPartialDelivery(t *testing.T) {
	cause := errors.New("the pipe closed")
	for _, test := range []struct {
		name      string
		written   int
		err       error
		want      error
		uncertain bool
	}{
		{name: "complete", written: 4},
		{name: "no bytes", err: cause, want: cause},
		{name: "partial error", written: 2, err: cause, want: cause, uncertain: true},
		{name: "short write", written: 2, want: io.ErrShortWrite, uncertain: true},
		{name: "empty write", want: io.ErrShortWrite},
		{name: "full write with error", written: 4, err: cause, want: cause, uncertain: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := &processBase{stdin: partialStdinWriter{written: test.written, err: test.err}}
			err := process.SendRawInput([]byte("abc\n"))
			if test.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.want)
			}
			require.Equal(t, test.uncertain, errors.Is(err, ErrDeliveryUncertain))
		})
	}
}

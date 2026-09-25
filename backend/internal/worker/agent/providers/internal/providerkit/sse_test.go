package providerkit

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readAllSSE(t *testing.T, body string, limit int) ([]SSEEvent, error) {
	t.Helper()
	var events []SSEEvent
	err := ReadSSE(strings.NewReader(body), limit, func(event SSEEvent) {
		events = append(events, event)
	})
	return events, err
}

func TestReadSSE(t *testing.T) {
	t.Parallel()

	t.Run("dispatches one event for each blank line", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, "data: one\n\ndata: two\n\n", 1024)
		require.NoError(t, err)
		require.Len(t, events, 2)
		assert.Equal(t, "one", string(events[0].Data))
		assert.Equal(t, "two", string(events[1].Data))
	})

	t.Run("joins several data lines with a newline", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, "data: {\"a\":\ndata: 1}\n\n", 1024)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "{\"a\":\n1}", string(events[0].Data))
	})

	t.Run("reads the event type and the id", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, "event: goal.updated\nid: 7\ndata: x\n\n", 1024)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, SSEEvent{Event: "goal.updated", ID: "7", Data: []byte("x")}, events[0])
	})

	t.Run("resets the type and the id after each event", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, "event: a\nid: 1\ndata: x\n\ndata: y\n\n", 1024)
		require.NoError(t, err)
		require.Len(t, events, 2)
		assert.Equal(t, SSEEvent{Data: []byte("y")}, events[1])
	})

	t.Run("strips one space after the colon and keeps the rest", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, "data:no-space\n\ndata:   three\n\n", 1024)
		require.NoError(t, err)
		require.Len(t, events, 2)
		assert.Equal(t, "no-space", string(events[0].Data))
		assert.Equal(t, "  three", string(events[1].Data))
	})

	t.Run("ignores comments and unknown fields", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, ": keep-alive\nretry: 1000\nfoo: bar\ndata: x\n\n", 1024)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "x", string(events[0].Data))
	})

	t.Run("dispatches no event that holds no data line", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, "event: ping\n\n: comment\n\n\n", 1024)
		require.NoError(t, err)
		assert.Empty(t, events)
	})

	t.Run("dispatches an empty data line as empty data", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, "data\n\n", 1024)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Empty(t, events[0].Data)
	})

	t.Run("reads CRLF line endings", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, "event: e\r\ndata: x\r\n\r\n", 1024)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, SSEEvent{Event: "e", Data: []byte("x")}, events[0])
	})

	t.Run("discards an event that the body ends in the middle of", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, "data: whole\n\ndata: {\"half\":", 1024)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, "whole", string(events[0].Data))
	})

	t.Run("ignores an id that holds a NUL", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, "id: a\x00b\ndata: x\n\n", 1024)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Empty(t, events[0].ID)
	})

	t.Run("reads an empty body as no event", func(t *testing.T) {
		t.Parallel()
		events, err := readAllSSE(t, "", 1024)
		require.NoError(t, err)
		assert.Empty(t, events)
	})

	t.Run("refuses one line over the limit", func(t *testing.T) {
		t.Parallel()
		_, err := readAllSSE(t, "data: "+strings.Repeat("x", 64)+"\n\n", 32)
		assert.ErrorIs(t, err, ErrSSEEventTooLarge)
	})

	t.Run("refuses joined data over the limit", func(t *testing.T) {
		t.Parallel()
		line := "data: " + strings.Repeat("x", 20) + "\n"
		events, err := readAllSSE(t, line+line+line+"\n", 50)
		assert.ErrorIs(t, err, ErrSSEEventTooLarge)
		assert.Empty(t, events)
	})

	t.Run("refuses a limit that is not positive", func(t *testing.T) {
		t.Parallel()
		_, err := readAllSSE(t, "data: x\n\n", 0)
		assert.Error(t, err)
	})

	t.Run("returns the read error of the body", func(t *testing.T) {
		t.Parallel()
		failure := errors.New("connection reset")
		var events []SSEEvent
		err := ReadSSE(io.MultiReader(strings.NewReader("data: x\n\n"), failingReader{failure}), 1024, func(event SSEEvent) {
			events = append(events, event)
		})
		assert.ErrorIs(t, err, failure)
		require.Len(t, events, 1, "an event completed before the failure is still dispatched")
	})
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

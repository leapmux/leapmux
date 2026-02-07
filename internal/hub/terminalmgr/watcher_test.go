package terminalmgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestManager_WatchAndBroadcast(t *testing.T) {
	m := New()
	w := m.Watch("t1")
	defer m.Unwatch("t1", w)

	event := &leapmuxv1.TerminalEvent{
		TerminalId: "t1",
		Event: &leapmuxv1.TerminalEvent_Data{
			Data: &leapmuxv1.TerminalData{Data: []byte("hello")},
		},
	}
	m.Broadcast("t1", event)

	select {
	case got := <-w.C():
		require.NotNil(t, got.GetData())
		assert.Equal(t, "hello", string(got.GetData().GetData()))
	default:
		require.Fail(t, "expected event on channel")
	}
}

func TestManager_Unwatch(t *testing.T) {
	m := New()
	w := m.Watch("t1")
	m.Unwatch("t1", w)

	// After unwatch, broadcast should not deliver.
	m.Broadcast("t1", &leapmuxv1.TerminalEvent{
		TerminalId: "t1",
		Event: &leapmuxv1.TerminalEvent_Closed{
			Closed: &leapmuxv1.TerminalClosed{},
		},
	})

	select {
	case <-w.C():
		require.Fail(t, "did not expect event after unwatch")
	default:
	}
}

func TestManager_BroadcastNoWatchers(t *testing.T) {
	m := New()
	// Should not panic.
	m.Broadcast("nonexistent", &leapmuxv1.TerminalEvent{TerminalId: "nonexistent"})
}

func TestManager_BufferOverflow(t *testing.T) {
	m := New()
	w := m.Watch("t1")
	defer m.Unwatch("t1", w)

	event := &leapmuxv1.TerminalEvent{
		TerminalId: "t1",
		Event: &leapmuxv1.TerminalEvent_Data{
			Data: &leapmuxv1.TerminalData{Data: []byte("x")},
		},
	}

	// Fill the buffer (256 capacity).
	for i := 0; i < 256; i++ {
		m.Broadcast("t1", event)
	}

	// Next broadcast should drop silently, not panic.
	m.Broadcast("t1", event)
}

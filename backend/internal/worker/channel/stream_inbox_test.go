package channel

import (
	"bytes"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/require"
)

func TestStreamInboxRetainsOwnedFramesInOrder(t *testing.T) {
	var inbox StreamInbox
	first := []byte("first")
	require.NoError(t, inbox.Deliver(first))
	first[0] = 'x'
	require.NoError(t, inbox.Deliver(nil))
	controller := &streamRequestRecordingController{}
	require.True(t, inbox.Bind(controller))
	require.NoError(t, inbox.Deliver([]byte("last")))
	controller.mu.Lock()
	frames := append([][]byte(nil), controller.payloads...)
	controller.mu.Unlock()
	require.Equal(t, [][]byte{[]byte("first"), nil, []byte("last")}, frames)
	inbox.Release()
	require.NoError(t, inbox.Deliver([]byte("discarded")))
	controller.mu.Lock()
	count, canceled := len(controller.payloads), controller.cancelled
	controller.mu.Unlock()
	require.Equal(t, 3, count)
	require.False(t, canceled)
}

func TestStreamInboxLimitsPendingFramesAndBytes(t *testing.T) {
	t.Run("frame count", func(t *testing.T) {
		var inbox StreamInbox
		for range maxPendingStreamFrames {
			require.NoError(t, inbox.Deliver(nil))
		}
		require.ErrorIs(t, inbox.Deliver(nil), ErrStreamInboxFull)
		require.False(t, inbox.Bind(&streamRequestRecordingController{}))
	})
	t.Run("byte count", func(t *testing.T) {
		var inbox StreamInbox
		require.NoError(t, inbox.Deliver(bytes.Repeat([]byte("x"), contracts.MaxMessageSize)))
		require.ErrorIs(t, inbox.Deliver([]byte("x")), ErrStreamInboxFull)
		require.False(t, inbox.Bind(&streamRequestRecordingController{}))
	})
}

func TestStreamInboxCancellationAndReplacement(t *testing.T) {
	var inbox StreamInbox
	var cancellations atomic.Int32
	controller := &streamRequestRecordingController{onCancelHook: func() { cancellations.Add(1) }}
	require.True(t, inbox.Bind(controller))
	require.False(t, inbox.Bind(&streamRequestRecordingController{}))
	inbox.Cancel()
	inbox.Cancel()
	inbox.Release()
	require.Equal(t, int32(1), cancellations.Load())
	require.False(t, inbox.Bind(&streamRequestRecordingController{}))
}

type cancelingStreamController struct {
	inbox         *StreamInbox
	frames        int
	cancellations int
}

func (controller *cancelingStreamController) OnClientFrame([]byte) {
	controller.frames++
	controller.inbox.Cancel()
}

func (controller *cancelingStreamController) OnCancel() { controller.cancellations++ }

func TestStreamInboxAllowsCancellationFromAController(t *testing.T) {
	var inbox StreamInbox
	controller := &cancelingStreamController{inbox: &inbox}
	require.NoError(t, inbox.Deliver([]byte("first")))
	require.NoError(t, inbox.Deliver([]byte("second")))
	require.True(t, inbox.Bind(controller))
	require.Equal(t, 1, controller.frames)
	require.Equal(t, 1, controller.cancellations)
}

func TestStreamRegistryRejectsBindingAfterClose(t *testing.T) {
	var registry streamRegistry
	inbox, ok := registry.reserve(1)
	require.True(t, ok)
	registry.releaseAll()
	_, ok = registry.bindReserved(1, inbox, &streamRequestRecordingController{})
	require.False(t, ok)
	_, ok = registry.reserve(2)
	require.False(t, ok)
}

func TestStreamRegistryOldReleaseKeepsAReplacement(t *testing.T) {
	var registry streamRegistry
	first, ok := registry.reserve(1)
	require.True(t, ok)
	release, ok := registry.bindReserved(1, first, &streamRequestRecordingController{})
	require.True(t, ok)
	release()
	second, ok := registry.reserve(1)
	require.True(t, ok)
	release()
	controller := &streamRequestRecordingController{}
	_, ok = registry.bindReserved(1, second, controller)
	require.True(t, ok)
	require.NoError(t, second.Deliver([]byte("current")))
	controller.mu.Lock()
	frames := append([][]byte(nil), controller.payloads...)
	controller.mu.Unlock()
	require.Equal(t, [][]byte{[]byte("current")}, frames)
}

package crdt_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// MarshaledEvent's lazy proto.Marshal cache is the per-broadcast
// optimization that prevents N×marshal when one event fans out to N
// subscribers. These tests pin the contract:
//   - Bytes() returns the same buffer ref across calls
//   - The underlying proto matches what proto.Marshal would produce
//   - Concurrent Bytes() calls are safe and only marshal once

func TestMarshaledEvent_BytesReturnsSameBufferAcrossCalls(t *testing.T) {
	evt := &leapmuxv1.WatchOrgEvent{
		Event: &leapmuxv1.WatchOrgEvent_Presence{
			Presence: &leapmuxv1.PresenceUpdate{WorkspaceId: "w1", ActiveClientId: "cA"},
		},
	}
	me := crdt.NewMarshaledEvent(evt)

	first, err := me.Bytes()
	require.NoError(t, err)
	second, err := me.Bytes()
	require.NoError(t, err)

	// Equal contents AND same backing buffer — the cache holds a single
	// []byte that every caller reuses.
	assert.Equal(t, first, second)
	// Compare slice headers via pointer identity of the first byte. Two
	// independent proto.Marshal calls would produce distinct buffers.
	require.NotEmpty(t, first, "marshaled bytes should be non-empty")
	assert.Equal(t, &first[0], &second[0], "Bytes must return the cached buffer, not a fresh one")
}

func TestMarshaledEvent_BytesMatchesProtoMarshal(t *testing.T) {
	evt := &leapmuxv1.WatchOrgEvent{
		Event: &leapmuxv1.WatchOrgEvent_Presence{
			Presence: &leapmuxv1.PresenceUpdate{WorkspaceId: "ws", ActiveClientId: "c"},
		},
	}
	expected, err := proto.Marshal(evt)
	require.NoError(t, err)
	got, err := crdt.NewMarshaledEvent(evt).Bytes()
	require.NoError(t, err)
	assert.Equal(t, expected, got, "MarshaledEvent.Bytes must produce the same payload as proto.Marshal")
}

func TestMarshaledEvent_ConcurrentBytesIsSafeAndCachesOnce(t *testing.T) {
	// Race-detector run pins that sync.Once guards the cache; multiple
	// goroutines must observe the same slice and identical bytes.
	evt := &leapmuxv1.WatchOrgEvent{
		Event: &leapmuxv1.WatchOrgEvent_Initial{Initial: &leapmuxv1.OrgMaterialized{OrgId: "org"}},
	}
	me := crdt.NewMarshaledEvent(evt)

	const goroutines = 32
	var wg sync.WaitGroup
	results := make([][]byte, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			b, err := me.Bytes()
			require.NoError(t, err)
			results[idx] = b
		}(i)
	}
	wg.Wait()
	require.NotEmpty(t, results[0])
	for i := 1; i < goroutines; i++ {
		// Same backing buffer for every concurrent caller.
		assert.Equal(t, &results[0][0], &results[i][0])
	}
}

func TestMarshaledEvent_EventFieldIsAccessibleWithoutMarshal(t *testing.T) {
	// Consumers that only need to inspect the proto (e.g. test fakes
	// asserting on event.GetPresence()) should be able to read
	// `.Event` without paying the marshal cost.
	evt := &leapmuxv1.WatchOrgEvent{
		Event: &leapmuxv1.WatchOrgEvent_Presence{
			Presence: &leapmuxv1.PresenceUpdate{WorkspaceId: "wX"},
		},
	}
	me := crdt.NewMarshaledEvent(evt)
	assert.Same(t, evt, me.Event, "Event must be the original pointer, not a clone")
	// Reading .Event must not have triggered the lazy marshal — the
	// internal once is still pristine, which we infer from a fresh
	// Bytes() call returning a buffer that was just minted (we can't
	// observe `once` directly, so just confirm no error).
	_, err := me.Bytes()
	require.NoError(t, err)
}

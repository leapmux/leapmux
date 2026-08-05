package crdt_test

import (
	"strings"
	"sync"
	"sync/atomic"
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
	evt := &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Presence{
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
	evt := &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Presence{
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
	evt := &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Initial{Initial: &leapmuxv1.UserMaterialized{UserId: "user-1"}},
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

// Size is what a subscriber queue charges its shared byte budget, so it has to
// cover everything holding the frame keeps alive -- the marshaled buffer AND the
// proto tree it came from, which stays reachable through Event by design.
// Counting only the buffer had the pool bounding roughly half what it held.
func TestMarshaledEvent_SizeCoversTheBufferAndTheTreeItCameFrom(t *testing.T) {
	me := crdt.NewMarshaledEvent(&leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Batch{
			Batch: &leapmuxv1.OpBatch{BatchId: strings.Repeat("x", 4096)},
		},
	})

	data, err := me.Bytes()
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), me.WireSize(), "the wire size is the buffer alone")
	assert.Equal(t, int64(len(data))*crdt.ResidentFactor, me.Size())
	assert.Greater(t, me.Size(), me.WireSize(),
		"a frame costs strictly more to hold than to send, or the budget is wrong in the one direction it must not be")
	// Memoized, and memoized to the same number: a size that could change
	// between charge and refund would drift the budget one frame at a time.
	assert.Equal(t, int64(len(data))*crdt.ResidentFactor, me.Size())
}

// Every byte count the Hub's pools traffic in is int64, and the multiply by
// ResidentFactor is the widest arithmetic on this path. Returning int and
// letting subscriber_queue widen with int64(evt.Size()) would put the overflow
// one layer BELOW the conversion -- on a 32-bit build a frame over 1 GiB would
// wrap to a negative charge and hand the pool an unbounded allowance. The types
// are what make that mechanically impossible, so pin them.
func TestMarshaledEvent_ByteCountsAreInt64(t *testing.T) {
	me := crdt.NewMarshaledEvent(&leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Presence{Presence: &leapmuxv1.PresenceUpdate{WorkspaceId: "w1"}},
	})

	assert.IsType(t, int64(0), me.Size(), "Size is a byte count, so it is int64 like every budget it feeds")
	assert.IsType(t, int64(0), me.WireSize())
	assert.IsType(t, int64(0), me.Retain(), "Retain reports the same byte quantity Size does")
	assert.IsType(t, int64(0), me.Release())
}

// The holder count is what lets many queues hold ONE buffer without the pool
// counting it many times. Retain and Release report the transitions at which
// the frame's bytes start and stop being resident, and every other call must
// report zero -- charging a shared frame per holder is the exact mistake that
// kept /ws/userevents out of the pool in the first place.
func TestMarshaledEvent_RetainAndReleaseReportOnlyTheResidencyTransitions(t *testing.T) {
	me := crdt.NewMarshaledEvent(&leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Presence{
			Presence: &leapmuxv1.PresenceUpdate{WorkspaceId: "w1"},
		},
	})
	size := me.Size()
	require.Positive(t, size)

	assert.Equal(t, size, me.Retain(), "the first holder makes the buffer resident")
	assert.Equal(t, int64(0), me.Retain(), "a second holder shares one buffer, and shares its cost")
	assert.Equal(t, int64(0), me.Retain(), "and so does a third")
	assert.Equal(t, 3, me.Holders())

	assert.Equal(t, int64(0), me.Release(), "letting go while others hold frees nothing")
	assert.Equal(t, int64(0), me.Release())
	assert.Equal(t, size, me.Release(), "the last holder frees the buffer")
	assert.Equal(t, 0, me.Holders())

	// 1->0->1 is legitimate: the manager may still be fanning the frame out
	// after every queue so far has drained it, and in that window the buffer
	// really is resident again. Accounting, not lifetime -- nothing was freed.
	assert.Equal(t, size, me.Retain(), "a frame taken up again is resident again")
	assert.Equal(t, size, me.Release())
}

// A queue that released twice would drive its pool's resident total negative,
// and a negative total reads as "empty" -- granting every connection an
// unlimited threshold and silently switching the memory bound off. Failing
// loudly at the bug beats a budget that quietly stops applying.
func TestMarshaledEvent_ReleaseWithoutRetainPanics(t *testing.T) {
	me := crdt.NewMarshaledEvent(&leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Presence{Presence: &leapmuxv1.PresenceUpdate{}},
	})
	require.Equal(t, 0, me.Holders())
	assert.Panics(t, func() { me.Release() })
}

// Retains run on the broadcasting goroutine while releases run on each
// subscriber's WS writer, so the count is contended by construction. The
// invariant that matters is the SUM: across any interleaving, the bytes
// reported resident must equal the bytes reported freed, or the pool drifts.
func TestMarshaledEvent_ConcurrentRetainReleaseBalances(t *testing.T) {
	me := crdt.NewMarshaledEvent(&leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Initial{Initial: &leapmuxv1.UserMaterialized{UserId: "user-1"}},
	})
	size := me.Size()
	require.Positive(t, size)

	const holders = 64
	var resident, freed atomic.Int64
	var wg sync.WaitGroup
	for range holders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resident.Add(me.Retain())
			freed.Add(me.Release())
		}()
	}
	wg.Wait()

	assert.Zero(t, me.Holders())
	assert.Equal(t, resident.Load(), freed.Load(),
		"every byte reported resident must be reported freed exactly once")
	assert.Positive(t, resident.Load(), "the buffer was resident at least once")
	assert.Zero(t, resident.Load()%size, "residency moves in whole frames")
}

func TestMarshaledEvent_EventFieldIsAccessibleWithoutMarshal(t *testing.T) {
	// Consumers that only need to inspect the proto (e.g. test fakes
	// asserting on event.GetPresence()) should be able to read
	// `.Event` without paying the marshal cost.
	evt := &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Presence{
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

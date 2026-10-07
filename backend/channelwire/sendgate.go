package channelwire

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// SendGate serializes the outbound frames of one channel.
//
// Three permits protect three invariants:
//
//   - Each correlation ID holds a sequence permit for a complete message.
//     Stream events reuse an ID. Their chunks must never mix.
//   - The frame permit covers one chunk's encryption and write.
//     The Noise nonce is implicit, so ciphertext order must match wire order.
//   - The chunked permit covers a complete message with multiple chunks.
//     The hub admits one such sequence per channel and direction.
//     Another ID's single-chunk message can overtake this message.
//
// The worker and tunnel senders share this mechanism. Browser sends need no
// permit because their synchronous chunk loop prevents event-loop interleaving.
// The zero value works. Tests can construct a sender without a constructor.
type SendGate struct {
	once            sync.Once
	frame           chan struct{}
	chunked         chan struct{}
	sequenceMu      sync.Mutex
	sequences       map[uint64]*sendSequence
	sequenceWaiters atomic.Int32
	chunkedWaiters  atomic.Int32
	// frameWaiters counts senders parked on the frame permit. Production
	// never reads it; see FrameWaiters for why it exists.
	frameWaiters atomic.Int32
}

type sendSequence struct {
	permit chan struct{}
	users  int
}

// FrameWaiters reports the callers that wait for the frame permit.
// Tests use this count to release a held frame after another sender waits.
// The count excludes the caller that holds the permit.
func (g *SendGate) FrameWaiters() int {
	return int(g.frameWaiters.Load())
}

// SendWaiters lets a test wait for a send before it releases a held frame.
func (g *SendGate) SendWaiters() int {
	return g.FrameWaiters() + int(g.sequenceWaiters.Load()) + int(g.chunkedWaiters.Load())
}

// acquireSequence prevents messages with the same correlation ID from mixing their chunks.
// The reference count includes each waiter. The last caller removes the sequence.
func (g *SendGate) acquireSequence(ctx, lifetime context.Context, correlationID uint64) (func(), error) {
	g.sequenceMu.Lock()
	if g.sequences == nil {
		g.sequences = make(map[uint64]*sendSequence)
	}
	sequence := g.sequences[correlationID]
	if sequence == nil {
		sequence = &sendSequence{permit: make(chan struct{}, 1)}
		g.sequences[correlationID] = sequence
	}
	sequence.users++
	g.sequenceMu.Unlock()
	releaseUser := func() {
		g.sequenceMu.Lock()
		sequence.users--
		if sequence.users == 0 {
			delete(g.sequences, correlationID)
		}
		g.sequenceMu.Unlock()
	}
	if err := acquire(sequence.permit, &g.sequenceWaiters, ctx, lifetime); err != nil {
		releaseUser()
		return nil, err
	}
	return func() {
		<-sequence.permit
		releaseUser()
	}, nil
}

// ErrSendAborted reports that a send stopped before it acquired a permit.
// A caller cancellation before the first frame does not damage the channel.
// After the first frame, only the transport lifetime can stop permit acquisition.
var ErrSendAborted = errors.New("channel send aborted before the frame was written")

func (g *SendGate) init() {
	g.once.Do(func() {
		g.frame = make(chan struct{}, 1)
		g.chunked = make(chan struct{}, 1)
	})
}

// acquire takes a permit or returns ErrSendAborted when a context ends.
// A nil lifetime adds no cancellation source.
// The fast path permits an uncontended acquisition with a cancelled context.
// This matches ctxutil.Mutex. Two contexts require this separate mechanism.
// Tests observe waiting. Pass nil when no test needs the count.
func acquire(permit chan struct{}, waiting *atomic.Int32, ctx, lifetime context.Context) error {
	select {
	case permit <- struct{}{}:
		return nil
	default:
	}
	if waiting != nil {
		waiting.Add(1)
		defer waiting.Add(-1)
	}
	var life <-chan struct{}
	if lifetime != nil {
		life = lifetime.Done()
	}
	select {
	case permit <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrSendAborted, ctx.Err())
	case <-life:
		return fmt.Errorf("%w: %w", ErrSendAborted, lifetime.Err())
	}
}

// WithFrame runs fn while holding the frame permit, so callers can inspect or
// mutate send-side CipherState without racing Encrypt on another goroutine.
func (g *SendGate) WithFrame(ctx, lifetime context.Context, fn func() error) error {
	g.init()
	if err := acquire(g.frame, &g.frameWaiters, ctx, lifetime); err != nil {
		return err
	}
	defer func() { <-g.frame }()
	return fn()
}

// Send splits plaintext and writes each chunk through sendChunk.
// A sequence permit prevents another message with the same ID from entering.
// The operation context controls acquisition only before the first frame.
// Each later frame observes only the transport lifetime.
// A caller cancellation must not leave an incomplete message at the receiver.
// Pass nil for a sender without a separate transport lifetime.
func (g *SendGate) Send(ctx, lifetime context.Context, correlationID uint64, plaintext []byte,
	sendChunk func(chunk []byte, flags leapmuxv1.ChannelMessageFlags) error) error {
	g.init()
	releaseSequence, err := g.acquireSequence(ctx, lifetime, correlationID)
	if err != nil {
		return err
	}
	defer releaseSequence()
	if len(plaintext) > contracts.MaxPlaintextPerChunk {
		if err := acquire(g.chunked, &g.chunkedWaiters, ctx, lifetime); err != nil {
			return err
		}
		defer func() { <-g.chunked }()
	}
	committed := false
	return SendChannelFrames(plaintext, func(chunk []byte, flags leapmuxv1.ChannelMessageFlags) error {
		entryCtx := ctx
		if committed {
			// Only the transport lifetime can stop a message after its first frame.
			entryCtx = context.Background()
		}
		if err := acquire(g.frame, &g.frameWaiters, entryCtx, lifetime); err != nil {
			return err
		}
		defer func() { <-g.frame }()
		committed = true
		return sendChunk(chunk, flags)
	})
}

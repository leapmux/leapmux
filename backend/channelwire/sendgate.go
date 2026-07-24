package channelwire

import (
	"context"
	"errors"
	"fmt"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// SendGate serializes one channel's outbound frames.
//
// Two permits, because two invariants need two different scopes:
//
//   - frame is held across ONE chunk's encrypt + write. The Noise transport
//     nonce is an implicit counter and the peer decrypts in strict arrival
//     order, so ciphertext order MUST equal wire order; one chunk is the
//     smallest scope that guarantees it -- and the smallest that lets another
//     sender's chunk land between messages.
//   - chunked is held across a whole MULTI-chunk message. The Hub relay admits
//     at most ONE chunked sequence per channel+direction and tears the channel
//     down on a second (internal/hub/channelmgr.chunkTracker.Track), and both
//     peer receivers bound live reassembly by DefaultMaxIncompleteChunked. A
//     single-chunk message never takes it, which is exactly what lets a 32 KiB
//     tunnel frame or a flow-control grant overtake a multi-megabyte message.
//
// It lives here because both Go senders of this wire contract need it and
// neither can see the other's copy -- the same reason SendChannelFrames does.
// The browser (frontend/src/lib/channel.ts) needs no counterpart: its
// sendEncryptedMessage chunk loop is synchronous JavaScript, so the event loop
// already makes it atomic and the Hub's one-sequence rule holds for free.
//
// Its zero value is a usable gate: the permits are created on first use, so a
// struct that embeds one by value needs no constructor. That matches
// ctxutil.Mutex and tunnel.latchedErr -- and it is load-bearing here, because
// channelSender is built by struct literal in dispatcher_test.go and
// session_test.go.
type SendGate struct {
	once    sync.Once
	frame   chan struct{}
	chunked chan struct{}
}

// ErrSendAborted reports that a send gave up waiting for a permit rather than
// failing on the wire.
//
// The distinction is load-bearing: tunnel.Channel.sendInnerContext cancels the
// whole channel on an encrypt/write failure, and must NOT do so because one
// caller's context ended before its first chunk. An acquire failure AFTER the
// first chunk can only be the lifetime ending -- i.e. the channel is already
// cancelled -- so "never cancel on ErrSendAborted" is correct at both points.
var ErrSendAborted = errors.New("channel send aborted before the frame was written")

func (g *SendGate) init() {
	g.once.Do(func() {
		g.frame = make(chan struct{}, 1)
		g.chunked = make(chan struct{}, 1)
	})
}

// acquire takes permit, giving up with ErrSendAborted (wrapping the cause) when
// ctx or lifetime ends first. A nil lifetime is simply not observed.
//
// The non-blocking fast path comes first so an uncontended acquire under an
// already-cancelled ctx still succeeds, matching ctxutil.Mutex.Lock. ctxutil.Mutex
// itself is not reused here: it bounds an acquire by ONE context, and this needs
// two (operation + transport lifetime) without allocating a linked context per
// frame -- which is exactly the per-frame allocation this change exists to remove.
func acquire(permit chan struct{}, ctx, lifetime context.Context) error {
	select {
	case permit <- struct{}{}:
		return nil
	default:
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

// Send splits plaintext and puts every chunk on the wire through sendChunk,
// under the two permits described on the type.
//
// ctx gates ENTRY ONLY -- the chunked-permit acquire and the FIRST chunk's
// frame-permit acquire. Once a chunk is on the wire the message is COMMITTED:
// abandoning it midway leaves the peer holding a partial reassembly it can never
// complete (pinning its bytes, burning one of its DefaultMaxIncompleteChunked
// slots, and occupying the Hub's single in-flight chunked sequence) for the
// channel's life. So every chunk after the first waits on lifetime alone.
//
// lifetime may be nil for a sender whose transport has no separate lifetime to
// observe. Pass context.Background() for ctx when entry needs no bound.
func (g *SendGate) Send(ctx, lifetime context.Context, plaintext []byte,
	sendChunk func(chunk []byte, flags leapmuxv1.ChannelMessageFlags) error) error {
	g.init()
	if len(plaintext) > MaxPlaintextPerChunk {
		if err := acquire(g.chunked, ctx, lifetime); err != nil {
			return err
		}
		defer func() { <-g.chunked }()
	}
	committed := false
	return SendChannelFrames(plaintext, func(chunk []byte, flags leapmuxv1.ChannelMessageFlags) error {
		entryCtx := ctx
		if committed {
			// Committed: only the transport's own lifetime may abort us now.
			entryCtx = context.Background()
		}
		if err := acquire(g.frame, entryCtx, lifetime); err != nil {
			return err
		}
		defer func() { <-g.frame }()
		committed = true
		return sendChunk(chunk, flags)
	})
}

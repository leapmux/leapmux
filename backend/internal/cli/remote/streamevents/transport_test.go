package streamevents

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// capturingHandler is a slog.Handler that records emitted records so a test can
// assert a malformed frame was surfaced (and at what level).
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

// fakeChannel is a minimal channelLike that captures the stream callback so a
// test can invoke it deterministically (standing in for the channel demux
// goroutine), and exposes a cancellable Context().
type fakeChannel struct {
	cb  func(*leapmuxv1.InnerStreamMessage)
	ctx context.Context
}

func (f *fakeChannel) SendRPCNoWait(_ string, _ []byte, _ ...chan *leapmuxv1.InnerRpcResponse) (uint32, error) {
	return 1, nil
}
func (f *fakeChannel) RegisterStream(_ uint32, cb func(*leapmuxv1.InnerStreamMessage)) { f.cb = cb }
func (f *fakeChannel) UnregisterStream(_ uint32)                                       {}
func (f *fakeChannel) UnregisterPending(_ uint32)                                      {}
func (f *fakeChannel) Context() context.Context                                        { return f.ctx }
func (f *fakeChannel) Close()                                                          {}

// TestChannelTransport_DropsFramesAfterTeardown asserts the cb guard: a frame the
// channel demux delivers AFTER teardown (Done() closed) must not reach onFrame,
// so a late frame can't run consumer logic (e.g. resetting reconnect backoff) for
// a session that already ended.
func TestChannelTransport_DropsFramesAfterTeardown(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	tr := NewChannelTransport(fc, nil)

	frames := 0
	cancel, done, err := tr.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {
		frames++
	})
	require.NoError(t, err)
	require.NotNil(t, fc.cb)

	// A frame BEFORE teardown is delivered (empty payload unmarshals to a default
	// WatchEventsResponse).
	fc.cb(&leapmuxv1.InnerStreamMessage{})
	require.Equal(t, 1, frames)

	// Tear down and wait for Done() (the goroutine sets `closed` before closing done).
	cancel()
	<-done

	// A late frame the demux still had in flight must be dropped, not delivered.
	fc.cb(&leapmuxv1.InnerStreamMessage{})
	require.Equal(t, 1, frames, "onFrame must not run after Done()")
}

// TestChannelTransport_TeardownNotBlockedByInFlightFrame pins the deadlock fix:
// the transport must NOT hold its frame mutex across onFrame. onFrame chains into
// the consumer's synchronous stdout encode, so a back-pressured `--follow` reader
// blocks it; holding the mutex across that call would wedge the teardown goroutine
// (which needs the mutex to set `closed`/cancel), and Done() would never close.
// With the mutex held only across the `closed` check, teardown completes promptly
// even while a frame is stuck in onFrame.
func TestChannelTransport_TeardownNotBlockedByInFlightFrame(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	tr := NewChannelTransport(fc, nil)

	entered := make(chan struct{})
	release := make(chan struct{})
	cancel, done, err := tr.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {
		close(entered)
		<-release // simulate a back-pressured stdout encode that blocks
	})
	require.NoError(t, err)
	require.NotNil(t, fc.cb)

	// Deliver a frame on a separate goroutine; its onFrame blocks inside the cb,
	// standing in for the channel demux goroutine stuck writing to a paused pipe.
	go fc.cb(&leapmuxv1.InnerStreamMessage{})
	<-entered // the cb is now inside onFrame

	// Teardown must finish even though a frame is wedged in onFrame.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Done() did not close while a frame was blocked in onFrame (teardown deadlock)")
	}
	close(release) // let the blocked frame drain so the goroutine exits
}

// TestChannelTransport_LogsMalformedFrame asserts a frame that fails to decode as
// a WatchEventsResponse is surfaced at warn (not dropped silently) AND that the
// stream stays alive -- a single corrupt frame must not reach onFrame nor end the
// subscription, so a later valid frame still delivers.
func TestChannelTransport_LogsMalformedFrame(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	h := &capturingHandler{}
	tr := NewChannelTransport(fc, slog.New(h))

	frames := 0
	_, _, err := tr.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {
		frames++
	})
	require.NoError(t, err)
	require.NotNil(t, fc.cb)

	// A payload that isn't a valid WatchEventsResponse (wire type 7 is invalid).
	fc.cb(&leapmuxv1.InnerStreamMessage{Payload: []byte{0xff, 0xff, 0xff}})
	require.Equal(t, 0, frames, "a malformed frame must not reach onFrame")
	require.Len(t, h.records, 1, "the malformed frame must be logged")
	require.Equal(t, slog.LevelWarn, h.records[0].Level)

	// The stream survived the bad frame: a subsequent valid frame still delivers.
	fc.cb(&leapmuxv1.InnerStreamMessage{})
	require.Equal(t, 1, frames, "a valid frame after a malformed one must still deliver")
}

package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/protobuf/proto"
)

// replaySink sends catch-up events to one subscriber and stops as soon
// as the transport underneath it dies.
//
// The catch-up burst is the largest group that the worker sends. The worker sends
// a CatchUpStart and up to contracts.MessagePageLimit messages for each agent.
// It then sends a to-do refresh, a status, each pending control request, and a
// CatchUpComplete. Each terminal gets a screen snapshot and a status. Each send used to
// discard its error, so a client that dropped at the start of a page
// refresh had the worker marshal, encrypt and hand every remaining event
// to a transport that was already gone, and only the next LIVE broadcast
// noticed.
//
// Latching the first transport failure is what makes the rest of the
// burst free. A per-message rejection (channel.ErrMessageRejected) does
// NOT latch: the channel is healthy and the next event may well fit, so
// treating it as fatal would abandon a replay over one oversized message.
//
// The watch session owns the sink, but a replayed terminal status is sent by
// whichever goroutine drains that terminal's status queue (see
// ReplayTerminalStatus). So the latch takes a lock. replayID stays the session's
// alone: only the agent replay reads it.
type replaySink struct {
	sender   channel.ResponseWriter
	replayID uint64
	onRetire func()

	mu   sync.Mutex
	dead error
}

// newReplaySink requires an explicit retirement callback. Only an ownerless replay passes nil.
func newReplaySink(sender channel.ResponseWriter, replayID uint64, onRetire func()) *replaySink {
	return &replaySink{sender: sender, replayID: replayID, onRetire: onRetire}
}

// send emits one event, or does nothing once the transport is known dead.
func (s *replaySink) send(resp *leapmuxv1.WatchEventsResponse) {
	if !s.alive() {
		return
	}
	err := broadcastWatchEvent(s.sender, resp)
	if err == nil {
		return
	}
	// Match live broadcast: keep replaying after a per-message size reject,
	// but leave a warn so oversized historical events are not silent.
	if errors.Is(err, channel.ErrMessageRejected) {
		slog.Warn("catch-up: dropping one oversized event; continuing replay",
			"error", err)
		return
	}
	if transportDead(err) {
		s.retire(err)
	}
}

// fail reports a replay preparation error and stops this exact stream.
// Fix the failure before delivery so synchronous reentry cannot continue the burst.
func (s *replaySink) fail(err error) {
	if s.retire(err) {
		sendInternalError(s.sender, "failed to prepare transcript replay")
	}
}

// retire removes live interest before a failed replay can expose its ending
// frame. It reports whether this call retired the sink. The lock covers the latch
// alone, because onRetire can reenter the sink.
func (s *replaySink) retire(err error) bool {
	if err == nil {
		return false
	}
	s.mu.Lock()
	if s.dead != nil {
		s.mu.Unlock()
		return false
	}
	s.dead = err
	s.mu.Unlock()
	if s.onRetire != nil {
		s.onRetire()
	}
	return true
}

// broadcastWatchEvent sends a WatchEventsResponse as a stream message.
//
// It returns the send error so callers can tell a dead transport from a
// refused message -- see transportDead. A marshal failure is reported as
// an error too, so a caller mid-burst does not mistake it for delivery,
// but wrapped in errEventNotMarshalable: it says nothing about the
// transport, and abandoning a whole catch-up replay over one
// un-encodable envelope would lose the message page, the status and the
// control requests behind it.
func broadcastWatchEvent(sender channel.ResponseWriter, resp *leapmuxv1.WatchEventsResponse) error {
	payload, err := marshalWatchEvent(resp, "")
	if err != nil {
		return err
	}
	return sender.SendStream(&leapmuxv1.InnerStreamMessage{
		Payload: payload,
	})
}

// marshalWatchEvent renders one WatchEventsResponse for the wire.
//
// Both send paths go through it -- the fan-out, which marshals once and
// sends to many, and the replay, which marshals and sends one at a time
// -- so there is a single answer to what a marshal failure MEANS. That
// matters more than the few lines it saves: the failure is classified
// downstream by transportDead, and a second definition here is a second
// place for that classification to drift.
//
// entityID is "" on the replay path, which has no entity in hand.
func marshalWatchEvent(resp *leapmuxv1.WatchEventsResponse, entityID string) ([]byte, error) {
	slog.Debug("stream payload", "payload", lazyProtoJSON(resp))
	payload, err := proto.Marshal(resp)
	if err != nil {
		// entity_id is attached rather than made a separate log call so
		// the fan-out keeps the field it always carried.
		slog.Error("failed to marshal WatchEventsResponse", "entity_id", entityID, "error", err)
		return nil, fmt.Errorf("marshal WatchEventsResponse: %w: %w", err, errEventNotMarshalable)
	}
	return payload, nil
}

// alive reports whether sends are still reaching the client. Callers
// check it between entities to skip the DB reads, git shell-outs and PTY
// snapshots that would only feed sends nobody receives.
func (s *replaySink) alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dead == nil
}

// WatchEvents opens a long-lived bidirectional subscription. The opening
// InnerRpcRequest carries the first WatchEventsRequest; every revision after
// that rides an InnerStreamRequest on the SAME stream. Catch-up replay runs
// only for entities that transition into WATCH_MODE_FULL.
//
// Dispatcher ctx is intentionally not threaded: the live stream outlives the
// handler via watchSession.
//
// registerOwnerGuarded decodes the opening request and refuses an undecodable
// one with a stream error frame before this handler runs.
func handleWatchEvents(svc *Service) func(context.Context, channel.Caller, *leapmuxv1.WatchEventsRequest, channel.ResponseWriter) {
	return func(_ context.Context, caller channel.Caller, r *leapmuxv1.WatchEventsRequest, sender channel.ResponseWriter) {
		s := newWatchSession(svc, caller, sender)
		// Queue the opening request before BindStream can replay newer client frames.
		s.submit(r)
		release, ok := sender.BindStream(s)
		if !ok {
			// BindStream refused — the transport has no revise/cancel path for
			// this stream. newWatchSession already claimed channel ownership
			// (BeginSession) and minted a bgCtx child, but run() never starts so
			// its deferred cleanup never fires — retire both explicitly so we do
			// not orphan ownership or leak the ctx (the agent.go handler cancels
			// its own ctrl here for the same reason).
			s.svc.Watchers.UnwatchSession(s.channelID, s.sessionID)
			s.cancel()
			sendFailedPrecondition(sender, "stream binding unavailable")
			return
		}
		// Counted on a handler goroutine the dispatcher's gate admitted, so
		// the Add lands before any dispatcher wait that returns true can
		// have completed: no Add follows the join that covers it.
		s.svc.watchSessions.Add()
		go func() {
			defer s.svc.watchSessions.Done()
			s.run(release)
		}()
	}
}

// replayTerminalCatchUp brings one freshly-subscribed terminal up to the
// current PTY state: the minimum screen bytes it is missing, then the
// startup status it may have joined too late to see.
func (svc *Service) replayTerminalCatchUp(
	sink *replaySink,
	termID string,
	afterOffset int64,
	row db.Terminal,
) {
	// The frontend's after_offset tells us how far it has already
	// processed; ScreenSnapshotSince returns an incremental delta when the
	// offset is inside the retained ring, or a full-state snapshot (with
	// is_snapshot=true) when the subscriber has fallen behind or is
	// cold-subscribing. A caller that is caught up gets (nil, _, false)
	// and no event is sent.
	data, endOffset, isSnapshot := svc.Terminals.ScreenSnapshotSince(termID, afterOffset)
	if len(data) > 0 {
		sink.send(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{
				TerminalEvent: &leapmuxv1.TerminalEvent{
					TerminalId: termID,
					Event: &leapmuxv1.TerminalEvent_Data{
						Data: &leapmuxv1.TerminalData{
							Data:       data,
							IsSnapshot: isSnapshot,
							EndOffset:  endOffset,
						},
					},
				},
			},
		})
	}

	// Replay current startup status so a subscriber that joins after
	// READY / STARTUP_FAILED was broadcast still converges (the prior
	// pure-broadcast design lost events for any watcher that attached
	// after the one-shot fire). The row comes from the access-control
	// loop, so a failure that predates a worker restart still surfaces via
	// the persisted startup_error column.
	//
	// This session registered for live events before the replay began. A status
	// sent outside the terminal's status queue could therefore land after a
	// newer live one, and the client would keep the older status: a tab stuck on
	// EXITED over a restarted shell, or a spinner after a failed startup. The
	// derive runs inside the queue's lock for the same reason. The screen data
	// above stays outside, because the client orders it by offset.
	svc.Watchers.ReplayTerminalStatus(termID, sink, func() *leapmuxv1.TerminalEvent {
		return svc.derivedTerminalStatusEvent(&row)
	})
}

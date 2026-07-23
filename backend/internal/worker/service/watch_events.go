package service

import (
	"context"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// replaySink sends catch-up events to one subscriber and stops as soon
// as the transport underneath it dies.
//
// The catch-up burst is by far the largest thing the worker sends: per
// agent a CatchUpStart, up to maxMessagePageLimit messages, a todo
// refresh, a status, every pending control request and a CatchUpComplete
// -- then a screen snapshot and a status per terminal. Each send used to
// discard its error, so a client that dropped at the start of a page
// refresh had the worker marshal, encrypt and hand every remaining event
// to a transport that was already gone, and only the next LIVE broadcast
// noticed.
//
// Latching the first transport failure is what makes the rest of the
// burst free. A per-message rejection (channel.ErrMessageRejected) does
// NOT latch: the channel is healthy and the next event may well fit, so
// treating it as fatal would abandon a replay over one oversized message.
type replaySink struct {
	sender channel.ResponseWriter
	dead   error
}

func newReplaySink(sender channel.ResponseWriter) *replaySink {
	return &replaySink{sender: sender}
}

// send emits one event, or does nothing once the transport is known dead.
func (s *replaySink) send(resp *leapmuxv1.WatchEventsResponse) {
	if s.dead != nil {
		return
	}
	err := broadcastWatchEvent(s.sender, resp)
	if transportDead(err) {
		s.dead = err
	}
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
func (s *replaySink) alive() bool { return s.dead == nil }

// WatchEvents registers the channel as a watcher for agent/terminal events.
// It replays messages per each agent entry's replay mode (LATEST page, or
// AFTER_CURSOR from its cursor_seq), sends a statusChange marker, replays
// pending control requests, then streams live events.
// Access control: only agents/terminals in workspaces accessible to the
// user (via the channel's accessible_workspace_ids) are watched.
//
// Dispatcher ctx is intentionally not threaded: the handler returns
// after registering watchers + completing the synchronous replay, but
// the live-event stream survives indefinitely via the registration
// the handler leaves behind in the WatcherManager.
// Using the dispatcher ctx for the replay's bootstrap reads would
// risk cancelling them when the handler unwinds before the bg
// goroutines finish writing to the stream.
func handleWatchEvents(svc *Service) channel.HandlerFunc {
	return func(_ context.Context, _ userid.UserID, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.WatchEventsRequest
		if err := unmarshalRequest(req, &r); err != nil {
			// SendStream, not SendError: this call's correlation id is
			// registered as a stream on the client, and an InnerRpcResponse
			// on a stream id is dropped without a listener to receive it.
			sendStreamError(sender, codes.InvalidArgument, "invalid request")
			return
		}

		// The channel id is the subscription key, and it is also the key
		// UnwatchAll is called with when the channel closes -- taking both
		// from the writer keeps them the same string by construction.
		channelID := sender.ChannelID()
		allowedWorkspaces := svc.AuthorizerFor(channelID).AccessibleSet()

		// Filter agents by access control and register watchers FIRST
		// so no broadcasts are missed during the replay phase. Retain
		// the fetched rows so the replay loop below doesn't have to
		// re-fetch them. A single batched SELECT replaces N GetAgentByID
		// round trips on page refresh; ListAgentsByIDs filters closed_at
		// IS NULL, so closed rows fall into the "not returned" branch and
		// land in rejectedAgentIDs with the same semantics as before.
		// Dedup by id: setWatches collapses a repeated entity into one
		// registration, and the replay loops below must agree with it or a
		// request naming an agent twice replays its whole catch-up burst
		// twice (two CatchUpStart/Complete brackets, the same message page
		// rendered twice) and a repeated terminal writes the same screen
		// bytes into xterm twice.
		requestedAgentIDs := make([]string, 0, len(r.GetAgents()))
		agentEntries := make([]*leapmuxv1.WatchAgentEntry, 0, len(r.GetAgents()))
		seenAgentIDs := make(map[string]struct{}, len(r.GetAgents()))
		for _, agentEntry := range r.GetAgents() {
			agentID := agentEntry.GetAgentId()
			if _, dup := seenAgentIDs[agentID]; dup {
				continue
			}
			seenAgentIDs[agentID] = struct{}{}
			requestedAgentIDs = append(requestedAgentIDs, agentID)
			agentEntries = append(agentEntries, agentEntry)
		}
		agentRowsByID := make(map[string]db.Agent, len(requestedAgentIDs))
		if len(requestedAgentIDs) > 0 {
			rows, err := svc.Queries.ListAgentsByIDs(bgCtx(), requestedAgentIDs)
			if err != nil {
				slog.Error("WatchEvents: ListAgentsByIDs failed", "error", err)
				// The set this channel watches is still whatever it was --
				// a failed lookup says nothing about the client's interest.
				// Rebind it at this stream anyway: the request arrived on a
				// fresh correlation id, and leaving the registrations
				// addressed to the previous one would keep events flowing
				// to a listener the client has already torn down.
				svc.Watchers.RebindWatches(channelID, sender)
				sendStreamError(sender, codes.Internal, "failed to list agents")
				return
			}
			for _, row := range rows {
				agentRowsByID[row.ID] = row
			}
		}
		var verifiedAgentIDs []string
		var verifiedAgents []*leapmuxv1.WatchAgentEntry
		var verifiedAgentRows []db.Agent
		var rejectedAgentIDs []string
		for _, agentEntry := range agentEntries {
			agentID := agentEntry.GetAgentId()
			agentRow, ok := agentRowsByID[agentID]
			if !ok || !allowedWorkspaces[agentRow.WorkspaceID] {
				rejectedAgentIDs = append(rejectedAgentIDs, agentID)
				continue
			}
			verifiedAgentIDs = append(verifiedAgentIDs, agentID)
			verifiedAgents = append(verifiedAgents, agentEntry)
			verifiedAgentRows = append(verifiedAgentRows, agentRow)
		}

		// Filter terminals by access control. Same batched-lookup and
		// dedup rationale as the agent loop above.
		requestedTerminals := r.GetTerminals()
		requestedTerminalIDs := make([]string, 0, len(requestedTerminals))
		afterOffsetByID := make(map[string]int64, len(requestedTerminals))
		for _, entry := range requestedTerminals {
			termID := entry.GetTerminalId()
			if _, dup := afterOffsetByID[termID]; dup {
				continue
			}
			requestedTerminalIDs = append(requestedTerminalIDs, termID)
			afterOffsetByID[termID] = entry.GetAfterOffset()
		}
		termRowsByID := make(map[string]db.Terminal, len(requestedTerminalIDs))
		// A failed lookup rejects every terminal, which must NOT be read
		// as "this channel no longer watches any terminal" -- that would
		// unsubscribe every live terminal on a transient DB error. Unlike
		// the agent path, which returns outright, this one degrades: the
		// terminal set is kept and merely rebound below.
		termLookupFailed := false
		if len(requestedTerminalIDs) > 0 {
			rows, err := svc.Queries.ListTerminalsByIDs(bgCtx(), requestedTerminalIDs)
			if err != nil {
				slog.Warn("WatchEvents: ListTerminalsByIDs failed", "error", err)
				termLookupFailed = true
			}
			for _, row := range rows {
				termRowsByID[row.ID] = row
			}
		}
		var verifiedTerminalIDs []string
		var verifiedTerminalRows []db.Terminal
		var rejectedTerminalIDs []string
		for _, termID := range requestedTerminalIDs {
			termRow, ok := termRowsByID[termID]
			if !ok || !allowedWorkspaces[termRow.WorkspaceID] {
				rejectedTerminalIDs = append(rejectedTerminalIDs, termID)
				continue
			}
			verifiedTerminalIDs = append(verifiedTerminalIDs, termID)
			verifiedTerminalRows = append(verifiedTerminalRows, termRow)
		}

		// Log any rejected entities for diagnostics.
		if len(rejectedAgentIDs) > 0 || len(rejectedTerminalIDs) > 0 {
			slog.Warn("WatchEvents: some requested entities not accessible",
				"rejected_agents", rejectedAgentIDs,
				"rejected_terminals", rejectedTerminalIDs,
				"verified_agents", len(verifiedAgents),
				"verified_terminals", len(verifiedTerminalIDs))
		}

		// Registration happens HERE, after both verifications and before
		// any replay, so the request's outcome and its side effect are
		// decided together. Registering as each entity kind was verified
		// meant a request that turned out to be wholly unsatisfiable had
		// already replaced both registries by the time it returned an
		// error.
		switch {
		case len(r.GetAgents()) == 0 && len(r.GetTerminals()) == 0:
			// An explicit "I am watching nothing". This is the only way a
			// client can retire its subscriptions without closing the
			// channel, so it is a legitimate request, not an error: the
			// frontend sends it when the last tab on a worker closes.
			//
			// Routed through UnwatchAll -- the same call the channel-close
			// path and ReleaseLocalStream use -- rather than spelling out
			// a pair of empty Set*Watches, so "retire this channel's
			// subscriptions" has one implementation and the explicit and
			// implicit paths cannot diverge if replace-semantics change.
			svc.Watchers.UnwatchAll(channelID)
			return
		case len(verifiedAgents) == 0 && len(verifiedTerminalIDs) == 0:
			// The client named entities and every one was rejected. Its
			// interest is unsatisfiable, but that is not the same as "it
			// wants nothing" -- an empty accessible-workspace set on a
			// channel whose access info has not landed yet produces this
			// too. Keep what is registered, rebind it to this stream, and
			// let the error trip the client's retry.
			svc.Watchers.RebindWatches(channelID, sender)
			sendStreamError(sender, codes.NotFound,
				fmt.Sprintf("agents %v and/or terminals %v not found or not accessible",
					rejectedAgentIDs, rejectedTerminalIDs))
			return
		default:
			// One call per entity kind, not one per entity: the request
			// states the channel's whole current interest, so entities it
			// no longer names are unsubscribed here.
			//
			// A PARTIALLY rejected request therefore unsubscribes the
			// rejected entity while reporting success, and this is only
			// correct because every rejection reachable today is DURABLE:
			// ListAgentsByIDs fails all-or-nothing (the error path returns
			// above), and an accessible-workspace set is only ever added to
			// after the channel opens. So a rejected agent is one that is
			// closed or was never granted -- unsubscribing it is right, and
			// the client is not waiting on it.
			//
			// Introduce a TRANSIENT rejection -- a chunked or partial id
			// lookup, an access set that can shrink -- and this silently
			// becomes a bug: that entity's tab loses its subscription with
			// no error frame, so nothing retries and it stays blank until
			// reload. Whoever adds one needs to make partial rejection
			// report itself; see
			// https://github.com/leapmux/leapmux/issues/314.
			svc.Watchers.SetAgentWatches(channelID, verifiedAgentIDs, sender)
			if termLookupFailed {
				svc.Watchers.RebindTerminalWatches(channelID, sender)
				// Rebinding preserves whatever this channel already held,
				// which is the right call for an established stream -- but
				// it registers NOTHING, so on a fresh channel (a page
				// refresh mints a new one) the requested terminals end up
				// unwatched while the client is told the subscription
				// succeeded. Its panes then sit empty for the channel's
				// whole life, because a healthy-looking stream never trips
				// the retry.
				//
				// The lookup failing is a worker-side fault, not a
				// statement about what the client may see, so say so and
				// let the client come back. The agents registered above
				// stay registered: the error ends this stream, and the
				// retry re-states the full interest.
				sendStreamError(sender, codes.Unavailable,
					fmt.Sprintf("could not resolve terminals %v; retry", requestedTerminalIDs))
				return
			}
			svc.Watchers.SetTerminalWatches(channelID, verifiedTerminalIDs, sender)
		}

		// One sink for the whole burst: the first dead-transport error
		// stops every remaining send, and the alive() checks below stop the
		// work that would have produced them.
		//
		// Built BEFORE the git batch below, not after: that batch forks a
		// git process per distinct working dir, which is the single most
		// expensive thing this handler does, and doing it ahead of the
		// first alive() check meant a client that had already dropped
		// still paid for every one of them.
		sink := newReplaySink(sender)

		// Compute git statuses in a single deduplicated batch so the
		// per-agent replay loop below doesn't serialize N git shell-outs
		// on page refresh (and multiple tabs on the same repo share one
		// call). The DB rows are already in verifiedAgentRows from the
		// access-control loop above.
		var replayGitStatuses []*leapmuxv1.AgentGitStatus
		if sink.alive() {
			replayDirs := make([]string, len(verifiedAgentRows))
			for i, row := range verifiedAgentRows {
				replayDirs[i] = row.WorkingDir
			}
			replayGitStatuses = gitutil.BatchGetGitStatus(bgCtx(), replayDirs)
		} else {
			// Keep the index-parallel contract the loop below relies on.
			replayGitStatuses = make([]*leapmuxv1.AgentGitStatus, len(verifiedAgentRows))
		}

		// Process each verified agent entry: replay messages, send status. Each
		// agent's catch-up is the same bracketed sequence (CatchUpStart -> message
		// replay -> todo refresh -> status -> control-request replay -> CatchUpComplete);
		// replayAgentCatchUp owns it so the replayStartTail/catchUpLatestSeq bracketing
		// invariant is visible at one boundary.
		for i, agentEntry := range verifiedAgents {
			if !sink.alive() {
				break
			}
			svc.replayAgentCatchUp(sink, agentEntry, verifiedAgentRows[i], replayGitStatuses[i])
		}

		// Each terminal's catch-up is the same pair (screen delta or
		// snapshot -> current startup status); replayTerminalCatchUp owns
		// it so this loop reads like its agent counterpart above.
		for i, termID := range verifiedTerminalIDs {
			if !sink.alive() {
				break
			}
			svc.replayTerminalCatchUp(sink, termID, afterOffsetByID[termID], verifiedTerminalRows[i])
		}

		// Stream stays open — events are pushed through the sender this
		// call registered in the WatcherManager. The handler returns
		// immediately; the registration is retired when the channel closes
		// (or, for a local-IPC stream, when the router releases it).
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
	status, startupError, startupMessage := svc.deriveTerminalStatus(&row)
	var termStatusChange *leapmuxv1.TerminalStatusChange
	switch status {
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING:
		termStatusChange = buildTerminalStartingStatus(termID, startupMessage, nil)
	case leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED:
		termStatusChange = buildTerminalFailedStatus(termID, startupError)
	default:
		termStatusChange = buildTerminalReadyStatus(termID)
	}
	sink.send(&leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{
			TerminalEvent: &leapmuxv1.TerminalEvent{
				TerminalId: termID,
				Event:      &leapmuxv1.TerminalEvent_StatusChange{StatusChange: termStatusChange},
			},
		},
	})
}

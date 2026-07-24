// Package service watcher provides a fan-out event manager for broadcasting
// WatchEventsResponse messages to subscribed E2EE channel clients.
package service

import (
	"errors"
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// registration is one channel's live subscription to one entity.
//
// The manager mints every registration, stores it BY VALUE, and never
// hands out a pointer to it. That is what keeps the generation below
// meaningful: a subscriber cannot alias a registration into several
// entities, so registering entity B can no longer overwrite the
// generation entity A was registered with.
type registration struct {
	channelID string
	sender    channel.ResponseWriter

	// gen identifies this registration, minted by the owning registry on
	// every watch call. broadcast snapshots it alongside the sender and
	// hands it back to retire, so a send failure retires only the
	// registration that actually failed -- never a fresher sender
	// installed while the (necessarily unlocked) send loop was in flight.
	//
	// Generations are minted per registry and never reused, so a channel
	// that is dropped and then registers again gets a fresh number, which
	// keeps a still-in-flight broadcast's stale snapshot from matching --
	// and therefore retiring -- the new registration.
	gen uint64
}

// watcherRegistry is one entity kind's subscription table:
// entity ID -> channel ID -> registration.
//
// The inner map makes "one registration per channel per entity"
// structural rather than a rule the watch path has to re-enforce with a
// linear scan: re-subscribing is a single assignment that cannot half
// apply, and the broadcast path needs no dedup pass before sending.
type watcherRegistry struct {
	mu       sync.RWMutex
	byEntity map[string]map[string]registration
	nextGen  uint64
}

func newWatcherRegistry() *watcherRegistry {
	return &watcherRegistry{byEntity: make(map[string]map[string]registration)}
}

// setWatches makes channelID's subscriptions in this registry exactly
// entityIDs: each listed entity is (re)registered against sender with a
// fresh generation, and every entity this channel was watching that the
// new set omits is dropped.
//
// Replace rather than add, because a WatchEvents request is a statement
// of the client's whole current interest, not an increment. Adding only
// leaked: a client that closed a tab kept the registration for it --
// nothing revokes a subscription, since a stream close is client-local
// and produces no frame the worker can see -- so the worker went on
// marshalling, encrypting and shipping every event for a tab nobody was
// listening to, for the life of the channel.
//
// Note what this does and does not reclaim: pruning happens on the NEXT
// WatchEvents, so a channel that stops watching everything is retired by
// channel close (or broadcast's send-failure sweep), not by replace. That
// is why the frontend sends an empty request rather than just closing its
// stream handle when the last tab goes away.
//
// This is safe because one channel carries at most one live WatchEvents
// stream: the browser runs a single unified stream per worker and
// restarts it when the tab set changes, the CLI cancels and drains the
// previous subscription before opening the next, and each local-IPC
// stream gets its own synthetic channel id. That invariant is already
// load-bearing here -- a registration is keyed by channel, so two
// concurrent partial streams on one channel would already deafen each
// other on every entity they shared.
func (r *watcherRegistry) setWatches(channelID string, entityIDs []string, sender channel.ResponseWriter) {
	// Also dedups a request that names the same entity twice.
	keep := make(map[string]struct{}, len(entityIDs))
	for _, id := range entityIDs {
		keep[id] = struct{}{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Deleting from a map while ranging it is defined behaviour in Go.
	for entityID, byChannel := range r.byEntity {
		if _, wanted := keep[entityID]; wanted {
			continue
		}
		delete(byChannel, channelID)
		if len(byChannel) == 0 {
			delete(r.byEntity, entityID)
		}
	}
	for entityID := range keep {
		byChannel := r.byEntity[entityID]
		if byChannel == nil {
			byChannel = make(map[string]registration, 1)
			r.byEntity[entityID] = byChannel
		}
		r.nextGen++
		byChannel[channelID] = registration{channelID: channelID, sender: sender, gen: r.nextGen}
	}
}

// rebindWatches re-points every registration channelID already holds at
// sender, leaving the entity set untouched.
//
// It exists for the one case where a request must NOT be read as a
// statement of interest -- the entity lookup failed, so the worker cannot
// tell "the client dropped these" from "the database blinked" -- but the
// request still arrived on a NEW stream. Keeping the old set while
// leaving the old sender in place would preserve the subscriptions and
// silently address them to a correlation id the client has already
// stopped listening on: events keep flowing, SendStream keeps succeeding,
// and nothing ever trips the reconnect that would recover it.
func (r *watcherRegistry) rebindWatches(channelID string, sender channel.ResponseWriter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, byChannel := range r.byEntity {
		if _, ok := byChannel[channelID]; !ok {
			continue
		}
		r.nextGen++
		byChannel[channelID] = registration{channelID: channelID, sender: sender, gen: r.nextGen}
	}
}

// unwatchAll drops every subscription held by channelID.
func (r *watcherRegistry) unwatchAll(channelID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for entityID, byChannel := range r.byEntity {
		delete(byChannel, channelID)
		if len(byChannel) == 0 {
			delete(r.byEntity, entityID)
		}
	}
}

// snapshot copies out entityID's registrations under the read lock.
//
// The send loop must run UNLOCKED -- a SendStream can block on the
// transport, and the dead-watcher sweep that follows takes the write
// lock -- so it cannot read the registry's stored registrations
// directly. Reading a sender there would race watch, which replaces the
// registration on re-subscribe while holding the write lock. sender is
// an interface (two words: type descriptor + data pointer), so such a
// read can tear and pair one implementation's type with another's
// pointer; copying the values out under the lock removes the shared
// access entirely and gives the send loop coherent senders.
func (r *watcherRegistry) snapshot(entityID string) []registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byChannel := r.byEntity[entityID]
	if len(byChannel) == 0 {
		return nil
	}
	out := make([]registration, 0, len(byChannel))
	for _, reg := range byChannel {
		out = append(out, reg)
	}
	return out
}

// retire drops the registrations whose sends failed. One lock
// acquisition regardless of how many failed simultaneously.
//
// A registration is dropped only if its generation still matches the one
// the broadcast sent through. A client that reconnected while the
// unlocked send loop was in flight has already been given a fresh
// generation, so the stale sender's failure leaves the new registration
// in place instead of silently unsubscribing a live client.
func (r *watcherRegistry) retire(entityID string, failed []registration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byChannel := r.byEntity[entityID]
	if byChannel == nil {
		return
	}
	dropped := 0
	for _, f := range failed {
		if cur, ok := byChannel[f.channelID]; ok && cur.gen == f.gen {
			delete(byChannel, f.channelID)
			dropped++
		}
	}
	// Gated on having dropped something so this reads as "clean up after
	// myself" -- which is what it means -- rather than "clean up after
	// anyone". Ungated it also fires when every generation check declined
	// to retire, and is correct then only because no other path leaves an
	// empty inner map behind: an invariant spread across three call sites
	// and visible at none of them.
	if dropped > 0 && len(byChannel) == 0 {
		delete(r.byEntity, entityID)
	}
}

// errEventNotMarshalable marks an envelope the worker could not encode.
// It is a defect in something the worker built itself, so it says
// nothing about the peer or the transport -- the event is lost, the
// subscription is not.
var errEventNotMarshalable = errors.New("watch event could not be marshalled")

// transportDead classifies a stream-send error.
//
// It reports true only when the underlying channel-RPC stream cannot
// deliver bytes at all (transport gone, correlation id closed, peer
// dropped), which makes every further send to that writer pointless.
// Two failures are explicitly NOT that, and both mean "this one event is
// lost, carry on": a per-message rejection, where the channel is healthy
// and the next message may well fit, and a WatchEventsResponse this
// package could not marshal, which never reached the transport to begin
// with.
//
// The marshal exemption is narrower than it may read, and deliberately
// so. It covers the RESPONSE marshal in broadcastWatchEvent, which
// errEventNotMarshalable wraps. The channel layer's own ENVELOPE marshal
// returns a plain error and therefore lands in the default arm as dead,
// which is the policy session.go argues for: an envelope the worker
// itself could not encode is a worker defect, and letting it silently
// drop every affected event with no transport error would leave the
// subscriber with nothing to reconnect from. Anything unrecognised
// defaulting to dead is the safe direction -- a wrongly-retired
// subscriber reconnects, a wrongly-kept one goes quiet forever.
//
// Transport failures now surface mainly as stream death → the deferred
// channelMgr.CloseAll() on Connect teardown, rather than as a per-send
// error: the connection writer's drain goroutine owns the only
// stream.Send, so a Hub that cannot keep up cancels the connection and
// CloseAll retires every watcher. Retirement here remains a backstop for
// the rarer case where a send still fails while the stream looks live.
//
// The distinction has two consumers -- broadcast, which decides whether
// to retire a subscription, and the WatchEvents replay, which decides
// whether to abandon the rest of a catch-up burst. Both must answer it
// the same way, so they answer it here.
func transportDead(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, channel.ErrMessageRejected):
		return false
	case errors.Is(err, errEventNotMarshalable):
		return false
	default:
		return true
	}
}

// broadcast fans resp out to every channel subscribed to entityID.
func (r *watcherRegistry) broadcast(entityID string, resp *leapmuxv1.WatchEventsResponse) {
	watchers := r.snapshot(entityID)
	if len(watchers) == 0 {
		return
	}

	payload, err := marshalWatchEvent(resp, entityID)
	if err != nil {
		// Nothing to retire: the failure is this worker's own encoding
		// defect, not a statement about any subscriber. Dropping the event
		// and keeping every registration is what transportDead decides for
		// the replay path too -- see errEventNotMarshalable.
		return
	}

	// Collect the registrations whose sends failed so we can drop them
	// after the send loop. A SendStream error that means the underlying
	// channel-RPC stream cannot deliver bytes (transport gone,
	// correlation ID closed, peer dropped) makes further broadcasts to
	// this watcher silently lost, so the registration goes. The channel
	// layer's eventual transport-level error surfaces to the frontend as
	// onError/onEnd, which trips the reconnect loop in
	// useWorkspaceConnection.ts and replays from DB.
	//
	// A per-message rejection is NOT such an error -- see
	// channel.ErrMessageRejected.
	var dead []registration
	for _, w := range watchers {
		err := w.sender.SendStream(&leapmuxv1.InnerStreamMessage{
			Payload: payload,
		})
		if err == nil {
			continue
		}
		if !transportDead(err) {
			// The channel is fine; this one message could not be sent.
			// Retiring the watcher here would silently deafen a live
			// client -- and because the transport never errors, nothing
			// would trip the frontend's reconnect to recover it.
			slog.Warn("broadcast: dropping one event; keeping watcher",
				"entity_id", entityID, "channel_id", w.channelID, "error", err)
			continue
		}
		// Retirement is still conditional on the generation matching in
		// retire below, so this logs the failure, not the outcome.
		slog.Warn("broadcast: SendStream failed",
			"entity_id", entityID, "channel_id", w.channelID, "error", err)
		dead = append(dead, w)
	}

	if len(dead) > 0 {
		r.retire(entityID, dead)
	}
}

// WatcherManager manages subscriptions for agent and terminal events.
// Events are broadcast to all watchers as InnerStreamMessage frames
// containing serialized WatchEventsResponse payloads.
//
// The two registries are independent: nothing reads both in one
// operation, and a generation is only ever compared within the registry
// that minted it, so each carries its own lock and counter.
type WatcherManager struct {
	agents    *watcherRegistry
	terminals *watcherRegistry
}

// NewWatcherManager creates a new WatcherManager.
func NewWatcherManager() *WatcherManager {
	return &WatcherManager{
		agents:    newWatcherRegistry(),
		terminals: newWatcherRegistry(),
	}
}

// SetAgentWatches makes channelID's agent subscriptions exactly
// agentIDs, routing their events through sender. Agents the channel
// previously watched that are absent from agentIDs are unsubscribed.
func (m *WatcherManager) SetAgentWatches(channelID string, agentIDs []string, sender channel.ResponseWriter) {
	m.agents.setWatches(channelID, agentIDs, sender)
}

// SetTerminalWatches makes channelID's terminal subscriptions exactly
// terminalIDs. Mirror of SetAgentWatches.
func (m *WatcherManager) SetTerminalWatches(channelID string, terminalIDs []string, sender channel.ResponseWriter) {
	m.terminals.setWatches(channelID, terminalIDs, sender)
}

// RebindTerminalWatches re-points channelID's existing terminal
// subscriptions at sender without changing which terminals it watches.
// Used when a WatchEvents request cannot be trusted as a statement of
// interest but must still redirect events to the stream that sent it.
func (m *WatcherManager) RebindTerminalWatches(channelID string, sender channel.ResponseWriter) {
	m.terminals.rebindWatches(channelID, sender)
}

// RebindWatches re-points every subscription channelID holds, of either
// kind, at sender. See RebindTerminalWatches for why rebinding without
// replacing is a distinct operation.
func (m *WatcherManager) RebindWatches(channelID string, sender channel.ResponseWriter) {
	m.agents.rebindWatches(channelID, sender)
	m.terminals.rebindWatches(channelID, sender)
}

// UnwatchAll removes all subscriptions for a given channel. Wired as the
// channel manager's close callback, so it is what retires an E2EE
// subscriber; local-IPC stream ids have no close callback and are
// retired by broadcast's send-failure sweep instead.
//
// The two registries are unlocked independently, so this is NOT atomic
// across both: a concurrent agent broadcast can observe the channel
// already gone while a terminal broadcast still sees it. That is
// unobservable in practice because a broadcast only ever reads one
// registry, and harmless in principle -- the losing side sends one event
// to a channel that is closing.
func (m *WatcherManager) UnwatchAll(channelID string) {
	m.agents.unwatchAll(channelID)
	m.terminals.unwatchAll(channelID)
}

// BroadcastAgentEvent sends an AgentEvent to all watchers of the given agent.
func (m *WatcherManager) BroadcastAgentEvent(agentID string, event *leapmuxv1.AgentEvent) {
	m.agents.broadcast(agentID, &leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
			AgentEvent: event,
		},
	})
}

// BroadcastTerminalEvent sends a TerminalEvent to all watchers of the given terminal.
func (m *WatcherManager) BroadcastTerminalEvent(terminalID string, event *leapmuxv1.TerminalEvent) {
	m.terminals.broadcast(terminalID, &leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_TerminalEvent{
			TerminalEvent: event,
		},
	})
}

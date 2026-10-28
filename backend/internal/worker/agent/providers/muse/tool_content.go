package muse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type nativeToolObservation struct {
	params           itemParams
	raw              []byte
	sink             agent.ProviderServices
	association      nativeResultAssociation
	unavailable      string
	publication      agent.TranscriptPublication
	publicationKnown bool
	rowReceipt       *agent.TranscriptWriteReceipt
}

// freezeToolObservations retains source facts before a delegate can change live state.
// The caller holds stateMu. The returned observations own their mutable bytes.
func (state *sessionState) freezeToolObservations(items map[string]*itemState) []nativeToolObservation {
	observations := make([]nativeToolObservation, 0, len(items))
	for _, item := range items {
		if item.params.Item.Status == contracts.MuseItemStatusInProgress || item.retainedRevision < item.params.Item.Revision || len(item.raw) == 0 {
			continue
		}
		observation := state.freezeToolObservation(item.params, item.raw, item.retainedWriteReceipt, item.retainedPublication)
		if observation != nil {
			observations = append(observations, *observation)
			item.retainedWriteReceipt = nil
		}
	}
	return observations
}

// freezeToolObservation retains the exact accepted revision, including a
// finalization descriptor whose canonical item may already have advanced.
// The caller holds stateMu.
func (state *sessionState) freezeToolObservation(params itemParams, raw []byte, receipt *agent.TranscriptWriteReceipt, publication *nativeToolPublication) *nativeToolObservation {
	if state.log == nil {
		return nil
	}
	candidates := state.ensureNativeItemIndex().candidates(params)
	if !slices.ContainsFunc(candidates, func(candidate itemParams) bool { return candidate.Item.ID == params.Item.ID }) {
		candidates = append(candidates, params)
	}
	observation := &nativeToolObservation{params: cloneItemParams(params), raw: slices.Clone(raw), sink: state.sink, association: state.log.match(params, candidates), unavailable: state.log.unavailable, rowReceipt: receipt}
	if publication != nil && publication.sessionID == params.SessionID {
		observation.publication = publication.publication
		observation.publicationKnown = true
	}
	return observation
}

func (state *sessionState) allSettledTools() map[string]*itemState {
	items := make(map[string]*itemState)
	for id, item := range state.items {
		if item.params.Item.Kind == contracts.MuseItemKindToolCall && item.params.Item.Status != contracts.MuseItemStatusInProgress {
			items[id] = item
		}
	}
	return items
}

// logItemTargets resolves only origins that the received immutable record can affect.
// The caller holds stateMu. A failed native validation requires the complete fallback.
func (state *sessionState) logItemTargets(record nativeRecord) map[string]*itemState {
	index := state.ensureNativeItemIndex()
	items := make(map[string]*itemState)
	addOrigin := func(position nativePosition) {
		for id, item := range index.origins[nativeOriginIdentity{stream: record.Stream, position: position}] {
			items[id] = item
		}
	}
	addOrigin(nativePosition{ID: record.ID, Sequence: record.Sequence})
	if record.Payload.Event.Kind == contracts.MuseLogEventToolResultBatchCommitted {
		key := nativeBatchIdentity{stream: record.Stream, turnID: record.Payload.RunID, batch: record.Payload.Event.BatchID}
		for _, position := range state.log.ensureNativeBatchIndex().origins[key] {
			addOrigin(position)
		}
	}
	return items
}

func (a *Agent) subscribeLog(id string, timeout time.Duration) error {
	deadline := a.Clock().Now().Add(timeout)
	ctx, finish := a.newChildProofContext(deadline)
	defer finish()
	return a.subscribeLogContext(ctx, id, deadline)
}

func (a *Agent) subscribeLogContext(ctx context.Context, id string, deadline time.Time) error {
	a.stateMu.Lock()
	state := a.sessions[id]
	current := a.currentSessionState(id, state) && state.log != nil
	healthy := current && state.log.subscriptionID > 0 && state.log.unavailable == ""
	a.stateMu.Unlock()
	if !current {
		return agent.ErrChildRouteNotReady
	}
	if healthy {
		return nil
	}
	if !a.hasCapability("rawLog") {
		a.markLogUnavailable(state, "The Muse host did not grant rawLog.")
		return nil
	}
	registration, err := a.registerNativeLogContext(ctx, id, state)
	if errors.Is(err, agent.ErrChildRouteNotReady) {
		return err
	}
	if registration.healthy {
		return nil
	}
	if err == nil {
		if registration.existing {
			err = a.readStoredLogBefore(id, state, deadline)
		} else {
			err = a.readLogTailBefore(id, state, registration.tail, deadline)
		}
	} else if state.childID != "" && ctx.Err() == nil {
		// A completed child has durable logs without a live subscription.
		if storedErr := a.readStoredLogBefore(id, state, deadline); storedErr == nil {
			err = nil
		} else {
			err = errors.Join(err, storedErr)
		}
	}
	if err != nil {
		a.markLogUnavailable(state, err.Error())
		slog.Warn("subscribe to the Muse native log", "error", err)
	} else {
		a.enrichLogItems(state)
	}
	// A log failure leaves the stable transcript available.
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return nil
}

func (a *Agent) readStoredLogBefore(id string, state *sessionState, deadline time.Time) error {
	timeout, err := a.childViewTimeout(deadline)
	if err != nil {
		return err
	}
	raw, err := a.request(methodLogStreams, map[string]string{"sessionId": id}, timeout, nil)
	if err != nil {
		return err
	}
	var reply struct {
		Streams *[]struct {
			Stream nativeStream    `json:"stream"`
			Tail   *nativePosition `json:"tail"`
		} `json:"streams"`
	}
	if json.Unmarshal(raw, &reply) != nil || reply.Streams == nil {
		return fmt.Errorf("the Muse stored log stream list is invalid")
	}
	matches := 0
	var tail nativePosition
	for _, stream := range *reply.Streams {
		if stream.Stream.Kind != contracts.MuseStreamKindSession || stream.Stream.ID != id {
			continue
		}
		matches++
		if stream.Tail == nil || stream.Tail.ID == "" || stream.Tail.Sequence <= 0 {
			return fmt.Errorf("the Muse stored session stream has no valid tail")
		}
		tail = *stream.Tail
	}
	if matches != 1 {
		return fmt.Errorf("the Muse stored log has no unique owned session stream")
	}
	return a.readLogTailBefore(id, state, tail, deadline)
}

func (a *Agent) readLogTailBefore(id string, state *sessionState, tail nativePosition, deadline time.Time) error {
	records, err := a.readLogPagesBefore(id, nil, tail, deadline)
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if !a.currentSessionState(id, state) {
		return fmt.Errorf("the Muse log owner changed during the read")
	}
	for _, record := range records {
		if addErr := state.log.add(record); addErr != nil {
			err = errors.Join(err, addErr)
			break
		}
	}
	if err == nil {
		state.log.unavailable = ""
	}
	return err
}

func (a *Agent) handleLogRecord(raw []byte) {
	var params struct {
		ID     int64           `json:"subscriptionId"`
		Record json.RawMessage `json:"record"`
	}
	var header struct {
		Stream nativeStream `json:"stream"`
	}
	if json.Unmarshal(raw, &params) != nil || json.Unmarshal(params.Record, &header) != nil {
		return
	}
	a.stateMu.Lock()
	state := a.sessions[header.Stream.ID]
	if header.Stream.Kind != contracts.MuseStreamKindSession || params.ID <= 0 || state == nil || state.log == nil || state.log.subscriptionID <= 0 || state.log.subscriptionID != params.ID {
		a.stateMu.Unlock()
		return
	}
	record, err := state.log.addRecord(params.Record)
	if err != nil {
		state.log.unavailable = err.Error()
	}
	var items map[string]*itemState
	if err != nil {
		items = state.allSettledTools()
	} else {
		items = state.logItemTargets(record)
	}
	observations := state.freezeToolObservations(items)
	a.stateMu.Unlock()
	if err != nil {
		slog.Warn("read a Muse native record", "error", err)
	}
	for _, observation := range observations {
		a.enrichTool(observation)
	}
}

func (a *Agent) markLogUnavailable(state *sessionState, reason string) {
	a.dispatcher.mu.Lock()
	a.markLogUnavailableLocked(state, reason)
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
}
func (a *Agent) markLogUnavailableLocked(state *sessionState, reason string) {
	a.stateMu.Lock()
	if state.log == nil {
		a.stateMu.Unlock()
		return
	}
	state.log.unavailable = reason
	a.stateMu.Unlock()
	a.enrichLogItemsLocked(state)
}

func (a *Agent) enrichLogItems(state *sessionState) {
	a.dispatcher.mu.Lock()
	a.enrichLogItemsLocked(state)
	a.dispatcher.mu.Unlock()
	a.drainDeferredOutputActions()
}
func (a *Agent) enrichLogItemsLocked(state *sessionState) {
	a.stateMu.Lock()
	observations := state.freezeToolObservations(state.allSettledTools())
	a.stateMu.Unlock()
	for _, observation := range observations {
		a.enrichTool(observation)
	}
}

// enrichToolGroup rechecks every settled item that shares the current or previous native identity.
func (a *Agent) enrichToolGroup(state *sessionState, item *itemState, previous itemParams) {
	a.stateMu.Lock()
	items := map[string]*itemState{item.params.Item.ID: item}
	index := state.ensureNativeItemIndex()
	for _, params := range []itemParams{item.params, previous} {
		if key, valid := nativeIdentity(params); valid {
			for id, candidate := range index.identities[key] {
				items[id] = candidate
			}
		}
	}
	observations := state.freezeToolObservations(items)
	a.stateMu.Unlock()
	for _, observation := range observations {
		a.enrichTool(observation)
	}
}

// handleLogGap recovers a gap only for the session that owns the subscription.
func (a *Agent) handleLogGap(raw []byte) {
	var gap struct {
		SubscriptionID int64           `json:"subscriptionId"`
		After          json.RawMessage `json:"after"`
		Next           json.RawMessage `json:"next"`
	}
	if json.Unmarshal(raw, &gap) != nil || gap.SubscriptionID <= 0 {
		return
	}
	a.stateMu.Lock()
	var state *sessionState
	var id string
	for sessionID, candidate := range a.sessions {
		if a.currentSessionState(sessionID, candidate) && candidate.log != nil && candidate.log.subscriptionID == gap.SubscriptionID {
			state, id = candidate, sessionID
			break
		}
	}
	a.stateMu.Unlock()
	if state == nil {
		return
	}
	var after, next nativePosition
	if json.Unmarshal(gap.After, &after) != nil || json.Unmarshal(gap.Next, &next) != nil ||
		after.ID == "" || after.Sequence <= 0 || next.ID == "" || next.Sequence <= after.Sequence {
		a.markLogUnavailableLocked(state, "the Muse log reported a gap")
		return
	}
	// The reader must remain free to receive the page reply. Process shutdown
	// joins every admitted gap, including deferred enrichment after that read.
	keyBytes, err := json.Marshal(struct {
		SessionID      string
		SubscriptionID int64
		After          nativePosition
		Next           nativePosition
	}{id, gap.SubscriptionID, after, next})
	if err != nil {
		a.markLogUnavailableLocked(state, "the Muse log gap identity is invalid")
		return
	}
	// The captured state distinguishes different owners of a reused native ID.
	key := fmt.Sprintf("%p:%s", state, keyBytes)
	a.logRecoveryWork.Run(key, func() {
		records, err := a.readLogPages(id, &after, next, a.APITimeout())
		a.dispatcher.mu.Lock()
		defer func() { a.dispatcher.mu.Unlock(); a.drainDeferredOutputActions() }()
		a.stateMu.Lock()
		if !a.currentSessionState(id, state) || state.log.subscriptionID != gap.SubscriptionID {
			a.stateMu.Unlock()
			return
		}
		for _, record := range records {
			if addErr := state.log.add(record); addErr != nil {
				err = addErr
				break
			}
		}
		a.stateMu.Unlock()
		if err != nil {
			a.markLogUnavailableLocked(state, "the Muse log reported a gap: "+err.Error())
			return
		}
		a.enrichLogItemsLocked(state)
	})
}

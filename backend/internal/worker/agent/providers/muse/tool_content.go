package muse

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type nativeToolObservation struct {
	params      itemParams
	raw         []byte
	sink        agent.ProviderServices
	association nativeResultAssociation
	unavailable string
}

// freezeToolObservations retains source facts before a delegate can change live state.
// The caller holds stateMu. The returned observations own their mutable bytes.
func (state *sessionState) freezeToolObservations(items map[string]*itemState) []nativeToolObservation {
	if state.log == nil {
		return nil
	}
	index := state.ensureNativeItemIndex()
	observations := make([]nativeToolObservation, 0, len(items))
	for _, item := range items {
		if item.params.Item.Status == contracts.MuseItemStatusInProgress {
			continue
		}
		observations = append(observations, nativeToolObservation{
			params: cloneItemParams(item.params), raw: slices.Clone(item.raw), sink: state.sink,
			association: state.log.match(item.params, index.candidates(item.params)), unavailable: state.log.unavailable,
		})
	}
	return observations
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
	a.stateMu.Lock()
	state := a.sessions[id]
	a.stateMu.Unlock()
	if state == nil {
		return fmt.Errorf("the Muse log has no owned session")
	}
	if !a.hasCapability("rawLog") {
		a.markLogUnavailable(state, "The Muse host did not grant rawLog.")
		return nil
	}
	var observedErr error
	var tail nativePosition
	_, err := a.request(methodLogSubscribe, map[string]any{"sessionId": id, "stream": nativeStream{Kind: contracts.MuseStreamKindSession, ID: id}}, timeout, func(raw json.RawMessage, err error) {
		if err != nil {
			observedErr = err
			return
		}
		var result struct {
			ID   int64           `json:"subscriptionId"`
			Tail *nativePosition `json:"tail"`
		}
		if json.Unmarshal(raw, &result) != nil || result.ID <= 0 || result.Tail == nil || result.Tail.ID == "" || result.Tail.Sequence <= 0 {
			observedErr = fmt.Errorf("the Muse log subscription acknowledgement is invalid")
			return
		}
		a.stateMu.Lock()
		defer a.stateMu.Unlock()
		if a.sessions[id] != state || state.retired {
			observedErr = fmt.Errorf("the Muse log session changed before subscription")
			return
		}
		for otherID, other := range a.sessions {
			if otherID != id && other.log != nil && other.log.subscriptionID == result.ID {
				observedErr = fmt.Errorf("the Muse log subscription belongs to another session")
				return
			}
		}
		if state.log != nil {
			state.log.subscriptionID = result.ID
			tail = *result.Tail
		}
	})
	if err == nil {
		err = observedErr
	}
	if err == nil {
		var records []json.RawMessage
		records, err = a.readLogPages(id, nil, tail, timeout)
		a.stateMu.Lock()
		for _, record := range records {
			if addErr := state.log.add(record); addErr != nil {
				err = addErr
				break
			}
		}
		a.stateMu.Unlock()
	}
	if err != nil {
		a.markLogUnavailable(state, err.Error())
		slog.Warn("subscribe to the Muse native log", "error", err)
	} else {
		a.enrichLogItems(state)
	}
	// A log failure leaves the stable transcript available.
	return nil
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
	a.stateMu.Lock()
	if state.log == nil {
		a.stateMu.Unlock()
		return
	}
	state.log.unavailable = reason
	a.stateMu.Unlock()
	a.enrichLogItems(state)
}

func (a *Agent) enrichLogItems(state *sessionState) {
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

func (a *Agent) enrichTool(observation nativeToolObservation) {
	association := observation.association
	records, reason := association.records, association.reason
	params := observation.params
	id := params.Item.ID
	raw := observation.raw
	unavailable := observation.unavailable
	stored, err := observation.sink.ReadToolResult(id)
	if err != nil {
		slog.Warn("read a Muse tool result for enrichment", "error", err)
		return
	}
	if stored == nil {
		return
	}
	data := make(map[string]any)
	if len(stored.Content.Supplemental) > 0 {
		if err := json.Unmarshal(stored.Content.Supplemental, &data); err != nil || data == nil {
			slog.Warn("read Muse supplemental records", "error", err)
			return
		}
	}
	if len(records) == 0 && unavailable == "" && association.pending {
		previous, ok := data[contracts.MuseSupplementFieldNativeRecords].([]any)
		if !ok || len(previous) == 0 {
			return
		}
	}
	delete(data, contracts.MuseSupplementFieldUnavailable)
	data[contracts.MuseSupplementFieldNativeRecords] = records
	if len(records) == 0 {
		data[contracts.MuseSupplementFieldNativeRecords] = []json.RawMessage{}
		turn := ""
		if params.Item.TurnID != nil {
			turn = *params.Item.TurnID
		}
		failure := reason
		if unavailable != "" && unavailable != reason {
			failure = unavailable
			if reason != "" {
				failure += ": " + reason
			}
		}
		data[contracts.MuseSupplementFieldUnavailable] = map[string]any{
			"sessionId": params.SessionID, "turnId": turn, "callId": params.Item.CallID,
			"itemIds": []string{id}, "reason": failure,
		}
	}
	supplement, err := json.Marshal(data)
	if err != nil {
		slog.Warn("encode Muse supplemental records", "error", err)
		return
	}
	if slices.Equal(stored.Content.Supplemental, supplement) {
		return
	}
	if !slices.Equal(stored.Content.Original, raw) {
		return
	}
	_, err = observation.sink.EnrichMessage(agent.MessageEnrichment{Seq: stored.Seq, SpanID: id, OriginalContent: raw, PreviousRevision: stored.Revision, SupplementalContent: supplement})
	if err != nil {
		slog.Warn("persist Muse supplemental records", "error", err)
	}
}

package muse

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var errMuseEnrichmentChanged = errors.New("the Muse tool supplement changed before the update")

// Canonical items keep authority without retaining a completed write's bytes.
type nativeToolPublication struct {
	publication agent.TranscriptPublication
	sessionID   string
}

type nativeToolEnrichment struct {
	observation nativeToolObservation
	dispatcher  *outputDispatcher
	prepared    atomic.Bool
	released    atomic.Bool
	attempt     *nativeEnrichmentAttempt
}
type nativeEnrichmentAttempt struct {
	receipt          *agent.MessageEnrichmentReceipt
	previousRevision int64
	supplemental     []byte
}

// enrichTool reserves the dependent write after its accepted native row.
// The caller holds dispatcher.mu. Capture cannot let a later write overtake it.
func (a *Agent) enrichTool(observation nativeToolObservation) {
	operation := &nativeToolEnrichment{observation: observation, dispatcher: a.dispatcher}
	queue := agent.OutputQueueFor(observation.sink)
	var owner *agent.OutputOwner
	var ticket *agent.OutputTicket
	prepared := false
	defer func() {
		if !prepared && queue != nil {
			queue.ClearOwner(owner)
		}
	}()
	if queue != nil {
		owner = agent.NewOutputOwner()
		size := len(observation.raw)
		for _, raw := range observation.association.records {
			size += len(raw)
		}
		ticket = queue.Append(agent.OutputWrite{Owner: owner, ScopeID: observation.params.Item.ID, RetainedBytes: size, Label: "enrich a Muse tool result", Run: operation.run})[0]
	}
	if !observation.publicationKnown {
		frozen := agent.CaptureTranscript(observation.sink, agent.MessageContent{Original: observation.raw, AgentSessionID: observation.params.SessionID}, agent.SpanInfo{SpanID: observation.params.Item.ID}).FrozenContent()
		operation.observation.publication = frozen.Publication
	}
	operation.prepared.Store(true)
	prepared = true
	a.deferOutputAction(func() {
		operation.released.Store(true)
		// The native write already owns its retry. Adding its dependent effect
		// must not repeat that failed write before storage can recover.
		if observation.rowReceipt != nil {
			if _, _, ordered := observation.rowReceipt.CapturedPublication(); ordered && !observation.rowReceipt.OrderedWriteComplete() {
				return
			}
		}
		var err error
		if queue == nil {
			err = operation.run()
		} else {
			err = queue.FlushAfterReady(ticket)
		}
		if err != nil {
			slog.Debug("retain Muse tool enrichment", "error", err)
		}
	})
}

func (operation *nativeToolEnrichment) run() error {
	if !operation.prepared.Load() || !operation.released.Load() {
		return agent.ErrOutputInFlight
	}
	if err := operation.dispatcher.checkPublicationLocks(); err != nil {
		return err
	}
	if operation.attempt != nil {
		if _, _, _, committed := operation.attempt.receipt.CommittedEnrichment(); committed {
			return nil
		}
	}
	return operation.persist()
}

func (operation *nativeToolEnrichment) read() (*agent.StoredMessage, error) {
	observation := operation.observation
	if sequence, stored := observation.rowReceipt.StoredMessageSequence(); stored {
		return observation.sink.ReadToolResultBySeq(sequence)
	}
	return observation.sink.ReadToolResultForSession(observation.params.Item.ID, observation.params.SessionID)
}

// nativeToolFramesEqual ignores only the framing fields omitted by view/page.
// Method and full params, including unknown fields and cursor, remain exact.
func nativeToolFramesEqual(first, second []byte) bool {
	for _, raw := range [][]byte{first, second} {
		line := providerkit.ParseLine(raw)
		if line.HasID() {
			return false
		}
		switch line.Method {
		case contracts.MuseMethodItemStarted, contracts.MuseMethodItemUpdated, contracts.MuseMethodItemCompleted:
		default:
			return false
		}
	}
	if slices.Equal(first, second) {
		return true
	}
	firstKey, firstValid := viewFrameKey(first)
	secondKey, secondValid := viewFrameKey(second)
	return firstValid && secondValid && firstKey == secondKey
}

func (operation *nativeToolEnrichment) persist() error {
	observation := operation.observation
	stored, err := operation.read()
	if err != nil {
		return fmt.Errorf("read the Muse tool result for enrichment: %w", err)
	}
	// Deletion or a newer selected result makes this observation obsolete.
	if stored == nil || stored.Content.AgentSessionID != observation.params.SessionID || !nativeToolFramesEqual(stored.Content.Original, observation.raw) {
		return nil
	}
	data := make(map[string]json.RawMessage)
	if len(stored.Content.Supplemental) > 0 {
		if err := json.Unmarshal(stored.Content.Supplemental, &data); err != nil {
			return fmt.Errorf("read the Muse tool supplement: %w", err)
		}
		if data == nil {
			data = make(map[string]json.RawMessage)
		}
	}
	records, reason := observation.association.records, observation.association.reason
	if len(records) == 0 && observation.unavailable == "" && observation.association.pending {
		var previous []json.RawMessage
		if json.Unmarshal(data[contracts.MuseSupplementFieldNativeRecords], &previous) != nil || len(previous) == 0 {
			return nil
		}
	}
	delete(data, contracts.MuseSupplementFieldUnavailable)
	if len(records) == 0 {
		records = []json.RawMessage{}
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("encode the Muse native records: %w", err)
	}
	data[contracts.MuseSupplementFieldNativeRecords] = encoded
	if len(records) == 0 {
		turn := ""
		if observation.params.Item.TurnID != nil {
			turn = *observation.params.Item.TurnID
		}
		failure := reason
		if observation.unavailable != "" && observation.unavailable != reason {
			failure = observation.unavailable
			if reason != "" {
				failure += ": " + reason
			}
		}
		unavailable, err := json.Marshal(map[string]any{"sessionId": observation.params.SessionID, "turnId": turn, "callId": observation.params.Item.CallID, "itemIds": []string{observation.params.Item.ID}, "reason": failure})
		if err != nil {
			return fmt.Errorf("encode the Muse unavailable result: %w", err)
		}
		data[contracts.MuseSupplementFieldUnavailable] = unavailable
	}
	supplement, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode the Muse tool supplement: %w", err)
	}
	if agent.JSONCanonicalEqual(stored.Content.Supplemental, supplement) {
		return nil
	}
	attempt := operation.attempt
	if attempt == nil || attempt.previousRevision != stored.Revision || !agent.JSONCanonicalEqual(attempt.supplemental, supplement) {
		attempt = &nativeEnrichmentAttempt{receipt: agent.NewMessageEnrichmentReceipt(), previousRevision: stored.Revision, supplemental: supplement}
		operation.attempt = attempt
	}
	written, err := observation.sink.EnrichMessage(agent.MessageEnrichment{
		Publication: observation.publication, AgentSessionID: observation.params.SessionID,
		Seq: stored.Seq, SpanID: observation.params.Item.ID, OriginalContent: stored.Content.Original,
		PreviousRevision: attempt.previousRevision, SupplementalContent: attempt.supplemental, WriteReceipt: attempt.receipt,
	})
	if _, _, _, committed := attempt.receipt.CommittedEnrichment(); committed {
		return nil
	}
	if err != nil {
		return fmt.Errorf("persist the Muse tool supplement: %w", err)
	}
	if written {
		return nil
	}
	latest, err := observation.sink.ReadToolResultBySeq(stored.Seq)
	if err != nil {
		return fmt.Errorf("read the competing Muse tool supplement: %w", err)
	}
	if latest == nil || latest.Content.AgentSessionID != observation.params.SessionID || !nativeToolFramesEqual(latest.Content.Original, observation.raw) {
		return nil
	}
	if nativeToolSupplementMatches(latest.Content.Supplemental, data) {
		return nil
	}
	return errMuseEnrichmentChanged
}

// A concurrent update can preserve our evidence while adding unrelated fields.
func nativeToolSupplementMatches(raw []byte, desired map[string]json.RawMessage) bool {
	var actual map[string]json.RawMessage
	if json.Unmarshal(raw, &actual) != nil || actual == nil {
		return false
	}
	for _, key := range []string{contracts.MuseSupplementFieldNativeRecords, contracts.MuseSupplementFieldUnavailable} {
		current, currentPresent := actual[key]
		wanted, wantedPresent := desired[key]
		if currentPresent != wantedPresent || currentPresent && !agent.JSONCanonicalEqual(current, wanted) {
			return false
		}
	}
	return true
}

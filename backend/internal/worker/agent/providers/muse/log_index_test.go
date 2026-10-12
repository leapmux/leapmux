package muse

import (
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeItemIndexTracksCompleteIdentities(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"session", "turn", "call", "stream kind", "stream ID", "origin ID", "origin sequence"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			first := resultItem("first", "call", "origin")
			second := resultItem("second", "call", "origin")
			switch field {
			case "session":
				second.SessionID, second.SourceRange.Stream.ID = "foreign", "foreign"
			case "turn":
				turn := "second-turn"
				second.Item.TurnID = &turn
			case "call":
				second.Item.CallID = "second-call"
			case "stream kind":
				second.SourceRange.Stream.Kind = "run"
			case "stream ID":
				second.SourceRange.Stream.ID = "foreign"
			case "origin ID":
				second.SourceRange.First.ID = "second-origin"
			case "origin sequence":
				second.SourceRange.First.Sequence = math.MaxInt64
			}
			index := &nativeItemIndex{}
			index.rebuild(map[string]*itemState{"first": {params: first}, "second": {params: second}})
			assert.Equal(t, []itemParams{first}, index.candidates(first))
			originMembers := 1
			if field == "call" || field == "turn" {
				originMembers = 2
			}
			assert.Len(t, index.origins[nativeOriginIdentity{stream: first.SourceRange.Stream, position: first.SourceRange.First}], originMembers)
		})
	}
}

func TestNativeItemIndexRemovesThePreviousMembership(t *testing.T) {
	t.Parallel()
	first := &itemState{params: resultItem("first", "call", "origin")}
	unrelated := &itemState{params: resultItem("unrelated", "other", "other-origin")}
	index := &nativeItemIndex{}
	index.rebuild(map[string]*itemState{"first": first, "unrelated": unrelated})
	previous := cloneItemParams(first.params)
	first.params.SourceRange.First = nativePosition{ID: "replacement-origin", Sequence: 99}
	index.replace(previous, first)
	assert.Empty(t, index.candidates(previous))
	assert.Equal(t, []itemParams{first.params}, index.candidates(first.params))
	assert.Equal(t, []itemParams{unrelated.params}, index.candidates(unrelated.params))
	previous = cloneItemParams(first.params)
	first.params.Item.TurnID = nil
	index.replace(previous, first)
	assert.Empty(t, index.candidates(previous))
	assert.Empty(t, index.candidates(first.params))
	assert.Equal(t, []itemParams{unrelated.params}, index.candidates(unrelated.params))
	assert.Len(t, index.identities, 1)
	assert.Len(t, index.origins, 1)
}

func TestNativeItemIndexRebuildsFromAuthoritativeItems(t *testing.T) {
	t.Parallel()
	index := &nativeItemIndex{}
	index.rebuild(nil)
	assert.Empty(t, index.identities)
	assert.Empty(t, index.origins)
	first := &itemState{params: resultItem("first", "call", "origin")}
	second := &itemState{params: resultItem("second", "call", "origin")}
	items := map[string]*itemState{"first": first, "second": second}
	index.rebuild(items)
	assert.Len(t, index.candidates(first.params), 2)
	delete(items, "first")
	index.rebuild(items)
	assert.Equal(t, []itemParams{second.params}, index.candidates(second.params))
	delete(items, "second")
	index.rebuild(items)
	assert.Empty(t, index.identities)
	assert.Empty(t, index.origins)
	for _, sequence := range []int64{0, -1} {
		first.params.SourceRange.First.Sequence = sequence
		index.replace(itemParams{}, first)
		assert.Empty(t, index.identities)
		assert.Empty(t, index.origins)
	}
}

func TestNativeBatchIndexRebuildsFromAuthoritativeRecords(t *testing.T) {
	t.Parallel()
	log := newNativeLog("session")
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":""}]`)
	require.NoError(t, log.add(origin))
	require.NoError(t, log.add(result))
	require.NoError(t, log.add(result))
	key := nativeBatchIdentity{stream: nativeStream{Kind: "session", ID: "session"}, turnID: "turn", batch: "batch"}
	assert.Equal(t, map[string]struct{}{"result": {}}, log.ensureNativeBatchIndex().results[key])
	assert.Equal(t, map[string]nativePosition{"origin": {ID: "origin", Sequence: 1}}, log.ensureNativeBatchIndex().origins[key])
	log.batchIndex = nil
	assert.Equal(t, map[string]struct{}{"result": {}}, log.ensureNativeBatchIndex().results[key])
	require.Error(t, log.add(logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[null]`)))
	log.batchIndex = nil
	item := resultItem("item", "call", "origin")
	association := log.match(item, []itemParams{item})
	assert.Empty(t, association.records)
	assert.False(t, association.pending)
	assert.NotEmpty(t, association.reason)
	assert.Equal(t, map[string]struct{}{"result": {}}, log.ensureNativeBatchIndex().results[key])
	empty := &nativeBatchIndex{}
	empty.rebuild(nil)
	assert.Empty(t, empty.results)
	assert.Empty(t, empty.origins)
}

func indexedRecordPair(t *testing.T, suffix string, sequence int) (json.RawMessage, json.RawMessage) {
	t.Helper()
	origin := logRecord("origin-"+suffix, "assistant_tool_calls_committed", "message_id", sequence, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result-"+suffix, "tool_result_batch_committed", "batch_id", sequence+1, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
	origin = json.RawMessage(strings.ReplaceAll(string(origin), `"batch"`, `"batch-`+suffix+`"`))
	result = json.RawMessage(strings.ReplaceAll(string(result), `"batch"`, `"batch-`+suffix+`"`))
	return origin, result
}

func indexedItem(id, origin string, sequence int) itemParams {
	item := resultItem(id, "call", origin)
	item.SourceRange.First.Sequence = int64(sequence)
	item.Item.Revision, item.Item.Status = 1, contracts.MuseItemStatusCompleted
	return item
}

func TestNativeLogIndexedTargetsKeepBothArrivalOrders(t *testing.T) {
	for _, recordsFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "item first", true: "records first"}[recordsFirst], func(t *testing.T) {
			a, sink := testAgent(t)
			sink.UpdateSessionID("session")
			a.sessions["session"].log.subscriptionID = 1
			origin, result := indexedRecordPair(t, "one", 1)
			item := indexedItem("item", "origin-one", 1)
			var original []byte
			if !recordsFirst {
				original = feed(t, a, contracts.MuseMethodItemCompleted, item)
				assert.Empty(t, sink.Messages()[0].SupplementalContent)
			}
			for _, record := range []json.RawMessage{result, origin, origin, result} {
				feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": record})
			}
			if recordsFirst {
				original = feed(t, a, contracts.MuseMethodItemCompleted, item)
			}
			require.Len(t, sink.Messages(), 1)
			assert.Equal(t, original, sink.Messages()[0].Content)
			assert.Contains(t, string(sink.Messages()[0].SupplementalContent), `"id":"origin-one"`)
			assert.Contains(t, string(sink.Messages()[0].SupplementalContent), `"id":"result-one"`)
			assert.NotContains(t, string(sink.Messages()[0].SupplementalContent), `"nativeResultUnavailable"`)
		})
	}
}

type indexedReadSink struct {
	agent.ProviderServices
	mu    sync.Mutex
	reads map[string]int
}

func (sink *indexedReadSink) ReadToolResult(id string) (*agent.StoredMessage, error) {
	sink.mu.Lock()
	sink.reads[id]++
	sink.mu.Unlock()
	return sink.ProviderServices.ReadToolResult(id)
}

func TestNativeLogIndexedTargetsWithdrawEveryAffectedItem(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	state := a.sessions["session"]
	state.log.subscriptionID = 1
	reader := &indexedReadSink{ProviderServices: state.sink, reads: make(map[string]int)}
	state.sink = reader
	for index, suffix := range []string{"one", "two"} {
		sequence := index*2 + 1
		origin, result := indexedRecordPair(t, suffix, sequence)
		for _, record := range []json.RawMessage{origin, result} {
			feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": record})
		}
		feed(t, a, contracts.MuseMethodItemCompleted, indexedItem("item-"+suffix, "origin-"+suffix, sequence))
	}
	reader.reads = make(map[string]int)
	feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": logRecord("unrelated", "native_note", "message_id", 5, `"value":0`)})
	assert.Empty(t, reader.reads)
	feed(t, a, contracts.MuseMethodItemCompleted, indexedItem("ambiguous", "origin-one", 1))
	assert.Equal(t, map[string]int{"item-one": 1, "ambiguous": 1}, reader.reads)
	messages := sink.Messages()
	require.Len(t, messages, 3)
	assert.Contains(t, string(messages[0].SupplementalContent), `"nativeResultUnavailable"`)
	assert.NotContains(t, string(messages[0].SupplementalContent), `"id":"result-one"`)
	assert.Contains(t, string(messages[1].SupplementalContent), `"id":"result-two"`)
	assert.NotContains(t, string(messages[1].SupplementalContent), `"nativeResultUnavailable"`)
	assert.Contains(t, string(messages[2].SupplementalContent), `"nativeResultUnavailable"`)
	conflict := logRecord("origin-two", "assistant_tool_calls_committed", "message_id", 3, `"tool_calls":[null]`)
	feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": conflict})
	assert.Contains(t, string(sink.Messages()[1].SupplementalContent), `"nativeResultUnavailable"`)
	assert.NotContains(t, string(sink.Messages()[1].SupplementalContent), `"id":"result-two"`)
}

func TestNativeLogIndexedTargetsRejectForeignStreams(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	state := a.sessions["session"]
	state.log.subscriptionID = 1
	origin, result := indexedRecordPair(t, "one", 1)
	for _, record := range []json.RawMessage{origin, result} {
		feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": record})
	}
	feed(t, a, contracts.MuseMethodItemCompleted, indexedItem("item", "origin-one", 1))
	before := sink.Messages()[0]
	foreign := json.RawMessage(strings.ReplaceAll(string(result), `"id":"session"`, `"id":"child"`))
	feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": foreign})
	assert.Equal(t, before, sink.Messages()[0])
	assert.Len(t, state.log.records, 2)
	assert.Empty(t, state.log.unavailable)
}

func TestNativeLogIndexedTargetsSurviveConcurrentDelivery(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	a.sessions["session"].log.subscriptionID = 1
	var wait sync.WaitGroup
	for index := range 16 {
		suffix := jsonNumber(index)
		sequence := index*2 + 1
		origin, result := indexedRecordPair(t, suffix, sequence)
		item := indexedItem("item-"+suffix, "origin-"+suffix, sequence)
		for _, record := range []json.RawMessage{origin, result} {
			wait.Add(1)
			go func() {
				defer wait.Done()
				feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": record})
			}()
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			feed(t, a, contracts.MuseMethodItemCompleted, item)
		}()
	}
	wait.Wait()
	messages := sink.Messages()
	require.Len(t, messages, 16)
	for _, message := range messages {
		assert.Contains(t, string(message.SupplementalContent), `"nativeRecords"`)
		assert.NotContains(t, string(message.SupplementalContent), `"nativeResultUnavailable"`)
	}
}

func scanNativeAssociation(log *nativeLog, item itemParams, items []itemParams) nativeResultAssociation {
	if item.SessionID != log.sessionID || item.Item.TurnID == nil || item.Item.CallID == "" || item.SourceRange.Stream.Kind != contracts.MuseStreamKindSession || item.SourceRange.Stream.ID != log.sessionID || item.SourceRange.First.ID == "" || item.SourceRange.First.Sequence <= 0 {
		return nativeResultAssociation{reason: "The native item supplies no exact result identity."}
	}
	matches := 0
	for _, candidate := range items {
		if sameItemIdentity(item, candidate) {
			matches++
		}
	}
	if matches != 1 {
		return nativeResultAssociation{reason: "The native result matches more than one item."}
	}
	origin, exists := log.records[item.SourceRange.First.ID]
	if !exists {
		return nativeResultAssociation{reason: "The native origin record is unavailable.", pending: true}
	}
	if log.invalid[origin.record.ID] {
		return nativeResultAssociation{reason: "The native origin record is unavailable."}
	}
	if origin.record.Sequence != item.SourceRange.First.Sequence || origin.record.Stream != item.SourceRange.Stream {
		return nativeResultAssociation{reason: "The native origin record does not match the cited position."}
	}
	event := origin.record.Payload.Event
	if origin.record.Payload.RunID != *item.Item.TurnID || event.Kind != contracts.MuseLogEventAssistantToolCallsCommitted || event.MessageID == "" {
		return nativeResultAssociation{reason: "The native origin record does not match this turn."}
	}
	callIndex := -1
	for index, call := range event.Calls {
		if *call.CallID == item.Item.CallID {
			if callIndex != -1 || *call.Name != item.Item.Tool || *call.Args != item.Item.Args {
				return nativeResultAssociation{reason: "The native call identity is ambiguous."}
			}
			callIndex = index
		}
	}
	if callIndex < 0 {
		return nativeResultAssociation{reason: "The native origin record contains no matching call."}
	}
	var result *storedRecord
	for _, candidate := range log.records {
		value := candidate.record
		if value.Payload.RunID != *item.Item.TurnID || value.Payload.Event.Kind != contracts.MuseLogEventToolResultBatchCommitted || value.Payload.Event.BatchID != event.MessageID {
			continue
		}
		if log.invalid[value.ID] || result != nil {
			return nativeResultAssociation{reason: "The native result batch is ambiguous."}
		}
		count := 0
		for _, entry := range value.Payload.Event.Results {
			if *entry.CallID == item.Item.CallID && *entry.Index == callIndex {
				count++
			}
		}
		if count != 1 {
			return nativeResultAssociation{reason: "The native result batch contains no unique matching result."}
		}
		copy := candidate
		result = &copy
	}
	if result == nil {
		return nativeResultAssociation{reason: "The native result batch is unavailable.", pending: true}
	}
	return nativeResultAssociation{records: []json.RawMessage{origin.raw, result.raw}}
}

func TestNativeLogIndexedAssociationMatchesTheStoredRecordScan(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"empty", "origin", "result", "duplicate", "invalid origin", "invalid result", "ambiguous item", "another batch"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			log := newNativeLog("session")
			origin, result := indexedRecordPair(t, "one", 1)
			item := indexedItem("item", "origin-one", 1)
			items := map[string]*itemState{"item": {params: item}}
			if stage != "empty" {
				require.NoError(t, log.add(origin))
			}
			if stage != "empty" && stage != "origin" {
				require.NoError(t, log.add(result))
			}
			switch stage {
			case "duplicate":
				require.NoError(t, log.add(origin))
				require.NoError(t, log.add(result))
			case "invalid origin":
				require.Error(t, log.add(logRecord("origin-one", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[null]`)))
			case "invalid result":
				require.Error(t, log.add(logRecord("result-one", "tool_result_batch_committed", "batch_id", 2, `"results":[null]`)))
			case "ambiguous item":
				items["second"] = &itemState{params: indexedItem("second", "origin-one", 1)}
			case "another batch":
				require.NoError(t, log.add(json.RawMessage(strings.ReplaceAll(string(result), `"id":"result-one","sequence":2`, `"id":"another","sequence":3`))))
			}
			var all []itemParams
			for _, value := range items {
				all = append(all, value.params)
			}
			index := &nativeItemIndex{}
			index.rebuild(items)
			expected := scanNativeAssociation(log, item, all)
			actual := log.match(item, index.candidates(item))
			assert.Equal(t, expected, actual)
		})
	}
}

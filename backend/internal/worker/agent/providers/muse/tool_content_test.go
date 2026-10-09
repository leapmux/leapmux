package muse

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMalformedLogSubscriptionReportsUnavailable(t *testing.T) {
	for _, source := range []string{`{}`, `{"subscriptionId":0}`, `{"subscriptionId":-1}`, `{"subscriptionId":1,"tail":false}`} {
		t.Run(source, func(t *testing.T) {
			a, _ := testAgent(t)
			a.handshake.GrantedCapabilities = []string{"rawLog"}
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				require.Equal(t, methodLogSubscribe, method)
				return agenttest.RPCReply{Result: json.RawMessage(source)}
			}})
			require.NoError(t, a.subscribeLog("session", time.Second))
			assert.NotEmpty(t, a.sessions["session"].log.unavailable)
		})
	}
}

func TestLogSubscriptionFailureMarksTheRetainedNativeResult(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	a.handshake.GrantedCapabilities = []string{"rawLog"}
	item := resultItem("item", "call", "origin")
	item.Item.Revision = 1
	item.Item.Status = "completed"
	raw := feed(t, a, "item/completed", item)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		require.Equal(t, methodLogSubscribe, method)
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"native log unavailable"}`)}
	}})
	require.NoError(t, a.subscribeLog("session", time.Second))
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, raw, messages[0].Content)
	assert.Contains(t, string(messages[0].SupplementalContent), `"nativeResultUnavailable"`)
	assert.Contains(t, string(messages[0].SupplementalContent), "native log unavailable")
}

func TestConflictingNativeOriginWithdrawsAnEarlierSupplement(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	a.sessions["session"].log.subscriptionID = 1
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
	for _, record := range []json.RawMessage{origin, result} {
		feed(t, a, "log/record", map[string]any{"subscriptionId": 1, "record": record})
	}
	item := resultItem("item", "call", "origin")
	item.Item.Revision = 1
	item.Item.Status = "completed"
	raw := feed(t, a, "item/completed", item)
	require.Contains(t, string(sink.Messages()[0].SupplementalContent), `"nativeRecords"`)
	conflicting := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{}"}]`)
	feed(t, a, "log/record", map[string]any{"subscriptionId": 1, "record": conflicting})
	assert.Equal(t, raw, sink.Messages()[0].Content)
	assert.Contains(t, string(sink.Messages()[0].SupplementalContent), `"nativeResultUnavailable"`)
}

func TestNativeLogRejectsAnAbsentOrUnregisteredSubscription(t *testing.T) {
	for _, id := range []int64{0, -1, 42} {
		t.Run(jsonNumber(int(id)), func(t *testing.T) {
			a, _ := testAgent(t)
			record := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[]`)
			feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": id, "record": record})
			assert.Empty(t, a.sessions["session"].log.records)
		})
	}
}

func TestLogSubscriptionReadsEveryPageThroughItsCapturedTail(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	a.handshake.GrantedCapabilities = []string{"rawLog"}
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
	item := resultItem("item", "call", "origin")
	item.Item.Revision, item.Item.Status = 1, contracts.MuseItemStatusCompleted
	item.Item.VisibleOutput = "Native preview"
	original := feed(t, a, contracts.MuseMethodItemCompleted, item)
	var methods []string
	pages := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
		methods = append(methods, method)
		if method == methodLogSubscribe {
			return agenttest.RPCReply{Result: json.RawMessage(`{"subscriptionId":7,"tail":{"id":"result","sequence":2}}`)}
		}
		require.Equal(t, "log/page", method)
		pages++
		var params struct {
			SessionID string          `json:"sessionId"`
			Stream    nativeStream    `json:"stream"`
			From      *nativePosition `json:"from"`
		}
		require.NoError(t, json.Unmarshal(raw, &params))
		assert.Equal(t, "session", params.SessionID)
		assert.Equal(t, nativeStream{Kind: "session", ID: "session"}, params.Stream)
		var record json.RawMessage
		var position nativePosition
		if pages == 1 {
			assert.Nil(t, params.From)
			record, position = origin, nativePosition{ID: "origin", Sequence: 1}
			feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 7, "record": result})
		} else {
			require.Equal(t, 2, pages)
			require.NotNil(t, params.From)
			assert.Equal(t, nativePosition{ID: "origin", Sequence: 1}, *params.From)
			record, position = result, nativePosition{ID: "result", Sequence: 2}
		}
		data, err := json.Marshal(map[string]any{"records": []json.RawMessage{record}, "nextCursor": map[string]any{"stream": params.Stream, "after": position}})
		require.NoError(t, err)
		return agenttest.RPCReply{Result: data}
	}})
	require.NoError(t, a.subscribeLog("session", time.Second))
	assert.Equal(t, []string{methodLogSubscribe, "log/page", "log/page"}, methods)
	assert.Equal(t, 2, pages)
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, original, sink.Messages()[0].Content)
	var supplement struct {
		Records []json.RawMessage `json:"nativeRecords"`
	}
	require.NoError(t, json.Unmarshal(sink.Messages()[0].SupplementalContent, &supplement))
	require.Len(t, supplement.Records, 2)
	assert.Equal(t, []byte(origin), []byte(supplement.Records[0]))
	assert.Equal(t, []byte(result), []byte(supplement.Records[1]))
	assert.NotContains(t, string(sink.Messages()[0].SupplementalContent), `"nativeResultUnavailable"`)
}

func TestLogPageFailureKeepsThePreviewAndMarksUnavailable(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	a.handshake.GrantedCapabilities = []string{"rawLog"}
	item := resultItem("item", "call", "origin")
	item.Item.Revision, item.Item.Status = 1, contracts.MuseItemStatusCompleted
	item.Item.VisibleOutput = "Native preview"
	original := feed(t, a, contracts.MuseMethodItemCompleted, item)
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
		if method == methodLogSubscribe {
			return agenttest.RPCReply{Result: json.RawMessage(`{"subscriptionId":7,"tail":{"id":"result","sequence":2}}`)}
		}
		require.Equal(t, "log/page", method)
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"native page unavailable"}`)}
	}})
	require.NoError(t, a.subscribeLog("session", time.Second))
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, original, sink.Messages()[0].Content)
	assert.Contains(t, string(sink.Messages()[0].SupplementalContent), `"nativeResultUnavailable"`)
	assert.Contains(t, string(sink.Messages()[0].SupplementalContent), "native page unavailable")
}

func TestNativeLogPagesRejectInvalidPagesAndKeepTheReadPrefix(t *testing.T) {
	valid := `{"id":"first","sequence":1,"stream":{"kind":"session","id":"session"},"payload":{}}`
	cursor := `{"stream":{"kind":"session","id":"session"},"after":{"id":"first","sequence":1}}`
	for _, source := range []string{
		`{}`,
		`{"records":null,"nextCursor":null}`,
		`{"records":[],"nextCursor":null}`,
		`{"records":[` + valid + `]}`,
		`{"records":[` + valid + `],"nextCursor":null}`,
		`{"records":[` + valid + `],"nextCursor":` + strings.ReplaceAll(cursor, "session", "foreign") + `}`,
		`{"records":[` + strings.Replace(valid, `"sequence":1`, `"sequence":2`, 1) + `],"nextCursor":` + cursor + `}`,
		`{"records":[` + strings.Replace(valid, `"sequence":1`, `"sequence":0`, 1) + `],"nextCursor":` + cursor + `}`,
		`{"records":[` + strings.Replace(valid, `"id":"session"`, `"id":"foreign"`, 1) + `],"nextCursor":` + cursor + `}`,
		`{"records":[` + valid + `],"nextCursor":` + strings.Replace(cursor, `"id":"first"`, `"id":"other"`, 1) + `}`,
	} {
		t.Run(source, func(t *testing.T) {
			a, _ := testAgent(t)
			pages := 0
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
				require.Equal(t, methodLogPage, method)
				pages++
				require.Equal(t, 1, pages)
				return agenttest.RPCReply{Result: json.RawMessage(source)}
			}})
			_, err := a.readLogPages("session", nil, nativePosition{ID: "last", Sequence: 2}, time.Second)
			require.Error(t, err)
		})
	}
	t.Run("read prefix", func(t *testing.T) {
		a, _ := testAgent(t)
		pages := 0
		a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, _ json.RawMessage) agenttest.RPCReply {
			require.Equal(t, methodLogPage, method)
			pages++
			if pages == 1 {
				return agenttest.RPCReply{Result: json.RawMessage(`{"records":[` + valid + `],"nextCursor":` + cursor + `}`)}
			}
			require.Equal(t, 2, pages)
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"native page failed"}`)}
		}})
		records, err := a.readLogPages("session", nil, nativePosition{ID: "last", Sequence: 2}, time.Second)
		require.ErrorContains(t, err, "native page failed")
		require.Len(t, records, 1)
		assert.Equal(t, []byte(valid), []byte(records[0]))
	})
}

func TestASecondNativeBatchWithdrawsAnEarlierSupplement(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	a.sessions["session"].log.subscriptionID = 1
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
	for _, record := range []json.RawMessage{origin, result} {
		feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": record})
	}
	item := resultItem("item", "call", "origin")
	item.Item.Revision, item.Item.Status = 1, contracts.MuseItemStatusCompleted
	original := feed(t, a, contracts.MuseMethodItemCompleted, item)
	require.Contains(t, string(sink.Messages()[0].SupplementalContent), `"nativeRecords"`)
	conflict := logRecord("other-result", "tool_result_batch_committed", "batch_id", 3, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":0}"}]`)
	feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": conflict})
	assert.Equal(t, original, sink.Messages()[0].Content)
	assert.Contains(t, string(sink.Messages()[0].SupplementalContent), `"nativeResultUnavailable"`)
	assert.NotContains(t, string(sink.Messages()[0].SupplementalContent), `"id":"result"`)
}

func TestMalformedNativeOriginWithdrawsAnEarlierSupplement(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	a.sessions["session"].log.subscriptionID = 1
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
	for _, record := range []json.RawMessage{origin, result} {
		feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": record})
	}
	item := resultItem("item", "call", "origin")
	item.Item.Revision, item.Item.Status = 1, contracts.MuseItemStatusCompleted
	original := feed(t, a, contracts.MuseMethodItemCompleted, item)
	require.Contains(t, string(sink.Messages()[0].SupplementalContent), `"nativeRecords"`)
	conflict := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[null]`)
	feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": conflict})
	assert.Equal(t, original, sink.Messages()[0].Content)
	assert.Contains(t, string(sink.Messages()[0].SupplementalContent), `"nativeResultUnavailable"`)
	assert.NotContains(t, string(sink.Messages()[0].SupplementalContent), `"id":"result"`)
}

func TestNativeLogMarksEveryAmbiguousItemUnavailable(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	a.sessions["session"].log.subscriptionID = 1
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
	for _, record := range []json.RawMessage{origin, result} {
		feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": record})
	}
	for _, id := range []string{"first", "second"} {
		item := resultItem(id, "call", "origin")
		item.Item.Revision, item.Item.Status = 1, contracts.MuseItemStatusCompleted
		feed(t, a, contracts.MuseMethodItemCompleted, item)
	}
	messages := sink.Messages()
	require.Len(t, messages, 2)
	for _, message := range messages {
		assert.Contains(t, string(message.SupplementalContent), `"nativeResultUnavailable"`)
		assert.NotContains(t, string(message.SupplementalContent), `"id":"result"`)
	}
}

func TestMissingNativeLogCapabilityKeepsThePreviewAndReportsUnavailable(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	item := resultItem("item", "call", "origin")
	item.Item.Revision, item.Item.Status = 1, contracts.MuseItemStatusCompleted
	item.Item.VisibleOutput = "Native preview"
	original := feed(t, a, contracts.MuseMethodItemCompleted, item)
	requests := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(_ string, _ json.RawMessage) agenttest.RPCReply {
		requests++
		return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"ungranted log interface"}`)}
	}})
	require.NoError(t, a.subscribeLog("session", time.Second))
	assert.Zero(t, requests)
	require.Len(t, sink.Messages(), 1)
	assert.Equal(t, original, sink.Messages()[0].Content)
	assert.Contains(t, string(sink.Messages()[0].SupplementalContent), `"nativeResultUnavailable"`)
	assert.Contains(t, string(sink.Messages()[0].SupplementalContent), "rawLog")
}

func TestNativeLogAmbiguousActiveItemWithdrawsAnEarlierSupplement(t *testing.T) {
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	a.sessions["session"].log.subscriptionID = 1
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
	for _, record := range []json.RawMessage{origin, result} {
		feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": record})
	}
	first := resultItem("first", "call", "origin")
	first.Item.Revision, first.Item.Status = 1, contracts.MuseItemStatusCompleted
	first.Item.VisibleOutput = "Native preview"
	original := feed(t, a, contracts.MuseMethodItemCompleted, first)
	require.Contains(t, string(sink.Messages()[0].SupplementalContent), `"id":"result"`)
	second := resultItem("second", "call", "origin")
	second.Item.Revision, second.Item.Status = 1, contracts.MuseItemStatusInProgress
	feed(t, a, contracts.MuseMethodItemStarted, second)
	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.Equal(t, original, messages[0].Content)
	assert.Contains(t, string(messages[0].SupplementalContent), `"nativeResultUnavailable"`)
	assert.NotContains(t, string(messages[0].SupplementalContent), `"id":"result"`)
}

func TestWrongTypedOwnedNativeRecordWithdrawsAnEarlierSupplement(t *testing.T) {
	for _, tc := range []struct {
		name, kind, id, field, entries string
		sequence                       int
	}{
		{"call name", "assistant_tool_calls_committed", "origin", "tool_calls", `[{"call_id":"call","name":42,"args":"{}"}]`, 1},
		{"call arguments", "assistant_tool_calls_committed", "origin", "tool_calls", `[{"call_id":"call","name":"bash","args":false}]`, 1},
		{"result index", "tool_result_batch_committed", "result", "results", `[{"tool_call_index":"wrong","tool_call_id":"call","text":""}]`, 2},
		{"result text", "tool_result_batch_committed", "result", "results", `[{"tool_call_index":0,"tool_call_id":"call","text":42}]`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink := testAgent(t)
			sink.UpdateSessionID("session")
			a.sessions["session"].log.subscriptionID = 1
			origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
			result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
			for _, record := range []json.RawMessage{origin, result} {
				feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": record})
			}
			item := resultItem("item", "call", "origin")
			item.Item.Revision, item.Item.Status = 1, contracts.MuseItemStatusCompleted
			original := feed(t, a, contracts.MuseMethodItemCompleted, item)
			require.Contains(t, string(sink.Messages()[0].SupplementalContent), `"id":"result"`)
			identity := "message_id"
			if tc.kind == contracts.MuseLogEventToolResultBatchCommitted {
				identity = "batch_id"
			}
			invalid := logRecord(tc.id, tc.kind, identity, tc.sequence, `"`+tc.field+`":`+tc.entries)
			feed(t, a, contracts.MuseMethodLogRecord, map[string]any{"subscriptionId": 1, "record": invalid})
			require.Len(t, sink.Messages(), 1)
			assert.Equal(t, original, sink.Messages()[0].Content)
			assert.Contains(t, string(sink.Messages()[0].SupplementalContent), `"nativeResultUnavailable"`)
			assert.NotContains(t, string(sink.Messages()[0].SupplementalContent), `"id":"result"`)
		})
	}
}

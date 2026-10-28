package muse

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resultItem(id, call, origin string) itemParams {
	turn := "turn"
	return itemParams{SessionID: "session", SourceRange: nativeSourceRange{Stream: nativeStream{Kind: "session", ID: "session"}, First: nativePosition{ID: origin, Sequence: 1}}, Item: nativeItem{ID: id, Kind: "toolCall", CallID: call, TurnID: &turn, Tool: "bash", Args: `{"command":"exit 7"}`}}
}
func logRecord(id, kind, message string, sequence int, extra string) json.RawMessage {
	return json.RawMessage(`{"id":"` + id + `","sequence":` + jsonNumber(sequence) + `,"stream":{"kind":"session","id":"session"},"payload":{"kind":"run","run_id":"turn","event":{"kind":"` + kind + `","` + message + `":"batch",` + extra + `}}}`)
}
func jsonNumber(value int) string { raw, _ := json.Marshal(value); return string(raw) }

// Muse 1.4.13 retains ephemeral task deltas as positions without event content.
func omittedTaskRecord(id string, sequence int) json.RawMessage {
	return json.RawMessage(`{"retained_marker":"omitted_live_only","schema_version":1,"stream":{"kind":"session","id":"session"},"position":{"id":"` + id + `","sequence":` + jsonNumber(sequence) + `},"omitted_record":{"record_type":"status","durability":"ephemeral","payload_type":"runtime.session","payload_schema_version":1,"payload_kind":"task","omission_class":"task_tool_delta_v1"}}`)
}

func TestNativeLogPreservesOmittedPositionsWithoutUsingThemAsResults(t *testing.T) {
	t.Parallel()
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	marker := omittedTaskRecord("omitted", 2)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 3, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
	log := newNativeLog("session")
	for _, raw := range []json.RawMessage{origin, marker, result, marker} {
		require.NoError(t, log.add(raw))
	}
	assert.Equal(t, "omitted", log.sequences[2])
	assert.Equal(t, marker, log.records["omitted"].raw)
	item := resultItem("item", "call", "origin")
	association := log.match(item, []itemParams{item})
	require.Empty(t, association.reason)
	require.Len(t, association.records, 2)
	assert.Equal(t, origin, association.records[0])
	assert.Equal(t, result, association.records[1])
	item.SourceRange.First = nativePosition{ID: "omitted", Sequence: 2}
	association = log.match(item, []itemParams{item})
	assert.Empty(t, association.records, "a retained marker carries no tool evidence")
	assert.NotEmpty(t, association.reason)
}

func TestNativeLogRejectsMalformedOmissionMarkers(t *testing.T) {
	t.Parallel()
	for _, change := range []string{
		"missing discriminator", "null discriminator", "unknown discriminator", "wrong schema", "missing position", "empty ID", "zero sequence", "negative sequence", "fractional sequence", "overflowing sequence", "foreign stream", "foreign session", "missing metadata", "missing record type", "durable omission", "missing payload type", "zero payload schema", "missing payload kind", "missing omission class", "envelope ID", "envelope sequence", "event payload",
	} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			var marker map[string]any
			require.NoError(t, json.Unmarshal(omittedTaskRecord("omitted", 2), &marker))
			position := marker["position"].(map[string]any)
			stream := marker["stream"].(map[string]any)
			omitted := marker["omitted_record"].(map[string]any)
			switch change {
			case "missing discriminator":
				delete(marker, "retained_marker")
			case "null discriminator":
				marker["retained_marker"] = nil
			case "unknown discriminator":
				marker["retained_marker"] = "future"
			case "wrong schema":
				marker["schema_version"] = 2
			case "missing position":
				delete(marker, "position")
			case "empty ID":
				position["id"] = ""
			case "zero sequence":
				position["sequence"] = 0
			case "negative sequence":
				position["sequence"] = -1
			case "fractional sequence":
				position["sequence"] = 1.5
			case "overflowing sequence":
				position["sequence"] = 1e40
			case "foreign stream":
				stream["kind"] = "session.live"
			case "foreign session":
				stream["id"] = "foreign"
			case "missing metadata":
				delete(marker, "omitted_record")
			case "missing record type":
				delete(omitted, "record_type")
			case "durable omission":
				omitted["durability"] = "durable"
			case "missing payload type":
				delete(omitted, "payload_type")
			case "zero payload schema":
				omitted["payload_schema_version"] = 0
			case "missing payload kind":
				delete(omitted, "payload_kind")
			case "missing omission class":
				delete(omitted, "omission_class")
			case "envelope ID":
				marker["id"] = "competing"
			case "envelope sequence":
				marker["sequence"] = 2
			case "event payload":
				marker["payload"] = map[string]any{"kind": "run", "run_id": "turn"}
			}
			raw, err := json.Marshal(marker)
			require.NoError(t, err)
			_, err = decodeNativeRecord(raw, "session")
			require.Error(t, err)
		})
	}
}
func TestNativeLogMatchesAnExactOriginAndResultBatch(t *testing.T) {
	t.Parallel()
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
	log := newNativeLog("session")
	require.NoError(t, log.add(result))
	require.NoError(t, log.add(origin))
	association := log.match(resultItem("item", "call", "origin"), []itemParams{resultItem("item", "call", "origin")})
	require.Empty(t, association.reason)
	require.Len(t, association.records, 2)
	assert.JSONEq(t, string(origin), string(association.records[0]))
	assert.JSONEq(t, string(result), string(association.records[1]))
	require.NoError(t, log.add(origin))
}
func TestNativeLogRefusesAmbiguousItemsAndConflictingRecords(t *testing.T) {
	t.Parallel()
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	log := newNativeLog("session")
	require.NoError(t, log.add(origin))
	first, second := resultItem("first", "call", "origin"), resultItem("second", "call", "origin")
	association := log.match(first, []itemParams{first, second})
	assert.Empty(t, association.records)
	assert.NotEmpty(t, association.reason)
	conflicting := logRecord("origin", "assistant_tool_calls_committed", "message_id", 2, `"tool_calls":[]`)
	require.Error(t, log.add(conflicting))
	association = log.match(first, []itemParams{first})
	assert.Empty(t, association.records)
	assert.NotEmpty(t, association.reason)
}
func TestNativeLogRejectsForeignSessions(t *testing.T) {
	t.Parallel()
	log := newNativeLog("other")
	require.Error(t, log.add(logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[]`)))
}

func TestNativeLogRequiresTheExactOriginPosition(t *testing.T) {
	t.Parallel()
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":"{\"exit_code\":7}"}]`)
	log := newNativeLog("session")
	require.NoError(t, log.add(origin))
	require.NoError(t, log.add(result))
	item := resultItem("item", "call", "origin")
	item.SourceRange.First.Sequence = 99
	association := log.match(item, []itemParams{item})
	assert.Empty(t, association.records)
	assert.Contains(t, association.reason, "position")
}

func TestNativeLogRejectsMalformedCallAndResultArrays(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, kind, field, members string
	}{
		{"null calls", "assistant_tool_calls_committed", "tool_calls", "null"},
		{"null call", "assistant_tool_calls_committed", "tool_calls", `[null,{"call_id":"call","name":"bash","args":"{}"}]`},
		{"absent call ID", "assistant_tool_calls_committed", "tool_calls", `[{"name":"bash","args":"{}"}]`},
		{"absent call name", "assistant_tool_calls_committed", "tool_calls", `[{"call_id":"call","args":"{}"}]`},
		{"absent arguments", "assistant_tool_calls_committed", "tool_calls", `[{"call_id":"call","name":"bash"}]`},
		{"null arguments", "assistant_tool_calls_committed", "tool_calls", `[{"call_id":"call","name":"bash","args":null}]`},
		{"null results", "tool_result_batch_committed", "results", "null"},
		{"null result", "tool_result_batch_committed", "results", `[null,{"tool_call_index":0,"tool_call_id":"call","text":""}]`},
		{"absent result index", "tool_result_batch_committed", "results", `[{"tool_call_id":"call","text":""}]`},
		{"negative result index", "tool_result_batch_committed", "results", `[{"tool_call_index":-1,"tool_call_id":"call","text":""}]`},
		{"absent result ID", "tool_result_batch_committed", "results", `[{"tool_call_index":0,"text":""}]`},
		{"absent result text", "tool_result_batch_committed", "results", `[{"tool_call_index":0,"tool_call_id":"call"}]`},
		{"null result text", "tool_result_batch_committed", "results", `[{"tool_call_index":0,"tool_call_id":"call","text":null}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log := newNativeLog("session")
			identity := "message_id"
			if tc.kind == "tool_result_batch_committed" {
				identity = "batch_id"
			}
			raw := logRecord("malformed", tc.kind, identity, 1, `"`+tc.field+`":`+tc.members)
			require.Error(t, log.add(raw))
			assert.Empty(t, log.records)
		})
	}
}

func TestNativeLogKeepsTheOriginalCallIndexAndEmptyResult(t *testing.T) {
	t.Parallel()
	log := newNativeLog("session")
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"other","name":"bash","args":"{}"},{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":1,"tool_call_id":"call","text":""}]`)
	require.NoError(t, log.add(origin))
	require.NoError(t, log.add(result))
	item := resultItem("item", "call", "origin")
	association := log.match(item, []itemParams{item})
	require.Empty(t, association.reason)
	require.Len(t, association.records, 2)
	assert.Equal(t, []byte(origin), []byte(association.records[0]))
	assert.Equal(t, []byte(result), []byte(association.records[1]))
}

func TestNativeLogKeepsDistinctOriginPositionsSeparate(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"sequence", "stream ID", "stream kind"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			log := newNativeLog("session")
			origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
			result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":""}]`)
			require.NoError(t, log.add(origin))
			require.NoError(t, log.add(result))
			item := resultItem("item", "call", "origin")
			other := resultItem("other", "call", "origin")
			switch change {
			case "sequence":
				other.SourceRange.First.Sequence = 99
			case "stream ID":
				other.SourceRange.Stream.ID = "foreign"
			case "stream kind":
				other.SourceRange.Stream.Kind = "run"
			}
			association := log.match(item, []itemParams{item, other})
			require.Empty(t, association.reason)
			require.Len(t, association.records, 2)
			assert.Equal(t, []byte(origin), []byte(association.records[0]))
			assert.Equal(t, []byte(result), []byte(association.records[1]))
		})
	}
}

func TestNativeLogAssociationDistinguishesPendingAndInvalidRecords(t *testing.T) {
	t.Parallel()
	origin := logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
	result := logRecord("result", "tool_result_batch_committed", "batch_id", 2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":""}]`)
	item := resultItem("item", "call", "origin")
	log := newNativeLog("session")
	association := log.match(item, []itemParams{item})
	assert.True(t, association.pending)
	assert.Empty(t, association.records)
	require.NoError(t, log.add(origin))
	association = log.match(item, []itemParams{item})
	assert.True(t, association.pending)
	assert.Empty(t, association.records)
	require.NoError(t, log.add(result))
	association = log.match(item, []itemParams{item})
	assert.False(t, association.pending)
	assert.Empty(t, association.reason)
	require.Len(t, association.records, 2)
	require.Error(t, log.add(logRecord("origin", "assistant_tool_calls_committed", "message_id", 1, `"tool_calls":[null]`)))
	association = log.match(item, []itemParams{item})
	assert.False(t, association.pending)
	assert.NotEmpty(t, association.reason)
	assert.Empty(t, association.records)
}

func BenchmarkNativeLogAssociateAllItems(b *testing.B) {
	for _, size := range []int{32, 256, 1024} {
		b.Run(jsonNumber(size), func(b *testing.B) {
			log := newNativeLog("session")
			items := make([]itemParams, size)
			for index := range items {
				suffix := jsonNumber(index)
				batch := "batch-" + suffix
				origin := logRecord("origin-"+suffix, "assistant_tool_calls_committed", "message_id", index*2+1, `"tool_calls":[{"call_id":"call","name":"bash","args":"{\"command\":\"exit 7\"}"}]`)
				result := logRecord("result-"+suffix, "tool_result_batch_committed", "batch_id", index*2+2, `"results":[{"tool_call_index":0,"tool_call_id":"call","text":""}]`)
				var originValue, resultValue map[string]any
				if err := json.Unmarshal(origin, &originValue); err != nil {
					b.Fatal(err)
				}
				if err := json.Unmarshal(result, &resultValue); err != nil {
					b.Fatal(err)
				}
				originValue["payload"].(map[string]any)["event"].(map[string]any)["message_id"] = batch
				resultValue["payload"].(map[string]any)["event"].(map[string]any)["batch_id"] = batch
				for _, value := range []map[string]any{originValue, resultValue} {
					raw, err := json.Marshal(value)
					if err != nil {
						b.Fatal(err)
					}
					if err := log.add(raw); err != nil {
						b.Fatal(err)
					}
				}
				items[index] = resultItem("item-"+suffix, "call", "origin-"+suffix)
				items[index].SourceRange.First.Sequence = int64(index*2 + 1)
			}
			indexed := &nativeItemIndex{}
			source := make(map[string]*itemState, len(items))
			for _, item := range items {
				source[item.Item.ID] = &itemState{params: item}
			}
			indexed.rebuild(source)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				for _, item := range items {
					association := log.match(item, indexed.candidates(item))
					if association.reason != "" || len(association.records) != 2 {
						b.Fatalf("The native association failed: %s", association.reason)
					}
				}
			}
		})
	}
}

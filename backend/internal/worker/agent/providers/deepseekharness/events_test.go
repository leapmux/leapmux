package deepseekharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOneShotChildFollowUsesItsExactNativeMode(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { assert.NoError(t, connection.CloseNow()) }()
		_, raw, err := connection.Read(r.Context())
		if !assert.NoError(t, err) {
			return
		}
		var request struct {
			StreamID string `json:"streamId"`
			Payload  struct {
				Args struct {
					Request struct {
						Address struct {
							Kind   string `json:"kind"`
							Parent string `json:"parentSessionId"`
							Child  string `json:"childSessionId"`
							Mode   string `json:"mode"`
						} `json:"address"`
					} `json:"request"`
				} `json:"args"`
			} `json:"payload"`
		}
		if !assert.NoError(t, json.Unmarshal(raw, &request)) {
			return
		}
		address := request.Payload.Args.Request.Address
		assert.Equal(t, "subagent", address.Kind)
		assert.Equal(t, "native-root", address.Parent)
		assert.Equal(t, "native-one-shot", address.Child)
		frame := map[string]any{"type": "item", "streamId": request.StreamID, "value": map[string]any{"type": "snapshot", "cursor": -1, "records": []any{}, "hasMore": false, "projections": map[string]any{"values": map[string]any{}}}}
		if address.Mode != "one-shot" {
			frame = map[string]any{"type": "error", "streamId": request.StreamID, "error": map[string]any{"code": "subagent/unauthorized", "message": "subagent mode does not match the supplied address", "details": map[string]any{"childSessionId": "native-one-shot"}}}
		}
		encoded, err := json.Marshal(frame)
		if !assert.NoError(t, err) {
			return
		}
		assert.NoError(t, connection.Write(r.Context(), websocket.MessageText, encoded))
	}))
	defer server.Close()
	connection, _, err := websocket.Dial(context.Background(), strings.Replace(server.URL, "http:", "ws:", 1), nil)
	require.NoError(t, err)
	defer func() { assert.NoError(t, connection.CloseNow()) }()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.conn = connection
	require.NoError(t, a.beginChild(a.streams["root"], []byte(`{"version":0,"childId":"native-one-shot","childCreatedAt":1000,"mode":"one-shot","label":"Native workflow child"}`)))
	_, response, err := connection.Read(a.Context())
	require.NoError(t, err)
	var frame remoteMuxFrame
	require.NoError(t, json.Unmarshal(response, &frame))
	assert.Equal(t, "item", frame.Type, "the native host must accept the owned one-shot child address")
	assert.Nil(t, frame.Error)
}

func TestChildSnapshotReadsEveryNativeHistoryPageBeforeItBecomesReady(t *testing.T) {
	t.Parallel()
	var pages atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages.Add(1)
		assert.Equal(t, "/api/session/page", r.URL.Path)
		var request struct {
			ID      string `json:"rpcId"`
			Payload struct {
				Args struct {
					Request struct {
						Through int64 `json:"throughSeq"`
						Before  int64 `json:"beforeSeq"`
					} `json:"request"`
				} `json:"args"`
			} `json:"payload"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		assert.Equal(t, int64(54), request.Payload.Args.Request.Through)
		assert.Equal(t, int64(51), request.Payload.Args.Request.Before)
		value := map[string]any{"hasMore": false, "records": []any{
			map[string]any{"type": "event", "event": map[string]any{"type": "user/message", "seq": 8, "time": 1000, "data": map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "The first exact child task."}}}}},
			map[string]any{"type": "event", "event": map[string]any{"type": "assistant/message", "seq": 15, "time": 1001, "data": map[string]any{"message": map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "text", "text": "The earliest exact child answer."}}}}}},
		}}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": value}}))
	}))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.rpc = remoteRPC{endpoint: endpoint}
	stream := &sessionStream{sessionID: "native-child", parentSessionID: "native-root", childAgentID: "stored-child", lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
	stream.address = sessionAddress{Kind: "subagent", ParentSessionID: "native-root", ChildSessionID: "native-child", Mode: "continuable"}
	a.streams["child"] = stream
	raw := []byte(`{"type":"item","streamId":"child","value":{"type":"snapshot","cursor":54,"hasMore":true,"records":[{"type":"event","event":{"type":"assistant/message","seq":51,"time":2000,"data":{"message":{"role":"assistant","content":[{"type":"text","text":"The latest exact child answer."}]}}}}],"projections":{"values":{"subagent":{"mode":"continuable","label":"The native child","seq":0}}}}}`)
	require.NoError(t, a.handleFrame(raw))
	assert.Equal(t, int32(1), pages.Load(), "a native truncated snapshot requires its older page")
	child := sink.Child("stored-child")
	messages := child.Messages()
	require.Len(t, messages, 3)
	assert.Contains(t, string(messages[0].Content), "The first exact child task.")
	assert.Contains(t, string(messages[1].Content), "The earliest exact child answer.")
	assert.Contains(t, string(messages[2].Content), "The latest exact child answer.")
	select {
	case <-stream.ready:
	default:
		t.Fatal("the complete child snapshot did not become ready")
	}
}

func TestHistoryRecordsRefusesInvalidNativePages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		raw     string
		hasMore bool
	}{
		{name: "absent event", raw: `{}`},
		{name: "absent sequence", raw: `{"type":"assistant/message","data":{}}`},
		{name: "negative sequence", raw: `{"type":"assistant/message","seq":-1,"data":{}}`},
		{name: "past the snapshot", raw: `{"type":"assistant/message","seq":55,"data":{}}`},
		{name: "unsafe native integer", raw: `{"type":"assistant/message","seq":9007199254740992,"data":{}}`},
		{name: "zero cannot have an older page", raw: `{"type":"assistant/message","seq":0,"data":{}}`, hasMore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newOfflineAgent(t, &agenttest.Sink{})
			_, err := a.historyRecords(a.streams["root"], 54, historyPage{Records: []historyRecord{{Event: json.RawMessage(tc.raw)}}, HasMore: tc.hasMore})
			require.Error(t, err)
		})
	}
	a := newOfflineAgent(t, &agenttest.Sink{})
	_, err := a.historyRecords(a.streams["root"], 54, historyPage{HasMore: true})
	require.ErrorContains(t, err, "no earlier page cursor")
	for _, sequences := range [][]int64{{9, 9}, {9, 8}} {
		page := historyPage{}
		for _, sequence := range sequences {
			raw, err := json.Marshal(map[string]any{"type": "assistant/message", "seq": sequence, "data": map[string]any{}})
			require.NoError(t, err)
			page.Records = append(page.Records, historyRecord{Event: raw})
		}
		_, err := a.historyRecords(a.streams["root"], 54, page)
		require.ErrorContains(t, err, "invalid native sequence")
	}
	empty, err := a.historyRecords(a.streams["root"], -1, historyPage{})
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestHistoryRecordsStopsWhenAnOlderPageDoesNotMoveItsCursor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		value map[string]any
	}{
		{name: "empty earlier page", value: map[string]any{"records": []any{}, "hasMore": false}},
		{name: "repeated earlier page", value: map[string]any{"records": []any{map[string]any{"event": map[string]any{"type": "assistant/message", "seq": 51, "data": map[string]any{}}}}, "hasMore": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var request struct {
					ID string `json:"rpcId"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
					return
				}
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": tc.value}}))
			}))
			defer server.Close()
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			defer endpoint.Close()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.rpc = remoteRPC{endpoint: endpoint}
			_, err = a.historyRecords(a.streams["root"], 54, historyPage{Records: []historyRecord{{Event: json.RawMessage(`{"type":"assistant/message","seq":51,"data":{}}`)}}, HasMore: true})
			require.Error(t, err)
			assert.Equal(t, int32(1), calls.Load(), "a repeated or empty native page must not start a retry loop")
		})
	}
}

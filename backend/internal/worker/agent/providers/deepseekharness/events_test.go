package deepseekharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/internal/util/testutil"
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
			map[string]any{"type": "event", "event": map[string]any{"type": "user/message", "seq": 8, "time": 1000, "data": map[string]any{"role": "user", "source": map[string]any{"kind": "user"}, "content": []any{map[string]string{"type": "text", "text": "The first exact child task."}}}}},
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

// followOpen is one `session/follow` request that the fake native host received.
type followOpen struct {
	streamID string
	address  sessionAddress
}

// serveFollows answers each `session/follow` open of its client. answer states the frame of one
// attempt (1-based) for the stream of that open. The client reads the frames from the returned connection.
func serveFollows(t *testing.T, answer func(attempt int, streamID string) map[string]any) (*websocket.Conn, <-chan followOpen) {
	t.Helper()
	opens := make(chan followOpen, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = connection.CloseNow() }()
		for attempt := 1; ; attempt++ {
			_, raw, err := connection.Read(r.Context())
			if err != nil {
				return
			}
			var request struct {
				StreamID string `json:"streamId"`
				Payload  struct {
					Args struct {
						Request struct {
							Address sessionAddress `json:"address"`
						} `json:"request"`
					} `json:"args"`
				} `json:"payload"`
			}
			if !assert.NoError(t, json.Unmarshal(raw, &request)) {
				return
			}
			opens <- followOpen{streamID: request.StreamID, address: request.Payload.Args.Request.Address}
			encoded, err := json.Marshal(answer(attempt, request.StreamID))
			if !assert.NoError(t, err) {
				return
			}
			if err := connection.Write(r.Context(), websocket.MessageText, encoded); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	connection, _, err := websocket.Dial(testutil.DeadlineContext(t), strings.Replace(server.URL, "http:", "ws:", 1), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.CloseNow() })
	return connection, opens
}

func descriptorNotWritten(streamID string) map[string]any {
	return map[string]any{"type": "error", "streamId": streamID, "error": map[string]any{"code": "subagent/catalog-diagnostic", "message": "subagent descriptor is corrupt", "details": map[string]any{"parentSessionId": "native-root", "childSessionId": "native-one-shot", "reason": "corrupt"}}}
}

func emptySnapshot(streamID string) map[string]any {
	return map[string]any{"type": "item", "streamId": streamID, "value": map[string]any{"type": "snapshot", "cursor": 5, "records": []any{}, "hasMore": false, "projections": map[string]any{"values": map[string]any{"subagent": map[string]any{"mode": "one-shot", "seq": 3}}}}}
}

func readFrame(t *testing.T, ctx context.Context, connection *websocket.Conn) []byte {
	t.Helper()
	_, raw, err := connection.Read(ctx)
	require.NoError(t, err)
	return raw
}

const oneShotCatalog = `{"version":0,"childId":"native-one-shot","childCreatedAt":1000,"mode":"one-shot"}`

// Source: `@deepseek-ai/dsh` 0.2.0-rc.2, probed on a live workflow. The native process announces a
// one-shot workflow child (`subagent/catalog`) before it writes the descriptor of that child, so a
// follow at the announcement fails with "subagent descriptor is corrupt" (reason "corrupt", dsh-api-session-controller
// validateAddress) and a follow about 26 ms later succeeds. The state is transient, and the failure
// must not stop the agent.
func TestChildFollowRetriesWhileTheNativeDescriptorIsNotWritten(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	connection, opens := serveFollows(t, func(attempt int, streamID string) map[string]any {
		if attempt == 1 {
			return descriptorNotWritten(streamID)
		}
		return emptySnapshot(streamID)
	})
	a := newOfflineAgentOn(t, &agenttest.Sink{}, clock)
	a.conn = connection
	require.NoError(t, a.beginChild(a.streams["root"], []byte(oneShotCatalog)))
	first := <-opens
	require.NoError(t, a.handleFrame(readFrame(t, ctx, connection)), "an unwritten descriptor is a transient native state")

	delay, waiter := clock.AdvanceNext()
	waiter.MustWait(ctx)
	assert.Positive(t, delay, "the retry waits before it asks the native host again")
	second := <-opens
	assert.NotEqual(t, first.streamID, second.streamID, "a failed native stream cannot open again")
	assert.Equal(t, first.address, second.address, "the retry follows the same exact child")
	assert.NotContains(t, a.streams, first.streamID)
	stream := a.streams[second.streamID]
	require.NotNil(t, stream)

	require.NoError(t, a.handleFrame(readFrame(t, ctx, connection)))
	select {
	case <-stream.ready:
	default:
		t.Fatal("the retried follow did not deliver its snapshot")
	}
	assert.Nil(t, a.streamFailure, "the agent keeps running")
}

func TestChildFollowGivesUpWhenTheNativeDescriptorStaysUnwritten(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	connection, opens := serveFollows(t, func(_ int, streamID string) map[string]any { return descriptorNotWritten(streamID) })
	a := newOfflineAgentOn(t, &agenttest.Sink{}, clock)
	a.conn = connection
	require.NoError(t, a.beginChild(a.streams["root"], []byte(oneShotCatalog)))
	var delays []time.Duration
	var failure error
	for failure == nil {
		<-opens
		failure = a.handleFrame(readFrame(t, ctx, connection))
		if failure == nil {
			delay, waiter := clock.AdvanceNext()
			waiter.MustWait(ctx)
			delays = append(delays, delay)
		}
	}
	var native *remoteFailure
	require.ErrorAs(t, failure, &native)
	assert.Equal(t, "subagent/catalog-diagnostic", native.Code)
	assert.GreaterOrEqual(t, len(delays), 3, "a short race needs several tries")
	var total time.Duration
	for index, delay := range delays {
		assert.Positive(t, delay)
		if index > 0 {
			assert.GreaterOrEqual(t, delay, delays[index-1], "the retry delay does not shrink")
		}
		total += delay
	}
	assert.GreaterOrEqual(t, total, time.Second, "the retry outlasts a slow native write")
	assert.LessOrEqual(t, total, 30*time.Second, "a descriptor that never appears fails the stream")
}

func TestChildFollowDoesNotRetryAnotherFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		address sessionAddress
		child   string
		code    string
		reason  string
	}{
		{"another code of a child", sessionAddress{Kind: "subagent", ChildSessionID: "native-child"}, "stored-child", "subagent/unauthorized", ""},
		{"a descriptor that the native host cannot supply", sessionAddress{Kind: "subagent", ChildSessionID: "native-child"}, "stored-child", "subagent/catalog-diagnostic", "unsupported"},
		{"the root session", sessionAddress{Kind: "session", SessionID: "native-root"}, "", "subagent/catalog-diagnostic", "corrupt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			a := newOfflineAgentOn(t, &agenttest.Sink{}, clock)
			a.streams["followed"] = &sessionStream{address: tc.address, sessionID: "native-child", childAgentID: tc.child, lastSeq: -1, pending: map[string][]byte{}, ready: make(chan struct{})}
			raw, err := json.Marshal(map[string]any{"type": "error", "streamId": "followed", "error": map[string]any{"code": tc.code, "message": "native failure", "details": map[string]any{"reason": tc.reason}}})
			require.NoError(t, err)
			var native *remoteFailure
			require.ErrorAs(t, a.handleFrame(raw), &native)
			assert.Equal(t, tc.code, native.Code)
			_, scheduled := clock.Peek()
			assert.False(t, scheduled, "no retry is scheduled")
		})
	}
}

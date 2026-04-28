package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// sessionInfoCapturingWriter records every agent_session_info broadcast so
// individual fields (and the count of broadcasts) can be asserted in
// BroadcastSessionInfo dedup tests.
type sessionInfoCapturingWriter struct {
	channelID string
	mu        sync.Mutex
	infos     []map[string]interface{}
}

func (m *sessionInfoCapturingWriter) SendResponse(_ *leapmuxv1.InnerRpcResponse) error { return nil }
func (m *sessionInfoCapturingWriter) SendError(_ int32, _ string) error                { return nil }
func (m *sessionInfoCapturingWriter) SendStream(s *leapmuxv1.InnerStreamMessage) error {
	resp := &leapmuxv1.WatchEventsResponse{}
	if err := proto.Unmarshal(s.GetPayload(), resp); err != nil {
		return nil
	}
	msg := resp.GetAgentEvent().GetAgentMessage()
	if msg == nil || msg.GetSeq() != -1 {
		return nil
	}
	raw, err := msgcodec.Decompress(msg.GetContent(), msg.GetContentCompression())
	if err != nil {
		return nil
	}
	var env struct {
		Type string                 `json:"type"`
		Info map[string]interface{} `json:"info"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	if env.Type != "agent_session_info" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.infos = append(m.infos, env.Info)
	return nil
}

func (m *sessionInfoCapturingWriter) ChannelID() string { return m.channelID }

func (m *sessionInfoCapturingWriter) snapshot() []map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]interface{}, len(m.infos))
	copy(out, m.infos)
	return out
}

func newSessionInfoFixture(t *testing.T) (agent.OutputSink, *sessionInfoCapturingWriter) {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		Model:         "gpt-5",
		Effort:        "high",
	}))

	mock := &sessionInfoCapturingWriter{channelID: "ch-1"}
	w := &EventWatcher{ChannelID: "ch-1", Sender: channel.NewSender(mock)}
	svc.Watchers.WatchAgent("agent-1", w)

	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	return sink, mock
}

// TestBroadcastSessionInfo_FirstCallShipsEverything: from a fresh sink,
// every key is "new" (never seen before) so the broadcast carries the
// full input map.
func TestBroadcastSessionInfo_FirstCallShipsEverything(t *testing.T) {
	sink, mock := newSessionInfoFixture(t)

	sink.BroadcastSessionInfo(map[string]interface{}{"a": float64(1), "b": float64(2)})

	infos := mock.snapshot()
	require.Len(t, infos, 1)
	assert.Equal(t, float64(1), infos[0]["a"])
	assert.Equal(t, float64(2), infos[0]["b"])
}

// TestBroadcastSessionInfo_IdenticalRepeatIsDeduped: a second call with
// byte-identical content must not produce a wire event.
func TestBroadcastSessionInfo_IdenticalRepeatIsDeduped(t *testing.T) {
	sink, mock := newSessionInfoFixture(t)

	payload := map[string]interface{}{"a": float64(1), "b": float64(2)}
	sink.BroadcastSessionInfo(payload)
	sink.BroadcastSessionInfo(map[string]interface{}{"a": float64(1), "b": float64(2)})

	assert.Len(t, mock.snapshot(), 1, "second identical broadcast should be deduped")
}

// TestBroadcastSessionInfo_PerKeyDelta: when only one key changed, only
// that key crosses the wire — unchanged keys must be filtered out so
// reactive consumers aren't woken for nothing.
func TestBroadcastSessionInfo_PerKeyDelta(t *testing.T) {
	sink, mock := newSessionInfoFixture(t)

	sink.BroadcastSessionInfo(map[string]interface{}{"a": float64(1), "b": float64(2)})
	sink.BroadcastSessionInfo(map[string]interface{}{"a": float64(1), "b": float64(3)})

	infos := mock.snapshot()
	require.Len(t, infos, 2)
	// First call ships both keys.
	assert.Equal(t, float64(1), infos[0]["a"])
	assert.Equal(t, float64(2), infos[0]["b"])
	// Second call ships only the changed key.
	_, hasA := infos[1]["a"]
	assert.False(t, hasA, "unchanged key 'a' must not appear in the delta")
	assert.Equal(t, float64(3), infos[1]["b"])
}

// TestBroadcastSessionInfo_NewKeyPasses: a key that hasn't been seen
// before is treated as a change and shipped.
func TestBroadcastSessionInfo_NewKeyPasses(t *testing.T) {
	sink, mock := newSessionInfoFixture(t)

	sink.BroadcastSessionInfo(map[string]interface{}{"a": float64(1)})
	sink.BroadcastSessionInfo(map[string]interface{}{"c": float64(4)})

	infos := mock.snapshot()
	require.Len(t, infos, 2)
	assert.Equal(t, float64(1), infos[0]["a"])
	assert.Equal(t, float64(4), infos[1]["c"])
	_, hasA := infos[1]["a"]
	assert.False(t, hasA, "key absent from the new payload must not be re-shipped")
}

// TestBroadcastSessionInfo_NestedMapDedup: nested maps (context_usage,
// rate_limits) compare via reflect.DeepEqual; identical nested content
// is deduped.
func TestBroadcastSessionInfo_NestedMapDedup(t *testing.T) {
	sink, mock := newSessionInfoFixture(t)

	usage := map[string]interface{}{"tokens": float64(100), "context_window": float64(1000)}
	sink.BroadcastSessionInfo(map[string]interface{}{"context_usage": usage})
	sink.BroadcastSessionInfo(map[string]interface{}{
		"context_usage": map[string]interface{}{"tokens": float64(100), "context_window": float64(1000)},
	})

	assert.Len(t, mock.snapshot(), 1, "byte-identical nested map should be deduped")
}

// TestBroadcastSessionInfo_NestedMapChangeShipsWholeSubmap: any change
// inside a nested map ships the full sub-map. We don't dedup recursively
// because the frontend store merges by top-level key.
func TestBroadcastSessionInfo_NestedMapChangeShipsWholeSubmap(t *testing.T) {
	sink, mock := newSessionInfoFixture(t)

	sink.BroadcastSessionInfo(map[string]interface{}{
		"context_usage": map[string]interface{}{"tokens": float64(100), "context_window": float64(1000)},
	})
	sink.BroadcastSessionInfo(map[string]interface{}{
		"context_usage": map[string]interface{}{"tokens": float64(200), "context_window": float64(1000)},
	})

	infos := mock.snapshot()
	require.Len(t, infos, 2)
	got, ok := infos[1]["context_usage"].(map[string]interface{})
	require.True(t, ok, "context_usage must ship as a sub-map even when only one nested key changed")
	assert.Equal(t, float64(200), got["tokens"])
	assert.Equal(t, float64(1000), got["context_window"])
}

// TestBroadcastSessionInfo_EmptyInputDoesNothing: an empty info map is
// a no-op — neither comparison nor broadcast.
func TestBroadcastSessionInfo_EmptyInputDoesNothing(t *testing.T) {
	sink, mock := newSessionInfoFixture(t)

	sink.BroadcastSessionInfo(map[string]interface{}{})
	sink.BroadcastSessionInfo(nil)

	assert.Empty(t, mock.snapshot(), "empty/nil session info must not broadcast")
}

// TestBroadcastSessionInfo_ValueTypeChangeShips: a value whose type
// changed (e.g. number → string) is treated as a change. Defensive: if
// a provider mistakenly switches encodings we want the frontend to see
// the new shape rather than silently keep the old one.
func TestBroadcastSessionInfo_ValueTypeChangeShips(t *testing.T) {
	sink, mock := newSessionInfoFixture(t)

	sink.BroadcastSessionInfo(map[string]interface{}{"a": float64(1)})
	sink.BroadcastSessionInfo(map[string]interface{}{"a": "one"})

	infos := mock.snapshot()
	require.Len(t, infos, 2)
	assert.Equal(t, float64(1), infos[0]["a"])
	assert.Equal(t, "one", infos[1]["a"])
}

// TestBroadcastSessionInfo_ConcurrentCallsAreRaceFree drives many
// goroutines through the same sink under -race. The cache may produce
// either 1 or 2 broadcasts depending on interleaving (last-writer-wins
// for duplicate payloads), but the implementation must not panic or
// produce a data race.
func TestBroadcastSessionInfo_ConcurrentCallsAreRaceFree(t *testing.T) {
	sink, _ := newSessionInfoFixture(t)

	const concurrency = 16
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			sink.BroadcastSessionInfo(map[string]interface{}{"a": float64(1), "b": float64(2)})
		}()
	}
	wg.Wait()
}

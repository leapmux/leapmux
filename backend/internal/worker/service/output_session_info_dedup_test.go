package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
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

func (m *sessionInfoCapturingWriter) ChannelID() string   { return m.channelID }
func (*sessionInfoCapturingWriter) MaxPayloadBudget() int { return 0 }
func (*sessionInfoCapturingWriter) BindStream(channel.StreamController) (func(), bool) {
	return func() {}, false
}

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
	svc, _, _ := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:  "gpt-5",
			agent.OptionIDEffort: "high",
		}),
	}))

	mock := &sessionInfoCapturingWriter{channelID: "ch-1"}
	registerAgentWatch(svc, "ch-1", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, mock)

	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	return sink, mock
}

// TestBroadcastSessionInfo_FirstCallShipsEverything: from a fresh sink,
// every key is "new" (never seen before) so the broadcast carries the
// full input map.
func TestBroadcastSessionInfo_FirstCallShipsEverything(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	sink, mock := newSessionInfoFixture(t)

	sink.BroadcastSessionInfo(map[string]interface{}{"a": float64(1)})
	sink.BroadcastSessionInfo(map[string]interface{}{"a": "one"})

	infos := mock.snapshot()
	require.Len(t, infos, 2)
	assert.Equal(t, float64(1), infos[0]["a"])
	assert.Equal(t, "one", infos[1]["a"])
}

// TestBroadcastSessionInfo_ThinkingTokensNeverDeduped: the per-turn
// thinking_tokens estimate is exempt from the per-key dedup. The frontend clears
// its counter at several boundaries the worker can't all observe (turn end, each
// interleaved thinking phase, a pause for input), so a re-broadcast of an
// unchanged estimate must still ship -- otherwise the cleared counter would stay
// hidden until a strictly different value arrived. Other keys stay deduped.
func TestBroadcastSessionInfo_ThinkingTokensNeverDeduped(t *testing.T) {
	t.Parallel()

	sink, mock := newSessionInfoFixture(t)

	// Two byte-identical thinking_tokens broadcasts both ship -- no dedup.
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyThinkingTokens: int64(230)})
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyThinkingTokens: int64(230)})
	require.Len(t, mock.snapshot(), 2, "an equal thinking_tokens repeat re-ships (exempt from dedup)")

	// A non-thinking key is still deduped: an unchanged repeat is dropped.
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyTotalCostUsd: float64(0.5)})
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyTotalCostUsd: float64(0.5)})
	assert.Len(t, mock.snapshot(), 3, "a non-thinking key remains deduped")

	// In a mixed payload, only the non-thinking key dedups: an unchanged
	// thinking_tokens alongside an unchanged cost still ships (carrying just the
	// estimate), and the cost is filtered out as a no-op delta.
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyThinkingTokens: int64(230), contracts.SessionInfoKeyTotalCostUsd: float64(0.5)})
	infos := mock.snapshot()
	require.Len(t, infos, 4, "a mixed payload re-ships because thinking_tokens is never deduped")
	// The capturing writer round-trips through JSON, so the count returns as float64.
	assert.Equal(t, float64(230), infos[3][contracts.SessionInfoKeyThinkingTokens])
	_, hasCost := infos[3][contracts.SessionInfoKeyTotalCostUsd]
	assert.False(t, hasCost, "the unchanged cost is still deduped out of the mixed payload")
}

// TestBroadcastSessionInfo_RunningToolNeverDeduped: running_tool joins
// thinking_tokens in the exemption, and for the same reason. The frontend drops
// a span's entry when the tool's result row lands and at every turn/agent
// boundary -- none of which the worker observes -- so an identical repeat must
// still ship. A resolved-retry update is the case this exemption exists for: its
// payload can equal the previous one byte for byte, and a dedup would leave the
// badge stuck on the last attempt for as long as the tool runs.
func TestBroadcastSessionInfo_RunningToolNeverDeduped(t *testing.T) {
	t.Parallel()

	sink, mock := newSessionInfoFixture(t)

	running := func() map[string]interface{} {
		return map[string]interface{}{
			contracts.SessionInfoKeyRunningTool: map[string]interface{}{
				contracts.RunningToolFieldSpanId:         "toolu_A",
				contracts.RunningToolFieldElapsedSeconds: int64(30),
			},
		}
	}
	sink.BroadcastSessionInfo(running())
	sink.BroadcastSessionInfo(running())
	require.Len(t, mock.snapshot(), 2, "an equal running_tool repeat re-ships (exempt from dedup)")

	// A nested value change ships the whole sub-map, as it does for every key.
	sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyRunningTool: map[string]interface{}{
			contracts.RunningToolFieldSpanId:         "toolu_A",
			contracts.RunningToolFieldElapsedSeconds: int64(60),
		},
	})
	infos := mock.snapshot()
	require.Len(t, infos, 3)
	update, ok := infos[2][contracts.SessionInfoKeyRunningTool].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(60), update[contracts.RunningToolFieldElapsedSeconds])
}

// TestBroadcastSessionInfo_ConcurrentCallsAreRaceFree drives many
// goroutines through the same sink under -race. The cache may produce
// either 1 or 2 broadcasts depending on interleaving (last-writer-wins
// for duplicate payloads), but the implementation must not panic or
// produce a data race.
func TestBroadcastSessionInfo_ConcurrentCallsAreRaceFree(t *testing.T) {
	t.Parallel()

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

// TestSessionInfoKeysStateTheirDedupPolicy pins the seam between
// contracts/session-info.json and dedupExemptSessionInfoKeys. The contract owns
// the top-level agent_session_info vocabulary; this file owns which of those keys
// skip the per-key dedup. Nothing else compiles the two together, so a key added
// to the contract and forgotten in the exemption set takes the dedup in silence --
// the exact failure the exemption exists to prevent, where a counter or a badge
// stays hidden until a strictly different value arrives. Every contract key must
// appear in exactly one of the two sets, and every exempt key must still be a
// contract key, so a rename cannot leave a stale exemption behind.
//
// The contract holds the keys the BROWSER reads. A Go-only ephemeral key (the
// pi_* family, zcode_api_retry) is outside it and outside this guard, which is
// the right boundary: a key that drives a badge or a counter is browser-read by
// definition, so it is in the contract.
func TestSessionInfoKeysStateTheirDedupPolicy(t *testing.T) {
	t.Parallel()

	// The keys that carry meaningfully across turns, so the dedup is correct for
	// them. A new contract key belongs here or in dedupExemptSessionInfoKeys.
	deduped := map[string]struct{}{
		contracts.SessionInfoKeyTotalCostUsd:  {},
		contracts.SessionInfoKeyContextUsage:  {},
		contracts.SessionInfoKeyRateLimits:    {},
		contracts.SessionInfoKeyCodexTurnId:   {},
		contracts.SessionInfoKeyStreamingType: {},
	}

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	contractPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..",
		"contracts", "session-info.json")
	raw, err := os.ReadFile(contractPath)
	require.NoError(t, err, "the session-info contract must stay readable at %s", contractPath)

	var contract struct {
		Keys map[string]string `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(raw, &contract))
	require.NotEmpty(t, contract.Keys, "contracts/session-info.json must list its top-level keys")

	tokens := make(map[string]struct{}, len(contract.Keys))
	for name, token := range contract.Keys {
		tokens[token] = struct{}{}
		_, exempt := dedupExemptSessionInfoKeys[token]
		_, dedup := deduped[token]
		assert.True(t, exempt != dedup,
			"session-info key %s (%q) must be listed exactly once: in dedupExemptSessionInfoKeys, for live per-turn state the frontend drops, or in this test's deduped set, for state that carries across turns",
			name, token)
	}
	for token := range dedupExemptSessionInfoKeys {
		_, known := tokens[token]
		assert.True(t, known,
			"dedupExemptSessionInfoKeys holds %q, which contracts/session-info.json no longer lists -- a stale exemption exempts nothing", token)
	}
}

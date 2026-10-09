package gemini

import (
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiSessionUsageReadsTheInstalledNativeTokenShape(t *testing.T) {
	t.Parallel()
	usage := geminiSessionUsage(geminiSession{Messages: []geminiMessage{{Type: "gemini", Tokens: json.RawMessage(`{"input":42,"output":7,"cached":0,"total":49}`)}}})
	require.NotNil(t, usage)
	assert.Equal(t, int64(42), usage[contracts.ContextUsageFieldInputTokens])
	assert.Equal(t, int64(7), usage[contracts.ContextUsageFieldOutputTokens])
	assert.Equal(t, int64(0), usage[contracts.ContextUsageFieldCacheReadInputTokens])
}

func TestGeminiContextUsageReadsTheLastNativeRequestInsteadOfTheTurnSum(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	path := geminiFixtureSessionPath(directory, "root")
	writeGeminiSession(t, path, "root", hash, "main", "native prompt")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(`{"id":"first","type":"gemini","content":[{"text":"first answer"}],"tokens":{"input":100,"output":5,"cached":20,"thoughts":0,"total":105}}` + "\n" + `{"id":"last","type":"gemini","content":[{"text":"last answer"}],"tokens":{"input":140,"output":7,"cached":30,"thoughts":0,"total":147}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	sink := &agenttest.Sink{}
	services := &geminiOutputServices{ProviderServices: agent.NewProviderServices(sink), query: query, currentSession: func() string { return "root" }}
	require.NoError(t, services.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"stopReason":"end_turn","_meta":{"quota":{"token_count":{"input_tokens":240,"output_tokens":12}}}}`)}, agent.SpanInfo{}))
	usage, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens:              int64(110),
		contracts.ContextUsageFieldOutputTokens:             int64(7),
		contracts.ContextUsageFieldCacheReadInputTokens:     int64(30),
		contracts.ContextUsageFieldCacheCreationInputTokens: int64(0),
	}, usage)
}

func TestGeminiSessionUsagePreservesZeroAndRejectsInvalidCounts(t *testing.T) {
	t.Parallel()
	require.NotNil(t, geminiSessionUsage(geminiSession{Messages: []geminiMessage{{Type: "gemini", Tokens: json.RawMessage(`{"input":0,"output":0}`)}}}))
	for _, raw := range []string{`{`, `{}`, `null`, `{"input":-1,"output":0}`, `{"input":0,"output":-1}`, `{"input":1.5,"output":0}`, `{"input":0,"output":0,"cached":1}`, `{"input":1,"output":0,"cached":-1}`, `{"input":1}`, `{"output":1}`, `{"input_tokens":42,"output_tokens":7}`} {
		assert.Nil(t, geminiSessionUsage(geminiSession{Messages: []geminiMessage{{Type: "gemini", Tokens: json.RawMessage(raw)}}}), "record %q", raw)
	}
}

func writeGeminiQuota(t *testing.T, directory, sessionID, hash string, input int64) {
	t.Helper()
	path := geminiFixtureSessionPath(directory, sessionID)
	writeGeminiSession(t, path, sessionID, hash, "main", "native prompt")
	record, err := json.Marshal(map[string]any{
		"id": "native-answer", "type": "gemini", "tokens": map[string]int64{"input": input, "output": 0},
	})
	require.NoError(t, err)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.Write(append(record, '\n'))
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func TestCapturedGeminiDividerReadsItsOriginalNativeSession(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	writeGeminiQuota(t, directory, "original", hash, 10)
	writeGeminiQuota(t, directory, "replacement", hash, 20)
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("original")
	services := &geminiOutputServices{ProviderServices: agent.NewProviderServices(sink), query: query,
		currentSession: func() string { return "replacement" }}
	record := agent.CaptureTranscript(services, agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{})
	require.NoError(t, record.PersistTurnEnd())
	usage, found := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, found)
	assert.Equal(t, int64(10), usage.(map[string]any)[contracts.ContextUsageFieldInputTokens])
}

func TestCapturedGeminiUnknownSessionDoesNotReadTheCurrentSession(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	writeGeminiQuota(t, directory, "current", hash, 20)
	sink := &agenttest.Sink{}
	services := &geminiOutputServices{ProviderServices: agent.NewProviderServices(sink), query: query,
		currentSession: func() string { return "current" }}
	record := agent.CaptureTranscript(services, agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{})
	require.NoError(t, record.PersistTurnEnd())
	assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyContextUsage))
}

func TestCapturedGeminiOwnershipLostDuringQuotaReadCannotPublish(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	writeGeminiQuota(t, directory, "original", hash, 10)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var reads atomic.Int64
	query.Getenv = func(key string) string {
		if key == "GEMINI_CLI_HOME" && reads.Add(1) == 3 {
			close(entered)
			<-release
		}
		return ""
	}
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("original")
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	services := &geminiOutputServices{ProviderServices: agent.NewProviderServices(sink), query: query,
		currentSession: func() string { return "original" }}
	record := agent.CaptureTranscript(services, agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{})
	done := make(chan error, 1)
	go func() { done <- record.PersistTurnEnd() }()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the native quota read did not reach the blocked read")
	}
	sink.SetTurnState(agent.TurnState{}, 2)
	sink.SetTurnState(agent.TurnState{Active: true}, 3)
	sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: map[string]any{"replacement": true}})
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the captured divider did not finish after the native quota read")
	}
	assert.Equal(t, []any{map[string]any{"replacement": true}}, sink.SessionInfoValues(contracts.SessionInfoKeyContextUsage))
}

func TestCapturedGeminiDuplicateDividerDoesNotRepeatItsQuotaRead(t *testing.T) {
	t.Parallel()
	query, directory, hash := geminiStoreFixture(t)
	writeGeminiQuota(t, directory, "original", hash, 10)
	var reads atomic.Int64
	query.Getenv = func(string) string { reads.Add(1); return "" }
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("original")
	services := &geminiOutputServices{ProviderServices: agent.NewProviderServices(sink), query: query,
		currentSession: func() string { return "original" }}
	content := agent.MessageContent{Original: []byte(`{"done":true}`), IdempotencyKey: "native-divider"}
	first := agent.CaptureTranscript(services, content, agent.SpanInfo{})
	require.NoError(t, first.PersistTurnEnd())
	initialReads := reads.Load()
	require.Positive(t, initialReads)
	sink.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyContextUsage: map[string]any{"replacement": true}})
	second := agent.CaptureTranscript(services, content, agent.SpanInfo{})
	require.NoError(t, second.PersistTurnEnd())
	assert.Equal(t, initialReads, reads.Load())
	assert.Len(t, sink.SessionInfoValues(contracts.SessionInfoKeyContextUsage), 2)
	assert.Equal(t, map[string]any{"replacement": true}, sink.LastSessionInfo()[contracts.SessionInfoKeyContextUsage])
}

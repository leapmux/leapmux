package junie

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJunieToolTranscriptReportsInitialAndLaterNativePaths(t *testing.T) {
	t.Parallel()
	fixture := newJunieOutputPathFixture(t)
	source := &junieToolSource{home: fixture.home, workingDir: fixture.cwd}
	location := source.Locate(fixture.sessionID)
	require.True(t, location.Ready)
	initial, err := source.InitialSupplement(t.Context(), location.Path, fixture.original, agent.SpanInfo{SpanID: fixture.callID, Closing: true})
	require.NoError(t, err)
	require.NotEmpty(t, initial)
	// ACP content can omit AgentSessionID. The tracker's location still owns this pending row.
	later, err := source.ReadSupplements(t.Context(), location.Path, map[string]agent.MessageContent{fixture.callID: {Original: fixture.original}}, false)
	require.NoError(t, err)
	assert.True(t, agent.JSONCanonicalEqual(initial, later[fixture.callID]), "initial and later recovery must retain the same exact native receipt")
	foreign, err := source.ReadSupplements(t.Context(), location.Path, map[string]agent.MessageContent{fixture.callID: {Original: fixture.original, AgentSessionID: "session-foreign"}}, false)
	require.Error(t, err)
	assert.Empty(t, foreign)
	none, err := source.InitialSupplement(t.Context(), location.Path, []byte(`{"completion":"complete"}`), agent.SpanInfo{Closing: true})
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestJunieToolTranscriptPreservesHostSupplements(t *testing.T) {
	t.Parallel()
	fixture := newJunieOutputPathFixture(t)
	sink := &agenttest.Sink{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	transcript := newJunieToolTranscript(ctx, agent.NewProviderServices(sink), fixture.home, fixture.cwd)
	transcript.UpdateSessionID(fixture.sessionID)
	host := []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"` + fixture.callID + `","status":"completed","terminals":{"` + fixture.callID + `":{"output":"host bytes","exitCode":0,"truncated":false}}}`)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: fixture.original, Supplemental: host}, agent.SpanInfo{SpanID: fixture.callID, SpanType: "execute", Closing: true}))
	transcript.WaitForSupplementsForTest()
	messages := sink.Messages()
	require.Len(t, messages, 1)
	var extra map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(messages[0].SupplementalContent, &extra))
	assert.Contains(t, extra, "outputFilePath")
	assert.Contains(t, extra, "terminals")
	transcript.Reset()
	assert.Empty(t, transcript.PendingSpanIDsForTest())
}

type junieHoldingServices struct {
	agent.ProviderServices
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *junieHoldingServices) EnrichMessage(change agent.MessageEnrichment) (bool, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.ProviderServices.EnrichMessage(change)
}

func TestJunieToolTranscriptResetsWhileRecoveryOwnsTheOldNativeRow(t *testing.T) {
	t.Parallel()
	fixture := newJunieOutputPathFixture(t)
	writeJunieOutputPathEvents(t, fixture)
	sink := &agenttest.Sink{}
	held := &junieHoldingServices{ProviderServices: agent.NewProviderServices(sink), entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(held.release) })
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	transcript := newJunieToolTranscript(ctx, held, fixture.home, fixture.cwd)
	transcript.UpdateSessionID(fixture.sessionID)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: fixture.original}, agent.SpanInfo{SpanID: fixture.callID, SpanType: "execute", Closing: true}))
	transcript.WaitForSupplementsForTest()
	writeJunieOutputPathEvents(t, fixture, fixture.event)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"kind":"text","text":"next native frame"}`)}, agent.SpanInfo{}))
	select {
	case <-held.entered:
	case <-ctx.Done():
		t.Fatal("the native recovery did not reach the stored old row")
	}
	transcript.Reset()
	release()
	transcript.WaitForSupplementsForTest()
	assert.Empty(t, transcript.PendingSpanIDsForTest())
	messages := sink.Messages()
	require.Len(t, messages, 2)
	var supplement map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(messages[0].SupplementalContent, &supplement))
	var receipt junieOutputFilePath
	require.NoError(t, json.Unmarshal(supplement["outputFilePath"], &receipt))
	assert.Equal(t, fixture.sessionID, receipt.SessionID)
	assert.Equal(t, fixture.path, receipt.Path)
}

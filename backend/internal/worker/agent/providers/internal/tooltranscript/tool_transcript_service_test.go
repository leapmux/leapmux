package tooltranscript_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type historicalServiceSource struct {
	tooltranscript.SourceDefaults
	captured atomic.Bool
	reads    atomic.Int32
	started  chan struct{}
	release  chan struct{}
	start    sync.Once
}

func (*historicalServiceSource) ProviderName() string { return "historical service test" }
func (*historicalServiceSource) Locate(sessionID string) tooltranscript.Location {
	return tooltranscript.Location{SessionKey: sessionID, Path: sessionID, Ready: sessionID != ""}
}
func (*historicalServiceSource) ToolCallID(original []byte) string {
	var value struct {
		ToolCallID string `json:"toolCallId"`
	}
	if json.Unmarshal(original, &value) != nil {
		return ""
	}
	return value.ToolCallID
}
func (*historicalServiceSource) ReadSupplements(context.Context, tooltranscript.Location, map[string]agent.MessageContent, bool) (map[string][]byte, error) {
	return nil, nil
}
func (source *historicalServiceSource) CaptureSupplement(tooltranscript.Location, agent.MessageContent, agent.SpanInfo) tooltranscript.SupplementReader {
	if !source.captured.CompareAndSwap(false, true) {
		return nil
	}
	return func(_ context.Context, final bool) ([]byte, error) {
		if !final {
			return nil, fmt.Errorf("the captured reader requires a final pass")
		}
		source.reads.Add(1)
		source.start.Do(func() { close(source.started) })
		<-source.release
		return []byte(`{"native":true}`), nil
	}
}

type historicalCASResult struct {
	sequence, priorRevision     int64
	winnerWritten, staleWritten bool
	err                         error
}
type historicalCollisionServices struct {
	agent.ProviderServices
	targetSeq atomic.Int64
	injected  atomic.Bool
	result    chan historicalCASResult
}

func (services *historicalCollisionServices) EnrichMessage(change agent.MessageEnrichment) (bool, error) {
	if change.Seq != services.targetSeq.Load() || !services.injected.CompareAndSwap(false, true) {
		return services.ProviderServices.EnrichMessage(change)
	}
	competing := change
	competing.WriteReceipt = agent.NewMessageEnrichmentReceipt()
	competing.SupplementalContent = []byte(`{"winner":true}`)
	winner, err := services.ProviderServices.EnrichMessage(competing)
	if err == nil && !winner {
		err = fmt.Errorf("the competing enrichment did not commit")
	}
	if err != nil {
		services.result <- historicalCASResult{sequence: change.Seq, priorRevision: change.PreviousRevision, winnerWritten: winner, err: err}
		return false, err
	}
	stale, err := services.ProviderServices.EnrichMessage(change)
	services.result <- historicalCASResult{sequence: change.Seq, priorRevision: change.PreviousRevision, winnerWritten: winner, staleWritten: stale, err: err}
	return stale, err
}

func TestToolTranscriptHistoricalReadPreservesRealServiceCASAndGeometry(t *testing.T) {
	t.Parallel()
	waitCtx := testutil.DeadlineContext(t)
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), sqlDB))
	const ownerID = "historical-service-owner"
	const sessionID = "reused-native-session"
	const provider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	queries := db.New(sqlDB)
	require.NoError(t, queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: ownerID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: provider}))
	output := service.NewOutputHandler(sqlDB, queries, service.NewWatcherManager(), agent.NewManager(providers.Registry(), nil), nil, testutil.NewQuartzMock(t))
	t.Cleanup(output.CancelBackgroundCtx)
	base, err := output.NewRootSink(ownerID, provider)
	require.NoError(t, err)
	base.UpdateSessionID(sessionID)
	base.SetTurnState(agent.TurnState{Active: true}, 1)
	source := &historicalServiceSource{started: make(chan struct{}), release: make(chan struct{})}
	collision := &historicalCollisionServices{ProviderServices: base, result: make(chan historicalCASResult, 1)}
	transcriptCtx, cancel := context.WithCancel(t.Context())
	transcript := tooltranscript.New(transcriptCtx, collision, source)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(source.release) }) }
	t.Cleanup(func() { release(); cancel(); transcript.WaitForSupplementsForTest() })
	latest := func() db.Message {
		t.Helper()
		row, err := queries.GetLatestMessageByAgentID(t.Context(), ownerID)
		require.NoError(t, err)
		return row
	}
	bySeq := func(seq int64) db.Message {
		t.Helper()
		row, err := queries.GetMessageByAgentIDAndSeq(t.Context(), db.GetMessageByAgentIDAndSeqParams{AgentID: ownerID, Seq: seq})
		require.NoError(t, err)
		return row
	}
	waitWorker := func() {
		t.Helper()
		done := make(chan struct{})
		go func() { transcript.WaitForSupplementsForTest(); close(done) }()
		select {
		case <-done:
		case <-waitCtx.Done():
			t.Fatal("the supplement worker did not finish")
		}
	}
	original := []byte(`{"toolCallId":"call","result":"identical result"}`)
	base.OpenSpan("old-parent", "")
	color := base.ReserveSpanColor("call", "old-parent")
	base.OpenSpan("call", "old-parent")
	base.SetSpanType("call", "OldTool")
	old := agent.CaptureTranscript(transcript, agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: "call", ParentSpanID: "old-parent", SpanType: "OldTool", SpanColor: color, Closing: true})
	require.NoError(t, old.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	oldSeq, known := old.FrozenContent().WriteReceipt.StoredMessageSequence()
	require.True(t, known)
	oldBefore := bySeq(oldSeq)
	require.Equal(t, int64(0), oldBefore.SupplementalRevision)
	require.Equal(t, "old-parent", oldBefore.ParentSpanID)
	require.Positive(t, oldBefore.Depth)
	require.NotEmpty(t, oldBefore.SpanLines)
	collision.targetSeq.Store(oldSeq)
	transcript.AdoptNativeSession(sessionID)
	require.False(t, old.FrozenContent().Publication.Owner().IsSessionCurrent())
	select {
	case <-source.started:
	case <-waitCtx.Done():
		t.Fatal("the historical native read did not start")
	}
	base.ResetSpans()
	base.OpenSpan("current-parent", "")
	color = base.ReserveSpanColor("call", "current-parent")
	base.OpenSpan("call", "current-parent")
	base.SetSpanType("call", "CurrentTool")
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: "call", ParentSpanID: "current-parent", SpanType: "CurrentTool", SpanColor: color, Closing: true}))
	newer := latest()
	require.NotEqual(t, oldSeq, newer.Seq)
	require.Equal(t, oldBefore.AgentSessionID, newer.AgentSessionID)
	base.CloseSpan("call")
	base.OpenSpan("current-sentinel", "current-parent")
	base.SetSpanType("current-sentinel", "Sentinel")
	probe := func() db.Message {
		t.Helper()
		require.NoError(t, base.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"probe":true}`)}, agent.SpanInfo{ParentSpanID: "current-parent", ConnectorSpanID: "current-sentinel"}))
		return latest()
	}
	beforeProbe := probe()
	var lines []struct {
		SpanID string `json:"span_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(beforeProbe.SpanLines), &lines))
	hasSentinel := false
	for _, line := range lines {
		hasSentinel = hasSentinel || line.SpanID == "current-sentinel"
	}
	require.True(t, hasSentinel)
	release()
	var result historicalCASResult
	select {
	case result = <-collision.result:
	case <-waitCtx.Done():
		t.Fatal("the historical enrichment did not reach the real SQL collision")
	}
	require.NoError(t, result.err)
	assert.Equal(t, oldSeq, result.sequence)
	assert.Zero(t, result.priorRevision)
	assert.True(t, result.winnerWritten)
	assert.False(t, result.staleWritten)
	waitWorker()
	require.NoError(t, transcript.FinalizePending())
	waitWorker()
	after := bySeq(oldSeq)
	assert.Equal(t, int64(2), after.SupplementalRevision)
	stored, err := base.ReadToolResultBySeq(oldSeq)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, original, stored.Content.Original)
	assert.JSONEq(t, `{"winner":true,"native":true}`, string(stored.Content.Supplemental))
	assert.Equal(t, int32(1), source.reads.Load())
	expected := oldBefore
	expected.SupplementalContent = after.SupplementalContent
	expected.SupplementalContentCompression = after.SupplementalContentCompression
	expected.SupplementalRevision = after.SupplementalRevision
	assert.Equal(t, expected, after)
	assert.Equal(t, newer, bySeq(newer.Seq))
	afterProbe := probe()
	assert.Equal(t, beforeProbe.Depth, afterProbe.Depth)
	assert.Equal(t, beforeProbe.SpanLines, afterProbe.SpanLines)
	assert.Equal(t, beforeProbe.SpanColor, afterProbe.SpanColor)
	assert.Equal(t, "Sentinel", base.GetSpanType("current-sentinel"))
}

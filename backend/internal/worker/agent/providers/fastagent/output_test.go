package fastagent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

func newFastagentRootSubject(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	home, cwd := t.TempDir(), t.TempDir()
	a, _ := acptest.NewAgentForRPC(t, func() *Agent {
		return &Agent{home: home, workingDir: cwd, nativeCommands: true, bypassPermissions: true}
	}, func(a *Agent) *acp.Base { return &a.Base })
	sink := &agenttest.Sink{}
	services := agent.NewProviderServices(sink)
	a.SetSinkForTest(services)
	*a.HooksForTest() = a.hooks("", services)
	require.NoError(t, a.initializeRootArchive(false))
	return a, sink
}

func TestFastagentRootReplayKeepsCanonicalIdentitiesAndDeduplicates(t *testing.T) {
	t.Parallel()
	a, sink := newFastagentRootSubject(t)
	writeFastagentRootArchive(t, a.home, "session-1", a.workingDir, fastagentRootCommandMessages(t, "native output", false))
	require.NoError(t, a.replayRootArchive())
	first := sink.Messages()
	require.Len(t, first, 2)
	assert.Contains(t, string(first[0].Content), "printf computed")
	assert.Contains(t, string(first[1].Content), "native output")
	assert.Contains(t, string(first[1].Content), "fast-agent/root-archive")
	require.NoError(t, a.replayRootArchive())
	assert.Equal(t, first, sink.Messages())
}

func TestFastagentRootResumeBaselineDoesNotReplayHistoricalCalls(t *testing.T) {
	t.Parallel()
	a, sink := newFastagentRootSubject(t)
	old := fastagentRootCommandMessages(t, "old output", false)
	writeFastagentRootArchive(t, a.home, "session-1", a.workingDir, old)
	require.NoError(t, a.initializeRootArchive(true))
	next := fastagentRootCommandMessages(t, "new output", true)
	next[0].Timestamp = "2026-10-26T00:01:00Z"
	writeFastagentRootArchive(t, a.home, "session-1", a.workingDir, append(old, next...))
	require.NoError(t, a.replayRootArchive())
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.NotContains(t, string(rows[1].Content), "old output")
	assert.Contains(t, string(rows[1].Content), "new output")
	assert.Contains(t, string(rows[1].Content), `"status":"failed"`)
}

func TestFastagentRootInterceptionKeepsChildrenAndUnrelatedTools(t *testing.T) {
	t.Parallel()
	a, _ := newFastagentRootSubject(t)
	for _, tc := range []struct {
		session, raw string
		consumed     bool
	}{
		{"old-session", `{"sessionUpdate":"tool_call","toolCallId":"a","title":"bash"}`, false},
		{"session-1", `{"sessionUpdate":"tool_call","toolCallId":"r","title":"read_text_file"}`, false},
		{"session-1", `{"sessionUpdate":"tool_call","toolCallId":"c","title":"bash","_meta":{"parentToolCallId":"child"}}`, false},
		{"session-1", `{"sessionUpdate":"tool_call","toolCallId":"a","title":"bash"}`, true},
		{"session-1", `{"sessionUpdate":"tool_call_update","toolCallId":"a","status":"completed"}`, true},
		{"session-1", `{"sessionUpdate":"tool_call_update","toolCallId":"archived","title":"bash","_meta":{"fast-agent/root-archive":{}}}`, false},
		{"session-1", `{"sessionUpdate":"tool_call","toolCallId":"p","title":"process"}`, true},
		{"session-1", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}`, false},
	} {
		assert.Equal(t, tc.consumed, a.observeRootTool(tc.session, a.Sink(), json.RawMessage(tc.raw)), tc.raw)
	}
	a.prepareRootArchive()
	assert.False(t, a.observeRootTool("session-1", a.Sink(), json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"a","title":"read_text_file"}`)), "a new turn must not suppress an unrelated tool that reuses a live ID")
}

func TestFastagentRootResetRejectsTheOutgoingSession(t *testing.T) {
	t.Parallel()
	a, sink := newFastagentRootSubject(t)
	writeFastagentRootArchive(t, a.home, "session-1", a.workingDir, fastagentRootCommandMessages(t, "old output", false))
	a.clearProviderState()
	assert.NoError(t, a.replayRootArchive())
	assert.Empty(t, sink.Messages())
	a.SetSessionIDForTest("session-2")
	a.prepareRootArchive()
	writeFastagentRootArchive(t, a.home, "session-2", a.workingDir, fastagentRootCommandMessages(t, "new output", false))
	require.NoError(t, a.replayRootArchive())
	require.Len(t, sink.Messages(), 2)
	assert.Contains(t, string(sink.Messages()[1].Content), "new output")
}

func TestFastagentRootArchiveFailureReportsTheReadError(t *testing.T) {
	t.Parallel()
	a, sink := newFastagentRootSubject(t)
	a.finishPrompt(nil, false)
	assert.Empty(t, sink.Messages())
	notices := sink.LeapMuxNotifications()
	require.Len(t, notices, 1)
	assert.Contains(t, notices[0]["text"], "could not be read")
}

func TestFastagentRootPromptAfterWaitCannotWriteIntoTheRetiredTranscript(t *testing.T) {
	t.Parallel()
	a, sink := newFastagentRootSubject(t)
	writeFastagentRootArchive(t, a.home, "session-1", a.workingDir, fastagentRootCommandMessages(t, "late native output", false))
	a.HooksForTest().BeforeWaitCleanup()
	a.finishPrompt(nil, false)
	assert.Empty(t, sink.Messages(), "a detached prompt callback cannot replay after wait cleanup")
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestFastagentRootReplayRejectsAChangedCompletedResult(t *testing.T) {
	t.Parallel()
	a, sink := newFastagentRootSubject(t)
	writeFastagentRootArchive(t, a.home, "session-1", a.workingDir, fastagentRootCommandMessages(t, "original result", false))
	require.NoError(t, a.replayRootArchive())
	first := sink.Messages()
	writeFastagentRootArchive(t, a.home, "session-1", a.workingDir, fastagentRootCommandMessages(t, "conflicting result", false))
	require.ErrorContains(t, a.replayRootArchive(), "conflicting")
	assert.Equal(t, first, sink.Messages())
}

func TestFastagentRootFailedReadDoesNotMoveAnOlderToolIntoTheNextTurn(t *testing.T) {
	t.Parallel()
	a, sink := newFastagentRootSubject(t)
	a.prepareRootArchive()
	a.finishPrompt(nil, false)
	require.Empty(t, sink.Messages())
	old := fastagentRootCommandMessages(t, "older turn output", false)
	writeFastagentRootArchive(t, a.home, "session-1", a.workingDir, old)
	a.prepareRootArchive()
	next := fastagentRootCommandMessages(t, "current turn output", false)
	next[0].Timestamp = "2026-10-26T00:01:00Z"
	writeFastagentRootArchive(t, a.home, "session-1", a.workingDir, append(old, next...))
	a.finishPrompt(nil, false)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.NotContains(t, string(rows[1].Content), "older turn output")
	assert.Contains(t, string(rows[1].Content), "current turn output")
}

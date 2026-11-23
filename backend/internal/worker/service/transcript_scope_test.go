package service

import (
	"github.com/leapmux/leapmux/internal/worker/bgtask"

	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scopeCaptureOverride struct {
	agent.ProviderServices
	calls   int
	foreign agent.TranscriptPublication
}

func (writer *scopeCaptureOverride) CaptureMessage(content agent.MessageContent, span agent.SpanInfo) agent.MessageContent {
	writer.calls++
	content = writer.ProviderServices.CaptureMessage(content, span)
	content.Supplemental = []byte("{\"capture\":\"outer\"}")
	if writer.foreign != nil {
		content.Publication = writer.foreign
	}
	return content
}

func TestTranscriptScopeRetainsHistoricalAuthorityAndIndependentSpans(t *testing.T) {
	t.Parallel()
	svc, services := setupRootSink(t, "scope-root")
	services.UpdateSessionID("native-a")
	services.SetTurnState(agent.TurnState{Active: true}, 1)
	anchor := agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{})
	originalOwner := anchor.FrozenContent().Publication.Owner().(*transcriptOwner)
	services.SetTurnState(agent.TurnState{}, 2)
	services.UpdateSessionID("native-b")
	services.SetTurnState(agent.TurnState{Active: true}, 3)
	services.OpenSpan("new-live", "")
	childID, err := services.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "spawn", ProviderChildKey: "scope-child", AgentSessionID: "native-child"})
	require.NoError(t, err)
	child := services.ChildSink(childID)
	child.OpenSpan("child-live", "")
	scope, err := services.TranscriptScopeFor(anchor)
	require.NoError(t, err)
	color := scope.ReserveSpanColor("archive-parent", "")
	opening, err := scope.CaptureTranscript(agent.MessageContent{Original: []byte("{\"native\":\"opening\"}")}, agent.SpanInfo{SpanID: "archive-parent", SpanType: "bash", SpanColor: color})
	require.NoError(t, err)
	scope.OpenSpan("archive-parent", "")
	scope.SetSpanType("archive-parent", "bash")
	scope.OpenSpan("archive-child", "archive-parent")
	nested, err := scope.CaptureTranscript(agent.MessageContent{Original: []byte("{\"native\":\"nested\"}")}, agent.SpanInfo{SpanID: "archive-child", ParentSpanID: "archive-parent", Closing: true})
	require.NoError(t, err)
	noSpan, err := scope.CaptureTranscript(agent.MessageContent{Original: []byte("{\"native\":\"spawn\"}")}, agent.SpanInfo{SpanID: "native-spawn", ParentSpanID: "archive-parent", NoSpan: true})
	require.NoError(t, err)
	for _, capture := range []agent.CapturedTranscript{opening, nested, noSpan} {
		content := capture.FrozenContent()
		owner := content.Publication.Owner().(*transcriptOwner)
		assert.True(t, owner.sameLiveAuthority(originalOwner))
		assert.Equal(t, "native-a", content.AgentSessionID)
		assert.Equal(t, agent.OutputQueueFor(services), agent.OutputQueueFor(capture.TranscriptWriter()))
		assert.NotContains(t, owner.spanLines, "new-live")
		assert.NotContains(t, owner.spanLines, "child-live")
	}
	openingOwner := opening.FrozenContent().Publication.Owner().(*transcriptOwner)
	assert.Equal(t, "[]", openingOwner.spanLines)
	assert.Equal(t, color, openingOwner.spanColor)
	nestedOwner := nested.FrozenContent().Publication.Owner().(*transcriptOwner)
	assert.Equal(t, int32(1), nestedOwner.depth)
	var lines []SpanLine
	require.NoError(t, json.Unmarshal([]byte(nestedOwner.spanLines), &lines))
	require.Len(t, lines, 2)
	assert.Equal(t, SpanLineConnectorEnd, lines[1].Type)
	assert.Equal(t, int32(0), noSpan.FrozenContent().Publication.Owner().(*transcriptOwner).spanColor)
	assert.Equal(t, "bash", scope.GetSpanType("archive-parent"))
	scope.CloseSpan("archive-child")
	scope.CloseSpan("archive-parent")
	require.NoError(t, agent.PersistCapturedTranscriptBatch(services, func() []agent.CapturedTranscriptOperation {
		return []agent.CapturedTranscriptOperation{opening.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT), nested.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT), noSpan.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)}
	}))
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "scope-root"})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	for _, row := range rows {
		assert.Equal(t, "native-a", row.AgentSessionID)
		assert.True(t, row.TranscriptOnly)
	}
	live := requireRootOutputSink(t, svc.Output, "scope-root").currentSpanTracker().ActiveSpans()
	require.Len(t, live, 1)
	assert.Equal(t, "new-live", live[0].SpanID)
	childLive := requireChildOutputSink(t, requireRootOutputSink(t, svc.Output, "scope-root"), childID).currentSpanTracker().ActiveSpans()
	require.Len(t, childLive, 1)
	assert.Equal(t, "child-live", childLive[0].SpanID)
}

func TestTranscriptScopePreservesTheExactCaptureOverride(t *testing.T) {
	t.Parallel()
	_, services := setupRootSink(t, "scope-override")
	services.UpdateSessionID("native")
	writer := &scopeCaptureOverride{ProviderServices: services}
	anchor := agent.CaptureTranscript(writer, agent.MessageContent{}, agent.SpanInfo{})
	scope, err := writer.TranscriptScopeFor(anchor)
	require.NoError(t, err)
	captured, err := scope.CaptureTranscript(agent.MessageContent{Original: []byte("{\"native\":\"source\"}")}, agent.SpanInfo{})
	require.NoError(t, err)
	assert.Equal(t, 2, writer.calls, "the anchor and row each invoke the original decorated capture once")
	assert.Same(t, writer, captured.TranscriptWriter())
	assert.JSONEq(t, "{\"capture\":\"outer\"}", string(captured.FrozenContent().Supplemental))
}

func TestTranscriptScopeRejectsForeignAuthorityAndCapturedRows(t *testing.T) {
	t.Parallel()
	svc, services := setupRootSink(t, "scope-reject")
	services.UpdateSessionID("native")
	anchor := agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{})
	scope, err := services.TranscriptScopeFor(anchor)
	require.NoError(t, err)
	_, err = services.TranscriptScopeFor(agent.CapturedTranscript{})
	require.Error(t, err)
	childID, err := services.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "spawn", ProviderChildKey: "foreign", AgentSessionID: "child-session"})
	require.NoError(t, err)
	foreign := agent.CaptureTranscript(services.ChildSink(childID), agent.MessageContent{}, agent.SpanInfo{})
	_, err = services.TranscriptScopeFor(foreign)
	require.Error(t, err)
	_, err = scope.CaptureTranscript(agent.MessageContent{AgentSessionID: "foreign-session"}, agent.SpanInfo{})
	require.Error(t, err)
	_, err = scope.CaptureTranscript(anchor.FrozenContent(), agent.SpanInfo{})
	require.Error(t, err)
	writer := &scopeCaptureOverride{ProviderServices: services}
	wrappedAnchor := agent.CaptureTranscript(writer, agent.MessageContent{}, agent.SpanInfo{})
	wrapped, err := writer.TranscriptScopeFor(wrappedAnchor)
	require.NoError(t, err)
	writer.foreign = foreign.FrozenContent().Publication
	_, err = wrapped.CaptureTranscript(agent.MessageContent{}, agent.SpanInfo{})
	require.Error(t, err)
	assert.Empty(t, requireRootOutputSink(t, svc.Output, "scope-reject").currentSpanTracker().ActiveSpans())
}

func TestRetainedLiveTranscriptFrameSpansPublishActivityAndPrunePermissions(t *testing.T) {
	t.Parallel()

	svc, services := setupRootSink(t, "frame-spans-root")
	settles := holdSettles(t, svc.Output)
	svc.Output.processRunning = func(string) bool { return true }
	svc.Output.NoteAgentProcessStarted("frame-spans-root")
	svc.Output.WaitActivityRefreshes()

	captured := services.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	frameOwner, ok := captured.Publication.Owner().(agent.TranscriptFrameOwner)
	require.True(t, ok)
	frameSpans := frameOwner.TranscriptFrameSpans()

	frameSpans.OpenSpan("live-frame-span", "")
	svc.Output.WaitActivityRefreshes()
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING,
		svc.Output.AgentActivitySnapshot("frame-spans-root", "frame-spans-root").State)

	services.BroadcastSessionInfo(permissionCheckInfo("sess", "live-frame-span", true))
	require.NotEmpty(t, svc.Output.SessionInfoReplayEvents("frame-spans-root"))

	frameSpans.CloseSpan("live-frame-span")
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		svc.Output.AgentActivitySnapshot("frame-spans-root", "frame-spans-root").State)
	testutil.RequireEventually(t, func() bool { return len(settles.openWindows()) == 1 })
	require.Equal(t, 1, settles.close())
	svc.Output.WaitActivityRefreshes()
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		svc.Output.AgentActivityPublished("frame-spans-root", "frame-spans-root"))
	assert.Empty(t, svc.Output.SessionInfoReplayEvents("frame-spans-root"),
		"closing via live root frame.spans must prune permission checks")

	// ResetSpans via frame.spans also clears permission checks and activity.
	frameSpans.OpenSpan("live-frame-span-2", "")
	services.BroadcastSessionInfo(permissionCheckInfo("sess", "live-frame-span-2", true))
	frameSpans.ResetSpans()
	svc.Output.WaitActivityRefreshes()
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		svc.Output.AgentActivitySnapshot("frame-spans-root", "frame-spans-root").State)
	assert.Empty(t, svc.Output.SessionInfoReplayEvents("frame-spans-root"))

	// ResetSpanTracker / rebindSpanTracker clears active spans, permission checks, and publishes activity.
	services.OpenSpan("rebind-span", "")
	services.BroadcastSessionInfo(permissionCheckInfo("sess", "rebind-span", true))
	svc.Output.ResetSpanTracker("frame-spans-root")
	svc.Output.WaitActivityRefreshes()
	assert.False(t, svc.Output.hasOpenRootSpans("frame-spans-root"))
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE,
		svc.Output.AgentActivitySnapshot("frame-spans-root", "frame-spans-root").State)
	assert.Empty(t, svc.Output.SessionInfoReplayEvents("frame-spans-root"))
}

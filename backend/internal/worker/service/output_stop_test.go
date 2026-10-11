package service

import (
	"database/sql"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeliveredStopKeepsDistinctClaimsThatReuseOneRequestID(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	const agentID = "distinct-stop-claims"
	createClaimTestAgent(t, svc, agentID)
	sink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	_, err := svc.Agents.StartAgentWith(t.Context(), agent.Options{AgentID: agentID, WorkingDir: t.TempDir()}, sink, claudetest.StartSilent)
	require.NoError(t, err)
	sink.SetTurnState(agent.TurnState{Active: true}, 1)
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "same", Payload: []byte(`{"native":"first request"}`)}))
	first, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: agentID, RequestID: "same"})
	require.NoError(t, err)
	stop := svc.Output.NoteAgentStopRequested(agentID, agentID)
	svc.Output.WaitActivityRefreshes()
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "same", Payload: []byte(`{"native":"second request"}`)}))
	second, err := svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: agentID, RequestID: "same"})
	require.NoError(t, err)
	require.NotEqual(t, first.ClaimToken, second.ClaimToken)
	writer := &controlPublicationWriter{mockResponseWriter: mockResponseWriter{channelID: "distinct-stop-claims-wire"}}
	registerAgentWatch(svc, writer.ChannelID(), agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	target, err := svc.Agents.CaptureStopTarget(agentID)
	require.NoError(t, err)
	stop.Delivered(target)
	_, err = svc.Queries.GetControlRequest(t.Context(), db.GetControlRequestParams{AgentID: agentID, RequestID: "same"})
	assert.ErrorIs(t, err, sql.ErrNoRows)
	cancellations := writer.cancellationSnapshot()
	require.Len(t, cancellations, 1)
	assert.Equal(t, "same", cancellations[0].RequestId)
	assert.Equal(t, second.ClaimToken, cancellations[0].ClaimToken)
}

func TestStopAttemptFailureAndIgnoredReportsPreserveOtherAttempts(t *testing.T) {
	for _, report := range []string{"failed", "ignored"} {
		t.Run(report, func(t *testing.T) {
			t.Parallel()
			h, _ := newActivityHandler(t, "agent-1")
			h.setTurnActive("agent-1", "agent-1", true)
			first := h.NoteAgentStopRequested("agent-1", "agent-1")
			second := h.NoteAgentStopRequested("agent-1", "agent-1")
			if report == "failed" {
				first.Failed()
			} else {
				first.Context().ReportIgnored()
			}
			assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, h.AgentActivitySnapshot("agent-1", "agent-1").State)
			second.Failed()
			assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, h.AgentActivitySnapshot("agent-1", "agent-1").State)
			first.Context().ReportIgnored()
			assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, h.AgentActivitySnapshot("agent-1", "agent-1").State)
			h.WaitActivityRefreshes()
		})
	}
}

func TestOldStopAttemptReportsPreserveTheReplacementScope(t *testing.T) {
	for _, boundary := range []string{"turn", "process"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			h, _ := newActivityHandler(t, "agent-1")
			h.setTurnActive("agent-1", "agent-1", true)
			old := h.NoteAgentStopRequested("agent-1", "agent-1")
			if boundary == "turn" {
				h.setTurnActive("agent-1", "agent-1", false)
			} else {
				h.NoteAgentProcessStarted("agent-1")
			}
			h.setTurnActive("agent-1", "agent-1", true)
			current := h.NoteAgentStopRequested("agent-1", "agent-1")
			old.Failed()
			old.Context().ReportIgnored()
			assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, h.AgentActivitySnapshot("agent-1", "agent-1").State)
			current.Failed()
			assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, h.AgentActivitySnapshot("agent-1", "agent-1").State)
			h.WaitActivityRefreshes()
		})
	}
}

func TestTurnRefreshCannotRetireANewerStopAttempt(t *testing.T) {
	t.Parallel()
	h, _ := newActivityHandler(t, "agent-1")
	h.setTurnActive("agent-1", "agent-1", true)
	h.NoteAgentStopRequested("agent-1", "agent-1")
	h.WaitActivityRefreshes()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h.processRunning = func(string) bool {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return true
	}
	done := make(chan struct{})
	go func() { defer close(done); h.setTurnActive("agent-1", "agent-1", false) }()
	<-entered
	h.setTurnActive("agent-1", "agent-1", true)
	current := h.NoteAgentStopRequested("agent-1", "agent-1")
	close(release)
	<-done
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, h.AgentActivitySnapshot("agent-1", "agent-1").State)
	current.Failed()
	assert.Equal(t, leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, h.AgentActivitySnapshot("agent-1", "agent-1").State)
	h.WaitActivityRefreshes()
}

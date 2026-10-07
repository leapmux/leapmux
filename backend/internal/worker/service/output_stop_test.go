package service

import (
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
)

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

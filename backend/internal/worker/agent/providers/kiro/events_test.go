package kiro

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// notification encodes one Kiro notification.
func notification(t *testing.T, method string, params map[string]any) []byte {
	t.Helper()
	return frame(t, map[string]any{"method": method, "params": params})
}

func TestKiroSessionNotifyStatesTheStepMessage(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(notification(t, kiroSessionNotifyMethod, map[string]any{
		"sessionId": kiroTestSession, "message": "Found three failing tests.", "severity": "info", "agentName": "reviewer",
	}))
	a.HandleOutput(notification(t, kiroSessionNotifyMethod, map[string]any{
		"sessionId": kiroTestSession, "message": "The build broke.", "severity": "error",
	}))

	assert.Equal(t, []string{"reviewer: Found three failing tests."}, statusTexts(sink))
	assert.Equal(t, []string{"The build broke."}, agenttest.AgentErrorTexts(sink.Notifications()))
}

func TestKiroSessionNotifyOfAnotherSessionOrWithoutTextStatesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(notification(t, kiroSessionNotifyMethod, map[string]any{"sessionId": "sess-other", "message": "not ours"}))
	a.HandleOutput(notification(t, kiroSessionNotifyMethod, map[string]any{"sessionId": kiroTestSession, "message": "  "}))

	assert.Empty(t, sink.Notifications())
}

// The error of a step names the agent of the step, as its other messages do.
func TestKiroSessionNotifyErrorStatesItsAgent(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(notification(t, kiroSessionNotifyMethod, map[string]any{
		"sessionId": kiroTestSession, "message": " The build broke. ", "severity": "error", "agentName": " wf-coder ",
	}))

	assert.Equal(t, []string{"wf-coder: The build broke."}, agenttest.AgentErrorTexts(sink.Notifications()))
	assert.Empty(t, statusTexts(sink))
}

// Kiro sends every session of its process down one connection. Each notice
// that states a session reaches the transcript only for the session that the
// agent serves, and a notice that LeapMux cannot read states nothing.
func TestKiroSessionNoticesOfAnotherSessionStateNothing(t *testing.T) {
	t.Parallel()
	for _, method := range []string{
		kiroSessionNotifyMethod,
		kiroRateLimitMethod,
		kiroCustomAgentNotFoundMethod,
		kiroCustomAgentConfigErrorMethod,
		kiroPolicyErrorMethod,
	} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
			// One params object that each of the five notices can read.
			params := func(sessionID string) map[string]any {
				out := map[string]any{
					"message": "not ours", "severity": "error", "requestedAgent": "reviewer",
					"path": ".kiro/agents/reviewer.json", "error": "bad",
					"errors": []any{map[string]any{"message": "bad rule"}},
				}
				if sessionID != "" {
					out["sessionId"] = sessionID
				}
				return out
			}

			a.HandleOutput(notification(t, method, params("sess-other")))
			a.HandleOutput(notification(t, method, params("")))
			a.HandleOutput(frame(t, map[string]any{"method": method, "params": "unreadable"}))
			assert.Empty(t, sink.Notifications(), "no notice of another session reaches the transcript")

			// The same notice of the session that the agent serves does.
			a.HandleOutput(notification(t, method, params(kiroTestSession)))
			assert.NotEmpty(t, sink.Notifications())
		})
	}
}

func TestKiroUnreadableSystemNotifyStatesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(frame(t, map[string]any{"method": kiroSystemNotifyMethod, "params": []any{"delayed"}}))

	assert.Empty(t, sink.Notifications())
}

func TestKiroRateLimitStatesItsMessageOrAFallback(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(notification(t, kiroRateLimitMethod, map[string]any{"sessionId": kiroTestSession, "message": "High load. Retrying."}))
	a.HandleOutput(notification(t, kiroRateLimitMethod, map[string]any{"sessionId": kiroTestSession}))
	a.HandleOutput(notification(t, kiroRateLimitMethod, map[string]any{"sessionId": "sess-other", "message": "not ours"}))

	assert.Equal(t, []string{"High load. Retrying.", "The model service is busy"}, statusTexts(sink))
}

func TestKiroSystemNotifyStatesTheDelay(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(notification(t, kiroSystemNotifyMethod, map[string]any{"level": "warning", "message": "Responses are delayed."}))
	a.HandleOutput(notification(t, kiroSystemNotifyMethod, map[string]any{"level": "info", "message": ""}))

	assert.Equal(t, []string{"Responses are delayed."}, statusTexts(sink))
}

func TestKiroCustomAgentNotFoundStatesTheFallback(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(notification(t, kiroCustomAgentNotFoundMethod, map[string]any{
		"sessionId": kiroTestSession, "requestedAgent": "reviewer", "fallbackAgent": "vibe",
	}))
	a.HandleOutput(notification(t, kiroCustomAgentNotFoundMethod, map[string]any{
		"sessionId": kiroTestSession, "requestedAgent": "gone",
	}))

	assert.Equal(t, []string{
		`Kiro has no mode "reviewer", so it runs "vibe"`,
		`Kiro has no mode "gone", so it runs "vibe"`,
	}, statusTexts(sink))
}

// A resume whose stored custom agent no longer exists writes ONE row -- Kiro's
// own notice -- and the stored mode follows the fallback: the startup's shared
// fallback row stays suppressed beside the notice. Frames from
// .tmp/probe/kiro/logs/r2/agent.
func TestKiroStartupFallbackKeepsKirosOwnNoticeAsTheRow(t *testing.T) {
	t.Parallel()
	opts := agent.Options{Options: map[string]string{agent.OptionIDPermissionMode: "probe-agent"}}
	a, sink, requests := newKiroAgent(t, opts, nil)

	// Kiro states the missing agent while the session loads, before the
	// startup applies the stored mode.
	a.HandleOutput(notification(t, kiroCustomAgentNotFoundMethod, map[string]any{
		"sessionId": kiroTestSession, "requestedAgent": "probe-agent", "fallbackAgent": "vibe",
	}))
	require.NoError(t, a.ApplyPermissionModeStartup(
		&acp.SessionResult{CurrentModeID: "vibe", Modes: []acp.ModeInfo{{ID: "vibe", Name: "Vibe"}}},
		opts, contracts.KiroModeDefault, ""))

	assert.Equal(t, []string{`Kiro has no mode "probe-agent", so it runs "vibe"`}, statusTexts(sink))
	assert.Equal(t, "vibe", a.PermissionModeForTest(), "the stored mode follows the fallback")
	assert.Empty(t, requests(), "the startup writes no mode the session does not offer")
}

func TestKiroCustomAgentConfigErrorStatesThePath(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(notification(t, kiroCustomAgentConfigErrorMethod, map[string]any{
		"sessionId": kiroTestSession, "path": ".kiro/agents/reviewer.json", "error": "unexpected token ",
	}))

	assert.Equal(t, []string{"Kiro could not read the agent .kiro/agents/reviewer.json: unexpected token"}, agenttest.AgentErrorTexts(sink.Notifications()))
}

func TestKiroPolicyErrorStatesEachRule(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(notification(t, kiroPolicyErrorMethod, map[string]any{
		"sessionId": kiroTestSession,
		"errors": []any{
			map[string]any{"source": "~/.kiro/policy.json", "message": "unknown action"},
			map[string]any{"message": "bad glob"},
			map[string]any{"source": "x", "message": " "},
		},
	}))

	assert.Equal(t, []string{
		"Kiro permission rules: ~/.kiro/policy.json: unknown action",
		"Kiro permission rules: bad glob",
	}, agenttest.AgentErrorTexts(sink.Notifications()))
}

func TestKiroOtherExtensionNotificationsReachNoTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	for _, method := range []string{"_kiro/governance/state", "_kiro/mcp/status", "_kiro/safety/statusChanged"} {
		a.HandleOutput(notification(t, method, map[string]any{"sessionId": kiroTestSession}))
	}

	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.Notifications())

	// An unknown notification persists raw (decision 1).
	unknown := notification(t, "_kiro/mcp/server_initialized", map[string]any{"sessionId": kiroTestSession})
	a.HandleOutput(unknown)
	require.Len(t, sink.Messages(), 1)
	assert.JSONEq(t, string(unknown), string(sink.Messages()[0].Content))
}

func TestKiroUnknownExtensionRequestReceivesAnError(t *testing.T) {
	t.Parallel()
	a, sink, requests := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(frame(t, map[string]any{
		"id": 41, "method": "_kiro/auth/getAccessToken", "params": map[string]any{"sessionId": kiroTestSession},
	}))
	syncPeer(t, a)

	assert.Zero(t, sink.PublishedControlCount())
	var answered bool
	for _, request := range requests() {
		var response struct {
			ID    json.RawMessage `json:"id"`
			Error json.RawMessage `json:"error"`
		}
		require.NoError(t, json.Unmarshal([]byte(request.Raw), &response))
		if string(response.ID) == "41" {
			answered = true
			assert.NotEmpty(t, response.Error, "Kiro must not wait for an answer that never comes")
		}
	}
	assert.True(t, answered)
}

// The known `_kiro/` notification methods each have a verdict, and an unknown
// notification persists raw (decision 1). The list comes from the notification
// senders of the KAS 0.66.8 acp-server.js.
func TestKiroUnknownNotificationsPersist(t *testing.T) {
	t.Parallel()
	for _, method := range []string{
		"_kiro/code_references", "_kiro/governance/state", "_kiro/hooks/didChange",
		"_kiro/knowledge/indexingStarted", "_kiro/knowledge/indexingCompleted",
		"_kiro/mcp/status", "_kiro/mcp/governance_disabled", "_kiro/policy/changed",
		"_kiro/safety/statusChanged", "_kiro/safety/propertiesChanged", "_kiro/sandbox/status",
		"_kiro/sessions/changed", "_kiro/system/notify", "_kiro/spec/taskStatusChanged",
		"_kiro/spec/phaseCheckpoint", "_kiro/tools/didChange", "_kiro/powers/items_changed",
		"_kiro/progressive_context/items_changed", "_kiro/steering/documents_changed",
		"_kiro/terminal/settings_changed", "_kiro/workflow/node_failed",
		"_kiro/workflow/steps_queued", "_kiro/workflow/watch_poll", "_kiro/workflow/loop_iteration",
		"_kiro/hooks/cancel",
	} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
			a.HandleOutput(notification(t, method, map[string]any{"sessionId": kiroTestSession}))
			assert.Empty(t, sink.Messages(), "%s is known live chrome, not a row", method)
		})
	}

	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	unknown := notification(t, "_kiro/future_notice", map[string]any{"sessionId": kiroTestSession, "payload": 1})
	a.HandleOutput(unknown)
	rows := sink.Messages()
	require.Len(t, rows, 1, "an unknown notification persists raw")
	assert.JSONEq(t, string(unknown), string(rows[0].Content))
}

// An unknown session-info kind persists its update, so the reader can see what
// a later Kiro build states.
func TestKiroUnknownSessionInfoKindPersists(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	frameRaw := notification(t, "session/update", map[string]any{
		"sessionId": kiroTestSession,
		"update": map[string]any{
			"sessionUpdate": "session_info_update",
			"_meta":         map[string]any{"kiro": map[string]any{"kind": "future_kind", "detail": true}},
		},
	})
	update := json.RawMessage(`{"sessionUpdate":"session_info_update","_meta":{"kiro":{"kind":"future_kind","detail":true}}}`)
	a.HandleOutput(frameRaw)
	rows := sink.PersistedNotifications()
	require.Len(t, rows, 1, "an unknown session-info kind persists its update")
	assert.JSONEq(t, string(update), string(rows[0].Content))
}

// An unknown `_kiro/` notification of the current session persists byte for
// byte; one of another session reaches nothing.
func TestKiroUnknownNotificationSuite(t *testing.T) {
	t.Parallel()
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "current session", Frame: notification(t, "_kiro/future_notice", map[string]any{"sessionId": kiroTestSession, "value": 1})},
		{Name: "other session", Frame: notification(t, "_kiro/future_notice", map[string]any{"sessionId": "session-other", "value": 1}), OtherSession: true},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
		a.HandleOutput(tc.Frame)
		return &sink.Sink
	})
}

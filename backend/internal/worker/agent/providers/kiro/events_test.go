package kiro

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
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
	assert.Equal(t, []string{"The build broke."}, errorTexts(sink))
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

	assert.Equal(t, []string{"wf-coder: The build broke."}, errorTexts(sink))
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

func TestKiroCustomAgentConfigErrorStatesThePath(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	a.HandleOutput(notification(t, kiroCustomAgentConfigErrorMethod, map[string]any{
		"sessionId": kiroTestSession, "path": ".kiro/agents/reviewer.json", "error": "unexpected token ",
	}))

	assert.Equal(t, []string{"Kiro could not read the agent .kiro/agents/reviewer.json: unexpected token"}, errorTexts(sink))
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
	}, errorTexts(sink))
}

func TestKiroOtherExtensionNotificationsReachNoTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)

	for _, method := range []string{"_kiro/mcp/server_initialized", "_kiro/governance/state", "_kiro/a/later/method"} {
		a.HandleOutput(notification(t, method, map[string]any{"sessionId": kiroTestSession}))
	}

	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.Notifications())
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

package qoder

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestSDKInputRoutesSessionFrames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"user input", `{"type":"user","message":{"role":"user","content":"hello"}}`, "session-1"},
		{"session control", `{"type":"control_request","request_id":"r","request":{"subtype":"interrupt"}}`, "session-1"},
		{"control answer", `{"type":"control_response","response":{"request_id":"r","subtype":"success","response":{}}}`, "session-1"},
		{"explicit session", `{"type":"control_response","session_id":"child-session","response":{"request_id":"r","subtype":"success","response":{}}}`, "child-session"},
		{"daemon control", `{"type":"control_request","request_id":"r","request":{"subtype":"daemon.get_usage_info"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _, stdin := newGoalAgent(t)
			require.NoError(t, a.SendRawInput(agent.RawInput{Data: []byte(tc.raw)}))
			var frame map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(stdin.String()), &frame))
			if tc.want == "" {
				assert.NotContains(t, frame, "session_id")
			} else {
				assert.JSONEq(t, `"`+tc.want+`"`, string(frame["session_id"]))
			}
			var original map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &original))
			delete(frame, "session_id")
			delete(original, "session_id")
			assert.Equal(t, original, frame)
		})
	}
}

type daemonHandshakeStdin struct {
	agent   *Agent
	frames  []map[string]json.RawMessage
	failure string
}

func (s *daemonHandshakeStdin) Write(raw []byte) (int, error) {
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(raw, &frame); err != nil {
		return 0, err
	}
	s.frames = append(s.frames, frame)
	var request struct {
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(frame["request"], &request); err != nil {
		return 0, err
	}
	var id string
	if err := json.Unmarshal(frame["request_id"], &id); err != nil {
		return 0, err
	}
	payload := `{}`
	session := ""
	switch request.Subtype {
	case "daemon.initialize":
		payload = `{"daemon_protocol_version":2,"capabilities":["multi_session_v2"]}`
		if s.failure == "protocol" {
			payload = `{"daemon_protocol_version":1}`
		}
		if s.failure == "capability" {
			payload = `{"daemon_protocol_version":2,"capabilities":[]}`
		}
	case "daemon.authenticate":
		payload = `{"authenticated":true}`
		if s.failure == "unauthenticated" {
			payload = `{"authenticated":false}`
		}
	case "daemon.open_session":
		if s.failure != "missing session" {
			session = "opened-session"
		}
	case "daemon.list_byok_configs":
		payload = `{"configs":[]}`
	case "mcp_status":
		payload = `{"servers":[]}`
	}
	if s.failure == request.Subtype {
		s.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"error","request_id":%q,"error":"native refusal"}}`, id)))
	} else {
		s.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"control_response","session_id":%q,"response":{"subtype":"success","request_id":%q,"response":%s}}`, session, id, payload)))
	}
	return len(raw), nil
}

func (*daemonHandshakeStdin) Close() error { return nil }

func TestStartDaemonOpensAndInitializesOneSession(t *testing.T) {
	t.Parallel()
	a, _, _ := newGoalAgent(t)
	a.sessionID = ""
	a.opts.WorkingDir = t.TempDir()
	stdin := &daemonHandshakeStdin{agent: a}
	a.SetStdinForTest(stdin)
	args := []string{"--session-id", "opened-session", "--model", "provider/model"}
	require.NoError(t, a.startDaemon(args))
	require.Len(t, stdin.frames, 6)
	assert.Equal(t, "opened-session", a.nativeSessionID())
	for i, method := range []string{"daemon.initialize", "daemon.authenticate", "daemon.list_byok_configs", "daemon.open_session", "initialize", "mcp_status"} {
		var request map[string]any
		require.NoError(t, json.Unmarshal(stdin.frames[i]["request"], &request))
		assert.Equal(t, method, request["subtype"])
		if i < 4 {
			assert.NotContains(t, stdin.frames[i], "session_id")
		} else {
			assert.JSONEq(t, `"opened-session"`, string(stdin.frames[i]["session_id"]))
		}
		if i == 0 {
			assert.Equal(t, float64(2), request["daemon_protocol_version"])
			assert.Equal(t, []any{"multi_session_v2"}, request["capabilities"])
			assert.Equal(t, map[string]any{"type": "qodercli"}, request["auth"])
		}
		if i == 3 {
			assert.Equal(t, a.opts.WorkingDir, request["cwd"])
			assert.Equal(t, []any{"--session-id", "opened-session", "--model", "provider/model"}, request["worker_args"])
		}
	}
}

func TestStartDaemonRejectsIncompleteHandshakes(t *testing.T) {
	t.Parallel()
	for failure, want := range map[string]string{
		"daemon.initialize":        "initialize the Qoder daemon: native refusal",
		"daemon.authenticate":      "authenticate the Qoder daemon: native refusal",
		"unauthenticated":          "has no authenticated identity",
		"protocol":                 "did not confirm protocol version 2",
		"capability":               "did not confirm protocol version 2",
		"daemon.open_session":      "open the Qoder session: native refusal",
		"daemon.list_byok_configs": "read the Qoder custom model catalog: native refusal",
		"missing session":          "did not identify the opened session",
		"initialize":               "initialize Qoder stream: native refusal",
	} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			a, _, _ := newGoalAgent(t)
			a.sessionID = ""
			a.SetStdinForTest(&daemonHandshakeStdin{agent: a, failure: failure})
			require.ErrorContains(t, a.startDaemon([]string{"--model", "provider/model"}), want)
		})
	}
}

func TestDaemonAuthenticationNeverPublishesTheJobToken(t *testing.T) {
	t.Parallel()
	a, _, stdin := newGoalAgent(t)
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	a.credentials = qoderCredentials{jobToken: "controlled-job-token"}
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"auth-1","request":{"type":"daemon.auth.fetch_job_token","reason":"initial"}}`))
	var response struct {
		SessionID string `json:"session_id"`
		Response  struct {
			Subtype    string         `json:"subtype"`
			RequestID  string         `json:"request_id"`
			Credential map[string]any `json:"response"`
		} `json:"response"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdin.String()), &response))
	assert.Empty(t, response.SessionID)
	assert.Equal(t, "auth-1", response.Response.RequestID)
	assert.Equal(t, "success", response.Response.Subtype)
	assert.Equal(t, map[string]any{"token": "controlled-job-token"}, response.Response.Credential)
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PublishedControls())
}

func TestDaemonAuthenticationWithoutACredentialRefusesTheCallback(t *testing.T) {
	t.Parallel()
	a, _, stdin := newGoalAgent(t)
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewModelProgressResetSink(agent.NewProviderServices(sink))
	a.HandleOutput([]byte(`{"type":"control_request","request_id":"missing-token","request":{"type":"daemon.auth.fetch_job_token"}}`))
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"error","request_id":"missing-token","error":"No Qoder job token is configured"}}`, stdin.String())
	assert.Empty(t, sink.PublishedControls())
}

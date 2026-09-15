package agent

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
)

type sessionInputSender interface {
	SendInputForSession(string, string, []*leapmuxv1.Attachment) error
}

func TestSessionInputRejectsMissingAndReplacedTargets(t *testing.T) {
	claude := &ClaudeCodeAgent{sink: &testSink{}}
	claude.claudeCodeHandleSystemInit([]byte(`{"session_id":"current"}`))
	for _, tc := range []struct {
		name     string
		provider any
	}{
		{"Claude", claude},
		{"Codex", &CodexAgent{threadID: "current"}},
		{"ACP", &acpBase{sessionID: "current"}},
		{"Pi", &PiAgent{sessionID: "runtime", sessionFile: "current"}},
		{"ZCode", &zcodeAgent{sessionID: "current"}},
		{"Copilot", &copilotAgent{sessionID: "current", copilotConnection: &copilotConnection{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender, ok := tc.provider.(sessionInputSender)
			require.True(t, ok, "the provider must validate a session-qualified input")
			for _, expected := range []string{"", "previous"} {
				require.ErrorContains(t, sender.SendInputForSession(expected, "Do not send this input.", nil), "session")
			}
		})
	}
}

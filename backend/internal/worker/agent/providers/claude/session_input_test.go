package claude

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestClaudeSessionInputRejectsMissingAndReplacedTargets(t *testing.T) {
	claude := &Agent{sink: agent.NewProviderServices(&agenttest.Sink{})}
	claude.claudeCodeHandleSystemInit([]byte(`{"session_id":"current"}`))
	agenttest.AssertRejectsMissingAndReplacedSessions(t, claude)
}

func TestClaudeIsCompactCommandAcceptsOnlyTheCompactCommandWithoutAttachments(t *testing.T) {
	t.Parallel()
	text := agent.ClassifyAttachments([]*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("keep")}})
	for _, tc := range []struct {
		name       string
		content    string
		classified []agent.ClassifiedAttachment
		want       bool
	}{
		{name: "the bare command", content: "/compact", want: true},
		{name: "the command with arguments", content: "/compact keep the plan", want: true},
		{name: "the command inside white space", content: "  /compact\n", want: true},
		{name: "the command with an attachment", content: "/compact", classified: text},
		{name: "a command whose name only starts with compact", content: "/compactor"},
		{name: "another command", content: "/clear"},
		{name: "the word without a slash", content: "compact"},
		{name: "the command after other text", content: "please /compact"},
		{name: "a slash alone", content: "/"},
		{name: "an empty message", content: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, claudeIsCompactCommand(tc.content, tc.classified))
		})
	}
}

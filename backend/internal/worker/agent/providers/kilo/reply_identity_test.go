package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKiloAssignsSeparateTranscriptKeysToSegmentsOfOneAssistantMessage(t *testing.T) {
	t.Parallel()
	a := newAgent(false)
	hooks := opencode.FamilyHooks(&a.FamilyBase)
	require.NotNil(t, hooks.AssistantTextKey)
	first := hooks.AssistantTextKey("msg_assistant")
	second := hooks.AssistantTextKey("msg_assistant")
	other := hooks.AssistantTextKey("msg_assistant_later")
	assert.Regexp(t, `^kilo-reply:"msg_assistant":[A-Za-z0-9]{48}$`, first)
	assert.Regexp(t, `^kilo-reply:"msg_assistant":[A-Za-z0-9]{48}$`, second)
	assert.Regexp(t, `^kilo-reply:"msg_assistant_later":[A-Za-z0-9]{48}$`, other)
	assert.NotEqual(t, first, second)
}

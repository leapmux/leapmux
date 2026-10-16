package acp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestBypassCannotApproveARequestFromARetiredSession(t *testing.T) {
	a, sink, requests := newRetiringTestAgent(t)
	_, err := a.ClearContext()
	require.NoError(t, err)
	checks := 0
	a.hooks.WithBypassPermission = func(claim func()) { checks++; claim() }
	a.hooks.PermissionAllowsBypass = func(*providerkit.ParsedLine) bool { return true }
	frame := []byte(`{"jsonrpc":"2.0","id":73,"method":"session/request_permission","params":{"sessionId":"session-1","options":[{"optionId":"allow","kind":"allow_once"}]}}`)
	a.PublishSessionControlRequest(providerkit.ParseLine(frame), acpPermissionCancelAnswer())
	syncTestPeer(t, a)
	assert.Zero(t, checks, "session ownership precedes every standing-policy check")
	assert.Empty(t, sink.PublishedControls())
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))["73"])
}

func TestPermissionApprovalAnswerRequiresOneUnambiguousOfferedOption(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`not JSON`, `null`, `{}`, `{"options":null}`, `{"options":"allow"}`,
		`{"options":[{"optionId":"yes","kind":"allow_always"}]}`,
		`{"options":[{"optionId":"","kind":"allow_once"}]}`,
		`{"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"yes","kind":"reject_once"}]}`,
		`{"options":[{"optionId":"yes","kind":"allow_once"},{"optionId":"other","kind":"allow_once"}]}`,
	} {
		assert.Nil(t, permissionApprovalAnswer(json.RawMessage(raw)), raw)
	}
	answer := permissionApprovalAnswer(json.RawMessage(`{"options":[{"optionId":"no","kind":"reject_once"},{"optionId":"native:once","kind":"allow_once"}]}`))
	encoded, err := json.Marshal(answer)
	require.NoError(t, err)
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"native:once"}}`, string(encoded))
}

func TestWithOptionValueClaimsUnderTheSettingsLock(t *testing.T) {
	t.Parallel()
	b, _ := newACPTurnBase(t, &agenttest.Stdin{})
	b.permissionMode = "test-bypass"
	b.options.values = map[string]string{"approval": "test-bypass"}
	count := 0
	claim := func() {
		count++
		unlocked := b.Mu.TryLock()
		if unlocked {
			b.Mu.Unlock()
		}
		assert.False(t, unlocked, "a settings change cannot pass the authorization check before the claim")
	}
	b.WithOptionValue(agent.OptionIDPermissionMode, "test-bypass", claim)
	b.WithOptionValue("approval", "test-bypass", claim)
	b.WithOptionValue("approval", "", claim)
	b.WithOptionValue("missing", "test-bypass", claim)
	b.WithOptionValue(agent.OptionIDPermissionMode, "ask", claim)
	assert.Equal(t, 2, count)
}

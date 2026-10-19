package commandcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestClassifyStreamRestartIsAPIRetry(t *testing.T) {
	t.Parallel()
	assert.Equal(t, agent.APIRetryNotification(), (commandcodeProvider{}).Classify(json.RawMessage(`{"type":"event","event":{"type":"stream_restart","attempt":1}}`)))
}

func TestCommandCodeTokenResumeRule(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, commandcodeProvider{})
}

func TestCommandCodePreservesTheResponseWithoutARequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertPreservesTheResponseWithoutARequest(t, commandcodeProvider{})
}

func TestCommandCodeWithholdsTheResponseForAMalformedRequest(t *testing.T) {
	t.Parallel()
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, commandcodeProvider{})
}

func TestCommandCodeResumeHandlesRejectNativePathLookup(t *testing.T) {
	p := commandcodeProvider{}
	for _, handle := range []string{"../another/session", `another\session`, ".", "..", "--yolo", " leading", "line\nbreak"} {
		_, err := p.ResolveResumeHandle(handle, "")
		assert.Error(t, err, handle)
	}
}

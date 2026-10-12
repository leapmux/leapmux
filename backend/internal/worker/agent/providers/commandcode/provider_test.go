package commandcode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

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

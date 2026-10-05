package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestControlResponseRefusalStatesTheReason(t *testing.T) {
	t.Parallel()
	var resolution ControlResponseResolution
	assert.NoError(t, resolution.Refusal(), "a resolution that is not withheld has no refusal")

	resolution.Refuse("  Factory Droid did not offer proceed_always  ")
	assert.True(t, resolution.Withhold)
	assert.EqualError(t, resolution.Refusal(), "Factory Droid did not offer proceed_always")
}

// A provider that withholds with no reason states the generic text, and so does
// a Withhold that is set without Refuse.
func TestControlResponseRefusalFallsBackToTheGenericText(t *testing.T) {
	t.Parallel()
	for name, resolution := range map[string]ControlResponseResolution{
		"an empty reason": func() ControlResponseResolution {
			var r ControlResponseResolution
			r.Refuse("")
			return r
		}(),
		"a blank reason": func() ControlResponseResolution {
			var r ControlResponseResolution
			r.Refuse(" \t ")
			return r
		}(),
		"a direct withhold": {Withhold: true},
	} {
		assert.EqualErrorf(t, resolution.Refusal(), ControlResponseRefusedText, "%s", name)
	}
}

// A provider can forward a response after all. Its earlier reason then states
// nothing, because the service forwards the response.
func TestControlResponseRefusalIsNilOnceTheResponseIsForwarded(t *testing.T) {
	t.Parallel()
	var resolution ControlResponseResolution
	resolution.Refuse("a reason")
	resolution.Withhold = false
	assert.NoError(t, resolution.Refusal())
}

func TestRefusalUnofferedOptionStatesTheProviderName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Factory Droid did not offer proceed_always",
		RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_DROID, "proceed_always"))
	assert.Equal(t, "Kimi Code did not offer Revise",
		RefusalUnofferedOption(leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE, "Revise"))
}

package agenttest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// AssertPublishedControlKeys asserts that provider derives the key of each
// request that sink recorded from the request itself
// (agent.Provider.ControlRequestKey), as the Worker does before it answers a
// stored request (agent.StoredControlRequestAgrees). The Worker refuses every
// answer to a request whose payload gives another key than its publication,
// so a key function that diverges from the publication leaves each such
// request without an answer.
//
// A provider's test calls it after it publishes each kind of request that the
// provider publishes, through the provider's own publication path. It fails
// for a sink that recorded no request, because such a call checks nothing.
func AssertPublishedControlKeys(t testing.TB, provider agent.Provider, sink *ControlSink) {
	t.Helper()
	published := sink.PublishedControls()
	require.NotEmpty(t, published, "the sink recorded no request, so no key is checked")
	for _, request := range published {
		key, ok := provider.ControlRequestKey(request.Payload, request.AgentSessionID)
		assert.Truef(t, ok, "the payload of %q in session %q gives no key: %s", request.RequestID, request.AgentSessionID, request.Payload)
		assert.Equalf(t, request.RequestID, key, "the payload in session %q gives another key than its publication: %s", request.AgentSessionID, request.Payload)
		assert.Truef(t, agent.StoredControlRequestAgrees(provider, request.RequestID, request.Payload, request.AgentSessionID),
			"the Worker refuses every answer to %q", request.RequestID)
	}
}

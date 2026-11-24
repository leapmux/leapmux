package agenttest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// sessionKeyedProvider keys an envelope by its request_id, as the default
// does, and any other payload by the session that the Worker stores it with,
// as a provider keys the workspace-trust question. AssertPublishedControlKeys
// calls only ControlRequestKey, so the embedded Provider stays nil.
type sessionKeyedProvider struct{ agent.Provider }

func (sessionKeyedProvider) ControlRequestKey(payload []byte, agentSessionID string) (string, bool) {
	if key, ok := agent.EnvelopeControlRequestKey(payload); ok {
		return key, true
	}
	return "session:" + agentSessionID, agentSessionID != ""
}

// publishedSink is a sink that recorded each request in order.
func publishedSink(t *testing.T, requests ...agent.ControlRequest) *ControlSink {
	t.Helper()
	sink := &ControlSink{}
	for _, request := range requests {
		require.NoError(t, sink.PublishControlRequest(request))
	}
	return sink
}

// The suite guards 26 provider packages, so it must fail each publication
// whose key the Worker would read back differently, and pass each one that it
// reads back as published, a key that the session gives included.
func TestAssertPublishedControlKeys(t *testing.T) {
	t.Parallel()
	envelope := []byte(`{"type":"control_request","request_id":"req-1"}`)
	sessionQuestion := []byte(`{"type":"question"}`)

	t.Run("passes for keys that the requests give", func(t *testing.T) {
		sink := publishedSink(t,
			agent.ControlRequest{RequestID: "req-1", Payload: envelope},
			agent.ControlRequest{RequestID: "session:s1", AgentSessionID: "s1", Payload: sessionQuestion},
		)
		assert.False(t, failsUnder(func(tb testing.TB) { AssertPublishedControlKeys(tb, sessionKeyedProvider{}, sink) }))
	})
	for name, sink := range map[string]*ControlSink{
		// Such a call checks nothing.
		"fails for a sink that recorded no request": {},
		"fails for a publication under another key": publishedSink(t, agent.ControlRequest{RequestID: "req-2", Payload: envelope}),
		"fails for a payload that gives no key": publishedSink(t,
			agent.ControlRequest{RequestID: "req-1", Payload: envelope},
			agent.ControlRequest{RequestID: "session:s1", Payload: sessionQuestion},
		),
		"fails for a key of another session": publishedSink(t, agent.ControlRequest{RequestID: "session:s1", AgentSessionID: "s2", Payload: sessionQuestion}),
		// The Worker reads no key from a payload that is no JSON object.
		"fails for a payload that is no JSON object": publishedSink(t, agent.ControlRequest{RequestID: "session:s1", AgentSessionID: "s1", Payload: []byte(`not json`)}),
	} {
		t.Run(name, func(t *testing.T) {
			assert.True(t, failsUnder(func(tb testing.TB) { AssertPublishedControlKeys(tb, sessionKeyedProvider{}, sink) }))
		})
	}
}

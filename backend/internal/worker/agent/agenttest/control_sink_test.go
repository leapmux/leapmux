package agenttest

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// The recording sink stores the session of a control request as the worker's
// own sink does. A provider test reads that session to prove which session an
// answer must match, so a sink that recorded only the stated field would hide
// a request that reached it before the session was known.
func TestControlSinkStoresTheSessionOfEachRequest(t *testing.T) {
	t.Parallel()
	sink := &ControlSink{}

	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "before-any-session"}))
	sink.UpdateSessionID("session-1")
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "held-session"}))
	require.NoError(t, sink.PublishControlRequest(agent.ControlRequest{RequestID: "stated-session", AgentSessionID: "session-2"}))

	sessions := map[string]string{}
	for _, record := range sink.PublishedControls() {
		sessions[record.RequestID] = record.AgentSessionID
	}
	assert.Equal(t, map[string]string{
		"before-any-session": "",
		"held-session":       "session-1",
		"stated-session":     "session-2",
	}, sessions)
}

// A refused publication records nothing, whatever session the request states.
func TestControlSinkRecordsNoRequestThatItRefuses(t *testing.T) {
	t.Parallel()
	sink := &ControlSink{PublicationError: errors.New("store failed")}

	err := sink.PublishControlRequest(agent.ControlRequest{RequestID: "refused", AgentSessionID: "session-1"})

	require.EqualError(t, err, "store failed")
	assert.Empty(t, sink.PublishedControls())
}
